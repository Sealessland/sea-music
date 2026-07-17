## ADDED Requirements

### Requirement: Idempotent social relations
The system SHALL support follow, like, and favorite creation and removal with uniqueness constraints and idempotent request semantics.

#### Scenario: Like request is repeated
- **WHEN** the same user repeats a like operation for the same video
- **THEN** the relation exists once and the public like count increases at most once

#### Scenario: Concurrent unlike and like
- **WHEN** conflicting operations arrive concurrently for one user and video
- **THEN** the stored relation and derived count converge without becoming negative

### Requirement: Threaded comments
The system SHALL support paginated top-level comments, bounded replies, creator or moderator deletion, and stable cursor ordering.

#### Scenario: Deleted comment has replies
- **WHEN** an authorized actor deletes a comment that has replies
- **THEN** the system retains a tombstone needed for thread structure and hides the deleted body

### Requirement: Time-positioned danmaku
The system SHALL accept bounded, sanitized danmaku messages at a video timestamp and query them by time window using deterministic pagination.

#### Scenario: Viewer loads a playback window
- **WHEN** the viewer requests danmaku for a valid time interval
- **THEN** the system returns only visible messages in that interval ordered deterministically

### Requirement: Eventually consistent counters
Public interaction counters SHALL be asynchronously aggregated from authoritative relations and SHALL be repairable by reconciliation jobs.

#### Scenario: Cached count diverges
- **WHEN** reconciliation detects a mismatch between a public counter and authoritative rows
- **THEN** the system corrects the counter and records the discrepancy

