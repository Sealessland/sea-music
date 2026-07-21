#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

RUN_ID="${SEA_QUEUE_BENCH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="artifacts/queue-benchmarks/$RUN_ID"
REPEATS="${SEA_QUEUE_BENCH_REPEATS:-3}"
PROJECT_PREFIX=$(printf '%s' "sea-music-bench-$RUN_ID" | tr '[:upper:]_' '[:lower:]-' | tr -cd 'a-z0-9-')


case "$REPEATS" in
    *[!0-9]*|0) echo "SEA_QUEUE_BENCH_REPEATS must be a positive integer" >&2; exit 2 ;;
esac

mkdir -p "$RUN_DIR"
printf 'run_id=%s\nrepeats=%s\nrequests=%s\nconcurrency=%s\n' \
    "$RUN_ID" "$REPEATS" "${SEA_LOAD_REQUESTS:-500}" "${SEA_LOAD_CONCURRENCY:-16}" >"$RUN_DIR/environment.txt"

repeat=1
while [ "$repeat" -le "$REPEATS" ]; do
    broker_index=0
    for broker in kafka rocketmq jetstream; do
        broker_index=$((broker_index + 1))
        slot=$(((repeat - 1) * 3 + broker_index))
        port_offset=$((slot * 20))
        project="$PROJECT_PREFIX-$broker-$repeat"
        mkdir -p "$RUN_DIR/$broker"
        result="$RUN_DIR/$broker/run-$repeat.json"
        COMPOSE_PROJECT_NAME="$project" \
        POSTGRES_PORT=$((25432 + port_offset)) \
        REDIS_PORT=$((26379 + port_offset)) \
        S3_PORT=$((28333 + port_offset)) \
        SEAWEED_MASTER_PORT=$((29333 + port_offset)) \
        KAFKA_PORT=$((29092 + port_offset)) \
        ROCKETMQ_NAMESERVER_PORT=$((29876 + port_offset)) \
        ROCKETMQ_PROXY_PORT=$((28081 + port_offset)) \
        NATS_PORT=$((24222 + port_offset)) \
        SEA_KAFKA_BROKERS="127.0.0.1:$((29092 + port_offset))" \
        SEA_ROCKETMQ_ENDPOINT="127.0.0.1:$((28081 + port_offset))" \
        SEA_NATS_URL="nats://127.0.0.1:$((24222 + port_offset))" \
        SEA_LOAD_DATABASE_URL="postgres://sea_music:local-postgres-password@127.0.0.1:$((25432 + port_offset))/sea_music?sslmode=disable" \
        SEA_LOAD_REDIS_URL="redis://:local-redis-password@127.0.0.1:$((26379 + port_offset))/11" \
        SEA_LOAD_RESET_COMPOSE=true \
        SEA_EVENT_BROKER="$broker" \
        SEA_LOAD_OUTPUT_DIR="$RUN_DIR/$broker" \
        SEA_LOAD_HTTP_ADDRESS="127.0.0.1:$((38100 + slot))" \
        ./scripts/loadtest.sh >"$RUN_DIR/$broker/run-$repeat.path"
        generated=$(sed -n '$p' "$RUN_DIR/$broker/run-$repeat.path")
        cp "$generated" "$result"
    done
    repeat=$((repeat + 1))
done

jq -s '
  def median: sort as $values | ($values | length) as $length |
    if $length % 2 == 1 then $values[($length / 2 | floor)]
    else (($values[$length / 2 - 1] + $values[$length / 2]) / 2) end;
  def aggregate: {
    runs: length,
    requests: (map(.scenarios[] | .requests) | add),
    errors: (map(.scenarios[] | .errors) | add),
    throughput_rps: (map(.scenarios[] | select(.name == "burst-like-toggle") | .throughput_rps) | median),
    p95_ms: (map(.scenarios[] | select(.name == "burst-like-toggle") | .p95_ms) | median),
    p99_ms: (map(.scenarios[] | select(.name == "burst-like-toggle") | .p99_ms) | median),
    backlog_recovery_ms: (map(.backlog_recovery_ms) | median)
  };
  {schema_version: 1, methodology: "loadtest burst-like-toggle; identical request/concurrency settings", variants: {
    kafka: ([.[] | select(.event_broker == "kafka")] | aggregate),
    rocketmq: ([.[] | select(.event_broker == "rocketmq")] | aggregate),
    jetstream: ([.[] | select(.event_broker == "jetstream")] | aggregate)
  }}
' "$RUN_DIR"/*/run-*.json >"$RUN_DIR/report.json"

(cd "$RUN_DIR" && sha256sum environment.txt kafka/run-*.json rocketmq/run-*.json jetstream/run-*.json >SHA256SUMS)
cat "$RUN_DIR/report.json"
printf '\narchive=%s\n' "$RUN_DIR"
