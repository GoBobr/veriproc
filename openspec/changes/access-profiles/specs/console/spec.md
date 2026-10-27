# Delta Spec for Capability: console

## MODIFIED Requirements

### Requirement: Console SQLite Data Model

The console database SHALL be distinct from the upstream control-plane database and store only
console-local state, with minimum tables for users (or equivalent subject registry), api_tokens (storing
the effective console role with the authenticated subject; session tables optional), instance_preferences,
hidden_station_runs (or equivalent acknowledgment table), and audit_log.
When an access profile is configured, the api_tokens table SHALL NOT be the authority for authenticating
console users, and SHALL NOT be populated from user-supplied token definitions.

#### Scenario: Cookie-session CSRF note

- **GIVEN** the console authentication model
- **WHEN** cookie-based sessions are used
- **THEN** mutating endpoints MUST use CSRF protection
- **AND** the reference implementation uses token-based (not cookie-session) authentication, in which case
  the CSRF requirement does not apply

#### Scenario: Console Local Token Table Not Authoritative

- **GIVEN** a gateway configured with an access profile
- **WHEN** the gateway resolves the identity of a console user
- **THEN** the identity is resolved from the access profile and not from the console-local token table

**Additional normative rules for this requirement:**

- optional tables MAY store saved filters, column preferences, or per-user dashboard layout
- the hidden-run table MUST be console-local, identifying target instance, station, task, and retry index,
  recording acting user and timestamp, and allowing the renderer to suppress an acknowledged failed run
  without mutating upstream state

## ADDED Requirements

### Requirement: Console Authentication Source

The console gateway SHALL authenticate console users against an access profile shared with the
managed instances, so that a subject recognized by the daemons is recognized by the console with the
same subject name and the same effective role, and SHALL stop accepting console-local token
definitions as an independent credential source once a profile is configured.

#### Scenario: Shared Identity Consistent

- **GIVEN** an access profile shared by a daemon and the console gateway
- **WHEN** the same token authenticates against both
- **THEN** both resolve the same subject and the same effective console role

#### Scenario: Profile Replaces Local Token Definitions

- **GIVEN** a gateway configured with an access profile
- **WHEN** a token that appears only in the gateway's own legacy inline token list is presented
- **THEN** the request is rejected as unauthenticated

### Requirement: Console Identity Introspection

The console gateway SHALL expose an endpoint that reports the effective identity of the calling
principal, returning the subject and the effective console role, and SHALL report an anonymous
subject when the request carries no credential and anonymous browsing is enabled.

#### Scenario: Authenticated Identity Reported

- **GIVEN** a request presenting a valid profile token
- **WHEN** the identity endpoint is called
- **THEN** the response reports the profile subject and the effective console role for the token

#### Scenario: Anonymous Identity Reported

- **GIVEN** a gateway with anonymous browsing enabled
- **WHEN** the identity endpoint is called without a credential
- **THEN** the response reports the reserved anonymous subject and the read-only effective role

### Requirement: Console Anonymous Browsing

The console gateway SHALL support an anonymous-browsing mode in which requests without a credential
are admitted as a read-only principal on read endpoints, and SHALL never admit an anonymous principal
on a mutating endpoint.

#### Scenario: Anonymous Read Admitted

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request without a credential calls a read endpoint
- **THEN** the request is processed and the response contains no secret material

#### Scenario: Anonymous Mutation Rejected

- **GIVEN** the gateway running in anonymous-browsing mode
- **WHEN** a request without a credential calls a mutating endpoint
- **THEN** the request is rejected as unauthenticated with a message stating that a token is required

#### Scenario: Anonymous Browsing Disabled

- **GIVEN** the gateway not running in anonymous-browsing mode
- **WHEN** a request without a credential calls any endpoint subject to authentication
- **THEN** the request is rejected as unauthenticated

### Requirement: Anonymous Scope Independence

The gateway's anonymous-browsing setting and each managed daemon's anonymous-reader setting SHALL be
independent, each gating only its own surface, so that enabling anonymous access on one surface does
not enable it on the other.

#### Scenario: Scopes Are Independent

- **GIVEN** the gateway running in anonymous-browsing mode while a managed daemon does not allow
  anonymous readers
- **WHEN** the gateway relays a credential-less read to that daemon
- **THEN** the gateway's own anonymous read of console-local content succeeds while the daemon's
  anonymous-read capability remains governed solely by the daemon's own setting

### Requirement: Instance Visibility Filtering

The console gateway SHALL report only those managed instances the calling principal is allowed to
reach, hiding instances that reject the gateway's credential, and SHALL recompute that set after a
successful login so that instances become visible to a stronger credential.

#### Scenario: Inaccessible Instance Hidden

- **GIVEN** a managed instance whose upstream rejects the gateway's credential
- **WHEN** the instance list is requested
- **THEN** that instance is absent from the returned list

#### Scenario: Instance Appears After Login

- **GIVEN** an instance hidden from an anonymous caller
- **WHEN** the same gateway is queried with a credential that is accepted upstream
- **THEN** the instance is present in the returned list

### Requirement: Console Login Flow

The console login dialog SHALL accept a credential only, SHALL NOT accept a user-chosen role or
subject, and SHALL determine the effective identity from the server response to the submitted
credential.

#### Scenario: Login Sends Only Credential

- **GIVEN** an unauthenticated console session
- **WHEN** the operator submits the login dialog
- **THEN** only the credential is sent and no role or subject is supplied by the client

#### Scenario: Role Derived From Server

- **GIVEN** a login submission with a valid `operator` credential
- **WHEN** the identity response is received
- **THEN** the console session reflects the role reported by the server, not one chosen by the user

#### Scenario: Invalid Credential Rejected With Message

- **GIVEN** a login submission with an unknown credential
- **WHEN** the identity response is received
- **THEN** the console states that the credential was rejected and remains in the anonymous or
  previous session state

### Requirement: Anonymous Console Session

When the gateway is running in anonymous-browsing mode, the console SHALL open directly into a
read-only session over the visible instances, SHALL offer a login control to upgrade in place, and
SHALL NOT present the login dialog as a mandatory first screen.

#### Scenario: Opens Without Login

- **GIVEN** a gateway running in anonymous-browsing mode
- **WHEN** the console is opened with no stored session
- **THEN** the dashboard is presented over the visible instances without requiring a login

#### Scenario: Login Upgrades In Place

- **GIVEN** an anonymous console session
- **WHEN** the operator logs in with an `operator` credential
- **THEN** the session is upgraded without a page reload and the newly visible instances and controls
  become available

#### Scenario: Logout Returns To Anonymous

- **GIVEN** an authenticated console session
- **WHEN** the operator logs out
- **THEN** the session returns to its anonymous or unauthenticated state and operator controls are
  removed

### Requirement: No Console User Management

The console gateway SHALL expose no capability to create, list, modify, or revoke access-profile
entries, so that access management remains a local, out-of-band operation.

#### Scenario: No Profile Mutation Endpoint

- **GIVEN** the gateway running with an access profile configured
- **WHEN** a request targets any console endpoint for creating, modifying, or revoking profile
  entries
- **THEN** no such endpoint is served and the request receives a not-found response

#### Scenario: Mutating Endpoint Does Not Write Profile

- **GIVEN** an operator-authenticated request to any console mutating endpoint
- **WHEN** the request completes
- **THEN** the access profile file is unchanged on disk
