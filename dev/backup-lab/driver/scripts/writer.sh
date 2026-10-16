#!/usr/bin/env bash
# Runs in the driver: one small API write every ~0.25 s for N seconds, logging "status milliseconds".
# It shows what an app write feels while a backup holds the database.
#   writer.sh BASE_URL APIKEY SECONDS
base=$1 key=$2 secs=$3
end=$((SECONDS + secs))
while [ "$SECONDS" -lt "$end" ]; do
  t0=$(date +%s%N)
  code=$(curl -s -m 30 -o /dev/null -w '%{http_code}' -X PUT -H "X-Api-Key: $key" -H 'Content-Type: application/json' \
    -d '{"instanceName":"Lab writer probe","logLevel":"info"}' "$base/api/v1/settings/general")
  t1=$(date +%s%N)
  echo "$code $(((t1 - t0) / 1000000))"
  sleep 0.25
done
