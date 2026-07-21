#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

RUN_ID="${SEA_QUEUE_BENCH_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="artifacts/queue-benchmarks/$RUN_ID"
REPEATS="${SEA_QUEUE_BENCH_REPEATS:-3}"

case "$REPEATS" in
    *[!0-9]*|0) echo "SEA_QUEUE_BENCH_REPEATS must be a positive integer" >&2; exit 2 ;;
esac

mkdir -p "$RUN_DIR"
printf 'run_id=%s\nrepeats=%s\nrequests=%s\nconcurrency=%s\n' \
    "$RUN_ID" "$REPEATS" "${SEA_LOAD_REQUESTS:-500}" "${SEA_LOAD_CONCURRENCY:-16}" >"$RUN_DIR/environment.txt"

repeat=1
while [ "$repeat" -le "$REPEATS" ]; do
    for broker in kafka rocketmq jetstream; do
        mkdir -p "$RUN_DIR/$broker"
        result="$RUN_DIR/$broker/run-$repeat.json"
        docker compose --profile rocketmq --profile jetstream stop broker rocketmq-init rocketmq-broker rocketmq-nameserver jetstream >/dev/null 2>&1 || true
        SEA_EVENT_BROKER="$broker" \
        SEA_LOAD_OUTPUT_DIR="$RUN_DIR/$broker" \
        SEA_LOAD_HTTP_ADDRESS="127.0.0.1:$((38100 + repeat))" \
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
