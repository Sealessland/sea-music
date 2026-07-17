CREATE TABLE social.video_counters (
    video_id uuid PRIMARY KEY REFERENCES video.videos(id) ON DELETE CASCADE,
    likes bigint NOT NULL DEFAULT 0,
    favorites bigint NOT NULL DEFAULT 0,
    comments bigint NOT NULL DEFAULT 0,
    danmaku bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT video_counters_nonnegative CHECK (likes >= 0 AND favorites >= 0 AND comments >= 0 AND danmaku >= 0)
);
