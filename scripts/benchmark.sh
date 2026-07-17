#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

K6_IMAGE="${SEA_BENCH_K6_IMAGE:-grafana/k6:2.0.0}"
RATE="${SEA_BENCH_RATE:-2000}"
DURATION="${SEA_BENCH_DURATION:-60s}"
REPEATS="${SEA_BENCH_REPEATS:-5}"
PATTERN="${SEA_BENCH_ACCESS_PATTERN:-pareto80}"
VIDEO_LIMIT="${SEA_BENCH_VIDEO_LIMIT:-500}"
WARMUP_RATE="${SEA_BENCH_WARMUP_RATE:-500}"
WARMUP_DURATION="${SEA_BENCH_WARMUP_DURATION:-15s}"
PRE_ALLOCATED_VUS="${SEA_BENCH_PRE_ALLOCATED_VUS:-128}"
MAX_VUS="${SEA_BENCH_MAX_VUS:-1024}"
P95_LIMIT_MS="${SEA_BENCH_P95_LIMIT_MS:-50}"
P99_LIMIT_MS="${SEA_BENCH_P99_LIMIT_MS:-100}"
DEPENDENCY_CPUS="${SEA_BENCH_DEPENDENCY_CPUS:-}"
SERVICE_CPUS="${SEA_BENCH_SERVICE_CPUS:-}"
LOAD_CPUS="${SEA_BENCH_LOAD_CPUS:-}"
SERVICE_GOMAXPROCS="${SEA_BENCH_SERVICE_GOMAXPROCS:-2}"
SERVICE_GOMEMLIMIT="${SEA_BENCH_SERVICE_GOMEMLIMIT:-1GiB}"
LOAD_MEMORY="${SEA_BENCH_LOAD_MEMORY:-1g}"
DATABASE_URL="${SEA_BENCH_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable}"
REDIS_URL="${SEA_BENCH_REDIS_URL:-redis://:local-redis-password@127.0.0.1:26379/11}"
ADDRESS="${SEA_BENCH_HTTP_ADDRESS:-127.0.0.1:38085}"
RUN_ID="${SEA_BENCH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="artifacts/benchmarks/$RUN_ID"
RAW_TMP_DIR="/dev/shm/sea-music-benchmark-$RUN_ID"

case "$RATE:$REPEATS:$VIDEO_LIMIT" in
    *[!0-9:]*|0:*|*:0:*|*:*:0) echo "rate, repeats, and video limit must be positive integers" >&2; exit 2 ;;
esac
case "$PATTERN" in hot|uniform|pareto80) ;; *) echo "unsupported access pattern: $PATTERN" >&2; exit 2 ;; esac

mkdir -p "$RUN_DIR/runs"
mkdir -p "$RAW_TMP_DIR"
chmod 0777 "$RAW_TMP_DIR"

API_BINARY=/tmp/sea-music-api-benchmark
WORKER_BINARY=/tmp/sea-music-worker-benchmark
API_PID=""
WORKER_PID=""
DEPENDENCIES_PINNED=false
ALL_CPUS="0-$(($(nproc) - 1))"

cleanup_processes() {
    for pid in "$WORKER_PID" "$API_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill -TERM "$pid"
            wait "$pid" || true
        fi
    done
    API_PID=""
    WORKER_PID=""
}

restore_dependencies() {
    if [ "$DEPENDENCIES_PINNED" != true ]; then return; fi
    for id in $(docker compose ps -q postgres redis object-store broker); do
        docker update --cpuset-cpus "$ALL_CPUS" "$id" >/dev/null || true
    done
    DEPENDENCIES_PINNED=false
}

cleanup_all() {
    cleanup_processes
    restore_dependencies
    find "$RAW_TMP_DIR" -type f -delete 2>/dev/null || true
    rmdir "$RAW_TMP_DIR" 2>/dev/null || true
}
trap cleanup_all EXIT INT TERM

docker compose up -d --wait postgres redis object-store broker
if [ -n "$DEPENDENCY_CPUS" ]; then
    for id in $(docker compose ps -q postgres redis object-store broker); do
        docker update --cpuset-cpus "$DEPENDENCY_CPUS" "$id" >/dev/null
    done
    DEPENDENCIES_PINNED=true
fi
docker pull "$K6_IMAGE" >/dev/null
SEA_DATABASE_URL="$DATABASE_URL" go run -buildvcs=false ./cmd/migrate up
SEA_ALLOW_DEVELOPMENT_FIXTURES=true SEA_LOAD_DATASET=true \
SEA_LOAD_DATASET_USERS="${SEA_BENCH_DATASET_USERS:-1000}" \
SEA_LOAD_DATASET_VIDEOS="${SEA_BENCH_DATASET_VIDEOS:-500}" \
SEA_DATABASE_URL="$DATABASE_URL" go run -buildvcs=false ./cmd/fixture
go build -buildvcs=false -o "$API_BINARY" ./cmd/api
go build -buildvcs=false -o "$WORKER_BINARY" ./cmd/worker

