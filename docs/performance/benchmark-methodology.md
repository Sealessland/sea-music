# Reproducible HTTP benchmark

`make benchmark` runs the public video-detail endpoint with the pinned
`grafana/k6:2.0.0` image and archives every input and output under
`artifacts/benchmarks/<UTC timestamp>/`.

## Model

- k6 `constant-arrival-rate` is an open workload model: request starts are not
  delayed merely because the service is responding slowly.
- The default workload offers 2,000 requests/second for 60 seconds after a
  15-second warm-up, repeated five times for each cache variant.
- Cache-disabled and cache-enabled runs alternate order between repeats to
  reduce time-order bias.
- The default `pareto80` access pattern sends 80% of requests across the first
  20% of the 500 deterministic videos and the rest across the cold set.
- The service must keep errors below 0.1%, P95 below 50ms, P99 below 100ms, and
  `dropped_iterations` at zero. A threshold failure is archived rather than
  discarded.

Configuration is explicit through `SEA_BENCH_*` environment variables. Useful
overrides include:

```sh
SEA_BENCH_RATE=3000 \
SEA_BENCH_DURATION=5m \
SEA_BENCH_REPEATS=5 \
SEA_BENCH_ACCESS_PATTERN=uniform \
make benchmark
```

Supported access patterns are `hot`, `uniform`, and `pareto80`.

For a resource-controlled regression run on a shared 4-core/8-thread host,
assign complete SMT sibling pairs rather than splitting siblings between the
service and load generator. For the topology where `0/4`, `1/5`, `2/6`, and
`3/7` are sibling pairs:

```sh
SEA_BENCH_DEPENDENCY_CPUS=1,5 \
SEA_BENCH_SERVICE_CPUS=2,6 \
SEA_BENCH_LOAD_CPUS=3,7 \
SEA_BENCH_SERVICE_GOMAXPROCS=2 \
SEA_BENCH_SERVICE_GOMEMLIMIT=1GiB \
SEA_BENCH_LOAD_MEMORY=1g \
make benchmark
```

The runner restores project dependency containers to all CPUs when it exits.
Per-request k6 samples are written to tmpfs during measurement and copied into
the archive after the service stops, avoiding benchmark-induced disk writes on
the measured path.

## Archive contents

- `environment.txt`: tool version, source revision when available, kernel,
  CPU, memory, and container image inventory.
- `targets.json`: exact video IDs used by the workload.
- `runs/*/raw.json.gz`: timestamped k6 measurement stream.
- `runs/*/summary.json`: stable machine-readable result for one run.
- `runs/*/metrics-{before,after}.prom`: application saturation and backlog
  evidence around the run.
- `runs/*/{api,worker,k6}.log`: process evidence and failures.
- `report.json`: median aggregation across repetitions and cache comparison.
- `SHA256SUMS`: integrity hashes for inputs and raw results.

## Interpretation boundary

This local profile is suitable for regression evidence and for explaining an
optimization. It is not a production capacity or SLA claim because the load
generator, API, Worker, PostgreSQL, Redis, Kafka, and object store share one
host. A publication-grade capacity result should run the same checked-in script
from a dedicated load-generator machine against a dedicated, pinned target
environment, and publish both machines' resource telemetry.
