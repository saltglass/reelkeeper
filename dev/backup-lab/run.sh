#!/usr/bin/env bash
# Reelkeeper backup lab: stress-tests Reelkeeper's own backup and restore end to end.
#
#   dev/backup-lab/run.sh                 everything, from scratch (about an hour)
#   RUN_ONLY="s1 s2" run.sh               only some scenarios (names below); the core instances are still built
#   LAB_REUSE=1 run.sh                    keep the containers and volumes of an earlier run (development)
#   SCALE=0.2 run.sh                      a smaller seed, for a quick check of the lab itself
#   LAB_IMAGE=some/image:tag run.sh       test that image instead of building the working tree
#   REELKEEPER_LAB_IMAGE=my/registry/reelkeeper run.sh   take the older versions (0.8.1, 0.8.4) from here instead of ghcr.io/saltglass/reelkeeper
#
# Nothing here contacts a real service. See README.md.
cd "$(dirname "$0")" || exit 1
# shellcheck source=lib.sh
. ./lib.sh
# shellcheck source=scenarios.sh
. ./scenarios.sh

TS=$(date +%Y%m%d-%H%M%S)
mkdir -p "$LAB/results"
OUT=$LAB/results/$TS.work
STATE=$LAB/state
if [ -z "${LAB_REUSE:-}" ]; then rm -rf "${LAB:?}/state"; fi
mkdir -p "$STATE" "$OUT"
: >"$OUT/checks.tsv"
: >"$OUT/notes.tsv"
: >"$OUT/metrics.tsv"
REPORT=$LAB/results/$TS.md
SCALE=${SCALE:-1}
ROOT=$(cd "$LAB/../.." && pwd)
# The version under test. Unless the caller names an image, it is built from the working tree.
LAB_IMAGE_GIVEN=${LAB_IMAGE:-}
LAB_IMAGE=${LAB_IMAGE:-reelkeeper-lab:current}
# Where the older versions (0.8.1, 0.8.4) the upgrade scenarios need are pulled from, without the tag.
# They are needed locally, so pull them (or retag your own copies) before a run.
REELKEEPER_LAB_IMAGE=${REELKEEPER_LAB_IMAGE:-ghcr.io/saltglass/reelkeeper}
export OUT STATE SCALE LAB_IMAGE REELKEEPER_LAB_IMAGE

preflight() {
  scenario "preflight"
  local missing=0
  for c in docker jq perl go colima; do
    command -v "$c" >/dev/null || { echo "missing tool: $c"; missing=1; }
  done
  [ "$missing" = 0 ] || exit 2
  docker info >/dev/null 2>&1 || { echo "docker isn't running (try: colima start)"; exit 2; }
  # The older versions the upgrade scenarios start from must be pulled; the version under test is built.
  # They come from REELKEEPER_LAB_IMAGE (default ghcr.io/saltglass/reelkeeper), tags 0.8.4 and 0.8.1.
  for tag in 0.8.4 0.8.1; do
    docker image inspect "$REELKEEPER_LAB_IMAGE:$tag" >/dev/null 2>&1 || { echo "image $REELKEEPER_LAB_IMAGE:$tag isn't pulled"; exit 2; }
  done
  if [ -n "$LAB_IMAGE_GIVEN" ]; then
    docker image inspect "$LAB_IMAGE" >/dev/null 2>&1 || { echo "image $LAB_IMAGE (LAB_IMAGE) isn't available"; exit 2; }
  fi
  pass "tools and images present"
}

# Builds the version under test from the working tree (the Dockerfile at the repo root), the way
# `just image-local` does, unless the caller named an image in LAB_IMAGE.
build_current() {
  if [ -n "$LAB_IMAGE_GIVEN" ]; then
    echo "testing the image $LAB_IMAGE (LAB_IMAGE)"
    return
  fi
  local version
  version=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)
  echo "building the working tree ($version) as $LAB_IMAGE"
  (cd "$ROOT" && docker buildx build --load --build-arg VERSION="$version" -t "$LAB_IMAGE" .) || { echo "image build failed"; exit 2; }
}

build() {
  echo "building labtool (linux/arm64, static) and the driver image"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags lab -trimpath -ldflags '-s -w' -o "$LAB/bin/labtool" ./dev/backup-lab/labtool) || { echo "labtool build failed"; exit 2; }
  "${COMPOSE[@]}" build driver >/dev/null || { echo "driver image build failed"; exit 2; }
}

bring_up() {
  if [ -z "${LAB_REUSE:-}" ]; then
    "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1
  fi
  "${COMPOSE[@]}" up -d >/dev/null || { echo "compose up failed"; exit 2; }
  local svc
  for svc in rk-sqlite rk-pg rk-fresh-sqlite rk-fresh-pg rk-wrongkey rk-old081 rk-old081-pg rk-old084 rk-old084-sqlite; do
    wait_ping "$svc" 90 || { echo "$svc did not come up"; "${COMPOSE[@]}" logs "$svc" | tail -20; exit 2; }
    if [ -z "${LAB_REUSE:-}" ]; then : >"$(keyfile "$svc")"; fi
  done
}

