# Tasks

## 1. Access Profile Parsing And Verification

- [x] 1.1 Add an access-profile package that parses `subject:role:token` entries with `#` comment labels, skips blank and comment lines, splits each entry into exactly three fields, and rejects a role outside `reader`/`operator` with an error naming the offending line — verify with unit tests covering the "Profile Entry Grants Role", "Label Annotation On Entry", and "Role Outside Taxonomy Rejected" scenarios (`internal/accessprofile`, new test file).
- [x] 1.2 Support the digest-marked token form in the profile parser so an entry whose token field is prefixed with `sha256:` registers the digest directly instead of hashing the literal, and build the lookup map as `sha256(literal)` for literal entries — verify with unit tests covering the "Digest Entry Authenticates" and "Literal Entry Still Authenticates" scenarios.
- [x] 1.3 Implement the profile-backed authenticator that hashes the presented bearer token once and resolves the entry's subject and role, returning distinct errors for a missing token and an unknown token — verify with unit tests asserting the subject and role are identical for a literal entry and its digest form.
- [x] 1.4 Implement last-known-good reloading: on a failed re-parse the previously loaded entries stay in effect and the invalid revision grants nothing — verify with a unit test that writes an invalid revision over a valid one and asserts the previous subject still authenticates while the invalid entry's subject does not ("Malformed Entry Revokes Nothing On Reload").

## 2. Profile Reload, Permissions, And Atomic Writes

- [x] 2.1 Add mtime-and-size based change detection with a bounded reload interval and an injectable clock, so a changed file is re-read without a restart — verify with a unit test that modifies the file, advances the clock past the interval, and asserts the new subject authenticates ("Grant Takes Effect") and a removed subject no longer does ("Revocation Takes Effect").
- [x] 2.2 Emit a warning naming the profile file when it is readable by group or others, while continuing to use it — verify with a unit test that creates a mode-0644 file, loads it, and asserts the warning is emitted.
- [x] 2.3 Implement atomic profile writes: build the new content in a temporary file in the target directory with owner-only permissions, validate it by re-parsing, then rename it over the target, leaving the previous file intact on any failure — verify with unit tests for "Reader Never Sees Partial File" (a concurrent reader always observes a complete revision) and "Failed Update Keeps Access" (a failed write leaves existing grants working).

## 3. Daemon Authentication Modes

- [x] 3.1 Extend the daemon configuration with an access-profile path and an `allow_unauthenticated_readers` flag governing the daemon's own API surface, and resolve the credential source so the profile is used when configured and `VERIPROC_AUTH_TOKENS` remains honored when no profile is set — verify with unit tests covering "Profile Configured" and "Env-Only Configuration Unchanged" (env-only behavior unchanged).
- [x] 3.2 Implement the three authentication modes in the API middleware — authentication disabled, credentialed, and credentialed with anonymous readers — where an anonymous reader is admitted only on read endpoints and a mutating request without a credential is always rejected — verify with API tests covering "Authentication Disabled Permits Mutating Calls", "Credentialed Mode Demands A Token", "Anonymous Reader Admitted On Reads", and "Anonymous Reader Denied On Mutations".
- [x] 3.3 Represent the anonymous principal with a reserved subject distinct from every profile subject and without a quota — verify with a test asserting the resolved principal's subject differs from any profile subject ("Anonymous Subject Is Distinct").
- [x] 3.4 Improve the authentication and authorization error messages so a missing credential names the accepted roles and a credential list to supply, and an insufficient role states that operator authorization is required, without including any token value — verify with API tests covering "Missing Credential Message", "Insufficient Role Message", and "No Secret In Rejection".

## 4. CLI Access Diagnostics And Users Commands

