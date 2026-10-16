# shellcheck shell=bash
# The lab's scenarios. Sourced by run.sh. Every scenario asserts: a check passes or fails with a reason,
# then the run goes on.

# Tables a restore leaves out on purpose: goose's version table (not dumped) and sessions (everyone
# signs in again). The dumps skip goose; comparisons ignore sessions.

# seq_checks SVC: after a restore, new writes must work (identity sequences continue past the restored rows).
seq_checks() {
  local svc=$1 n job t
  n=$RANDOM
  if make_backup "$svc"; then pass "$svc: a new manual backup works after the restore" "$BNAME"; else fail "$svc: backup after restore" "http $CODE $(msg)"; fi
  api "$svc" POST /profiles -H 'Content-Type: application/json' -d "{\"name\":\"After restore $n\",\"appliesTo\":\"both\"}"
  if [ "$CODE" = 201 ] || [ "$CODE" = 200 ]; then pass "$svc: adding a quality profile (a new row, new id) works" "id $(jq -r .id <<<"$BODY")"; else fail "$svc: add a profile after restore" "http $CODE $(msg)"; fi
  api "$svc" POST /system/tasks/jobs.prune/run
  if [ "$CODE" = 200 ]; then
    job=$(jq -r .job.id <<<"$BODY")
    for ((t = 0; t < 30; t++)); do
      api "$svc" GET "/system/tasks"
      st=$(jq -r --arg id "$job" '[.recent[]? | select((.id|tostring) == $id)][0].status // empty' <<<"$BODY")
      [ "$st" = succeeded ] || [ "$st" = failed ] && break
      sleep 1
    done
    if [ "$st" = succeeded ]; then pass "$svc: a task run after the restore (a new jobs row) works" "job $job"; else fail "$svc: task run after restore" "job $job status '${st:-unknown}'"; fi
  else fail "$svc: run a task after restore" "http $CODE $(msg)"; fi
  d curl -s -o /tmp/tag.json -w '%{http_code}' -H "X-Api-Key: $(cat "$(keyfile "$svc")")" -H 'Content-Type: application/json' \
    -d "{\"label\":\"after-restore-$n\"}" "$(inst_url "$svc")/compat/radarr/hd/api/v3/tag" >"$OUT/tagcode"
  if [ "$(cat "$OUT/tagcode")" = 200 ] || [ "$(cat "$OUT/tagcode")" = 201 ]; then pass "$svc: creating a tag (compat API, new row) works"; else fail "$svc: create tag after restore" "http $(cat "$OUT/tagcode")"; fi
}

# Scenario 1 / 2: in-place restore.
s_inplace() { # svc
  local svc=$1 eng d0 d1 counts rows
  eng=$(inst_engine "$svc")
  scenario "$2: in-place restore ($eng)"
  dump_to "$svc" "$svc.D0.txt"
  counts=$(lt counts --db "$(inst_db "$svc")")
  if make_backup "$svc"; then
    pass "$svc: manual backup B1 made" "$BNAME, $BSIZE bytes, ${BSECS}s, peak ${BMEM} MiB (docker stats), ${BRSS} MiB rss"
    metric "backup, default scale" "$eng" "$(jq -r '[.[]]|add' <<<"$counts")" "$BSIZE" "$BSECS" "$BMEM" "$BRSS"
  else
    fail "$svc: backup B1" "http $CODE $(msg)"
    return
  fi
  B1=$BNAME
  echo "$B1" >"$STATE/$svc.B1"
  download_backup "$svc" "$B1" "$svc.B1.backup" >/dev/null
  api "$svc" POST "/backups/$B1/check"
  rows=$(jq -c '.rows' <<<"$BODY")
  if jq -ne --argjson a "$counts" --argjson b "$rows" '($a | del(.sessions)) == $b' >/dev/null; then
    pass "$svc: B1's own row counts equal the database's, table by table (sessions left out by design)" "$(jq -r .total <<<"$BODY") rows"
  else
    fail "$svc: B1's counts differ from the database's" "$(jq -nc --argjson a "$counts" --argjson b "$rows" '[$a|to_entries[]|select(.key!="sessions")|select($b[.key]!=.value)|{(.key):[.value,$b[.key]]}]')"
  fi
  # heavy changes
  out=$(lt mutate --db "$(inst_db "$svc")" --ops 4000 2>&1)
  note "$svc mutate: $(head -c 300 <<<"$out" | tr '\n' ' ')"
  dump_to "$svc" "$svc.D1.txt"
  if cmp_dumps "$svc.D0.txt" "$svc.D1.txt" --ignore sessions; then fail "$svc: mutate changed nothing" "D1 equals D0"; else pass "$svc: D1 (after mutate) differs from D0" "$(tail -1 <<<"$CMP_OUT")"; fi
  # restore B1
  if restore_backup "$svc" "$B1"; then
    pass "$svc: restore staged and the app came back" "${RSECS}s, peak ${RMEM} MiB (docker stats), ${RRSS} MiB rss"
    metric "restore, default scale" "$eng" "$(jq -r '[.[]]|add' <<<"$counts")" "$BSIZE" "$RSECS" "$RMEM" "$RRSS"
  else
    fail "$svc: restore" "http $RCODE ${RMSG0:-no restart seen}"
    return
  fi
  if [ "$RESTORE_OK" = true ]; then pass "$svc: lastRestore.ok is true" "$RESTORE_MSG"; else fail "$svc: lastRestore" "ok='$RESTORE_OK' $RESTORE_MSG"; fi
  echo "$RESTORE_SAFETY" >"$STATE/$svc.safety"
  dump_to "$svc" "$svc.D2.txt"
  assert_same "$svc: after the restore the database equals D0 (everything but sessions)" "$svc.D0.txt" "$svc.D2.txt"
  n=$(count_of "$svc" sessions)
  if [ "$n" = 0 ]; then pass "$svc: sessions were emptied by the restore (by design)"; else fail "$svc: sessions after restore" "$n rows"; fi
  api "$svc" GET /backups
  if jq -e --arg s "$RESTORE_SAFETY" '.backups[] | select(.name == $s and .kind == "safety")' <<<"$BODY" >/dev/null; then
    pass "$svc: the safety backup exists and is listed as kind safety" "$RESTORE_SAFETY"
  else fail "$svc: safety backup" "'$RESTORE_SAFETY' not in the list"; fi
  smoke_check "$svc: after the restore, logins, API key, endpoints and stored credentials all work" "$svc" --creds
  lt fkcheck --db "$(inst_db "$svc")" >"$OUT/fk.txt" 2>&1 && pass "$svc: foreign keys are clean after the restore" || fail "$svc: fkcheck" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
  seq_checks "$svc"
}

# Scenario 5: undo = restore the safety backup of scenario 1/2; the database must equal D1.
s_undo() { # svc
  local svc=$1 safety
  scenario "$2: undo, restore the safety backup ($(inst_engine "$svc"))"
  safety=$(cat "$STATE/$svc.safety" 2>/dev/null)
  if [ -z "$safety" ]; then fail "$svc: no safety backup to restore" "scenario 1/2 did not record one"; return; fi
  if restore_backup "$svc" "$safety"; then
    if [ "$RESTORE_OK" = true ]; then pass "$svc: safety backup restored" "${RSECS}s"; else fail "$svc: undo restore" "$RESTORE_MSG"; fi
  else fail "$svc: undo restore" "http $RCODE ${RMSG0:-no restart seen}"; return; fi
  dump_to "$svc" "$svc.D3.txt"
  assert_same "$svc: after the undo the database equals D1 (the state just before the restore)" "$svc.D1.txt" "$svc.D3.txt"
  if ping_ok "$svc"; then pass "$svc: app healthy after the undo"; else fail "$svc: app health after undo"; fi
}

# fresh_empty SVC: a wiped instance with no account, showing the first-run screen
fresh_empty() {
  wipe_inst "$1"
  start_inst "$1" || { fail "start $1" "does not answer"; return 1; }
  apin "$1" GET /auth/state
  if [ "$(jq -r .setupRequired <<<"$BODY")" = true ]; then return 0; fi
  fail "$1 should show the first-run screen" "$BODY"
  return 1
}

