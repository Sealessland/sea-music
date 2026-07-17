# Fault drills

Run all drills against isolated test databases:

```sh
./scripts/fault-drill.sh
```

The command uses real PostgreSQL, Redis, SeaweedFS, Kafka, ffprobe, and ffmpeg.

## Covered failures

- Outbox broker outage: publishing to an unavailable address must return the leased row to `pending`; after switching to the real broker, the same event drains and becomes `published`.
- Dispatcher ack window: an acknowledged event whose database lease expires is republished with the same event ID; Inbox executes the side effect once.
- Poison event: three failed attempts create one audited dead letter and publish it to the real `.dlq` topic; only admin replay is accepted.
- Redis outage: hot feed reads use the bounded PostgreSQL snapshot and set `degraded=true`; no unbounded synchronous recomputation occurs.
- Worker interruption: an abandoned media lease expires, a new worker claims the same deterministic job, overwrites deterministic rendition keys, and completes it.

Inspect `/metrics` after a drill for `sea_music_outbox_events`, `sea_music_outbox_oldest_seconds`, `sea_music_processing_jobs`, and `sea_music_counter_drift_total`. Use Tempo to follow the stable trace context across API, Kafka, consumer, SQL, Redis, and ffmpeg spans.
