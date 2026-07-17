ALTER TABLE video.videos
    ADD COLUMN category text NOT NULL DEFAULT 'general',
    ADD CONSTRAINT videos_category_valid CHECK (category ~ '^[a-z][a-z0-9_-]{1,31}$');

CREATE INDEX videos_category_published_idx ON video.videos (category, published_at DESC, id DESC)
    WHERE state = 'published';

CREATE TABLE social.blocks (
    blocker_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    blocked_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id),
    CONSTRAINT blocks_no_self CHECK (blocker_id <> blocked_id)
);

CREATE INDEX blocks_blocked_idx ON social.blocks (blocked_id, blocker_id);