# Scenario 3: across engines through download and upload.
s_cross() {
  scenario "S3: across engines (download, upload, restore)"
  local up name
  # SQLite -> Postgres: rk-sqlite's B1 into rk-pg
  upload_backup rk-pg rk-sqlite.B1.backup
  if [ "$CODE" = 201 ]; then pass "S3: rk-sqlite's backup uploaded to rk-pg" "$UPNAME, keyMatches=$(jq -r .keyMatches <<<"$BODY"), engine=$(jq -r .engine <<<"$BODY")"; else fail "S3: upload sqlite backup to pg" "http $CODE $(msg)"; return; fi
  if restore_backup rk-pg "$UPNAME" rk-sqlite && [ "$RESTORE_OK" = true ]; then
    pass "S3: restored into rk-pg" "${RSECS}s"
    metric "restore, SQLite backup into Postgres" pg "" "" "$RSECS" "$RMEM" "$RRSS"
  else fail "S3: restore sqlite backup into pg" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
  dump_to rk-pg "S3.pg-from-sqlite.txt"
  assert_same "S3: Postgres now equals rk-sqlite's D0" "rk-sqlite.D0.txt" "S3.pg-from-sqlite.txt"
  smoke_check "S3: rk-pg works with the SQLite instance's accounts, key and credentials" rk-pg --creds
  lt fkcheck --db "$(inst_db rk-pg)" >"$OUT/fk.txt" 2>&1 && pass "S3: foreign keys clean in rk-pg" || fail "S3: fkcheck rk-pg" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
  seq_checks rk-pg
  # Postgres -> SQLite: rk-pg's original B1 into rk-sqlite
  upload_backup rk-sqlite rk-pg.B1.backup
  if [ "$CODE" = 201 ]; then pass "S3: rk-pg's backup uploaded to rk-sqlite" "$UPNAME, engine=$(jq -r .engine <<<"$BODY")"; else fail "S3: upload pg backup to sqlite" "http $CODE $(msg)"; return; fi
  if restore_backup rk-sqlite "$UPNAME" rk-pg && [ "$RESTORE_OK" = true ]; then
    pass "S3: restored into rk-sqlite" "${RSECS}s"
    metric "restore, Postgres backup into SQLite" sqlite "" "" "$RSECS" "$RMEM" "$RRSS"
  else fail "S3: restore pg backup into sqlite" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
  dump_to rk-sqlite "S3.sqlite-from-pg.txt"
  assert_same "S3: SQLite now equals rk-pg's D0" "rk-pg.D0.txt" "S3.sqlite-from-pg.txt"
  smoke_check "S3: rk-sqlite works with the Postgres instance's accounts, key and credentials" rk-sqlite --creds
  lt fkcheck --db "$(inst_db rk-sqlite)" >"$OUT/fk.txt" 2>&1 && pass "S3: foreign keys clean in rk-sqlite" || fail "S3: fkcheck rk-sqlite" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
  seq_checks rk-sqlite
}

# Scenario 4: first-run restore, each engine from the other engine's backup.
s_firstrun() {
  scenario "S4: first-run restore"
  # an install that has an account refuses
  apin rk-sqlite POST /auth/setup/restore -H 'Content-Type: application/octet-stream' --data-binary "@/work/rk-pg.B1.backup"
  if [ "$CODE" = 409 ]; then pass "S4: first-run restore is refused (409) once an account exists" "$(msg)"; else fail "S4: first-run restore on an installed instance" "http $CODE $(msg)"; fi
  fresh_empty rk-fresh-sqlite || return
  if first_run_restore rk-fresh-sqlite rk-pg.B1.backup rk-pg && [ "$RESTORE_OK" = true ]; then
    pass "S4: rk-fresh-sqlite restored from rk-pg's backup on the first-run screen" "${RSECS}s; $RESTORE_MSG"
  else fail "S4: first-run restore into sqlite" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
  dump_to rk-fresh-sqlite "S4.fresh-sqlite.txt"
  assert_same "S4: rk-fresh-sqlite equals rk-pg's D0" "rk-pg.D0.txt" "S4.fresh-sqlite.txt"
  smoke_check "S4: sign in with the seeded password and the API key on rk-fresh-sqlite" rk-fresh-sqlite --creds
  api rk-fresh-sqlite GET /backups
  if jq -e '.backups[] | select(.kind == "safety")' <<<"$BODY" >/dev/null; then pass "S4: a safety backup of the empty database was made"; else fail "S4: safety backup on first run" "none listed"; fi
  fresh_empty rk-fresh-pg || return
  if first_run_restore rk-fresh-pg rk-sqlite.B1.backup rk-sqlite && [ "$RESTORE_OK" = true ]; then
    pass "S4: rk-fresh-pg restored from rk-sqlite's backup on the first-run screen" "${RSECS}s; $RESTORE_MSG"
  else fail "S4: first-run restore into pg" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
  dump_to rk-fresh-pg "S4.fresh-pg.txt"
  assert_same "S4: rk-fresh-pg equals rk-sqlite's D0" "rk-sqlite.D0.txt" "S4.fresh-pg.txt"
  smoke_check "S4: sign in with the seeded password and the API key on rk-fresh-pg" rk-fresh-pg --creds
  seq_checks rk-fresh-pg
}

# Scenario 6: a chain of hops, compared with the original every time.
s_chain() {
  scenario "S6: chain SQLite > Postgres > SQLite > Postgres > SQLite > Postgres > SQLite"
  local hop=1 from to name file
  # hop 1: B1 of rk-sqlite (file) -> rk-fresh-pg, first run
  fresh_empty rk-fresh-pg || return
  if first_run_restore rk-fresh-pg rk-sqlite.B1.backup rk-sqlite && [ "$RESTORE_OK" = true ]; then pass "S6 hop 1: SQLite > Postgres (first run)" "${RSECS}s"; else fail "S6 hop 1" "http $RCODE ${RMSG0:-} $RESTORE_MSG"; return; fi
  dump_to rk-fresh-pg "S6.hop1.txt"
  assert_same "S6 hop 1: equals the original dump" "rk-sqlite.D0.txt" "S6.hop1.txt"
  from=rk-fresh-pg
  to=rk-fresh-sqlite
  for hop in 2 3 4 5 6; do
    make_backup "$from" || { fail "S6 hop $hop: backup on $from" "http $CODE $(msg)"; return; }
    name=$BNAME
    file="S6.hop$hop.backup"
    download_backup "$from" "$name" "$file" >/dev/null
    if [ "$hop" = 2 ]; then
      fresh_empty "$to" || return
      first_run_restore "$to" "$file" rk-sqlite
    else
      upload_backup "$to" "$file"
      [ "$CODE" = 201 ] || { fail "S6 hop $hop: upload to $to" "http $CODE $(msg)"; return; }
      restore_backup "$to" "$UPNAME" rk-sqlite
    fi
    if [ "$RESTORE_OK" = true ]; then pass "S6 hop $hop: $(inst_engine $from) > $(inst_engine $to)" "${RSECS}s"; else fail "S6 hop $hop: restore into $to" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; return; fi
    dump_to "$to" "S6.hop$hop.txt"
    assert_same "S6 hop $hop: equals the original dump (no drift)" "rk-sqlite.D0.txt" "S6.hop$hop.txt"
    tmp=$from
    from=$to
    to=$tmp
  done
  lt fkcheck --db "$(inst_db "$from")" >"$OUT/fk.txt" 2>&1 && pass "S6: foreign keys clean at the end of the chain" || fail "S6: fkcheck" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
  smoke_check "S6: the end of the chain works with the original accounts and credentials" "$from" --creds
  seq_checks "$from"
}

# schema_of SVC: the newest migration the database has
schema_of() { lt sql --db "$(inst_db "$1")" --query "SELECT MAX(version_id) FROM goose_db_version" | tail -1; }

# One older-version case: an instance of an old Reelkeeper, seeded, backed up through its own API,
# restored into a current instance (the upgrade path).
old_case() { # old target mode(inplace|firstrun) ignore-cols
  local old=$1 target=$2 mode=$3 icols=$4 schema oldschema file
  scenario "S7: $old (Reelkeeper $(docker inspect -f '{{.Config.Image}}' "$(cname "$old")" | sed 's/.*://'), $(inst_engine "$old")) -> $target ($mode)"
  SMOKE_SKIP="backfill" provision "$old" "${SCALE_OLD:-0.3}" || return
  save_orig "$old"
  oldschema=$(schema_of "$old")
  note "$old runs schema version $oldschema; the current schema is $(schema_of rk-sqlite)"
  SMOKE_SKIP="backfill" smoke_check "S7: $old works after seeding (its API, its schema $oldschema)" "$old" --creds
  dump_to "$old" "$old.D0.txt"
  if make_backup "$old"; then pass "S7: backup made through $old's own API" "$BNAME, $BSIZE bytes, app $(jq -r .app <<<"$BODY"), schema $(jq -r .schemaVersion <<<"$BODY")"; else fail "S7: backup on $old" "http $CODE $(msg)"; return; fi
  file="$old.backup"
  download_backup "$old" "$BNAME" "$file" >/dev/null
  fresh_empty "$target" || return
  if [ "$mode" = inplace ]; then
    setup_inst "$target" || return
    upload_backup "$target" "$file"
    if [ "$CODE" = 201 ]; then pass "S7: the old backup uploaded to $target" "$UPNAME, keyMatches=$(jq -r .keyMatches <<<"$BODY")"; else fail "S7: upload $old backup" "http $CODE $(msg)"; return; fi
    api "$target" POST "/backups/$UPNAME/check"
    if [ "$CODE" = 200 ]; then pass "S7: the old backup checks as restorable" "schema $(jq -r .schemaVersion <<<"$BODY"), $(jq -r .total <<<"$BODY") rows"; else fail "S7: check old backup" "http $CODE $(msg)"; fi
    restore_backup "$target" "$UPNAME" "$old"
  else
    first_run_restore "$target" "$file" "$old"
  fi
  if [ "$RESTORE_OK" = true ]; then pass "S7: upgrade restore succeeded" "${RSECS}s; $RESTORE_MSG"; else fail "S7: upgrade restore" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; return; fi
  dump_to "$target" "$old.restored.txt"
  # What later migrations add or change: the backfill table (migration 19); blocklist.expires_at and blocklist.rule (migration 18)
  assert_same "S7: data equals the old dump (allowed: backfill table; ${icols:-no changed columns})" "$old.D0.txt" "$old.restored.txt" --ignore backfill ${icols:+--ignore-cols "$icols"}
  if [ -n "$icols" ]; then
    out=$(lt verify-blocklist --db "$(inst_db "$target")" 2>&1) && pass "S7: blocklist.rule and expires_at were derived as migration 18 says" "$(tail -1 <<<"$out")" || fail "S7: migration 18's derived columns" "$out"
  fi
  n=$(count_of "$target" backfill)
  if [ "$n" = 0 ]; then pass "S7: the new backfill table exists and is empty"; else fail "S7: backfill rows after upgrade" "$n"; fi
  if [ "$(schema_of "$target")" = "$(schema_of rk-sqlite)" ]; then pass "S7: the restored database is at the current schema" "version $(schema_of "$target")"; else fail "S7: schema version after upgrade" "$(schema_of "$target") vs $(schema_of rk-sqlite)"; fi
  smoke_check "S7: the upgraded instance works with the old accounts, key and credentials" "$target" --creds
  lt fkcheck --db "$(inst_db "$target")" >"$OUT/fk.txt" 2>&1 && pass "S7: foreign keys clean after the upgrade" || fail "S7: fkcheck" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
  left=$(d find "/vol/$target" -type d -name 'restore-*' | wc -l | tr -d ' ')
  if [ "$left" = 0 ]; then pass "S7: no scratch restore folder left behind"; else fail "S7: scratch folders left in the data folder" "$left"; fi
}

