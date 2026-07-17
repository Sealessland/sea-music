#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

export GOCACHE="${GOCACHE:-/tmp/sea-music-go-cache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/sea-music-go-mod}"
export SEA_DATABASE_URL="${SEA_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable}"

docker compose up -d --wait postgres redis object-store broker
go run -buildvcs=false ./cmd/migrate up
SEA_ALLOW_DEVELOPMENT_FIXTURES=true go run -buildvcs=false ./cmd/fixture

echo "bootstrap complete"
