#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

docker compose --profile observability up -d --wait
SEA_OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:34317 ./scripts/verify.sh

attempt=0
API_TRACES=0
WORKER_TRACES=0
until [ "$API_TRACES" -gt 0 ] && [ "$WORKER_TRACES" -gt 0 ]; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
        echo "Tempo did not receive both API and worker traces" >&2
        exit 1
    fi
    API_TRACES=$(curl --fail --silent --show-error \
        'http://127.0.0.1:33200/api/search?tags=service.name%3Dsea-music-api' |
        jq --exit-status '.traces | length')
    WORKER_TRACES=$(curl --fail --silent --show-error \
        'http://127.0.0.1:33200/api/search?tags=service.name%3Dsea-music-worker' |
        jq --exit-status '.traces | length')
    if [ "$API_TRACES" -eq 0 ] || [ "$WORKER_TRACES" -eq 0 ]; then
        sleep 0.5
    fi
done

echo "observability verification complete: api_traces=$API_TRACES worker_traces=$WORKER_TRACES"