# The isolation check must pass before anything else happens.
isolation() {
  scenario "isolation"
  local net internal t out ok=1
  net=$PREFIX"_lab"
  internal=$(docker network inspect "$net" -f '{{.Internal}}')
  if [ "$internal" = true ]; then pass "network $net is internal: true"; else fail "network internal" "$internal"; ok=0; fi
  # every lab container is on that network and no other
  for c in $("${COMPOSE[@]}" ps -q); do
    nets=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$c")
    name=$(docker inspect -f '{{.Name}}' "$c")
    if [ "$(echo $nets)" != "$net" ]; then fail "container $name networks" "$nets"; ok=0; fi
  done
  [ "$ok" = 1 ] && pass "all lab containers are on the lab network only"
  routes=$(d ip route)
  if grep -q '^default' <<<"$routes"; then fail "driver has a default route" "$routes"; ok=0; else pass "driver has no default route" "$(echo $routes)"; fi
  for t in 1.1.1.1:443 192.168.1.20:8080 192.168.1.30:8096 api.themoviedb.org:443; do
    out=$(d curl -s -m 5 -o /dev/null -w '%{http_code}' "http://$t/" 2>&1)
    rc=$?
    if [ "$out" = 000 ] && [ "$rc" != 0 ]; then pass "driver cannot reach $t"; else fail "driver reached $t" "http $out rc=$rc"; ok=0; fi
    out=$(d curl -s -m 5 -o /dev/null -w '%{http_code}' "https://$t/" 2>&1)
    [ "$out" = 000 ] || { fail "driver reached https://$t" "$out"; ok=0; }
  done
  # from a Reelkeeper container: it has no shell, so show its network namespace has no route
  pid=$(docker inspect -f '{{.State.Pid}}' "$(cname rk-sqlite)")
  rt=$(colima ssh -- sudo nsenter -t "$pid" -n ip route 2>/dev/null)
  if [ -n "$rt" ] && ! grep -q '^default' <<<"$rt"; then pass "rk-sqlite's own network namespace has no default route" "$(echo $rt)"; else fail "rk-sqlite namespace routes" "${rt:-could not read}"; ok=0; fi
  rt=$(colima ssh -- sudo nsenter -t "$(docker inspect -f '{{.State.Pid}}' "$(cname rk-pg)")" -n ip route 2>/dev/null)
  if [ -n "$rt" ] && ! grep -q '^default' <<<"$rt"; then pass "rk-pg's own network namespace has no default route"; else fail "rk-pg namespace routes" "${rt:-could not read}"; ok=0; fi
  if [ "$ok" != 1 ]; then
    echo "ISOLATION FAILED - aborting before anything else runs"
    write_report
    exit 3
  fi
}

# Once an instance has an account: ask the app itself to connect somewhere outside.
isolation_from_app() {
  scenario "isolation (from inside the app)"
  local t
  for t in http://1.1.1.1:8080 http://192.168.1.20:8080 http://api.themoviedb.org:80; do
    api rk-sqlite POST /settings/download-client/test -H 'Content-Type: application/json' -d "{\"url\":\"$t\",\"username\":\"a\",\"password\":\"b\"}"
    if [ "$CODE" = 422 ] && ! grep -qi 'version' <<<"$BODY"; then pass "the app cannot reach $t" "$(msg | cut -c1-120)"; else fail "the app reached $t?" "http $CODE $(echo "$BODY" | head -c 150)"; fi
  done
}

main() {
  preflight
  build_current
  build
  bring_up
  isolation
  scenario "provision the core instances"
  if [ -n "${LAB_REUSE:-}" ] && [ -s "$STATE/orig-rk-sqlite.key" ] && [ -s "$STATE/orig-rk-pg.key" ]; then
    note "LAB_REUSE: keeping the core instances of the earlier run"
    KINDS=$(task_kinds rk-sqlite)
  else
    provision rk-sqlite "$SCALE"
    provision rk-pg "$SCALE"
    if [ -s "$(keyfile rk-sqlite)" ] && [ -s "$(keyfile rk-pg)" ]; then
      save_orig rk-sqlite
      save_orig rk-pg
    fi
  fi
  if [ -s "$(keyfile rk-sqlite)" ] && [ -s "$(keyfile rk-pg)" ]; then
    smoke_check "rk-sqlite works after seeding" rk-sqlite --creds
    smoke_check "rk-pg works after seeding" rk-pg --creds
    isolation_from_app
  else
    echo "core instances could not be provisioned; stopping"
    write_report
    exit 4
  fi
  run_scenarios
  write_report
  echo
  echo "Report: $REPORT"
  exit "$FAILED"
}

main "$@"