- [x] 4.1 Render actionable messages for unauthenticated and unauthorized API rejections in the CLI — missing credential names `VERIPROC_TOKEN` and `--token`, insufficient role states that operator access is required — while preserving exit code `6` and never echoing the token value — verify with CLI tests covering "Missing Token Message" and "Insufficient Role Message".
- [x] 4.2 Verify that a read command without a token succeeds against an instance configured to allow anonymous readers — verify with a CLI test covering "Anonymous Read Succeeds" using a stub API that admits anonymous reads.
- [x] 4.3 Implement `veriproc users list` against a profile file resolved from `--profile`, then `VERIPROC_ACCESS_PROFILE`, then the CLI configuration default, printing subject, role, and label per entry and never printing a token value — verify with CLI tests covering "List Profile Entries" and "Profile Path Configuration".
- [x] 4.4 Implement `veriproc users add` that mints a high-entropy token with `crypto/rand`, stores it in the profile in digest-marked form, writes the file atomically, and prints the plaintext token exactly once — verify with a CLI test covering "Add Entry Mints Token" asserting the printed token authenticates against the resulting profile entry and that a second read of the file shows no plaintext token.
- [x] 4.5 Implement `veriproc users revoke` that removes the entry for a subject and writes the file atomically — verify with a CLI test covering "Revoke Entry Removes Access".
- [x] 4.6 Gate the mutating `users` subcommands on the caller's own credential resolving to an `operator` entry *in the profile file being edited*, without contacting any API: a missing credential, an unknown credential, or a `reader` entry all refuse the operation with exit code `6` and leave the file unchanged — verify with CLI tests covering "Mutating Command Requires Operator In Profile" and "Mutating Command Requires No Credential Without Operator Entry".

## 5. Console Gateway Authentication And Identity

- [x] 5.1 Add console configuration for the access-profile path and the anonymous-browsing flag, resolving the profile as the credential source once configured and stopping acceptance of the gateway's own inline legacy token definitions — verify with gateway tests covering "Profile Replaces Local Token Definitions".
- [x] 5.2 Ensure the gateway's anonymous-browsing setting is independent of each managed daemon's anonymous-reader setting, with the gateway's anonymous reads still relayed using its own service credential — verify with a gateway test covering "Scopes Are Independent".
- [x] 5.3 Verify console callers against the shared profile so the same token resolves to the same subject and the same effective console role as at the daemon, using the existing `reader → viewer` and `operator → operator` mapping — verify with a gateway test covering "Shared Identity Consistent" that compares the console resolution with the daemon authenticator's.
- [x] 5.4 Remove the query-parameter credential path from console authentication so a credential is accepted only from the `Authorization` header — verify with a gateway test asserting a request carrying only `?access_token=` is rejected.
- [x] 5.5 Add the console identity endpoint returning the effective subject, effective console role, and whether the principal is anonymous — verify with gateway tests covering "Authenticated Identity Reported" and "Anonymous Identity Reported".
- [x] 5.6 Implement console anonymous-browsing mode: credential-less requests are admitted as a read-only principal on read endpoints and rejected on mutating endpoints, while the mode being disabled keeps rejecting credential-less requests entirely — verify with gateway tests covering "Anonymous Read Admitted", "Anonymous Mutation Rejected", and "Anonymous Browsing Disabled".
- [x] 5.7 Serve no console endpoint that lists, adds, modifies, or revokes access-profile entries, and assert that no console mutating endpoint alters the profile file on disk — verify with gateway tests covering "No Profile Mutation Endpoint" and "Mutating Endpoint Does Not Write Profile".

## 6. Console Instance Visibility

- [x] 6.1 Record per-instance upstream access outcome during polling, distinguishing an authentication or authorization rejection from other upstream failures — verify with a gateway test asserting an instance whose upstream returns 401 is marked inaccessible.
- [x] 6.2 Filter the instance list so instances inaccessible to the gateway are omitted, and re-probe and re-filter after a successful identity resolution — verify with gateway tests covering "Inaccessible Instance Hidden" and "Instance Appears After Login".

## 7. Web Console Session And Login

- [x] 7.1 Rework the login dialog to collect a credential only, removing the subject and role inputs — verify with a webapp test asserting the form has no role or subject field and submits only the credential ("Login Sends Only Credential").
- [x] 7.2 Make the console session derive the subject and role from the server identity response instead of the client-declared values, and treat an unknown credential as a rejected login that preserves the previous session state — verify with webapp tests covering "Role Derived From Server" and "Invalid Credential Rejected With Message".
- [x] 7.3 Open the console directly into a read-only anonymous session when the gateway reports anonymous browsing is enabled, offering a login control that upgrades the session in place without a page reload, and restoring the anonymous state on logout — verify with webapp tests covering "Opens Without Login", "Login Upgrades In Place", and "Logout Returns To Anonymous".
- [x] 7.4 Hide instances the current session cannot reach, and re-show them after an in-place login upgrade — verify with a webapp test covering the instance list rendering for anonymous, viewer, and operator sessions.
- [x] 7.5 Ensure the console UI offers no user-management route, page, or control for creating, listing, or revoking access-profile entries — verify with a webapp test asserting no such route or control is rendered in any session state.

## 8. Documentation And End-To-End Verification

