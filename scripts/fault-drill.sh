#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export SEA_DATABASE_ADMIN_URL="${SEA_DATABASE_ADMIN_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/postgres?sslmode=disable}"
export SEA_EVENTS_TEST_DATABASE_URL="${SEA_EVENTS_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_events_test?sslmode=disable}"
export SEA_VIDEO_TEST_DATABASE_URL="${SEA_VIDEO_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_video_test?sslmode=disable}"
export SEA_DISCOVERY_TEST_DATABASE_URL="${SEA_DISCOVERY_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_discovery_test?sslmode=disable}"
export SEA_EVENTS_TEST_BROKER="${SEA_EVENTS_TEST_BROKER:-127.0.0.1:29092}"
export SEA_VIDEO_TEST_S3_ENDPOINT="${SEA_VIDEO_TEST_S3_ENDPOINT:-http://127.0.0.1:28333}"
export SEA_REDIS_TEST_URL="${SEA_REDIS_TEST_URL:-redis://:local-redis-password@127.0.0.1:26379/15}"

docker compose up -d --wait postgres redis object-store broker
SEA_TEST_DATABASE_NAME=sea_music_events_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_video_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_discovery_test go run -buildvcs=false ./cmd/testdb

go test -race -count=1 ./internal/events -run 'TestOutboxBacklog|TestAckWindow|TestPoison'
go test -race -count=1 ./internal/discovery -run 'TestHotRankingDeduplicates'
go test -race -count=1 ./internal/video -run 'TestRealFFmpegWorkerCreatesPlayableRenditionAndCover'

echo "fault drills complete"