s_old() {
  old_case rk-old081 rk-fresh-pg inplace "blocklist.expires_at,blocklist.rule"
  old_case rk-old081-pg rk-fresh-sqlite inplace "blocklist.expires_at,blocklist.rule"
  old_case rk-old084 rk-fresh-sqlite firstrun ""
  old_case rk-old084-sqlite rk-fresh-pg firstrun ""
  # A current backup is refused by an older Reelkeeper as newer, and nothing changes.
  scenario "S7: a current backup into rk-old084 (older) is refused"
  local before after name
  before=$(fp rk-old084)
  upload_backup rk-old084 rk-sqlite.B1.backup
  if [ "$CODE" = 201 ]; then
    name=$UPNAME
    api rk-old084 POST "/backups/$name/check"
    if [ "$CODE" = 422 ] && grep -qi 'newer' <<<"$BODY"; then pass "S7: check refuses it as newer" "$(msg)"; else fail "S7: check of a newer backup" "http $CODE $(msg)"; fi
    api rk-old084 POST "/backups/$name/restore"
    if [ "$CODE" = 422 ] && grep -qi 'newer' <<<"$BODY"; then pass "S7: restore refuses it as newer" "$(msg)"; else fail "S7: restore of a newer backup" "http $CODE $(msg)"; fi
  elif [ "$CODE" = 422 ] && grep -qi 'newer' <<<"$BODY"; then
    pass "S7: the upload itself refuses it as newer" "$(msg)"
  else fail "S7: upload of a newer backup" "http $CODE $(msg)"; fi
  sleep 3
  after=$(fp rk-old084)
  if [ "$before" = "$after" ]; then pass "S7: rk-old084's database is unchanged" "$after"; else fail "S7: database changed" "$before -> $after"; fi
  if ping_ok rk-old084; then pass "S7: rk-old084 is healthy"; else fail "S7: rk-old084 health"; fi
}

# Scenario 8: the wrong secret key.
s_wrongkey() {
  scenario "S8: a backup made with a different secret key"
  local before after name
  SMOKE_SKIP="" provision rk-wrongkey 0.1 || return
  save_orig rk-wrongkey
  before=$(fp rk-wrongkey)
  upload_backup rk-wrongkey rk-sqlite.B1.backup
  if [ "$CODE" = 201 ]; then pass "S8: upload is accepted (the file is valid) and marked keyMatches=$(jq -r .keyMatches <<<"$BODY")"; else fail "S8: upload" "http $CODE $(msg)"; return; fi
  name=$UPNAME
  if [ "$(jq -r .keyMatches <<<"$BODY")" = false ]; then pass "S8: the upload response says the key doesn't match"; else fail "S8: keyMatches" "$BODY"; fi
  api rk-wrongkey GET /backups
  if jq -e --arg n "$name" '.backups[] | select(.name == $n and .keyMatches == false)' <<<"$BODY" >/dev/null; then pass "S8: the list shows keyMatches=false"; else fail "S8: list keyMatches" "$(jq -c --arg n "$name" '.backups[]|select(.name==$n)' <<<"$BODY")"; fi
  api rk-wrongkey POST "/backups/$name/check"
  if [ "$CODE" = 422 ] && grep -q 'different REELKEEPER_SECRET_KEY' <<<"$BODY"; then pass "S8: check is refused with the wrong-key message" "$(msg)"; else fail "S8: check" "http $CODE $(msg)"; fi
  api rk-wrongkey POST "/backups/$name/restore"
  if [ "$CODE" = 422 ] && grep -q 'different REELKEEPER_SECRET_KEY' <<<"$BODY"; then pass "S8: restore is refused with the wrong-key message"; else fail "S8: restore" "http $CODE $(msg)"; fi
  sleep 4
  after=$(fp rk-wrongkey)
  if [ "$before" = "$after" ]; then pass "S8: the database is unchanged" "$after"; else fail "S8: database changed" "$before -> $after"; fi
  if ping_ok rk-wrongkey; then pass "S8: the app is healthy"; else fail "S8: health"; fi
  smoke_check "S8: rk-wrongkey still works with its own data and key" rk-wrongkey --creds
  # first run, wrong key
  fresh_empty rk-wrongkey || return
  apin rk-wrongkey POST /auth/setup/restore -H 'Content-Type: application/octet-stream' --data-binary "@/work/rk-sqlite.B1.backup"
  if [ "$CODE" = 422 ] && grep -q 'different REELKEEPER_SECRET_KEY' <<<"$BODY"; then pass "S8: first-run restore is refused with the wrong-key message" "$(msg)"; else fail "S8: first-run restore with the wrong key" "http $CODE $(msg)"; fi
  apin rk-wrongkey GET /auth/state
  if [ "$(jq -r .setupRequired <<<"$BODY")" = true ] && ping_ok rk-wrongkey; then pass "S8: still on the first-run screen, healthy"; else fail "S8: state after the refused first-run restore" "$BODY"; fi
}

