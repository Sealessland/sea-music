#!/usr/bin/env sh
set -eu

RUN_DIR=${1:?usage: check-benchmark.sh <benchmark-run-directory>}
REPORT="$RUN_DIR/report.json"

test -s "$REPORT"

expected_runs=$(jq -r '.methodology.expected_runs // empty' "$REPORT")
if [ -z "$expected_runs" ]; then
    repeats=$(sed -n 's/^repeats=//p' "$RUN_DIR/environment.txt")
    expected_runs=$((repeats * 2))
fi

actual_runs=$(find "$RUN_DIR/runs" -name k6-exit-code.txt -type f | wc -l | tr -d ' ')
if [ "$actual_runs" -ne "$expected_runs" ]; then
    echo "expected $expected_runs measured runs, found $actual_runs" >&2
    exit 1
fi

for exit_file in "$RUN_DIR"/runs/*/k6-exit-code.txt; do
    if [ "$(tr -d '[:space:]' <"$exit_file")" != 0 ]; then
        echo "k6 threshold failure: $exit_file" >&2
        exit 1
    fi
done

jq --exit-status '
  .schema_version == 1
  and (.variants | length >= 2)
  and all(.variants[];
    .runs > 0
    and .all_thresholds_passed == true
    and .dropped_iterations == 0
    and .failed_rate < 0.001
  )
' "$REPORT" >/dev/null

echo "benchmark gate passed: $actual_runs runs across $(jq '.variants | length' "$REPORT") variants"
