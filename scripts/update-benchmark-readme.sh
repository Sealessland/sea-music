#!/usr/bin/env sh
set -eu

REPORT=${1:?usage: update-benchmark-readme.sh <report.json> <README.md> <run-url> <commit>}
README=${2:?usage: update-benchmark-readme.sh <report.json> <README.md> <run-url> <commit>}
RUN_URL=${3:?usage: update-benchmark-readme.sh <report.json> <README.md> <run-url> <commit>}
COMMIT=${4:?usage: update-benchmark-readme.sh <report.json> <README.md> <run-url> <commit>}

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

BLOCK=$(mktemp)
OUTPUT=$(mktemp)
trap 'rm -f "$BLOCK" "$OUTPUT"' EXIT INT TERM

generated_at=$(jq -r '.generated_at' "$REPORT")
tool=$(jq -r '.methodology.tool' "$REPORT")
model=$(jq -r '.methodology.model' "$REPORT")
target_rps=$(jq -r '.methodology.target_rps' "$REPORT")
duration=$(jq -r '.methodology.duration' "$REPORT")
pattern=$(jq -r '.methodology.access_pattern' "$REPORT")
target_count=$(jq -r '.methodology.target_count' "$REPORT")
short_commit=$(printf '%s' "$COMMIT" | cut -c1-12)

{
    printf '> 最近一次通过门禁的 CI 基准：[workflow run](%s) · `%s` · %s\n\n' "$RUN_URL" "$short_commit" "$generated_at"
    printf '口径：`%s`、`%s`、目标 %s RPS、持续 %s、`%s` 分布、%s 个视频；每个对照组均执行，表中为重复运行中位数。共享 GitHub runner 数据仅用于回归比较，不代表生产 SLA。\n\n' \
        "$tool" "$model" "$target_rps" "$duration" "$pattern" "$target_count"
    printf '| 对照组 | 重复次数 | 实际 QPS | P95 | P99 | 错误率 | Dropped | 阈值 |\n'
    printf '|---|---:|---:|---:|---:|---:|---:|---|\n'
    jq -r '.variants | to_entries[] | [
      .key,
      (.value.runs | tostring),
      (.value.request_rate_rps | tostring),
      (.value.latency_ms.p95 | tostring),
      (.value.latency_ms.p99 | tostring),
      ((.value.failed_rate * 100) | tostring),
      (.value.dropped_iterations | tostring),
      (if .value.all_thresholds_passed then "通过" else "失败" end)
    ] | @tsv' "$REPORT" | while IFS="$(printf '\t')" read -r variant runs qps p95 p99 error_rate dropped thresholds; do
        printf '| `%s` | %s | %.2f | %.2f ms | %.2f ms | %.4f%% | %s | %s |\n' \
            "$variant" "$runs" "$qps" "$p95" "$p99" "$error_rate" "$dropped" "$thresholds"
    done
    if jq --exit-status '.comparison.cache_vs_no_cache_p95_change_percent != null and .comparison.cache_vs_no_cache_p99_change_percent != null' "$REPORT" >/dev/null; then
        p95_change=$(jq -r '.comparison.cache_vs_no_cache_p95_change_percent' "$REPORT")
        p99_change=$(jq -r '.comparison.cache_vs_no_cache_p99_change_percent' "$REPORT")
        printf '\n缓存相对无缓存：P95 `%.2f%%`，P99 `%.2f%%`（负值表示延迟降低）。\n' "$p95_change" "$p99_change"
    fi
} >"$BLOCK"

awk -v block_file="$BLOCK" '
  /<!-- benchmark-ci:start -->/ {
    print
    while ((getline line < block_file) > 0) print line
    close(block_file)
    replacing = 1
    next
  }
  /<!-- benchmark-ci:end -->/ {
    replacing = 0
    print
    found = 1
    next
  }
  !replacing { print }
  END { if (!found) exit 2 }
' "$README" >"$OUTPUT"

mv "$OUTPUT" "$README"
trap - EXIT INT TERM
rm -f "$BLOCK"