# Scenario 9: damaged files.
s_damaged() {
  scenario "S9: damaged backup files"
  local svc=rk-fresh-sqlite k i name before after up_code up_msg cls
  SMOKE_SKIP="" provision "$svc" 0.1 || return
  save_orig "$svc"
  make_backup "$svc" || { fail "S9: good backup" "http $CODE"; return; }
  download_backup "$svc" "$BNAME" "S9.good.backup" >/dev/null
  before=$(fp "$svc")
  i=0
  for k in flip truncate droplastline dropmiddleline empty header header-newer header-older header-format notbackup notbackup-gz headeronly gzcrc gztrailer garbage garbage-inside; do
    i=$((i + 1))
    lt damage --in /work/S9.good.backup --out "/work/S9.$k.backup" --kind "$k" >/dev/null || { fail "S9[$k]: making the damaged copy" "labtool failed"; continue; }
    # (a) uploaded
    upload_backup "$svc" "S9.$k.backup"
    up_code=$CODE
    up_msg=$(msg)
    # (b) placed straight into the folder
    name=$(printf 'reelkeeper-2020010100%04d-manual.backup' "$i")
    d sh -c "cp /work/S9.$k.backup /vol/$svc/backups/$name && chown 65532:65532 /vol/$svc/backups/$name"
    api "$svc" GET /backups
    listed=$(jq -r --arg n "$name" '[.backups[] | select(.name == $n)][0] | if .problem then "problem: " + .problem else "no problem" end' <<<"$BODY")
    api "$svc" POST "/backups/$name/check"
    chk_code=$CODE
    chk_msg=$(msg)
    prev=$(started_at "$svc")
    api "$svc" POST "/backups/$name/restore"
    rst_code=$CODE
    rst_msg=$(msg)
    rst_ok=""
    if [ "$rst_code" = 202 ]; then # a restore was staged: the app restarts, tries it, and says how it went
      wait_restarted "$svc" "$prev" 300 || fail "S9[$k]: the app did not come back after the staged restore"
      read_last_restore "$svc"
      rst_ok=$RESTORE_OK
    fi
    case $k in
      gzcrc | gztrailer | garbage | garbage-inside) cls=accepted ;; # the payload is checksummed; bytes after the last line aren't read
      header-older) cls=forged ;;
      header-newer) cls=newer ;;
      *) cls=rejected ;;
    esac
    case $cls in
      rejected)
        if [ "$up_code" = 422 ] && [ -n "$up_msg" ]; then pass "S9[$k]: upload refused" "$up_msg"; else fail "S9[$k]: upload should be refused" "http $up_code $up_msg"; fi
        case $listed in "problem: "*) pass "S9[$k]: placed in the folder, the list marks it damaged" "$listed" ;; *) fail "S9[$k]: list should show a problem" "$listed" ;; esac
        if [ "$chk_code" = 422 ] && [ -n "$chk_msg" ]; then pass "S9[$k]: check refused" "$chk_msg"; else fail "S9[$k]: check should be refused" "http $chk_code $chk_msg"; fi
        if [ "$rst_code" = 422 ] && [ -n "$rst_msg" ]; then pass "S9[$k]: restore refused" "$rst_msg"; else fail "S9[$k]: restore should be refused" "http $rst_code $rst_msg"; fi
        if grep -Eqi 'gzip:|flate|json:|invalid character|unexpected EOF|runtime|sql:' <<<"$up_msg $chk_msg $rst_msg"; then note "S9[$k]: the refusal message carries a Go error text (cosmetic): $up_msg"; fi
        ;;
      accepted)
        note "S9[$k]: upload http $up_code, list '$listed', check http $chk_code, restore http $rst_code ok='$rst_ok' (the checksummed content is intact; extra or damaged bytes after the last line are not read)"
        if [ "$up_code" = 422 ]; then pass "S9[$k]: refused at upload (acceptable)" "$up_msg";
        elif [ "$up_code" = 201 ] && [ "$chk_code" = 200 ] && [ "$rst_code" = 202 ] && [ "$rst_ok" = true ]; then
          pass "S9[$k]: ACCEPTED (not detected), but it restores correctly: the verified content is intact" "upload 201, check 200, restore ok"
        else fail "S9[$k]: unexpected handling" "upload $up_code, check $chk_code, restore $rst_code ok='$rst_ok'"; fi
        ;;
      newer)
        # a consistent file that says it is from a newer Reelkeeper: valid as a file, refused where it matters
        note "S9[$k]: upload http $up_code, list '$listed', check http $chk_code $chk_msg, restore http $rst_code $rst_msg"
        if [ "$chk_code" = 422 ] && grep -qi newer <<<"$chk_msg"; then pass "S9[$k]: check refuses it as newer" "$chk_msg"; else fail "S9[$k]: check of a newer file" "http $chk_code $chk_msg"; fi
        if [ "$rst_code" = 422 ] && grep -qi newer <<<"$rst_msg"; then pass "S9[$k]: restore refuses it as newer" "$rst_msg"; else fail "S9[$k]: restore of a newer file" "http $rst_code $rst_msg"; fi
        if [ "$up_code" = 201 ]; then note "S9[$k]: the upload accepts a file from a newer Reelkeeper and the list shows no problem for it; only check and restore say 'newer'"; fi
        ;;
      forged)
        # a consistent file whose header lies about the schema version: it passes every check; the restore itself must fail safely
        note "S9[$k]: upload http $up_code, check http $chk_code, restore http $rst_code, result ok='$rst_ok': ${RESTORE_MSG:-$rst_msg}"
        if [ "$up_code" = 422 ] || [ "$chk_code" = 422 ] || [ "$rst_code" = 422 ]; then pass "S9[$k]: refused up front" "$up_msg $chk_msg $rst_msg";
        elif [ "$rst_ok" = false ]; then pass "S9[$k]: passed the checks but the restore failed safely at startup" "$RESTORE_MSG";
        else fail "S9[$k]: a forged older-schema header was restored?" "upload $up_code, restore ok='$rst_ok' $RESTORE_MSG"; fi
        ;;
    esac
    after=$(fp "$svc")
    if [ "$before" = "$after" ]; then pass "S9[$k]: the database is unchanged" "$after"; else fail "S9[$k]: the database changed" "$before -> $after"; fi
    if ping_ok "$svc"; then pass "S9[$k]: the app is up"; else fail "S9[$k]: the app is not answering"; wait_ping "$svc" 60; fi
    UPNAME_KEEP=$UPNAME
    d find "/vol/$svc/backups" -name 'reelkeeper-2020*' -delete
    if [ -n "$UPNAME" ]; then api "$svc" DELETE "/backups/$UPNAME" >/dev/null; fi
  done
  api "$svc" GET /backups
  if [ "$CODE" = 200 ]; then pass "S9: the backups list still works"; else fail "S9: list after all variants" "http $CODE"; fi
  if make_backup "$svc"; then pass "S9: a new backup still works"; else fail "S9: backup after variants" "http $CODE $(msg)"; fi
  leftovers=$(d sh -c "ls -A /vol/$svc/backups | grep -c '^\\.' || true")
  note "S9: hidden temp files in the backup folder afterwards: $leftovers"
}

# counts_match SVC ROWS_JSON: the table counts equal the backup header's counts (sessions and goose aside)
counts_match() {
  local svc=$1 rows=$2 counts
  counts=$(lt counts --db "$(inst_db "$svc")")
  jq -ne --argjson a "$counts" --argjson b "$rows" '($a | del(.sessions)) == $b' >/dev/null
}

# Scenario 12: backups made while the database is being changed are consistent snapshots.
s_busy() {
  scenario "S12: backups while Reelkeeper is busy"
  local src i name holder tgt rows
  for src in rk-sqlite rk-pg; do
    holder=$(holds "$src")
    : >"$OUT/busy.names.$src"
    docker exec -d -u 65532:65532 "$DRIVER" sh -c "/labbin/labtool mutate --db '$(inst_db "$src")' --seconds 90 --pause 2ms >/work/busy.mutate.$src.log 2>&1"
    docker exec -d "$DRIVER" bash -c "bash /lab/writer.sh '$(inst_url "$src")' '$(cat "$(keyfile "$src")")' 90 >/work/busy.writer.$src.log 2>&1"
    sleep 2
    for i in 1 2 3 4 5 6; do
      if make_backup "$src"; then
        download_backup "$src" "$BNAME" "busy.$src.$i.backup" >/dev/null
        echo "$BNAME" >>"$OUT/busy.names.$src"
        api "$src" POST "/backups/$BNAME/check"
        printf '%s\n' "$(jq -c .rows <<<"$BODY")" >"$OUT/busy.rows.$src.$i"
        pass "S12: $src backup $i made while mutating" "${BSECS}s, $BSIZE bytes, peak ${BMEM} MiB"
        metric "backup while busy" "$(inst_engine "$src")" "" "$BSIZE" "$BSECS" "$BMEM" "$BRSS"
      else fail "S12: $src backup $i while busy" "http $CODE $(msg)"; fi
      sleep 3
    done
    for _ in $(seq 1 60); do
      grep -q '^mutate:' <(d cat "/work/busy.mutate.$src.log" 2>/dev/null) && break
      sleep 2
    done
    note "S12 $src mutate: $(d cat "/work/busy.mutate.$src.log" | tr '\n' ' ' | cut -c1-400)"
    wr=$(d cat "/work/busy.writer.$src.log")
    total=$(wc -l <<<"$wr" | tr -d ' ')
    bad=$(grep -vc '^200 ' <<<"$wr")
    maxms=$(awk '{ if ($2 > m) m = $2 } END { print m + 0 }' <<<"$wr")
    slow=$(awk '$2 > 1000 { n++ } END { print n + 0 }' <<<"$wr")
    note "S12 $src: an app write (PUT /settings/general) every ~0.25s during the backups: $total writes, $bad not 200, slowest ${maxms} ms, $slow slower than 1 s"
    if [ "$bad" = 0 ]; then pass "S12: $src app writes kept working during the backups" "slowest ${maxms} ms"; else fail "S12: $src app writes failed during backups" "$bad of $total failed; $(grep -v '^200 ' <<<"$wr" | sort | uniq -c | head -3 | tr '\n' ' ')"; fi
    # A SQLite backup is a read-only snapshot transaction, so it must not hold app writes up (it used to:
    # the slowest write was 1.7 s at 1x). Postgres never did.
    if [ "$(inst_engine "$src")" = sqlite ]; then
      if [ "$slow" = 0 ]; then pass "S12: $src: no app write was held up for a second by a backup" "slowest ${maxms} ms"; else fail "S12: $src: app writes were held up by a backup" "$slow writes took over 1 s, slowest ${maxms} ms"; fi
    fi
    # every backup restores into a fresh instance (alternating engines)
    i=0
    while read -r name <&3; do
      i=$((i + 1))
      if [ $((i % 2)) = 1 ]; then tgt=rk-fresh-pg; else tgt=rk-fresh-sqlite; fi
      fresh_empty "$tgt" || continue
      if first_run_restore "$tgt" "busy.$src.$i.backup" "$holder" && [ "$RESTORE_OK" = true ]; then
        rows=$(cat "$OUT/busy.rows.$src.$i")
        if counts_match "$tgt" "$rows"; then pass "S12: $src backup $i restored into $tgt, row counts match the file's header" "$(jq -r '[.[]]|add' <<<"$rows") rows"; else fail "S12: $src backup $i row counts differ from the header" "$(lt counts --db "$(inst_db "$tgt")" | cut -c1-300)"; fi
        lt fkcheck --db "$(inst_db "$tgt")" >"$OUT/fk.txt" 2>&1 && pass "S12: $src backup $i: fkcheck clean" || fail "S12: $src backup $i: fkcheck" "$(tail -3 "$OUT/fk.txt" | tr '\n' ' ')"
      else fail "S12: restore of $src backup $i into $tgt" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
    done 3<"$OUT/busy.names.$src"
  done
}

