CREATE TABLE eventing.outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    topic text NOT NULL,
    event_type text NOT NULL,
    event_version integer NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    payload jsonb NOT NULL,
    traceparent text,
    occurred_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 10,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    published_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT outbox_event_version_positive CHECK (event_version > 0),
    CONSTRAINT outbox_aggregate_version_nonnegative CHECK (aggregate_version >= 0),
    CONSTRAINT outbox_attempts_valid CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT outbox_state_valid CHECK (state IN ('pending', 'publishing', 'published', 'failed'))
);

CREATE INDEX outbox_pending_idx ON eventing.outbox (available_at, occurred_at, id)
    WHERE state = 'pending';
CREATE INDEX outbox_expired_lease_idx ON eventing.outbox (lease_until)
    WHERE state = 'publishing';

CREATE TABLE eventing.inbox (
    consumer_name text NOT NULL,
    event_id uuid NOT NULL,
    event_type text NOT NULL,
    event_version integer NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer_name, event_id),
    CONSTRAINT inbox_event_version_positive CHECK (event_version > 0)
);
