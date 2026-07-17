#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

# This target is intentionally destructive only to this Compose project's generated local volumes.
docker compose --profile observability down --volumes --remove-orphans
make bootstrap
make verify
make verify-observability
make fault-drill
SEA_LOAD_REQUESTS="${SEA_FINAL_LOAD_REQUESTS:-100}" \
SEA_LOAD_CONCURRENCY="${SEA_FINAL_LOAD_CONCURRENCY:-8}" \
make loadtest

echo "fresh-environment verification passed"
