## ADDED Requirements

### Requirement: Direct media upload
The system SHALL create time-limited object-storage upload grants and verify the uploaded object's ownership, size, type, and checksum before accepting a submission.

#### Scenario: Upload is finalized
- **WHEN** the creator finalizes an upload whose object metadata matches the grant
- **THEN** the system records the source asset exactly once and queues processing

#### Scenario: Invalid object is rejected
- **WHEN** the uploaded object exceeds limits or does not match its declared checksum
- **THEN** the system rejects finalization and does not publish or process the asset

### Requirement: Explicit publication lifecycle
Each video SHALL follow a persisted state machine covering draft, uploaded, processing, review, published, failed, and withdrawn states; invalid transitions MUST be rejected.

#### Scenario: Processing completes after review approval
- **WHEN** all required renditions are ready and review is approved
- **THEN** the video becomes published and emits one publication event

#### Scenario: Stale worker attempts a transition
- **WHEN** a worker updates a video using an outdated version
- **THEN** optimistic concurrency prevents overwriting the newer state

### Requirement: Recoverable media processing
The worker SHALL execute real media probing and rendition generation, persist attempt state, and safely retry transient failures without duplicating successful outputs.

#### Scenario: Worker restarts during processing
- **WHEN** a processing lease expires after a worker stops
- **THEN** another worker can resume the job without creating duplicate rendition records

### Requirement: Published video query
The system SHALL expose published metadata and playable rendition URLs while hiding drafts, rejected assets, and internal storage credentials from unauthorized users.

#### Scenario: Viewer opens a published video
- **WHEN** a viewer requests an existing published video
- **THEN** the response contains public metadata, creator data, interaction counts, and expiring playback URLs

