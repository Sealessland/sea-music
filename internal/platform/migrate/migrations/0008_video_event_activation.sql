ALTER TABLE video.processing_jobs DROP CONSTRAINT processing_jobs_state_valid;
ALTER TABLE video.processing_jobs
    ADD CONSTRAINT processing_jobs_state_valid CHECK (state IN ('queued', 'pending', 'processing', 'succeeded', 'failed'));

ALTER TABLE video.source_assets
    ADD COLUMN finalized_event_id uuid UNIQUE REFERENCES eventing.outbox(id);