- [x] 8.1 Document the access-profile format, the digest-marked token form, the atomic-write and permission rules, the daemon `allow_unauthenticated_readers` flag, the console anonymous-browsing flag (stating that the two are independent), the CLI-only `users` command and its operator-in-file gate, and the absence of any console user-management surface in `docs/docs/`, including a migration recipe from `VERIPROC_AUTH_TOKENS` — verify that every documented command and config key appears in the corresponding implementation and that the documented example profile loads successfully in a test.
- [x] 8.2 Run the full Go test suite and the webapp test suite and confirm no pre-existing test regressed — verify with `go test ./...` and the `webapp` test command reporting success.
- [x] 8.3 Exercise the flow end to end against a local daemon and gateway: bootstrap the profile with an operator entry via `users add`, grant a reader subject, authenticate with it from the CLI, revoke it with the operator credential, and observe the rejection without a daemon restart — verify with an integration script whose output is captured as the artifact.

## 9. Invalid Credential Downgrade To Guest

- [x] 9.1 Downgrade an unmatched credential to the anonymous principal on daemon read endpoints when the anonymous-reader allowance is enabled, keep rejecting it on mutating endpoints, and never widen capability beyond the anonymous allowance — verify with API tests covering "Invalid Credential Served As Guest On Reads", "Invalid Credential Rejected On Mutations", "Invalid Credential Without Anonymous Allowance", and "Downgrade Never Widens Capability".
- [x] 9.2 Report the downgrade to the caller through a response header on the daemon, so a client can tell a rejected credential from an accepted one — verify with an API test asserting the header is set on a downgraded read and absent on an accepted one.
- [x] 9.3 Apply the downgrade in the console gateway on *content* read endpoints when anonymous browsing is enabled, keep rejecting it on mutating endpoints, and report the rejection through the response header, while keeping the identity endpoint strict — verify with gateway tests covering "Invalid Credential Served As Guest On Content Reads", "Invalid Credential Rejected On Mutations", "Identity Endpoint Never Downgrades", and "Rejected Credential Reports Rejection".
- [x] 9.4 Warn in the CLI when a read was served as a guest after the server rejected the supplied credential, without echoing the credential value and without changing the exit code, so the command still completes successfully — verify with CLI tests covering "Rejected Credential Reports Guest Fallback" and "Rejected Credential On Mutation Fails".
- [x] 9.5 Surface the rejection in the web console: a login with an unmatched credential SHALL show the server's rejection message in a dialog, SHALL NOT become a guest session behind the operator's back, and the dialog SHALL be dismissible so browsing continues in the previous session state — verify with a webapp test covering the rejected login and the dismissal.
- [x] 9.6 Document the downgrade behaviour, the `X-VeriProc-Auth` response header, the strict console login path, and the converse that disabling the anonymous flags restores hard lockout — verify that every documented name appears in the corresponding implementation.

## 10. Upstream Credential Relay And Access-Denied Panes

- [x] 10.1 Relay the console caller's own credential on every gateway→daemon request when the caller is authenticated, falling back to the configured per-instance service credential for anonymous callers and sending no `Authorization` header when no service credential exists — verify with gateway tests covering "Authenticated Caller Credential Relayed", "Anonymous Caller Uses Service Credential", and "Anonymous Caller Without Service Credential Sends None".
- [x] 10.2 Restrict instance-list hiding to the upstream probe outcome so an instance whose content rejects the caller stays listed — verify with a gateway test covering "Content Rejection Does Not Hide Instance" alongside the existing "Inaccessible Instance Hidden" and "Instance Appears After Login".
- [x] 10.3 Report an upstream `401`/`403` on the dashboard as a structured access-denied state with an empty station array instead of a raw error blob — verify with a gateway test covering "Access Denial Reported Structurally".
- [x] 10.4 Render a permission notice in the instance pane for the access-denied state — pointing anonymous sessions at the top-bar login control rather than carrying its own login button — and make every dashboard/tree/info collection render null-safely so a `null` array never aborts rendering — verify with webapp tests covering "Permission Notice Rendered" and "Null Collections Never Crash The Pane".
- [x] 10.5 Document the upstream credential relay (caller credential, else service credential), the access-denied pane, and the resulting rights model in `docs/docs/10-web-console.md` — verify that every documented behaviour appears in the implementation.

## Workflow follow-up

- Archive the change after the project's review requirements are satisfied.
- Verify the archived result against `openspec/specs/`.
