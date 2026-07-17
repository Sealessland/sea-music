## ADDED Requirements

### Requirement: Production-style telemetry
The API and worker SHALL emit correlated structured logs, OpenTelemetry traces, and RED metrics with bounded-cardinality labels.

#### Scenario: Asynchronous publication is traced
- **WHEN** an upload finalization produces an event consumed by a worker
- **THEN** operators can correlate the API request, Outbox dispatch, broker message, and processing attempt using trace and domain identifiers

### Requirement: Honest health endpoints
The system SHALL distinguish process liveness from readiness and SHALL report unready when a required dependency prevents serving its contract.

#### Scenario: PostgreSQL is unavailable
- **WHEN** the API cannot reach PostgreSQL within the readiness budget
- **THEN** liveness remains successful while readiness fails with a non-sensitive dependency reason

### Requirement: Reproducible verification environment
The repository SHALL provide versioned local orchestration, deterministic fixtures, integration tests against real dependencies, and documented commands for end-to-end verification.

#### Scenario: Fresh environment is started
- **WHEN** a developer follows the documented bootstrap command on a supported machine
- **THEN** migrations, dependencies, API, worker, and observability components become verifiably ready

### Requirement: Measured performance scenarios
The repository SHALL define repeatable load scenarios for read-heavy video queries, interaction bursts, and event backlog recovery, and SHALL report latency, throughput, errors, saturation, and test conditions.

#### Scenario: Benchmark report is generated
- **WHEN** a load scenario completes
- **THEN** the report includes raw command/configuration, environment details, measured results, and identified bottlenecks without claiming unmeasured scale

### Requirement: Graceful degradation and shutdown
The API and worker SHALL honor timeouts, stop accepting new work during shutdown, finish or release in-flight leases, and expose defined degraded behavior for optional dependencies.

#### Scenario: Worker receives termination
- **WHEN** the worker receives a shutdown signal during a processing job
- **THEN** it either completes within the grace period or releases recoverable work without acknowledging unfinished processing
