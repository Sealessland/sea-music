CREATE TABLE social.follows (
    follower_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    followee_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (follower_id, followee_id),
    CONSTRAINT follows_no_self CHECK (follower_id <> followee_id)
);

CREATE INDEX follows_followee_idx ON social.follows (followee_id, follower_id);

CREATE TABLE social.video_likes (
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, video_id)
);

CREATE INDEX video_likes_video_idx ON social.video_likes (video_id, user_id);

CREATE TABLE social.video_favorites (
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, video_id)
);

CREATE INDEX video_favorites_video_idx ON social.video_favorites (video_id, user_id);

CREATE TABLE social.relation_versions (
    relation_kind text NOT NULL,
    actor_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    target_id uuid NOT NULL,
    version bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (relation_kind, actor_id, target_id),
    CONSTRAINT relation_versions_kind_valid CHECK (relation_kind IN ('follow', 'like', 'favorite')),
    CONSTRAINT relation_versions_positive CHECK (version > 0)
);
