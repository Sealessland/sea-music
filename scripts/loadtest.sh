#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
DATABASE_URL="${SEA_LOAD_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable}"
REDIS_URL="${SEA_LOAD_REDIS_URL:-redis://:local-redis-password@127.0.0.1:26379/11}"
ADDRESS="${SEA_LOAD_HTTP_ADDRESS:-127.0.0.1:38084}"

docker compose up -d --wait postgres redis object-store broker
SEA_DATABASE_URL="$DATABASE_URL" go run -buildvcs=false ./cmd/migrate up
SEA_ALLOW_DEVELOPMENT_FIXTURES=true SEA_LOAD_DATASET=true \
SEA_LOAD_DATASET_USERS="${SEA_LOAD_DATASET_USERS:-1000}" \
SEA_LOAD_DATASET_VIDEOS="${SEA_LOAD_DATASET_VIDEOS:-500}" \
SEA_DATABASE_URL="$DATABASE_URL" go run -buildvcs=false ./cmd/fixture

API_BINARY=/tmp/sea-music-api-load
WORKER_BINARY=/tmp/sea-music-worker-load
LOAD_BINARY=/tmp/sea-music-loadtest
go build -buildvcs=false -o "$API_BINARY" ./cmd/api
go build -buildvcs=false -o "$WORKER_BINARY" ./cmd/worker
go build -buildvcs=false -o "$LOAD_BINARY" ./cmd/loadtest

SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_HTTP_ADDRESS="$ADDRESS" \
"$API_BINARY" >/tmp/sea-music-api-load.log 2>&1 &
API_PID=$!
SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_COUNTER_RECONCILE_INTERVAL=1h \
"$WORKER_BINARY" >/tmp/sea-music-worker-load.log 2>&1 &
WORKER_PID=$!

cleanup() {
    for pid in "$WORKER_PID" "$API_PID"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill -TERM "$pid"
            wait "$pid"
        fi
    done
}
trap cleanup EXIT INT TERM

attempt=0
until curl --fail --silent --show-error "http://$ADDRESS/readyz" >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 40 ]; then
        sed -n '1,160p' /tmp/sea-music-api-load.log >&2
        exit 1
    fi
    sleep 0.25
done

suffix=$(date +%s)
username="load_runner_$suffix"
email="$username@example.test"
curl --fail --silent --show-error --header 'Content-Type: application/json' \
    --data "{\"username\":\"$username\",\"email\":\"$email\",\"password\":\"load runner password 2026\"}" \
    "http://$ADDRESS/api/v1/users" >/dev/null
LOGIN=$(curl --fail --silent --show-error --header 'Content-Type: application/json' \
    --data "{\"identity\":\"$email\",\"password\":\"load runner password 2026\"}" \
    "http://$ADDRESS/api/v1/sessions")
ACCESS_TOKEN=$(printf '%s' "$LOGIN" | jq --exit-status --raw-output '.access_token')
VIDEO_ID=$(docker compose exec -T postgres psql -U sea_music -d sea_music -tAc \
    "SELECT id FROM video.videos WHERE title LIKE 'Load video %' ORDER BY published_at DESC LIMIT 1" | tr -d '[:space:]')

mkdir -p artifacts/performance
RESULT="artifacts/performance/raw-$suffix.json"
SEA_LOAD_BASE_URL="http://$ADDRESS" SEA_LOAD_ACCESS_TOKEN="$ACCESS_TOKEN" SEA_LOAD_VIDEO_ID="$VIDEO_ID" \
SEA_LOAD_CONCURRENCY="${SEA_LOAD_CONCURRENCY:-16}" SEA_LOAD_REQUESTS="${SEA_LOAD_REQUESTS:-500}" \
"$LOAD_BINARY" >"$RESULT"
cp "$RESULT" artifacts/performance/latest.json
METRICS="artifacts/performance/raw-$suffix.prom"
curl --fail --silent --show-error "http://$ADDRESS/metrics" >"$METRICS"
cp "$METRICS" artifacts/performance/latest.prom

cleanup
trap - EXIT INT TERM
echo "$RESULT"
