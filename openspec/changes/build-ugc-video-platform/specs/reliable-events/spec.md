## ADDED Requirements

### Requirement: Atomic domain event capture
State changes that require asynchronous effects SHALL write an Outbox event in the same PostgreSQL transaction as the authoritative business change.

#### Scenario: Database transaction rolls back
- **WHEN** a business write fails and its transaction rolls back
- **THEN** neither the state change nor its domain event is visible

### Requirement: At-least-once event delivery
The dispatcher SHALL publish pending Outbox events with stable event identifiers and SHALL only mark delivery after broker acknowledgement.

#### Scenario: Dispatcher crashes after publish
- **WHEN** the dispatcher stops after broker acknowledgement but before updating the Outbox row
- **THEN** the event may be republished with the same identifier and consumers handle it idempotently

### Requirement: Idempotent consumers
Each consumer SHALL persist processed event identifiers or use an equivalent atomic uniqueness boundary before committing non-idempotent side effects.

#### Scenario: Duplicate event is consumed
- **WHEN** a consumer receives an event identifier it has completed
- **THEN** it acknowledges the duplicate without repeating the side effect

### Requirement: Retry and dead-letter operations
Consumers SHALL apply bounded exponential retry, route exhausted events to a dead-letter stream, and expose an authenticated replay operation.

#### Scenario: Poison event exhausts retries
- **WHEN** an event repeatedly fails with a non-recoverable payload error
- **THEN** it is quarantined with failure context and does not block later partition events indefinitely

