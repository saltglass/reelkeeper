# shellcheck shell=bash
# Helpers for run.sh. Written for bash 3.2 (macOS): no associative arrays.

LAB=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export COMPOSE_PROJECT_NAME=rk-backup-lab
COMPOSE=(docker compose -f "$LAB/compose.yaml")
PREFIX=rk-backup-lab
DRIVER=$PREFIX-driver-1

PG_URL_BASE="postgres://lab:lab-only-pg-password@postgres:5432"
PASS='Lab-Pass-1234!'
USERN=labadmin

# --- instance table ---------------------------------------------------------------------------
inst_engine() {
  case $1 in rk-pg | rk-fresh-pg | rk-old081-pg | rk-old084) echo pg ;; *) echo sqlite ;; esac
}
inst_pgdb() {
  case $1 in rk-pg) echo rk_pg ;; rk-fresh-pg) echo rk_fresh_pg ;; rk-old081-pg) echo rk_old081_pg ;; rk-old084) echo rk_old084_pg ;; esac
}
inst_db() { # the --db argument for labtool, as seen from the driver
  if [ "$(inst_engine "$1")" = pg ]; then echo "$PG_URL_BASE/$(inst_pgdb "$1")?sslmode=disable"; else echo "/vol/$1/reelkeeper.db"; fi
}
inst_secret() {
  case $1 in
    rk-wrongkey) echo 'lab-only-WRONG-key-BBBBBBBBBBBBBBBBBBBBBBBBBB' ;;
    *) echo 'lab-only-secret-key-AAAAAAAAAAAAAAAAAAAAAAAA' ;;
  esac
}
inst_url() { echo "http://$1:7676"; }
cname() { echo "$PREFIX-$1-1"; }

# --- running things in the driver ---------------------------------------------------------------
d() { docker exec "$DRIVER" "$@"; }                          # as root
du() { docker exec -u 65532:65532 "$DRIVER" "$@"; }       # as the app's user (files stay the app's)
lt() { du /labbin/labtool "$@"; }                            # labtool, as the app's user

tnow() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }
elapsed() { perl -e "printf '%.1f', $2 - $1"; }
psleep() { perl -MTime::HiRes=sleep -e "sleep($1)"; }

