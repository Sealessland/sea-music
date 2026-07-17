CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE identity.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username citext NOT NULL,
    email citext NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL DEFAULT 'member',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_username_unique UNIQUE (username),
    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_username_format CHECK (username::text ~ '^[a-z0-9_]{3,32}$'),
    CONSTRAINT users_email_length CHECK (length(email::text) <= 254),
    CONSTRAINT users_role_valid CHECK (role IN ('member', 'moderator', 'admin'))
);
