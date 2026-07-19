CREATE SCHEMA IF NOT EXISTS moderation;

CREATE TABLE moderation.review_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id text NOT NULL UNIQUE,
    input_hash text NOT NULL,
    request jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    result jsonb,
    error text,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    lease_owner text,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT review_operations_hash_valid CHECK (input_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT review_operations_status_valid CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    CONSTRAINT review_operations_attempts_valid CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT review_operations_result_valid CHECK (
        (status = 'completed' AND result IS NOT NULL AND completed_at IS NOT NULL)
        OR (status <> 'completed' AND result IS NULL)
    )
);

CREATE INDEX review_operations_claim_idx
    ON moderation.review_operations (available_at, created_at)
    WHERE status = 'pending';

CREATE INDEX review_operations_expired_lease_idx
    ON moderation.review_operations (lease_until)
    WHERE status = 'running';
