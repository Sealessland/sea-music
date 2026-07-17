# Repeatable load scenarios

This command is a functional load smoke, not the authoritative capacity
benchmark. It uses a fixed-concurrency closed workload and is retained for the
write-burst plus Outbox recovery scenario. For open-model HTTP performance
measurement, cache A/B repetition, resource controls, and immutable result
archives, use [`make benchmark`](benchmark-methodology.md).

Run:

```sh
make loadtest
```

The runner starts the formal API and Worker against the deterministic dataset and records raw JSON under `artifacts/performance/`.

1. `video-detail-read`: concurrent public detail reads, including PostgreSQL visibility/rendition lookup and S3 playback URL signing.
2. `burst-like-toggle`: concurrent authenticated PUT/DELETE requests against the natural `(user_id, video_id)` key, including Outbox writes.
3. `backlog_recovery_ms`: time after the write burst until the real Worker drains both pending rows and in-flight publishing leases through Kafka. Any failed Outbox row fails the run.

Defaults are 500 requests per HTTP scenario at concurrency 16. Override with `SEA_LOAD_REQUESTS` and `SEA_LOAD_CONCURRENCY`.

Each run also saves the final Prometheus snapshot as `raw-<timestamp>.prom`, so the zero backlog and SQL/Redis saturation signals can be audited with the latency JSON.