# ctr_ls SVC PATH: list a directory inside a container's filesystem (no shell in the image)
ctr_ls() {
  local pid
  pid=$(docker inspect -f '{{.State.Pid}}' "$(cname "$1")")
  colima ssh -- sudo ls -A "/proc/$pid/root$2" 2>&1 </dev/null
}

# Scenario 13: no space, read-only, no permission.
s_nospace() {
  scenario "S13: backup folder full, read-only, or not writable"
  local svc folder code before after name small
  # a small valid backup, from a fresh instance with the lab key
  fresh_empty rk-fresh-sqlite && setup_inst rk-fresh-sqlite && make_backup rk-fresh-sqlite && download_backup rk-fresh-sqlite "$BNAME" "S13.small.backup" >/dev/null
  small=$(d stat -c %s /work/S13.small.backup)
  note "S13: small backup for the full-folder restore test is $small bytes"
  for svc in rk-sqlite rk-pg; do
    api "$svc" GET /settings/backups
    for folder in /tiny /readonly /rootowned /rootowned/sub /nonexistent-parent/sub; do
      api "$svc" PUT /settings/backups -H 'Content-Type: application/json' -d "{\"folder\":\"$folder\",\"scheduled\":true,\"time\":\"03:30\",\"keep\":14}"
      if [ "$CODE" != 200 ]; then fail "S13: $svc: set the folder to $folder" "http $CODE $(msg)"; continue; fi
      api "$svc" POST /backups
      code=$CODE
      m=$(msg)
      if [ "$code" = 500 ] && [ -n "$m" ] && ! grep -qi 'goroutine\|panic\|nil pointer' <<<"$m"; then
        pass "S13: $svc: a backup into $folder fails with a clear error (500)" "$m"
      else fail "S13: $svc: a backup into $folder" "http $code $m"; fi
      if ping_ok "$svc"; then pass "S13: $svc: the app stays up after the failure in $folder"; else fail "S13: $svc: app health after $folder"; fi
      case $folder in
        /tiny)
          left=$(ctr_ls "$svc" /tiny | tr '\n' ' ')
          if [ -z "${left// /}" ]; then pass "S13: $svc: no partial file left in the full folder"; else fail "S13: $svc: files left in /tiny" "$left"; fi
          note "S13: $svc /tiny after the failed backup: [$left]"
          ;;
      esac
    done
    # a full folder and a restore: the safety backup can't be written, so nothing is restored
    api "$svc" PUT /settings/backups -H 'Content-Type: application/json' -d '{"folder":"/tiny","scheduled":true,"time":"03:30","keep":14}'
    upload_backup "$svc" "S13.small.backup"
    if [ "$CODE" = 201 ]; then
      name=$UPNAME
      before=$(fp "$svc")
      restore_backup "$svc" "$name" "$(holds "$svc")"
      if [ "$RESTORE_OK" = false ] && grep -qi 'safety backup' <<<"$RESTORE_MSG"; then pass "S13: $svc: a restore with a full backup folder is skipped, saying why" "$RESTORE_MSG"; else fail "S13: $svc: restore with a full folder" "ok='$RESTORE_OK' http $RCODE $RESTORE_MSG"; fi
      after=$(fp "$svc")
      if [ "$before" = "$after" ]; then pass "S13: $svc: the database is unchanged after the skipped restore"; else fail "S13: $svc: database changed" "$before -> $after"; fi
      note "S13: $svc /tiny after the skipped restore: [$(ctr_ls "$svc" /tiny | tr '\n' ' ')]"
    else fail "S13: $svc: upload a small backup into /tiny" "http $CODE $(msg)"; fi
    # upload of a big backup into the full folder
    upload_backup "$svc" "rk-sqlite.B1.backup"
    if [ "$CODE" = 500 ] || [ "$CODE" = 422 ]; then pass "S13: $svc: a big upload into the full folder fails clearly" "http $CODE $(msg)"; else fail "S13: $svc: upload into a full folder" "http $CODE $(msg)"; fi
    note "S13: $svc /tiny after the failed upload: [$(ctr_ls "$svc" /tiny | tr '\n' ' ')]"
    api "$svc" PUT /settings/backups -H 'Content-Type: application/json' -d '{"folder":"","scheduled":true,"time":"03:30","keep":14}'
    if make_backup "$svc"; then pass "S13: $svc: with the folder setting back to normal, a backup works" "$BNAME"; else fail "S13: $svc: backup after resetting the folder" "http $CODE $(msg)"; fi
  done
}

