## ADDED Requirements

### Requirement: Cursor-based following feed
The system SHALL return published videos from followed creators using opaque cursor pagination with deterministic tie-breaking and no offset-based deep pagination.

#### Scenario: New content arrives between pages
- **WHEN** a viewer requests the next feed page after newer videos were published
- **THEN** the cursor continues from the prior boundary without repeating already returned items

### Requirement: Windowed hot ranking
The system SHALL maintain a time-windowed hot ranking from weighted, deduplicated engagement events with score decay and a documented fallback.

#### Scenario: Ranking cache is unavailable
- **WHEN** the ranking cache cannot be read
- **THEN** the API returns a bounded persisted snapshot or a controlled degraded response rather than recomputing an unbounded ranking synchronously

### Requirement: Explainable recommendation recall
The system SHALL provide a basic recommendation feed built from explicit signals such as follows, categories, freshness, and popularity, and SHALL expose a machine-readable reason code for each item.

#### Scenario: User has no history
- **WHEN** a new user requests recommendations
- **THEN** the system returns diverse recent and popular published videos with cold-start reason codes

### Requirement: Visibility filtering
Every discovery result MUST enforce publication status, moderation visibility, and block relationships before returning content.

#### Scenario: Video is withdrawn after ranking
- **WHEN** a ranked video becomes withdrawn before a feed request
- **THEN** the system filters it from the response even if it remains in a cached candidate set