docker compose exec -T postgres psql -U sea_music -d sea_music -tAc \
    "SELECT id FROM video.videos WHERE title LIKE 'Load video %' ORDER BY published_at DESC, id DESC LIMIT $VIDEO_LIMIT" \
    | jq -R -s 'split("\n") | map(select(length > 0))' >"$RUN_DIR/targets.json"
jq --exit-status 'length > 0' "$RUN_DIR/targets.json" >/dev/null

{
    printf 'run_id=%s\n' "$RUN_ID"
    printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'k6_image=%s\n' "$K6_IMAGE"
    printf 'rate=%s\n' "$RATE"
    printf 'duration=%s\n' "$DURATION"
    printf 'repeats=%s\n' "$REPEATS"
    printf 'access_pattern=%s\n' "$PATTERN"
    printf 'video_limit=%s\n' "$VIDEO_LIMIT"
    printf 'service_log_level=warn\n'
    printf 'dependency_cpus=%s\n' "${DEPENDENCY_CPUS:-unrestricted}"
    printf 'service_cpus=%s\n' "${SERVICE_CPUS:-unrestricted}"
    printf 'load_generator_cpus=%s\n' "${LOAD_CPUS:-unrestricted}"
    printf 'service_gomaxprocs=%s\n' "$SERVICE_GOMAXPROCS"
    printf 'service_gomemlimit=%s\n' "$SERVICE_GOMEMLIMIT"
    printf 'load_generator_memory=%s\n' "$LOAD_MEMORY"
    printf 'go_version=%s\n' "$(go version)"
    printf 'kernel=%s\n' "$(uname -srvmo)"
    printf 'commit=%s\n' "$(git rev-parse HEAD 2>/dev/null || printf unavailable)"
    printf '\n[cpu]\n'
    lscpu
    printf '\n[memory]\n'
    free -h
    printf '\n[containers]\n'
    docker compose images
} >"$RUN_DIR/environment.txt"

start_services() {
    variant=$1
    directory=$2
    disable_cache=false
    if [ "$variant" = "no-cache" ]; then disable_cache=true; fi
    if [ -n "$SERVICE_CPUS" ]; then
        SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef SEA_LOG_LEVEL=warn \
        GOMAXPROCS="$SERVICE_GOMAXPROCS" GOMEMLIMIT="$SERVICE_GOMEMLIMIT" \
        SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_HTTP_ADDRESS="$ADDRESS" \
        SEA_S3_DISABLE_DOWNLOAD_CACHE="$disable_cache" \
        taskset -c "$SERVICE_CPUS" "$API_BINARY" >"$directory/api.log" 2>&1 &
    else
        SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef SEA_LOG_LEVEL=warn \
        GOMAXPROCS="$SERVICE_GOMAXPROCS" GOMEMLIMIT="$SERVICE_GOMEMLIMIT" \
        SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_HTTP_ADDRESS="$ADDRESS" \
        SEA_S3_DISABLE_DOWNLOAD_CACHE="$disable_cache" \
        "$API_BINARY" >"$directory/api.log" 2>&1 &
    fi
    API_PID=$!
    if [ -n "$SERVICE_CPUS" ]; then
        SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef SEA_LOG_LEVEL=warn \
        GOMAXPROCS="$SERVICE_GOMAXPROCS" GOMEMLIMIT="$SERVICE_GOMEMLIMIT" \
        SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_COUNTER_RECONCILE_INTERVAL=1h \
        taskset -c "$SERVICE_CPUS" "$WORKER_BINARY" >"$directory/worker.log" 2>&1 &
    else
        SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef SEA_LOG_LEVEL=warn \
        GOMAXPROCS="$SERVICE_GOMAXPROCS" GOMEMLIMIT="$SERVICE_GOMEMLIMIT" \
        SEA_DATABASE_URL="$DATABASE_URL" SEA_REDIS_URL="$REDIS_URL" SEA_COUNTER_RECONCILE_INTERVAL=1h \
        "$WORKER_BINARY" >"$directory/worker.log" 2>&1 &
    fi
    WORKER_PID=$!
    attempt=0
    until curl --fail --silent --show-error "http://$ADDRESS/readyz" >/dev/null; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 80 ]; then
            sed -n '1,160p' "$directory/api.log" >&2
            return 1
        fi
        sleep 0.25
    done
}

