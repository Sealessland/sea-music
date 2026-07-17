CREATE TABLE eventing.dead_letters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    consumer_name text NOT NULL,
    event_id uuid NOT NULL,
    original_topic text NOT NULL,
    envelope jsonb NOT NULL,
    attempts integer NOT NULL,
    last_error text NOT NULL,
    status text NOT NULL DEFAULT 'quarantined',
    replay_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    replayed_at timestamptz,
    CONSTRAINT dead_letters_attempts_positive CHECK (attempts > 0),
    CONSTRAINT dead_letters_replay_nonnegative CHECK (replay_count >= 0),
    CONSTRAINT dead_letters_status_valid CHECK (status IN ('quarantined', 'replayed')),
    CONSTRAINT dead_letters_consumer_event_unique UNIQUE (consumer_name, event_id)
);

CREATE INDEX dead_letters_status_created_idx ON eventing.dead_letters (status, created_at, id);