# Scenario 14: retention, the schedule and name collisions.
s_retention() {
  scenario "S14: retention, the schedule, same-second names"
  local svc=rk-fresh-sqlite i n names good u1 u2 prev
  SMOKE_SKIP="" provision "$svc" 0.1 || return
  save_orig "$svc"
  api "$svc" PUT /settings/backups -H 'Content-Type: application/json' -d '{"folder":"","scheduled":true,"time":"03:30","keep":3}'
  make_backup "$svc" && good=$BNAME && download_backup "$svc" "$good" "S14.good.backup" >/dev/null
  api "$svc" PUT /settings/general -H 'Content-Type: application/json' -d '{"instanceName":"Lab second file","logLevel":"info"}'
  sleep 1.1
  make_backup "$svc" && download_backup "$svc" "$BNAME" "S14.other.backup" >/dev/null
  api "$svc" PUT /settings/general -H 'Content-Type: application/json' -d '{"instanceName":"Lab fresh-sqlite 🧪","logLevel":"info"}'
  # uploads are never pruned
  upload_backup "$svc" "S14.good.backup"; u1=$UPNAME
  sleep 1.2
  upload_backup "$svc" "S14.good.backup"; u2=$UPNAME
  for i in 1 2 3 4 5 6; do
    make_backup "$svc" || fail "S14: manual backup $i" "http $CODE $(msg)"
    sleep 1.2
  done
  api "$svc" GET /backups
  n=$(jq '[.backups[] | select(.kind == "manual" or .kind == "scheduled") | select(.name | endswith("-uploaded.backup") | not)] | length' <<<"$BODY")
  if [ "$n" = 3 ]; then pass "S14: keep=3 and 6 manual backups: 3 manual/scheduled remain"; else fail "S14: retention" "$n manual/scheduled remain; $(jq -c '[.backups[]|.name]' <<<"$BODY")"; fi
  up=$(jq '[.backups[] | select(.name | endswith("-uploaded.backup"))] | length' <<<"$BODY")
  if [ "$up" = 2 ]; then pass "S14: uploaded backups are never pruned" "$up kept"; else fail "S14: uploaded backups" "$up remain (uploaded $u1 and $u2)"; fi
  # five restores: safety backups capped at 3, and they are the newest
  : >"$OUT/safeties"
  sleep 1.2
  upload_backup "$svc" "S14.good.backup"
  gu=$UPNAME
  for i in 1 2 3 4 5; do
    restore_backup "$svc" "$gu" "$svc" || { fail "S14: restore $i" "http $RCODE"; break; }
    echo "$RESTORE_SAFETY" >>"$OUT/safeties"
    sleep 1.2
  done
  api "$svc" GET /backups
  n=$(jq '[.backups[] | select(.kind == "safety")] | length' <<<"$BODY")
  if [ "$n" = 3 ]; then pass "S14: after 5 restores, safety backups are capped at 3"; else fail "S14: safety cap" "$n safety backups: $(jq -c '[.backups[]|select(.kind=="safety")|.name]' <<<"$BODY")"; fi
  newest3=$(tail -3 "$OUT/safeties" | sort | tr '\n' ' ')
  have=$(jq -r '[.backups[] | select(.kind == "safety") | .name] | sort | join(" ")' <<<"$BODY")
  if [ "$(echo $newest3)" = "$(echo $have)" ]; then pass "S14: the 3 kept are the newest 3"; else fail "S14: which safety backups were kept" "kept: $have; newest: $newest3"; fi
  # same-second names: wait for a second boundary, then make two backups back to back
  perl -MTime::HiRes=time,sleep -e '$t=time; sleep(1.02-($t-int($t)))'
  api "$svc" POST /backups; n1=$(jq -r .name <<<"$BODY")
  api "$svc" POST /backups; n2=$(jq -r .name <<<"$BODY")
  api "$svc" GET /backups
  if [ "$n1" != "$n2" ] && jq -e --arg a "$n1" --arg b "$n2" '[.backups[].name] | (index($a) != null) and (index($b) != null)' <<<"$BODY" >/dev/null; then
    pass "S14: two manual backups made back to back both survive, with different names" "$n1, $n2"
  else fail "S14: two manual backups made back to back: one replaced the other" "$n1 / $n2"; fi
  stamp() { sed -n 's/^reelkeeper-\([0-9]*-[0-9]*\).*/\1/p' <<<"$1"; }
  if [ "$(stamp "$n1")" = "$(stamp "$n2")" ]; then pass "S14: those two backups were made in the same second, so the collision case was exercised" "$(stamp "$n1")"; else note "S14: the two back-to-back backups fell in different seconds ($n1, $n2), so this run did not exercise the collision"; fi
  perl -MTime::HiRes=time,sleep -e '$t=time; sleep(1.02-($t-int($t)))'
  upload_backup "$svc" "S14.good.backup"; n1=$UPNAME
  upload_backup "$svc" "S14.other.backup"; n2=$UPNAME
  api "$svc" GET /backups
  if [ -n "$n1" ] && [ "$n1" != "$n2" ] && jq -e --arg a "$n1" --arg b "$n2" '[.backups[].name] | (index($a) != null) and (index($b) != null)' <<<"$BODY" >/dev/null; then
    pass "S14: two uploads made back to back both survive, with different names" "$n1, $n2"
  else fail "S14: two uploads made back to back: one replaced the other" "$n1 / $n2"; fi
  if [ "$(stamp "$n1")" = "$(stamp "$n2")" ]; then pass "S14: those two uploads were made in the same second" "$(stamp "$n1")"; else note "S14: the two uploads fell in different seconds ($n1, $n2), so this run did not exercise the collision"; fi
  # the scheduled backup
  api "$svc" PUT /settings/backups -H 'Content-Type: application/json' -d '{"folder":"","scheduled":true,"time":"00:00","keep":14}'
  api "$svc" GET /backups
  for name in $(jq -r '.backups[] | select(.kind == "scheduled") | .name' <<<"$BODY"); do api "$svc" DELETE "/backups/$name" >/dev/null; done
  lt touchjob --db "$(inst_db "$svc")" --kind backup.scheduled --ago 20m >/dev/null
  for _ in $(seq 1 45); do
    api "$svc" GET /backups
    n=$(jq '[.backups[] | select(.kind == "scheduled")] | length' <<<"$BODY")
    [ "$n" -ge 1 ] && break
    sleep 2
  done
  if [ "$n" = 1 ]; then pass "S14: the schedule made exactly one scheduled backup when it came due"; else fail "S14: scheduled backups after the task came due" "$n"; fi
  lt touchjob --db "$(inst_db "$svc")" --kind backup.scheduled --ago 20m >/dev/null
  sleep 75
  api "$svc" GET /backups
  n=$(jq '[.backups[] | select(.kind == "scheduled")] | length' <<<"$BODY")
  if [ "$n" = 1 ]; then pass "S14: the task came due again the same day and made no second scheduled backup"; else fail "S14: a second scheduled backup the same day" "$n"; fi
  # a scheduled backup is made by a running task, so its snapshot holds that task's own job as "running";
  # restoring it must recover that job instead of leaving it stuck
  api "$svc" GET /backups
  sched=$(jq -r '[.backups[] | select(.kind == "scheduled")][0].name' <<<"$BODY")
  restore_backup "$svc" "$sched" "$svc"
  if [ "$RESTORE_OK" = true ]; then pass "S14: restoring the scheduled backup works" "$RESTORE_MSG"; else fail "S14: restore of the scheduled backup" "ok='$RESTORE_OK' $RESTORE_MSG"; fi
  sleep 20
  stuck=$(lt sql --db "$(inst_db "$svc")" --query "SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')" | tail -1)
  if [ "$stuck" = 0 ]; then pass "S14: no job is left queued or running 20 s after restoring a backup that was made by a running task"; else fail "S14: stuck jobs after restoring a scheduled backup" "$stuck"; fi
  api "$svc" POST /system/tasks/backup.scheduled/run
  sleep 6
  api "$svc" GET /backups
  note "S14: after 'Run now' on the backup task, kinds are: $(jq -c '[.backups[].kind] | group_by(.) | map({(.[0]): length}) | add' <<<"$BODY") ('Run now' makes a manual backup, not a scheduled one)"
}

# --- the big instances: timing, and crashes ------------------------------------------------------------------
BIGSCALE=${BIGSCALE:-3}

# crash_restore SVC: kill the app at several points of a restore; the database must be exactly the old state or exactly the new one.
crash_restore() {
  local svc=$1 eng fpa fpb ba bb cur target name prev started delay f res i window out
  eng=$(inst_engine "$svc")
  scenario "S10: crash in the middle of a restore ($eng, scale $BIGSCALE)"
  # state A (now) and state B (after heavy changes), each with a backup
  fpa=$(fp "$svc")
  make_backup "$svc" || { fail "S10: backup of state A" "http $CODE"; return; }
  ba=$BNAME
  lt mutate --db "$(inst_db "$svc")" --ops 8000 >/dev/null 2>&1
  fpb=$(fp "$svc")
  make_backup "$svc" || { fail "S10: backup of state B" "http $CODE"; return; }
  bb=$BNAME
  if [ "$fpa" = "$fpb" ]; then fail "S10: states A and B should differ" "$fpa"; return; fi
  # one normal restore to A: its time is the window to hit
  restore_backup "$svc" "$ba" "$svc"
  if [ "$RESTORE_OK" = true ] && [ "$(fp "$svc")" = "$fpa" ]; then pass "S10: a normal restore of A works and takes ${RSECS}s (the window to kill in)"; else fail "S10: normal restore of A" "ok='$RESTORE_OK'"; return; fi
  window=$RSECS
  cur=A
  i=0
  for f in 0.04 0.2 0.4 0.6 0.8 0.95; do
    i=$((i + 1))
    if [ $cur = A ]; then target=B; name=$bb; else target=A; name=$ba; fi
    prev=$(started_at "$svc")
    api "$svc" GET /backups
    prevat=$(jq -r '.lastRestore.at // ""' <<<"$BODY")
    api "$svc" POST "/backups/$name/restore"
    if [ "$CODE" != 202 ]; then fail "S10: run $i: stage the restore" "http $CODE $(msg)"; continue; fi
    # wait for the new process (the old one stops about a second after the request)
    for _ in $(seq 1 400); do [ "$(started_at "$svc")" != "$prev" ] && break; psleep 0.05; done
    started=$(started_at "$svc")
    delay=$(perl -e "printf '%.2f', ($f * ($window - 1.2))")
    psleep "$delay"
    docker kill "$(cname "$svc")" >/dev/null 2>&1
    # it should restart by itself
    for _ in $(seq 1 10); do [ "$(started_at "$svc")" != "$started" ] && break; sleep 0.3; done
    if [ "$(started_at "$svc")" = "$started" ]; then docker start "$(cname "$svc")" >/dev/null; killstart="started by hand (docker doesn't restart a killed container)"; else killstart="restarted by docker"; fi
    if wait_ping "$svc" 300; then
      out=$(fp "$svc")
      if [ "$out" = "$fpa" ]; then res=A; elif [ "$out" = "$fpb" ]; then res=B; else res=NEITHER; fi
      # what happened to the request
      pend=$(d sh -c "test -e /vol/$svc/restore-pending.json && echo still-pending || echo cleared")
      rj=$(d cat "/vol/$svc/restore-result.json" 2>/dev/null)
      safeties=$(d sh -c "ls /vol/$svc/backups | grep -c safety")
      temps=$(d sh -c "ls -A /vol/$svc/backups | grep -c '^\\.' || true")
      api "$svc" GET /backups
      last=$(jq -r '.lastRestore | if . then "\(.backup) ok=\(.ok) at \(.at)" else "none" end' <<<"$BODY")
      lastname=$(jq -r '.lastRestore.backup // ""' <<<"$BODY")
      lastat=$(jq -r '.lastRestore.at // ""' <<<"$BODY")
      lastok=$(jq -r 'if .lastRestore then (.lastRestore.ok | tostring) else "" end' <<<"$BODY")
      lastmsg=$(jq -r '.lastRestore.message // ""' <<<"$BODY")
      if [ "$res" = NEITHER ]; then
        fail "S10: run $i (kill at ${delay}s of a ${window}s restore): the database is neither state A nor state B" "$out vs A $fpa / B $fpb"
      else
        if [ "$res" = "$target" ]; then outcome="the restore completed (database = new state)"; cur=$target; else outcome="the restore did not happen (database = old state)"; fi
        pass "S10: run $i (kill ${delay}s after the new process started, $(perl -e "printf '%.0f', 100*$f")% of the window): the database is exactly $res, the app started" "$outcome"
        # The result on the Backups page must describe THIS restore: done (the restore ran again after
        # the restart) or interrupted (the old data is still there), never the previous restore's.
        if [ "$res" = "$target" ]; then
          if [ "$lastname" = "$name" ] && [ "$lastok" = true ] && [ "$lastat" != "$prevat" ]; then pass "S10: run $i: the last-restore result names this restore and says it was restored" "$lastmsg"; else fail "S10: run $i: the database is the new state but the last-restore result is not this restore's success" "$last: $lastmsg"; fi
        else
          if [ "$lastname" = "$name" ] && [ "$lastok" = false ] && [ "$lastat" != "$prevat" ] && grep -q 'stopped during the restore' <<<"$lastmsg"; then pass "S10: run $i: the last-restore result names this restore and says it was interrupted" "$lastmsg"; else fail "S10: run $i: after a crash mid-restore the last-restore result does not say so" "$last: $lastmsg"; fi
        fi
        note "S10 $eng run $i: killed at ${delay}s -> database $res; request file: $pend; lastRestore: $last; safety backups in the folder: $safeties; hidden temp files in the folder: $temps; after the kill the container was $killstart"
      fi
    else
      fail "S10: run $i: the app did not come back after the kill" "$(docker logs --tail 5 "$(cname "$svc")" 2>&1 | tail -3 | tr '\n' ' ' | cut -c1-300)"
    fi
  done
  # the changed state B holds the lab's random mutations (some are not valid app data); finish on state A
  if [ $cur = B ]; then
    restore_backup "$svc" "$ba" "$svc"
    [ "$(fp "$svc")" = "$fpa" ] || fail "S10: could not return to state A" "$RESTORE_MSG"
  fi
  smoke_check "S10: the instance works after the crashes (state A)" "$svc" --creds
}

