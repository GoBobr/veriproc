# Delta Spec for Capability: api

## ADDED Requirements

### Requirement: API Authentication Modes

The daemon SHALL support three mutually exclusive authentication modes resolved from its configuration:
authentication disabled, in which no credential is required on any endpoint; credentialed, in which a
valid bearer token is required; and credentialed with anonymous readers, in which a valid bearer
token is required for mutating requests while requests without a token are admitted as an anonymous
`reader` on read endpoints.

#### Scenario: Authentication Disabled Permits Mutating Calls

- **GIVEN** a daemon started with authentication disabled
- **WHEN** a request without a token calls a mutating endpoint
- **THEN** the request is processed without an authentication failure

#### Scenario: Credentialed Mode Demands A Token

- **GIVEN** a daemon started with credentials configured and no anonymous-reader allowance
- **WHEN** a request without a token calls any endpoint subject to authentication
- **THEN** the request is rejected as unauthenticated

#### Scenario: Anonymous Reader Admitted On Reads

- **GIVEN** a daemon started with credentials configured and anonymous readers allowed
- **WHEN** a request without a token calls a read endpoint subject to authentication
- **THEN** the request is processed as an anonymous `reader` principal

#### Scenario: Anonymous Reader Denied On Mutations

- **GIVEN** a daemon started with credentials configured and anonymous readers allowed
- **WHEN** a request without a token calls a mutating endpoint
- **THEN** the request is rejected as unauthenticated and names the read/anonymous scope that was
  exceeded

### Requirement: Anonymous Principal Identity

An anonymous reader admitted under the anonymous-reader allowance SHALL be represented by a
distinguished, non-credential subject that is stable across requests and distinguishable from any
credential-bearing subject, so audit records and logs never attribute anonymous activity to a named
user.

#### Scenario: Anonymous Subject Is Distinct

- **GIVEN** a daemon started with anonymous readers allowed
- **WHEN** an anonymous reader performs a read request
- **THEN** the resolved principal carries a reserved anonymous subject distinct from any profile
  subject

### Requirement: Access Rejection Diagnostics

An authentication or authorization rejection SHALL return an error envelope whose message states
which credential or role is required, so a client can guide the operator without inspecting server
configuration; the rejection MUST NOT reveal any token value.

#### Scenario: Missing Credential Message

- **GIVEN** a credentialed daemon
- **WHEN** a request without a token calls an endpoint subject to authentication
- **THEN** the response states that a bearer token is required and names the accepted roles

#### Scenario: Insufficient Role Message

- **GIVEN** a credentialed daemon
- **WHEN** a `reader` credential calls a mutating endpoint
- **THEN** the response states that operator authorization is required

#### Scenario: No Secret In Rejection

- **GIVEN** any rejected request
- **WHEN** the error envelope is inspected
- **THEN** it contains no token value and no credential material from the access configuration
