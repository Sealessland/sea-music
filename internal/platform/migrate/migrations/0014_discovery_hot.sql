CREATE TABLE discovery.engagement_events (
    event_id uuid PRIMARY KEY,
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    weight double precision NOT NULL,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX engagement_events_window_idx ON discovery.engagement_events (occurred_at, video_id);

CREATE TABLE discovery.hot_snapshots (
    video_id uuid PRIMARY KEY REFERENCES video.videos(id) ON DELETE CASCADE,
    score double precision NOT NULL DEFAULT 0,
    calculated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT hot_snapshot_score_nonnegative CHECK (score >= 0)
);

CREATE INDEX hot_snapshots_score_idx ON discovery.hot_snapshots (score DESC, video_id);
