CREATE TABLE social.comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    parent_id uuid REFERENCES social.comments(id) ON DELETE RESTRICT,
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    deleted_by uuid REFERENCES identity.users(id),
    CONSTRAINT comments_body_length CHECK (length(body) <= 1000),
    CONSTRAINT comments_deleted_consistent CHECK ((deleted_at IS NULL) = (deleted_by IS NULL))
);

CREATE INDEX comments_top_level_cursor_idx ON social.comments (video_id, created_at DESC, id DESC)
    WHERE parent_id IS NULL;
CREATE INDEX comments_replies_idx ON social.comments (parent_id, created_at, id)
    WHERE parent_id IS NOT NULL;
