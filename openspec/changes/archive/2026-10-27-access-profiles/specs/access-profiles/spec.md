# Delta Spec for Capability: access-profiles

## Purpose

Defines the shared, passwd-like access-profile file that binds human-readable subjects to `reader` and
`operator` bearer credentials across a group of VeriProc instances, so grants and revocations propagate
to every consumer without restarting anything.

## ADDED Requirements

### Requirement: Access Profile Format

An access profile SHALL be a line-oriented text file in which each non-comment line binds a subject
name, a role of `reader` or `operator`, and a bearer token, so one file can be shared by a group of
instances. Blank lines and lines whose first non-space character is `#` SHALL be ignored. A token
value MUST NOT contain the field separator `:`, so exactly three fields SHALL be required per entry.

#### Scenario: Profile Entry Grants Role

- **GIVEN** an access profile containing the entry `alice:reader:token-value`
- **WHEN** the file is loaded
- **THEN** the subject `alice` is bound to the `reader` role against that profile

#### Scenario: Label Annotation On Entry

- **GIVEN** a profile entry followed by a trailing `#` comment naming the human or device it was
  issued to
- **WHEN** the file is loaded
- **THEN** the entry is accepted and the comment is retained as the entry's human-readable label

#### Scenario: Role Outside Taxonomy Rejected

- **GIVEN** a profile containing an entry whose role is neither `reader` nor `operator`
- **WHEN** the file is loaded
- **THEN** the malformed entry MUST NOT grant any access and the load MUST fail with an error that
  identifies the offending line

#### Scenario: Malformed Entry Revokes Nothing On Reload

- **GIVEN** a running process holding a previously validated profile revision
- **WHEN** a newly written revision fails validation
- **THEN** the previously validated revision MUST remain in effect and the invalid revision MUST NOT
  grant access to any of its entries

**Implementation status.** Not implemented. Credentials currently come only from the
`VERIPROC_AUTH_TOKENS` environment variable parsed once at daemon start; there is no profile file,
no labels, and no reload.

### Requirement: Credential Digest Storage

An access-profile entry SHALL be allowed to store the SHA-256 hex digest of a bearer token instead of
the literal token, marked with an explicit digest prefix, and a presented bearer token SHALL be
hashed before lookup so that digested and literal entries both authenticate the same caller.

#### Scenario: Digest Entry Authenticates

- **GIVEN** an entry whose token field holds the digest-marked SHA-256 hex digest of token `T`
- **WHEN** a client presents token `T`
- **THEN** the client is authenticated as that entry's subject with that entry's role

#### Scenario: Literal Entry Still Authenticates

- **GIVEN** an entry whose token field holds a literal token for backward compatibility
- **WHEN** the same literal token is presented
- **THEN** the client is authenticated with that entry's role

**Implementation status.** Not implemented. The daemon hashes stored tokens in memory at startup
only; no external file carries hashed credentials.

### Requirement: Profile Load Precedence

When a process is configured with an access-profile path, that profile SHALL be its credential
source, and the legacy `VERIPROC_AUTH_TOKENS` environment list SHALL remain fully honored only when
no profile path is configured.

#### Scenario: Profile Configured

- **GIVEN** both an access-profile path and `VERIPROC_AUTH_TOKENS` are set for a daemon
- **WHEN** the daemon starts
- **THEN** only subjects present in the profile file are authenticated

#### Scenario: Env-Only Configuration Unchanged

- **GIVEN** only `VERIPROC_AUTH_TOKENS` is set, with no profile path configured
- **WHEN** the daemon starts
- **THEN** authentication behaves exactly as before this change

### Requirement: Reload On File Change

A running process SHALL detect an access-profile modification within one bounded reload interval and
apply the new revision for subsequent requests without a restart, so that a grant or a revocation
affects every consumer of a shared profile within that interval.

#### Scenario: Revocation Takes Effect

- **GIVEN** a consumer that has loaded a profile granting `alice`
- **WHEN** `alice`'s entry is removed from the shared file
- **THEN** within one reload interval a request presenting `alice`'s token is rejected and no restart
  occurred

#### Scenario: Grant Takes Effect

- **GIVEN** a consumer that has not seen `bob`'s entry
- **WHEN** `bob`'s entry is added to the shared file
- **THEN** within one reload interval requests presenting `bob`'s token are accepted without a
  restart

### Requirement: Atomic Profile Updates

Every write to an access profile SHALL replace the file atomically and SHALL validate the new
contents before replacing, so concurrent readers observe either the previous or the new revision and
never a partial one; a failed update MUST leave the previous revision in place.

#### Scenario: Reader Never Sees Partial File

- **GIVEN** a consumer reloading an access profile while it is being updated
- **WHEN** the update is in progress
- **THEN** verification continues against a complete revision, never a truncated or interleaved one

#### Scenario: Failed Update Keeps Access

- **GIVEN** a valid profile file in use
- **WHEN** an update attempt fails validation or storage
- **THEN** the previous file remains intact and existing grants keep working

### Requirement: Profile File Permission Warning

A process loading an access profile that is readable beyond its owner SHALL emit a warning
identifying the file, and the management command that creates a profile file SHALL create it with
owner-only permissions.

#### Scenario: Permissive File Warned

- **GIVEN** a profile file readable by group or others on disk
- **WHEN** a consumer loads it
- **THEN** a warning naming the file is emitted while the file is still used

#### Scenario: Management Creates Restrictive File

- **GIVEN** a `users add` invocation creating a profile file that does not yet exist
- **WHEN** the file is written
- **THEN** the file is created with owner-only read and write permissions

### Requirement: Uniform Verification Across Consumers

Every consumer that verifies a token against the same access profile SHALL derive the same subject
and the same `reader` or `operator` role for that token, so CLI and web-console sessions bound to a
profile agree on who the caller is.

#### Scenario: Same Token Same Identity

- **GIVEN** a token present in a profile shared by a daemon and the console gateway
- **WHEN** the token is verified by each consumer
- **THEN** both report the same subject and the same role for the token