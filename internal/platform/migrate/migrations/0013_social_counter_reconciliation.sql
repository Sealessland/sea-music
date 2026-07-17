CREATE TABLE social.counter_reconciliations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    previous_counts jsonb NOT NULL,
    authoritative_counts jsonb NOT NULL,
    drift_total bigint NOT NULL,
    repaired_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT counter_reconciliation_drift_positive CHECK (drift_total > 0)
);

CREATE INDEX counter_reconciliations_video_idx ON social.counter_reconciliations (video_id, repaired_at DESC);