# --- results ----------------------------------------------------------------------------------------
SCEN="setup"
FAILED=0
rec() { # status check detail
  local detail
  detail=$(printf '%s' "$3" | tr '\n\t' '  ')
  set -- "$1" "$(printf '%s' "$2" | tr '\n\t' '  ')" "$detail"
  printf '%s\t%s\t%s\t%s\n' "$SCEN" "$1" "$2" "$3" >>"$OUT/checks.tsv"
  if [ "$1" = FAIL ]; then FAILED=1; printf '  \033[31mFAIL\033[0m %s: %s\n' "$2" "$3"; else printf '  \033[32mPASS\033[0m %s%s\n' "$2" "${3:+ ($3)}"; fi
}
pass() { rec PASS "$1" "${2:-}"; }
fail() { rec FAIL "$1" "${2:-}"; }
note() { local t; t=$(printf '%s' "$1" | tr '\n\t' '  '); printf '%s\t%s\n' "$SCEN" "$t" >>"$OUT/notes.tsv"; printf '  note: %s\n' "$t"; }
scenario() { SCEN="$1"; printf '\n\033[1m== %s\033[0m\n' "$1"; }
metric() { # label engine rows bytes seconds peakMiB rssMiB
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$@" >>"$OUT/metrics.tsv"
}
should_run() { [ -z "${RUN_ONLY:-}" ] || case " $RUN_ONLY " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# --- API -------------------------------------------------------------------------------------------
keyfile() { echo "${STATE:?}/$1.key"; }
holds() { cat "${STATE:?}/$1.holds" 2>/dev/null || echo "$1"; }
set_creds() { # svc APIKEY holder
  printf '%s' "$2" >"$(keyfile "$1")"
  printf '%s' "$3" >"${STATE:?}/$1.holds"
}
# The API key a backup carries is the key of the instance it was made on. save_orig remembers an
# instance's own key, so a restore of its backup anywhere hands that key to the restoring instance.
save_orig() { cp "$(keyfile "$1")" "${STATE:?}/orig-$1.key"; }
orig_key() { cat "${STATE:?}/orig-$1.key"; }
# api SVC METHOD PATH [curl args]  ->  CODE, BODY   (with the instance's current API key)
api() {
  local svc=$1 m=$2 p=$3 out
  shift 3
  out=$(d curl -s -m "${APITO:-600}" -X "$m" -H "X-Api-Key: $(cat "$(keyfile "$svc")")" -w '\n%{http_code}' "$@" "$(inst_url "$svc")/api/v1$p" 2>&1) || true
  CODE=${out##*$'\n'}
  BODY=${out%$'\n'*}
}
# apin: the same without a key (the first-run screen's endpoints)
apin() {
  local svc=$1 m=$2 p=$3 out
  shift 3
  out=$(d curl -s -m "${APITO:-600}" -X "$m" -w '\n%{http_code}' "$@" "$(inst_url "$svc")/api/v1$p" 2>&1) || true
  CODE=${out##*$'\n'}
  BODY=${out%$'\n'*}
}
ping_ok() { [ "$(d curl -s -m 3 -o /dev/null -w '%{http_code}' "$(inst_url "$1")/ping" 2>/dev/null)" = 200 ]; }
started_at() { docker inspect -f '{{.State.StartedAt}}' "$(cname "$1")" 2>/dev/null; }
wait_ping() { # svc [timeout]
  local i
  for ((i = 0; i < ${2:-120}; i++)); do
    if ping_ok "$1"; then return 0; fi
    sleep 1
  done
  return 1
}
wait_restarted() { # svc previousStartedAt [timeout]
  local i
  for ((i = 0; i < ${3:-300}; i++)); do
    if [ "$(started_at "$1")" != "$2" ] && ping_ok "$1"; then return 0; fi
    sleep 0.5
  done
  return 1
}
vmhwm() { # peak resident memory (MiB) of the app process since it started
  local pid kb
  pid=$(docker inspect -f '{{.State.Pid}}' "$(cname "$1")" 2>/dev/null)
  if [ -z "$pid" ] || [ "$pid" = 0 ]; then return 0; fi
  kb=$(colima ssh -- sh -c "grep VmHWM /proc/$pid/status" 2>/dev/null </dev/null | awk '{print $2}')
  if [ -n "$kb" ]; then perl -e "printf '%.0f', $kb/1024"; fi
}

# --- memory sampling (docker stats, as asked) --------------------------------------------------------
memsample_start() { # container short names; the first is "the app"
  MEMF=$OUT/mem.$RANDOM
  : >"$MEMF"
  (
    while :; do
      for c in "$@"; do docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' "$(cname "$c")" >>"$MEMF" 2>/dev/null; done
    done
  ) &
  MEMPID=$!
}
mem_peak() { # container short name -> peak MiB seen
  awk -v n="$(cname "$1")" '$1 == n {
      v = $2; u = v; sub(/[0-9.]+/, "", u); sub(/[A-Za-z]+$/, "", v)
      m = (u == "GiB") ? v * 1024 : (u == "MiB") ? v : (u == "KiB") ? v / 1024 : (u == "kB") ? v / 1000 : (u == "MB") ? v : (u == "GB") ? v * 1000 : v / 1048576
      if (m > best) best = m } END { printf "%.0f", best }' "$MEMF"
}
memsample_stop() {
  kill "$MEMPID" 2>/dev/null
  wait "$MEMPID" 2>/dev/null
  true
}
sampled() { if [ "$(inst_engine "$1")" = pg ]; then echo "$1 postgres"; else echo "$1"; fi; }

# --- instance life cycle -----------------------------------------------------------------------------
stop_inst() { docker stop -t 30 "$(cname "$1")" >/dev/null 2>&1; }
start_inst() { docker start "$(cname "$1")" >/dev/null 2>&1; wait_ping "$1" "${2:-120}"; }
wipe_inst() { # stop it and delete its database and data folder
  local svc=${1:?}
  stop_inst "$svc"
  d find "/vol/$svc" -mindepth 1 -delete
  if [ "$(inst_engine "$svc")" = pg ]; then
    d psql -h postgres -U lab -d postgres -v ON_ERROR_STOP=1 -q \
      -c "DROP DATABASE IF EXISTS $(inst_pgdb "$svc") WITH (FORCE)" -c "CREATE DATABASE $(inst_pgdb "$svc")" >/dev/null
  fi
  : >"$(keyfile "$svc")"
  : >"${STATE:?}/$svc.holds"
}
# setup_inst: create the account and settings through the API; keeps the API key
setup_inst() {
  local out k
  out=$(d bash /lab/setup-instance.sh "$(inst_url "$1")" "$1" 2>"$OUT/setup-$1.log") || {
    fail "setup $1" "$(head -c 300 "$OUT/setup-$1.log")"
    return 1
  }
  k=$(sed -n 's/^APIKEY=//p' <<<"$out")
  set_creds "$1" "$k" "$1"
  if [ -s "$OUT/setup-$1.log" ]; then note "setup $1: $(tr '\n' ' ' <"$OUT/setup-$1.log" | head -c 300)"; fi
}
task_kinds() { # the current version's task kinds, from an instance that is up
  api "$1" GET /system/tasks
  jq -r '[.tasks[].kind] | join(",")' <<<"$BODY"
}
seed_inst() { # svc scale  (the app is stopped while it runs)
  local svc=$1 scale=$2 t0 out
  stop_inst "$svc"
  t0=$(tnow)
  out=$(lt seed --db "$(inst_db "$svc")" --scale "$scale" --seed "${SEED:-1}" --key "$(inst_secret "$svc")" --kinds "$KINDS" 2>&1) || {
    fail "seed $svc" "$out"
    return 1
  }
  SEED_ROWS=$(sed -n 's/^seeded \([0-9]*\) rows.*/\1/p' <<<"$out")
  note "seeded $svc: ${SEED_ROWS} rows in $(elapsed "$t0" "$(tnow)")s"
  start_inst "$svc" || { fail "start $svc after seeding" "does not answer"; return 1; }
}
smoke() { # svc [extra smoke args]
  local svc=$1
  shift
  d env SMOKE_SKIP="${SMOKE_SKIP:-}" bash /lab/smoke.sh "$(inst_url "$svc")" "$USERN" "$PASS" "$(cat "$(keyfile "$svc")")" "$@"
}
smoke_check() { # name svc [args]
  local name=$1 svc=$2 out n_fail n_pass
  shift 2
  out=$(smoke "$svc" "$@")
  n_fail=$(grep -c '^FAIL' <<<"$out")
  n_pass=$(grep -c '^PASS' <<<"$out")
  if [ "$n_fail" = 0 ] && [ "$n_pass" -gt 28 ]; then pass "$name" "$n_pass checks"; else
    fail "$name" "$n_fail failed of $((n_fail + n_pass)): $(grep '^FAIL' <<<"$out" | head -4 | tr '\n' ';' | cut -c1-400)"
  fi
}
# provision SVC SCALE: a seeded, working instance
provision() {
  local svc=$1 scale=$2
  wipe_inst "$svc"
  start_inst "$svc" || { fail "start $svc" "does not answer"; return 1; }
  setup_inst "$svc" || return 1
  if [ -z "${KINDS:-}" ]; then KINDS=$(task_kinds "$svc"); fi
  seed_inst "$svc" "$scale" || return 1
}

# --- backups through the API ---------------------------------------------------------------------------
# make_backup SVC: sets BNAME BSIZE BSECS BMEM BRSS
make_backup() {
  local svc=$1 t0 t1
  # shellcheck disable=SC2046
  memsample_start $(sampled "$svc")
  t0=$(tnow)
  api "$svc" POST /backups
  t1=$(tnow)
  memsample_stop
  BSECS=$(elapsed "$t0" "$t1")
  BMEM=$(mem_peak "$svc")
  BRSS=$(vmhwm "$svc")
  if [ "$CODE" = 201 ]; then
    BNAME=$(jq -r .name <<<"$BODY")
    BSIZE=$(jq -r .size <<<"$BODY")
    return 0
  fi
  BNAME=""
  return 1
}
download_backup() { # svc name workfile
  local key
  key=$(cat "$(keyfile "$1")")
  d curl -s -m 600 -o "/work/$3" -w '%{http_code}' -H "X-Api-Key: $key" "$(inst_url "$1")/api/v1/backups/$2"
}
upload_backup() { # svc workfile -> CODE BODY UPNAME
  api "$1" POST /backups/upload -H 'Content-Type: application/octet-stream' --data-binary "@/work/$2"
  UPNAME=""
  if [ "$CODE" = 201 ]; then UPNAME=$(jq -r .name <<<"$BODY"); fi
}
msg() { jq -r '.message // .problem // empty' <<<"$BODY" 2>/dev/null; }
# restore_backup SVC NAME [holder]: stage a restore, wait for the restart, read the outcome.
# Sets RCODE RESTORE_OK RESTORE_MSG RESTORE_SAFETY RSECS RMEM RRSS. After it, the instance holds
# `holder`'s accounts and API key (default: its own).
restore_backup() {
  local svc=$1 name=$2 holder=${3:-$1} prev t0 t1
  prev=$(started_at "$svc")
  # shellcheck disable=SC2046
  memsample_start $(sampled "$svc")
  t0=$(tnow)
  api "$svc" POST "/backups/$name/restore"
  RCODE=$CODE
  RMSG0=$(msg)
  RESTORE_OK=""
  if [ "$RCODE" != 202 ]; then
    memsample_stop
    return 1
  fi
  set_creds "$svc" "$(orig_key "$holder")" "$holder"
  if ! wait_restarted "$svc" "$prev" 600; then
    memsample_stop
    return 1
  fi
  t1=$(tnow)
  memsample_stop
  RSECS=$(elapsed "$t0" "$t1")
  RMEM=$(mem_peak "$svc")
  RRSS=$(vmhwm "$svc")
  read_last_restore "$svc"
}
read_last_restore() {
  local t0
  t0=$(tnow)
  api "$1" GET /backups
  LISTSECS=$(elapsed "$t0" "$(tnow)") # the first list after a restart reads every backup file in full
  RESTORE_OK=$(jq -r 'if .lastRestore then (.lastRestore.ok | tostring) else "" end' <<<"$BODY")
  RESTORE_MSG=$(jq -r '.lastRestore.message // empty' <<<"$BODY")
  RESTORE_SAFETY=$(jq -r '.lastRestore.safety // empty' <<<"$BODY")
  RESTORE_BACKUP=$(jq -r '.lastRestore.backup // empty' <<<"$BODY")
}
# first_run_restore SVC WORKFILE HOLDER: restore on the first-run screen of a wiped instance
first_run_restore() {
  local svc=$1 file=$2 holder=$3 prev t0 t1
  prev=$(started_at "$svc")
  # shellcheck disable=SC2046
  memsample_start $(sampled "$svc")
  t0=$(tnow)
  apin "$svc" POST /auth/setup/restore -H 'Content-Type: application/octet-stream' --data-binary "@/work/$file"
  RCODE=$CODE
  RMSG0=$(msg)
  if [ "$RCODE" != 202 ]; then
    memsample_stop
    return 1
  fi
  set_creds "$svc" "$(orig_key "$holder")" "$holder"
  if ! wait_restarted "$svc" "$prev" 600; then
    memsample_stop
    return 1
  fi
  t1=$(tnow)
  memsample_stop
  RSECS=$(elapsed "$t0" "$t1")
  RMEM=$(mem_peak "$svc")
  RRSS=$(vmhwm "$svc")
  read_last_restore "$svc"
}

# --- dumps and comparing -----------------------------------------------------------------------------
dump_to() { # svc workfile
  lt dump --db "$(inst_db "$1")" --out "/work/$2" >/dev/null
}
fp() { lt fingerprint --db "$(inst_db "$1")" --skip sessions; }
cmp_dumps() { # a b [args]  -> CMP_OUT, status
  local a=$1 b=$2
  shift 2
  CMP_OUT=$(d /labbin/labtool compare "/work/$a" "/work/$b" "$@" 2>&1)
}
# assert_same NAME dumpA dumpB [compare args]  (sessions are left out: a restore empties them on purpose)
assert_same() {
  local name=$1 a=$2 b=$3
  shift 3
  if cmp_dumps "$a" "$b" --ignore sessions "$@"; then pass "$name" "$(tail -1 <<<"$CMP_OUT")"; else fail "$name" "$(head -6 <<<"$CMP_OUT" | tr '\n' '|' | cut -c1-600)"; fi
}
count_of() { # svc table
  lt sql --db "$(inst_db "$1")" --query "SELECT COUNT(*) FROM $2" | tail -1
}
