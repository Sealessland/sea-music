def median:
  sort as $values
  | ($values | length) as $length
  | if $length == 0 then null
    elif ($length % 2) == 1 then $values[($length / 2 | floor)]
    else (($values[$length / 2 - 1] + $values[$length / 2]) / 2)
    end;

def aggregate:
  {
    runs: length,
    request_rate_rps: (map(.metrics.http_reqs_rate) | median),
    requests: (map(.metrics.http_reqs_count) | add),
    dropped_iterations: (map(.metrics.dropped_iterations) | add),
    failed_rate: (map(.metrics.failed_rate) | median),
    latency_ms: {
      median: (map(.metrics.duration_ms.median) | median),
      p95: (map(.metrics.duration_ms.p95) | median),
      p99: (map(.metrics.duration_ms.p99) | median),
      max: (map(.metrics.duration_ms.max) | median)
    },
    all_thresholds_passed: (all(.[]; all(.thresholds[]?[]?; . == false)))
  };

sort_by(.config.variant) as $runs
| ($runs
  | group_by(.config.variant)
  | map({key: .[0].config.variant, value: aggregate})
  | from_entries) as $variants
| {
    schema_version: 1,
    generated_at: (now | todateiso8601),
    methodology: {
      tool: "grafana/k6:2.0.0",
      model: "constant-arrival-rate",
      target_rps: $runs[0].config.target_rps,
      duration: $runs[0].config.duration,
      access_pattern: $runs[0].config.access_pattern,
      target_count: $runs[0].config.target_count
    },
    variants: $variants,
    comparison: {
      cache_vs_no_cache_p95_change_percent:
        ((($variants.cache.latency_ms.p95 / $variants["no-cache"].latency_ms.p95) - 1) * 100),
      cache_vs_no_cache_p99_change_percent:
        ((($variants.cache.latency_ms.p99 / $variants["no-cache"].latency_ms.p99) - 1) * 100)
    }
  }
