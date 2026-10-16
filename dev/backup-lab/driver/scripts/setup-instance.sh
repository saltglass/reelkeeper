#!/usr/bin/env bash
# Runs in the driver. Creates the first account on a Reelkeeper instance (or signs in if there is
# one), then saves settings that hold stored credentials, so sealed values exist in the database.
#
#   setup-instance.sh BASE_URL LABEL
#
# Prints KEY=VALUE lines (APIKEY, USERNAME, PASSWORD) for the runner to keep.
set -uo pipefail
base=$1
label=${2:-lab}
user="labadmin"
pass='Lab-Pass-1234!'
hdr=$(mktemp)
trap 'rm -f "$hdr"' EXIT
api=$base/api/v1
cookie=""

# (A cookie jar doesn't work here: curl refuses cookies for single-word host names like rk-pg.)
# call sets $rc (the HTTP status) and remembers a session cookie the server hands out.
call() {
  rc=$(curl -s -D "$hdr" -o /tmp/setup.body -w '%{http_code}' ${cookie:+-H "Cookie: $cookie"} "$@")
  local set; set=$(sed -n 's/^[Ss]et-[Cc]ookie: \(reelkeeper_session=[^;]*\).*/\1/p' "$hdr" | head -1)
  if [ -n "$set" ]; then cookie=$set; fi
}

state=$(curl -s "$api/auth/state")
if [ "$(jq -r .setupRequired <<<"$state")" = "true" ]; then
  call -X POST "$api/auth/setup" -H 'content-type: application/json' -d "$(jq -n --arg u "$user" --arg p "$pass" '{username:$u,password:$p}')"
  c=$rc
  [ "$c" = 204 ] || { echo "ERROR setup returned $c: $(cat /tmp/setup.body)" >&2; exit 1; }
else
  call -X POST "$api/auth/login" -H 'content-type: application/json' -d "$(jq -n --arg u "$user" --arg p "$pass" '{username:$u,password:$p}')"
  c=$rc
  [ "$c" = 204 ] || { echo "ERROR login returned $c: $(cat /tmp/setup.body)" >&2; exit 1; }
fi
apikey=$(curl -s -H "Cookie: $cookie" "$api/settings/security" | jq -r .apiKey)
[ -n "$apikey" ] && [ "$apikey" != null ] || { echo "ERROR no API key" >&2; exit 1; }

put() { # path json [method]
  call -X "${3:-PUT}" "$api$1" -H 'content-type: application/json' -d "$2"
  local c=$rc
  case $c in 200|201|204) ;; *) echo "WARN ${3:-PUT} $1 -> $c $(head -c 200 /tmp/setup.body)" >&2 ;; esac
}

put /settings/general "$(jq -n --arg n "Lab $label 🧪" '{instanceName:$n,logLevel:"info"}')"
put /settings/download-client '{"url":"http://qbit.invalid:8080","username":"admin","password":"qbit-lab-pass-ü-1","folder":"/mnt/downloads/reelkeeper"}'
put /settings/notifications '{
  "emby":{"enabled":false,"url":"http://media.invalid:8096","apiKey":"emby-lab-key-123","pathFrom":"/media","pathTo":"/Volumes/Media"},
  "email":{"enabled":false,"host":"smtp.invalid","port":587,"username":"lab@example.invalid","password":"smtp-lab-pass-ü","from":"lab@example.invalid","to":"dan@example.invalid"},
  "webhook":{"enabled":false,"url":"http://hook.invalid/x","secret":"webhook-lab-secret"}}'
put /settings/metadata '{"tmdbApiKey":"0123456789abcdef0123456789abcdef","language":"en-US"}'
put /settings/solver '{"url":"http://trawl.invalid:8191","timeoutSeconds":60}'
put /settings/grabbing '{"mode":"off","tvMode":"off","queueLimit":5,"minSeeders":1}'
put /settings/cleanup '{"mode":"off","seedRatio":1,"seedHours":24,"stalledHours":12,"slowKBps":10,"slowHours":6,"metadataHours":1,"importFailedDays":3,"unregistered":true,"missingFiles":true,"emptyReplacedDays":30,"keepFreeGB":100}'
put /settings/backups '{"folder":"","scheduled":true,"time":"03:30","keep":14}'
put /settings/security '{"localLoginEnabled":true,"bypassNetworks":[],"trustedProxies":["10.9.9.9"],"sessionDays":30,
  "oidc":{"enabled":false,"issuerUrl":"https://auth.invalid/application/o/lab/","clientId":"lab-client","clientSecret":"oidc-lab-secret-ü","scopes":["openid","profile","email"],"groupsClaim":"groups","requiredGroup":"Griffin","buttonLabel":"Sign in with Lab"}}'
put /indexers '{"name":"Setup Torznab ü","kind":"torznab","baseUrl":"http://setup-tracker.invalid/api","enabled":false,"priority":10,"useSolver":false,"settings":{"apiKey":"indexer-lab-key-123456"},"categories":[2000,5000]}' POST

echo "APIKEY=$apikey"
echo "USERNAME=$user"
echo "PASSWORD=$pass"
