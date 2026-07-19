CREATE TABLE moderation.dispatch_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL UNIQUE,
    video_id uuid NOT NULL,
    video_version bigint NOT NULL,
    operation_id uuid,
    state text NOT NULL DEFAULT 'pending',
    failures integer NOT NULL DEFAULT 0,
    max_failures integer NOT NULL DEFAULT 10,
    lease_owner text,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    result jsonb,
    last_error text,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT moderation_dispatch_state_valid CHECK (state IN ('pending', 'dispatching', 'completed', 'failed')),
    CONSTRAINT moderation_dispatch_failures_valid CHECK (failures >= 0 AND max_failures > 0 AND failures <= max_failures),
    CONSTRAINT moderation_dispatch_result_valid CHECK (state <> 'completed' OR (result IS NOT NULL AND completed_at IS NOT NULL))
);

CREATE INDEX moderation_dispatch_claim_idx
    ON moderation.dispatch_jobs (available_at, created_at)
    WHERE state = 'pending';

CREATE INDEX moderation_dispatch_expired_lease_idx
    ON moderation.dispatch_jobs (lease_until)
    WHERE state = 'dispatching';
