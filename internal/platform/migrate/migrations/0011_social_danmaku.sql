CREATE TABLE social.danmaku (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    position_ms integer NOT NULL,
    body text NOT NULL,
    visible boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT danmaku_position_valid CHECK (position_ms BETWEEN 0 AND 43200000),
    CONSTRAINT danmaku_body_length CHECK (length(body) BETWEEN 1 AND 100)
);

CREATE INDEX danmaku_window_idx ON social.danmaku (video_id, position_ms, id)
    WHERE visible;
CREATE INDEX danmaku_author_rate_idx ON social.danmaku (author_id, created_at DESC);
