CREATE TABLE video.videos (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id uuid NOT NULL REFERENCES identity.users(id),
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'draft',
    version bigint NOT NULL DEFAULT 0,
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT videos_title_length CHECK (length(title) BETWEEN 1 AND 120),
    CONSTRAINT videos_description_length CHECK (length(description) <= 5000),
    CONSTRAINT videos_state_valid CHECK (state IN ('draft', 'uploaded', 'processing', 'review', 'published', 'failed', 'withdrawn')),
    CONSTRAINT videos_version_nonnegative CHECK (version >= 0)
);

CREATE INDEX videos_creator_created_idx ON video.videos (creator_id, created_at DESC, id DESC);
CREATE INDEX videos_published_cursor_idx ON video.videos (published_at DESC, id DESC) WHERE state = 'published';

CREATE TABLE video.source_assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id uuid NOT NULL UNIQUE REFERENCES video.videos(id) ON DELETE CASCADE,
    object_key text NOT NULL UNIQUE,
    size_bytes bigint,
    content_type text,
    checksum_sha256 text,
    status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    finalized_at timestamptz,
    CONSTRAINT source_assets_status_valid CHECK (status IN ('pending', 'uploaded', 'verified', 'rejected')),
    CONSTRAINT source_assets_size_positive CHECK (size_bytes IS NULL OR size_bytes > 0)
);

CREATE TABLE video.renditions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_asset_id uuid NOT NULL REFERENCES video.source_assets(id) ON DELETE CASCADE,
    config_version integer NOT NULL,
    kind text NOT NULL,
    object_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'pending',
    width integer,
    height integer,
    bitrate integer,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT renditions_identity_unique UNIQUE (source_asset_id, config_version, kind),
    CONSTRAINT renditions_status_valid CHECK (status IN ('pending', 'ready', 'failed'))
);

CREATE TABLE video.processing_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_asset_id uuid NOT NULL REFERENCES video.source_assets(id) ON DELETE CASCADE,
    config_version integer NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    lease_owner text,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT processing_jobs_identity_unique UNIQUE (source_asset_id, config_version),
    CONSTRAINT processing_jobs_state_valid CHECK (state IN ('pending', 'processing', 'succeeded', 'failed')),
    CONSTRAINT processing_jobs_attempts_valid CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts)
);

CREATE INDEX processing_jobs_claim_idx ON video.processing_jobs (available_at, created_at)
    WHERE state = 'pending';
CREATE INDEX processing_jobs_expired_lease_idx ON video.processing_jobs (lease_until)
    WHERE state = 'processing';

CREATE TABLE video.state_transitions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    video_id uuid NOT NULL REFERENCES video.videos(id) ON DELETE CASCADE,
    from_state text NOT NULL,
    to_state text NOT NULL,
    actor_id uuid REFERENCES identity.users(id),
    reason text NOT NULL DEFAULT '',
    resulting_version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX state_transitions_video_idx ON video.state_transitions (video_id, resulting_version);
