# Reelkeeper task runner. Run `just` to list recipes.
# CLAUDE.md's command list is kept in step with this file.

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X github.com/saltglass/reelkeeper/internal/buildinfo.Version=" + version
pg_test_url := "postgres://reelkeeper:reelkeeper@127.0.0.1:5433/reelkeeper?sslmode=disable"

default:
    @just --list

# Backend (live reload, :7677) plus the Vite dev server at http://localhost:7676
dev: web-deps
    #!/usr/bin/env bash
    set -euo pipefail
    trap 'kill 0' EXIT
    REELKEEPER_PORT=7677 go tool air &
    (cd web && npm run dev) &
    wait

# Run all Go and frontend tests (SQLite)
test: web-deps
    go test -race ./...
    cd web && npm test

# Run all Go tests on SQLite and Postgres (starts the dev stack if needed)
test-pg: dev-stack
    REELKEEPER_TEST_POSTGRES='{{pg_test_url}}' go test -race ./...

# Lint everything; must be clean before every commit
# Go is linted as the Linux build that CI and the container use, so a Mac
# doesn't pass code that fails there (some syscall types differ by OS).
lint: web-deps
    GOOS=linux golangci-lint run ./...
    cd web && npm run lint

# Format all code
fmt: web-deps
    golangci-lint fmt ./...
    go mod tidy
    cd web && npm run fmt

# Regenerate code from api/openapi.yaml (never edit generated files)
gen: web-deps
    go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
    cd web && npm run gen

# Build the production binary, with the web UI embedded, into ./bin/reelkeeper
build: web-deps
    cd web && npm run build
    CGO_ENABLED=0 go build -trimpath -ldflags '{{ldflags}}' -o bin/reelkeeper ./cmd/reelkeeper

image_name := "ghcr.io/saltglass/reelkeeper"

# Build the container image for amd64 and arm64 (checks both build; loads
# nothing). `just image-local` builds and loads one for this machine.
image:
    docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION={{version}} -t {{image_name}}:{{version}} .

# Build the container image for this machine and load it into Docker
image-local:
    docker buildx build --load --build-arg VERSION={{version}} -t {{image_name}}:dev .

# Install the web app's dependencies when package-lock.json changes
web-deps:
    #!/usr/bin/env bash
    set -euo pipefail
    cd web
    if [ ! -d node_modules ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
        npm ci
    fi

# Start the local Postgres, qBittorrent, Transmission, Deluge and Seerr containers (Colima)
dev-stack:
    @colima status >/dev/null 2>&1 || colima start
    docker compose -f dev/compose.yaml up -d --wait

# Run the integration tests that use the dev stack's real qBittorrent, Transmission and Deluge (moving torrents between them, and between a throwaway Transmission and the built-in client), and its SABnzbd with the made-up Usenet (fakenews).
# Needs REELKEEPER_TEST_QBIT_PASS set to the dev qBittorrent's Web UI password. No internet is used: see dev/compose.yaml.
test-clients: dev-stack
    REELKEEPER_TEST_QBIT_URL=http://localhost:8081 REELKEEPER_TEST_QBIT_USER=${REELKEEPER_TEST_QBIT_USER:-admin} \
    REELKEEPER_TEST_TRANSMISSION_URL=http://localhost:9092 REELKEEPER_TEST_TRANSMISSION_USER=reelkeeper REELKEEPER_TEST_TRANSMISSION_PASS=reelkeeper \
    REELKEEPER_TEST_DELUGE_URL=http://localhost:8112 REELKEEPER_TEST_DELUGE_PASS=reelkeeper \
    REELKEEPER_TEST_SABNZBD_URL=http://localhost:8085 \
    go test ./internal/download -run 'TestReal' -count=1 -v -timeout 60m

# Stop the dev stack (keeps its data volumes)
dev-stack-down:
    docker compose -f dev/compose.yaml down

# Stress-test Reelkeeper's own backup and restore in an isolated lab (about an hour; results in dev/backup-lab/results/)
backup-lab:
    @colima status >/dev/null 2>&1 || colima start
    dev/backup-lab/run.sh

# Measure the built-in client's memory with thousands of made-up seeding torrents (about 1.5 hours; loopback only, nothing leaves the computer).
# Results in tmp/load-builtin/. LOAD_RUNS="n:seeds[:peers],..." and LOAD_SETTLE=2m pick fewer or shorter runs; LOAD_REAL=1 makes the torrents real-sized
# (5-40 GB, about 1,600 pieces each), the case to compare with a real library. See internal/download/builtin/load_test.go.
load-builtin:
    LOAD_OUT={{justfile_directory()}}/tmp/load-builtin go test -tags loadtest ./internal/download/builtin -run '^TestLoadBuiltin$' -count=1 -v -timeout 0

# Remove the backup lab's containers, network and volumes
backup-lab-down:
    docker compose -f dev/backup-lab/compose.yaml down -v --remove-orphans