# crash_backup SVC: kill the app while a backup is being written.
crash_backup() {
  local svc=$1 eng before names started delay f temps listed
  eng=$(inst_engine "$svc")
  scenario "S11: crash in the middle of a backup ($eng, scale $BIGSCALE)"
  api "$svc" GET /backups
  before=$(jq -r '[.backups[].name] | sort | join(" ")' <<<"$BODY")
  temps0=$(d sh -c "ls -A /vol/$svc/backups | grep -c '^\.writing' || true")
  for f in 0.15 0.4 0.7 0.9; do
    docker exec -d "$DRIVER" sh -c "curl -s -m 120 -X POST -H 'X-Api-Key: $(cat "$(keyfile "$svc")")' -o /work/crashbackup.out -w '%{http_code}' $(inst_url "$svc")/api/v1/backups >/work/crashbackup.code 2>&1"
    started=$(started_at "$svc")
    delay=$(perl -e "printf '%.2f', $f * ${BSECS:-3}")
    psleep "$delay"
    docker kill "$(cname "$svc")" >/dev/null 2>&1
    for _ in $(seq 1 10); do [ "$(started_at "$svc")" != "$started" ] && break; sleep 0.3; done
    if [ "$(started_at "$svc")" = "$started" ]; then docker start "$(cname "$svc")" >/dev/null; fi
    wait_ping "$svc" 120 || { fail "S11: the app did not come back (kill at ${delay}s)"; continue; }
    api "$svc" GET /backups
    names=$(jq -r '[.backups[].name] | sort | join(" ")' <<<"$BODY")
    problems=$(jq -r '[.backups[] | select(.problem != null)] | length' <<<"$BODY")
    temps=$(d sh -c "ls -A /vol/$svc/backups | grep '^\\.' | tr '\\n' ' '")
    # Nothing from before may be gone, nothing may be damaged, and at most the backup that was being made may
    # have been finished by the moment of the kill (a kill near the very end of a backup lands after it).
    lost=0
    for n in $before; do case " $names " in *" $n "*) ;; *) lost=$((lost + 1)) ;; esac; done
    extra=$(($(wc -w <<<"$names") - $(wc -w <<<"$before") + lost))
    if [ "$lost" = 0 ] && [ "$extra" -le 1 ] && [ "$problems" = 0 ]; then
      pass "S11: kill at ${delay}s: no partial .backup in the list, nothing damaged" "$([ "$extra" = 1 ] && echo "the backup had just finished")"
    else fail "S11: kill at ${delay}s: the list changed" "before: $before | after: $names | $problems damaged"; fi
    note "S11 $eng kill at ${delay}s: temp files in the folder after the restart: [${temps}]"
    if [ -z "${temps// /}" ]; then pass "S11: kill at ${delay}s: the restart removed the killed backup's temp file"; else fail "S11: kill at ${delay}s: temp files remain after the restart" "$temps"; fi
    if make_backup "$svc"; then pass "S11: the next backup works after the crash" "$BNAME"; else fail "S11: backup after the crash" "http $CODE $(msg)"; fi
    api "$svc" GET /backups
    before=$(jq -r '[.backups[].name] | sort | join(" ")' <<<"$BODY")
  done
  total=$(d sh -c "ls -A /vol/$svc/backups | grep -c '^\\.writing' || true")
  size=$(d sh -c "du -ck /vol/$svc/backups/.writing* 2>/dev/null | tail -1 | cut -f1")
  note "S11 $eng: ${total} .writing-* temp files are in the backup folder after 4 kills and restarts (${size:-0} KB)"
  if [ "$total" = 0 ]; then pass "S11: no .writing-* temp files remain after 4 kills and restarts"; else fail "S11: .writing-* temp files remain in the backup folder after the restarts" "${total} in the folder, ${size:-0} KB"; fi
}

# Scenario 15 and the big-instance crash scenarios.
s_big() {
  local svc eng rows
  # the older-version and wrong-key instances are done; free their memory for the big ones
  for svc in rk-old081 rk-old081-pg rk-old084 rk-old084-sqlite rk-wrongkey; do stop_inst "$svc"; done
  for svc in ${BIG_ONLY:-rk-fresh-pg rk-fresh-sqlite}; do
    eng=$(inst_engine "$svc")
    scenario "S15: size and timing at $BIGSCALE x the default scale ($eng)"
    SMOKE_SKIP="" provision "$svc" "$BIGSCALE" || continue
    save_orig "$svc"
    rows=$(lt counts --db "$(inst_db "$svc")" | jq '[.[]]|add')
    docker exec -d "$DRIVER" bash -c "bash /lab/writer.sh '$(inst_url "$svc")' '$(cat "$(keyfile "$svc")")' 25 >/work/big.writer.$svc.log 2>&1"
    sleep 2
    if make_backup "$svc"; then
      pass "S15: $svc backup of $rows rows" "${BSECS}s, $BSIZE bytes, peak ${BMEM} MiB (docker stats), ${BRSS} MiB rss"
      metric "backup, ${BIGSCALE}x scale" "$eng" "$rows" "$BSIZE" "$BSECS" "$BMEM" "$BRSS"
    else fail "S15: big backup on $svc" "http $CODE $(msg)"; continue; fi
    name=$BNAME
    sleep 25
    wr=$(d cat "/work/big.writer.$svc.log")
    note "S15 $svc: an app write (PUT /settings/general) every ~0.25s around a ${BSECS}s backup of $rows rows: $(wc -l <<<"$wr" | tr -d ' ') writes, $(grep -vc '^200 ' <<<"$wr") not 200 ($(grep -v '^200 ' <<<"$wr" | sort | uniq -c | head -2 | tr '\n' ' ')), slowest $(awk '{ if ($2 > m) m = $2 } END { print m + 0 }' <<<"$wr") ms"
    if [ "$(grep -vc '^200 ' <<<"$wr")" != 0 ]; then fail "S15: $svc: app writes FAIL while a ${BSECS}s backup runs" "$(grep -v '^200 ' <<<"$wr" | sort | uniq -c | head -2 | tr '\n' ' ')"; else pass "S15: $svc: app writes survived the ${BSECS}s backup" "slowest $(awk '{ if ($2 > m) m = $2 } END { print m + 0 }' <<<"$wr") ms"; fi
    download_backup "$svc" "$name" "big.$svc.backup" >/dev/null
    if restore_backup "$svc" "$name" "$svc" && [ "$RESTORE_OK" = true ]; then
      pass "S15: $svc restore of $rows rows" "${RSECS}s, peak ${RMEM} MiB (docker stats), ${RRSS} MiB rss"
      metric "restore, ${BIGSCALE}x scale" "$eng" "$rows" "$BSIZE" "$RSECS" "$RMEM" "$RRSS"
    else fail "S15: big restore on $svc" "http $RCODE ${RMSG0:-} ok='$RESTORE_OK' $RESTORE_MSG"; fi
    api "$svc" GET /backups
    note "S15 $svc: the first GET /backups after the restart took ${LISTSECS}s; $(jq '.backups | length' <<<"$BODY") backups totalling $(jq '[.backups[].size] | add' <<<"$BODY") bytes"
    # The results of reading each backup in full are saved, so a restart doesn't read them all again
    # (it took 9 s for three big files at 3x before).
    if (( $(perl -e "print (${LISTSECS:-99} < 1 ? 1 : 0)") )); then pass "S15: $svc: the first GET /backups after a restart is fast (no full re-read of the backup files)" "${LISTSECS}s"; else fail "S15: $svc: the first GET /backups after a restart took ${LISTSECS}s; the files were read again" "$(jq '.backups | length' <<<"$BODY") backups, $(jq '[.backups[].size] | add' <<<"$BODY") bytes"; fi
    if (( $(perl -e "print (${RRSS:-0} > 250 ? 1 : 0)") )); then note "S15 $svc: restore peak RSS ${RRSS} MiB is above CLAUDE.md's 250 MB steady-state target (a one-off spike during restore)"; fi
    if (( $(perl -e "print (${BRSS:-0} > 250 ? 1 : 0)") )); then note "S15 $svc: backup peak RSS ${BRSS} MiB is above CLAUDE.md's 250 MB steady-state target"; fi
    if [ "$eng" = sqlite ]; then note "S15 $svc: files in the data folder after the restore: $(d sh -c "ls -l /vol/$svc | grep -v '^total' | awk '{print \$5, \$9}' | tr '\n' ';'")"; fi
    smoke_check "S15: $svc works after the big restore" "$svc" --creds
    crash_restore "$svc"
    crash_backup "$svc"
  done
}

