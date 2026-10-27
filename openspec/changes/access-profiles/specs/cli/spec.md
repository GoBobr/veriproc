# Delta Spec for Capability: cli

## ADDED Requirements

### Requirement: Access Denied Diagnostics

When the API rejects a CLI request as unauthenticated or unauthorized, the CLI SHALL render an
actionable message naming the missing credential or insufficient role and how to supply it, SHALL
exit with the authentication exit code `6`, and MUST NOT echo any token value.

#### Scenario: Missing Token Message

- **GIVEN** a CLI invocation without a token against an instance requiring credentials
- **WHEN** the API rejects the request as unauthenticated
- **THEN** the CLI prints a message stating that a token is required and names the `VERIPROC_TOKEN`
  environment variable and the `--token` flag
- **AND** the CLI exits `6`

#### Scenario: Insufficient Role Message

- **GIVEN** a CLI invocation presenting a `reader` token to a mutating command
- **WHEN** the API rejects the request as unauthorized
- **THEN** the CLI prints a message stating that operator access is required
- **AND** the CLI exits `6`

#### Scenario: Anonymous Read Succeeds

- **GIVEN** an instance configured to allow anonymous readers
- **WHEN** a CLI invocation without a token performs a read command
- **THEN** the command succeeds without an authentication error

### Requirement: Users Management Commands

The CLI SHALL provide a `users` command group that lists, adds, and revokes entries of an access
profile file, and SHALL operate on that file directly without contacting any VeriProc API or the web
console.

#### Scenario: List Profile Entries

- **GIVEN** an access profile file containing several entries
- **WHEN** the operator runs `users list`
- **THEN** the CLI prints one line per entry with the subject, the role, and the entry label
- **AND** the CLI MUST NOT print the token value of any entry

#### Scenario: Add Entry Mints Token

- **GIVEN** an operator invoking `users add` with a subject and a role
- **WHEN** the CLI writes the new entry
- **THEN** the CLI prints the subject, the role, and the newly minted token exactly once
- **AND** the profile file is updated atomically and remains valid

#### Scenario: Revoke Entry Removes Access

- **GIVEN** an access profile containing an entry for a subject
- **WHEN** the operator invokes `users revoke` for that subject
- **THEN** the entry is removed from the profile file
- **AND** the profile file is updated atomically and remains valid

#### Scenario: Profile Path Configuration

- **GIVEN** an access profile path provided via configuration, environment, or flag
- **WHEN** the operator runs any `users` subcommand
- **THEN** the CLI operates on that file and states which file it operated on

**Implementation status.** Not implemented. The CLI has no user-management commands; credentials are
only configured out of band.

### Requirement: Users Command Operator Gate

A mutating `users` subcommand SHALL be permitted only when the caller's own credential resolves to an
`operator` entry in the access profile file being edited, and SHALL otherwise refuse the operation
without contacting any API.

#### Scenario: Mutating Command Requires Operator In Profile

- **GIVEN** a CLI invocation presenting a credential that resolves to a `reader` entry in the profile
  file being edited
- **WHEN** the operator runs `users add` or `users revoke`
- **THEN** the CLI refuses the operation, states that operator access is required, and exits `6`
- **AND** the profile file is left unchanged

#### Scenario: Mutating Command Requires No Credential Without Operator Entry

- **GIVEN** a CLI invocation presenting no credential, or a credential absent from the profile file
- **WHEN** the operator runs `users add` or `users revoke`
- **THEN** the CLI refuses the operation, states that operator access is required, and exits `6`
- **AND** the profile file is left unchanged
