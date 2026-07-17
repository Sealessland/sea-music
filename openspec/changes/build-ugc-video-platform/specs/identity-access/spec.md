## ADDED Requirements

### Requirement: Secure account sessions
The system SHALL register users with unique identities, store passwords using an adaptive password hash, and issue short-lived access tokens plus revocable rotating refresh sessions.

#### Scenario: Login succeeds
- **WHEN** a user submits valid credentials
- **THEN** the system returns an access token and a refresh session without exposing the password hash

#### Scenario: Reused refresh token is rejected
- **WHEN** a client presents a refresh token that has already been rotated or revoked
- **THEN** the system rejects it and revokes the affected session family

### Requirement: Resource authorization
The system MUST authorize protected operations using both the authenticated identity and ownership or role rules.

#### Scenario: Non-owner edits a submission
- **WHEN** an authenticated user attempts to edit another creator's video without moderator privileges
- **THEN** the system returns a forbidden response and does not modify the video

### Requirement: Abuse-aware rate limiting
The system SHALL enforce configurable limits by endpoint class, user identity, and client address while exposing retry information.

#### Scenario: Write limit exceeded
- **WHEN** a caller exceeds the configured interaction write rate
- **THEN** the system rejects the request with a retry-after value and records a rate-limit metric