docker_k6() {
    if [ -n "$LOAD_CPUS" ]; then
        docker run --rm --network host --user "$(id -u):$(id -g)" \
            --cpuset-cpus "$LOAD_CPUS" --memory "$LOAD_MEMORY" \
            -v "$ROOT:/work" -v "$RAW_TMP_DIR:/bench-tmp" "$@"
    else
        docker run --rm --network host --user "$(id -u):$(id -g)" \
            --memory "$LOAD_MEMORY" -v "$ROOT:/work" -v "$RAW_TMP_DIR:/bench-tmp" "$@"
    fi
}

run_k6() {
    variant=$1
    repeat=$2
    run_name=$3
    run_directory="$RUN_DIR/runs/$run_name"
    mkdir -p "$run_directory"
    chmod 0777 "$run_directory"
    start_services "$variant" "$run_directory"
    curl --fail --silent --show-error "http://$ADDRESS/metrics" >"$run_directory/metrics-before.prom"

    docker_k6 "$K6_IMAGE" run --quiet \
        -e BASE_URL="http://$ADDRESS" -e TARGETS_FILE="/work/$RUN_DIR/targets.json" \
        -e ACCESS_PATTERN="$PATTERN" -e RATE="$WARMUP_RATE" -e DURATION="$WARMUP_DURATION" \
        -e PRE_ALLOCATED_VUS="$PRE_ALLOCATED_VUS" -e MAX_VUS="$MAX_VUS" \
        -e P95_LIMIT_MS=10000 -e P99_LIMIT_MS=10000 \
        -e VARIANT="$variant-warmup" -e REPEAT="$repeat" \
        -e SUMMARY_PATH="/work/$run_directory/warmup-summary.json" \
        /work/benchmarks/k6/video-detail.js \
        >"$run_directory/warmup.log" 2>&1 || true

    set +e
    raw_name="sea-music-$RUN_ID-$run_name.json.gz"
    rm -f "$RAW_TMP_DIR/$raw_name"
    docker_k6 "$K6_IMAGE" run --quiet --out "json=/bench-tmp/$raw_name" \
        -e BASE_URL="http://$ADDRESS" -e TARGETS_FILE="/work/$RUN_DIR/targets.json" \
        -e ACCESS_PATTERN="$PATTERN" -e RATE="$RATE" -e DURATION="$DURATION" \
        -e PRE_ALLOCATED_VUS="$PRE_ALLOCATED_VUS" -e MAX_VUS="$MAX_VUS" \
        -e P95_LIMIT_MS="$P95_LIMIT_MS" -e P99_LIMIT_MS="$P99_LIMIT_MS" \
        -e VARIANT="$variant" -e REPEAT="$repeat" \
        -e SUMMARY_PATH="/work/$run_directory/summary.json" \
        /work/benchmarks/k6/video-detail.js >"$run_directory/k6.log" 2>&1
    exit_code=$?
    set -e
    if [ -s "$RAW_TMP_DIR/$raw_name" ]; then
        cp "$RAW_TMP_DIR/$raw_name" "$run_directory/raw.json.gz"
        rm -f "$RAW_TMP_DIR/$raw_name"
    fi
    printf '%s\n' "$exit_code" >"$run_directory/k6-exit-code.txt"
    curl --fail --silent --show-error "http://$ADDRESS/metrics" >"$run_directory/metrics-after.prom"
    cleanup_processes
    test -s "$run_directory/summary.json"
    chmod 0755 "$run_directory"
}

repeat=1
while [ "$repeat" -le "$REPEATS" ]; do
    if [ $((repeat % 2)) -eq 1 ]; then
        first=no-cache
        second=cache
    else
        first=cache
        second=no-cache
    fi
    run_k6 "$first" "$repeat" "$(printf '%02d' "$repeat")-01-$first"
    run_k6 "$second" "$repeat" "$(printf '%02d' "$repeat")-02-$second"
    repeat=$((repeat + 1))
done

find "$RUN_DIR/runs" -name summary.json -type f -print0 \
    | sort -z \
    | xargs -0 jq -s -f scripts/summarize-benchmark.jq >"$RUN_DIR/report.json"
sha256sum "$RUN_DIR"/environment.txt "$RUN_DIR"/targets.json \
    "$RUN_DIR"/runs/*/summary.json "$RUN_DIR"/runs/*/raw.json.gz >"$RUN_DIR/SHA256SUMS"
ln -sfn "$RUN_ID" artifacts/benchmarks/latest

cat "$RUN_DIR/report.json"
printf '\narchive=%s\n' "$RUN_DIR"