# Scenario 16: the existing opt-in test against a real backup (read-only; the file is never copied).
s_real() {
  scenario "S16: TestRealBackup against a real backup (SQLite and Postgres)"
  local f=${REAL_BACKUP:-} pgurl=${REAL_PG_URL:-postgres://reelkeeper:reelkeeper@127.0.0.1:5433/reelkeeper?sslmode=disable} out
  if [ -z "$f" ]; then note "S16: REAL_BACKUP isn't set; skipped"; return; fi
  if [ ! -f "$f" ]; then note "S16: $f not found; skipped"; fail "real backup test" "file not found"; return; fi
  if ! docker ps --format '{{.Names}}' | grep -q '^reelkeeper-dev-postgres$'; then
    note "S16: the dev stack's Postgres isn't running; starting only its postgres service (dev/compose.yaml itself is untouched)"
    docker compose -f "$ROOT/dev/compose.yaml" up -d --wait postgres >/dev/null 2>&1
  fi
  out=$(cd "$ROOT" && REELKEEPER_TEST_POSTGRES="$pgurl" REELKEEPER_REAL_BACKUP="$f" go test ./internal/backup -run TestRealBackup -v -count=1 2>&1)
  rc=$?
  echo "$out" | sed 's/^/    /' | tail -8
  printf '%s\n' "$out" >"$OUT/real-backup-test.txt"
  if [ $rc = 0 ] && grep -q -- '--- PASS: TestRealBackup' <<<"$out" && ! grep -q 'SKIP' <<<"$out"; then
    pass "S16: TestRealBackup passes on SQLite and Postgres" "$(grep -o '[0-9,]* rows in [0-9]* tables[^"]*' <<<"$out" | head -1)"
  else fail "S16: TestRealBackup" "$(tail -5 <<<"$out" | tr '\n' ' ' | cut -c1-400)"; fi
}

# Scenario 17 (extra): SQLite accepts values its columns' types say it shouldn't; a backup must not turn that into a trap.
s_loose() {
  scenario "S17: SQLite's loose typing: odd values in a backup"
  local svc=rk-fresh-sqlite pg=rk-fresh-pg before after name file
  SMOKE_SKIP="" provision "$svc" 0.1 || return
  save_orig "$svc"
  # (a) an integer too big for Postgres' 32-bit INTEGER: fine in SQLite
  lt sql --db "$(inst_db "$svc")" --exec "UPDATE titles SET year = 3000000000 WHERE id = (SELECT MIN(id) FROM titles)" >/dev/null
  if make_backup "$svc"; then pass "S17a: a backup of a database holding year=3000000000 works" "$BNAME"; else fail "S17a: backup" "http $CODE $(msg)"; return; fi
  file="S17a.backup"
  download_backup "$svc" "$BNAME" "$file" >/dev/null
  fresh_empty "$pg" && setup_inst "$pg" || return
  save_orig "$pg"
  before=$(fp "$pg")
  upload_backup "$pg" "$file"
  api "$pg" POST "/backups/$UPNAME/check"
  note "S17a: check of the SQLite backup on Postgres says http $CODE $(msg)"
  restore_backup "$pg" "$UPNAME" "$pg"
  after=$(fp "$pg")
  if [ "$RESTORE_OK" = false ] && [ "$before" = "$after" ]; then pass "S17a: Postgres refused the out-of-range value, said so, and kept its data" "$RESTORE_MSG"; else fail "S17a: restore of an out-of-range value into Postgres" "ok='$RESTORE_OK' db unchanged: $([ "$before" = "$after" ] && echo yes || echo NO) $RESTORE_MSG"; fi
  ping_ok "$pg" && pass "S17a: Postgres instance healthy after the refused restore" || fail "S17a: health"
  # (b) text in an integer column
  lt sql --db "$(inst_db "$svc")" --exec "UPDATE titles SET year = 'n/a' WHERE id = (SELECT MIN(id) FROM titles)" >/dev/null
  before=$(fp "$svc")
  if make_backup "$svc"; then pass "S17b: a backup of a database holding the text 'n/a' in an integer column is made" "$BNAME"; else fail "S17b: backup" "http $CODE $(msg)"; return; fi
  name=$BNAME
  api "$svc" POST "/backups/$name/check"
  chk=$CODE
  restore_backup "$svc" "$name" "$svc"
  after=$(fp "$svc")
  note "S17b: check said http $chk; the restore of that same backup said ok='$RESTORE_OK': $RESTORE_MSG"
  if [ "$RESTORE_OK" = true ]; then pass "S17b: such a backup restores"; else fail "S17b: a backup that checks fine (http $chk) can't be restored: SQLite let a text value into an integer column and the restore refuses it" "$RESTORE_MSG"; fi
  if [ "$before" = "$after" ]; then pass "S17b: the database is unchanged either way"; else fail "S17b: database changed" "$before -> $after"; fi
}

run_scenarios() {
  if should_run s1; then s_inplace rk-sqlite "S1"; fi
  if should_run s2; then s_inplace rk-pg "S2"; fi
  if should_run s5; then
    s_undo rk-sqlite "S5a"
    s_undo rk-pg "S5b"
  fi
  if should_run s3; then s_cross; fi
  if should_run s4; then s_firstrun; fi
  if should_run s6; then s_chain; fi
  if should_run s7; then s_old; fi
  if should_run s8; then s_wrongkey; fi
  if should_run s9; then s_damaged; fi
  if should_run s12; then s_busy; fi
  if should_run s13; then s_nospace; fi
  if should_run s14; then s_retention; fi
  if should_run s15; then s_big; fi
  if should_run s16; then s_real; fi
  if should_run s17; then s_loose; fi
}

# --- the report -----------------------------------------------------------------------------------------
write_report() {
  local host
  host=$(uname -srm)
  {
    echo "# Reelkeeper backup lab: results"
    echo
    echo "Run \`$TS\` on $host. Reelkeeper under test: \`$LAB_IMAGE\` $(docker image inspect "$LAB_IMAGE" -f '{{.Id}}' 2>/dev/null | cut -c8-19) ($(docker image inspect "$LAB_IMAGE" -f '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null)), seed scale $SCALE."
    echo
    echo "## Scenarios"
    echo
    echo "| Scenario | Result | Passed | Failed | First failure |"
    echo "| --- | --- | --- | --- | --- |"
    awk -F'\t' '
      { s = $1; if (!(s in seen)) { seen[s] = 1; order[++n] = s }
        if ($2 == "PASS") p[s]++; else { f[s]++; if (!(s in first)) first[s] = $3 ": " $4 } }
      END { for (i = 1; i <= n; i++) { s = order[i]; gsub(/\|/, "/", first[s]);
              printf "| %s | %s | %d | %d | %s |\n", s, (f[s] > 0 ? "FAIL" : "PASS"), p[s], f[s], substr(first[s], 1, 300) } }' "$OUT/checks.tsv"
    echo
    echo "## Every check"
    echo
    echo "| Scenario | Result | Check | Detail |"
    echo "| --- | --- | --- | --- |"
    awk -F'\t' '{ gsub(/\|/, "/", $3); gsub(/\|/, "/", $4); printf "| %s | %s | %s | %s |\n", $1, $2, $3, substr($4, 1, 400) }' "$OUT/checks.tsv"
    echo
    echo "## Timing, size and memory"
    echo
    echo "Peak memory is the highest \`docker stats\` sample of the app container while the step ran (sampled about once a second);"
    echo "RSS is the process's own peak resident size since it started (VmHWM). CLAUDE.md's target is under 250 MB steady state."
    echo
    echo "| Step | Engine | Rows | File bytes | Seconds | Peak MiB (docker stats) | Peak RSS MiB | Over 250 MB? |"
    echo "| --- | --- | --- | --- | --- | --- | --- | --- |"
    awk -F'\t' '{ printf "| %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, (($6 + 0 > 250 || $7 + 0 > 250) ? "YES" : "no") }' "$OUT/metrics.tsv"
    echo
    echo "## Notes"
    echo
    awk -F'\t' '{ printf "- (%s) %s\n", $1, $2 }' "$OUT/notes.tsv"
  } >"$REPORT"
}
