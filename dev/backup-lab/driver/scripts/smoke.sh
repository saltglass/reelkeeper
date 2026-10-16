#!/usr/bin/env bash
# Runs in the driver: proves an instance works. Prints PASS/FAIL lines; exits non-zero on any FAIL.
#
#   smoke.sh BASE_URL USERNAME PASSWORD APIKEY [--creds] [--no-login]
#
# --creds also checks that stored credentials still open (they are sealed in the database with the
# secret key; the API only reports them as set when decrypting works).
set -uo pipefail
base=$1 user=$2 pass=$3 key=$4
shift 4
creds=0 login=1
for a in "$@"; do case $a in --creds) creds=1 ;; --no-login) login=0 ;; esac; done
api=$base/api/v1
hdr=$(mktemp)
trap 'rm -f "$hdr"' EXIT
fails=0
pass_() { echo "PASS $1 ${2:-}"; }
fail_() { echo "FAIL $1 ${2:-}"; fails=$((fails + 1)); }

if [ "$login" = 1 ]; then
  c=$(curl -s -D "$hdr" -o /dev/null -w '%{http_code}' -X POST "$api/auth/login" -H 'content-type: application/json' \
    -d "$(jq -n --arg u "$user" --arg p "$pass" '{username:$u,password:$p}')")
  cookie=$(sed -n 's/^[Ss]et-[Cc]ookie: \(reelkeeper_session=[^;]*\).*/\1/p' "$hdr" | head -1)
  [ "$c" = 204 ] && [ -n "$cookie" ] && pass_ "login($user)" || fail_ "login($user)" "http $c"
  # a signed-in session works for the API too
  c=$(curl -s -o /dev/null -w '%{http_code}' -H "Cookie: $cookie" "$api/movies")
  [ "$c" = 200 ] && pass_ "session cookie works" || fail_ "session cookie works" "http $c"
  c=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$api/auth/login" -H 'content-type: application/json' \
    -d '{"username":"seed-user-one","password":"Seed-User-Pass-1!"}')
  [ "$c" = 204 ] && pass_ "login(seed-user-one)" || fail_ "login(seed-user-one)" "http $c"
fi

get() { # name path [jq filter that must print "true"]
  local c body
  case " ${SMOKE_SKIP:-} " in *" $1 "*) return ;; esac
  c=$(curl -s -o /tmp/smoke.body -w '%{http_code}' -H "X-Api-Key: $key" "$api$2")
  if [ "$c" != 200 ]; then fail_ "$1" "http $c $(head -c 160 /tmp/smoke.body)"; return; fi
  if [ -n "${3:-}" ]; then
    if [ "$(jq -r "$3" /tmp/smoke.body 2>/dev/null)" = true ]; then pass_ "$1"; else fail_ "$1" "check '$3' failed: $(head -c 200 /tmp/smoke.body)"; fi
  else pass_ "$1"; fi
}

get movies /movies
get series /series
get wanted /wanted
get history /history
get queue /queue
get blocklist /blocklist
get backfill /backfill
get summary /summary
get profiles /profiles
get indexers /indexers
get library-roots /library/roots
get review /library/review
get cleanup-events /cleanup/events
get system-status /system/status
get tasks /system/tasks
get backups /backups
get settings-general /settings/general
get settings-security /settings/security '.apiKey != null'
get settings-grabbing /settings/grabbing
get settings-cleanup /settings/cleanup
get settings-backups /settings/backups
get settings-solver /settings/solver
get settings-metadata /settings/metadata
get settings-download-client /settings/download-client
get settings-notifications /settings/notifications

# the compat API for seeded profile slugs
for spec in "radarr/hd" "radarr/lab-profile-1" "sonarr/lab-profile-2"; do
  c=$(curl -s -o /tmp/smoke.body -w '%{http_code}' -H "X-Api-Key: $key" "$base/compat/$spec/api/v3/system/status")
  [ "$c" = 200 ] && pass_ "compat $spec status" || fail_ "compat $spec status" "http $c"
done
c=$(curl -s -o /tmp/smoke.body -w '%{http_code}' -H "X-Api-Key: $key" "$base/compat/radarr/lab-profile-1/api/v3/movie")
[ "$c" = 200 ] && pass_ "compat radarr movie list" "($(jq length /tmp/smoke.body) movies)" || fail_ "compat radarr movie list" "http $c"
c=$(curl -s -o /tmp/smoke.body -w '%{http_code}' -H "X-Api-Key: $key" "$base/compat/sonarr/lab-profile-2/api/v3/series")
[ "$c" = 200 ] && pass_ "compat sonarr series list" "($(jq length /tmp/smoke.body) series)" || fail_ "compat sonarr series list" "http $c"

if [ "$creds" = 1 ]; then
  get cred:api-key /settings/security ".apiKey == \"$key\""
  get cred:oidc-secret /settings/security '.oidc.clientSecretSet == true'
  get cred:download-client-password /settings/download-client '.passwordSet == true'
  get cred:tmdb-key /settings/metadata '.tmdbApiKeySet == true'
  get cred:emby-key /settings/notifications '.emby.apiKeySet == true'
  get cred:smtp-password /settings/notifications '.email.passwordSet == true'
  get cred:webhook-secret /settings/notifications '.webhook.secretSet == true'
  get cred:indexer-secrets /indexers '([.[] | select(.secretsSet | length > 0)] | length) > 0'
  get cred:no-unreadable /settings/security '(.unreadableCredentials // []) | length == 0'
fi
exit $((fails > 0))
