CREATE TABLE identity.sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    rotated_at timestamptz,
    revoked_at timestamptz,
    replaced_by uuid REFERENCES identity.sessions(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sessions_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX sessions_family_active_idx
    ON identity.sessions (family_id)
    WHERE revoked_at IS NULL;

CREATE INDEX sessions_user_active_idx
    ON identity.sessions (user_id, expires_at)
    WHERE revoked_at IS NULL;
