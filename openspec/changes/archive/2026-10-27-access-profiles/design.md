# Design

## Context

Two independent credential paths exist today and neither supports shared, hot-reloadable identities:

- **Daemon (`internal/auth`, `cmd/veriprocd/main.go::loadAuth`)**: `VERIPROC_AUTH_TOKENS` is parsed once at startup into `auth.StaticAuthenticator`, a `map[sha256hex]Principal`. The map is immutable at runtime (only `AddToken` exists, unused). There is no file support, no anonymous principal, and no way to express "read-only without a token". Authentication is binary: the variable is empty ⇒ `authn == nil` ⇒ the middleware is a no-op and **everything**, including mutations, is open; the variable is set ⇒ every `/api/v1` path (except `/health`, `/readiness`, `/version`) demands a valid token.
- **Console (`internal/console`)**: its own token registry in the gateway's SQLite (`api_tokens`, keyed by `token_hash`), seeded at startup from `console.yaml`'s inline `auth.tokens` list (`Gateway.SeedTokens`). This is a second, diverging source of truth: the same human can be `alice` in one place and `op@local` in the other. `AuthConfig.TokensEnv` / `TokensFile` are declared in `config.go` but never read — documented-but-dead config drift. The console authentication path also accepts a credential via the `?access_token=` query parameter, which leaks into logs and Referer headers. The console's CORS middleware sets `Access-Control-Allow-Origin: *` while permitting the `Authorization` header.

The console gateway is an *optional* CLI-adjacent client, not an identity authority (each `veriprocd` remains authoritative for its own state per `openspec/specs/console/spec.md`). The user has explicitly rejected making the console an authority and wants minimal user management: grant/revoke reader (viewer) and operator (admin) access per group of instances without a database or an admin HTTP API.

The role taxonomy stays as-is: `reader` / `operator` on the daemon side; the console already maps `reader → viewer` and `operator → operator` through `console.NormalizeRole` (which also accepts the aliases `admin → operator` and `writer → operator`). No new role names are introduced; "admin" in operator vocabulary is simply an `operator`.

Deployment reality (from `e2e-vp/`): instances run as containers, share `/tcenas2` storage, and are configured through per-instance env files — so a profile file on shared storage is reachable by every daemon and by the console gateway, and editing it is a plain file operation for an operator with shell access.

## Goals / Non-Goals

**Goals:**

- One shared, file-based credential source consumable by both the daemon and the console gateway, with hot reload so a revoke takes effect without a restart.
- Three access states at the API boundary: credentialed reader, credentialed operator, and (optionally) anonymous reader — selectable independently on the daemon (direct API access) and on the console gateway (browser sessions).
- The console remains an optional client; the CLI and console must work identically whether or not the console runs.
- Minimal user management: list/add/revoke by file edit from the CLI only, with the profile file itself as the authority that decides who may grant access; the web console gains no user-management surface.

**Non-Goals:**

- A user database, an admin HTTP API (`POST/DELETE /api/v1/auth/tokens`), or remote user management over the network. Filesystem access to the profile *is* the admin boundary.
- Token expiry, rotation schedules, per-token scopes beyond role, or a user directory (LDAP/SSO).
- Changing the role vocabulary or adding a distinct `admin` capability class; `operator` is the admin class.
- Multi-tenancy of profiles (one profile per process; a group of instances shares one file).
- Session cookies, CSRF tokens, SSO, or password-based login.

## Decisions

### D1 — A shared access-profile file, env var demoted to legacy fallback

**Decision.** A new capability `access-profiles` owns a passwd-like file: `subject:role:token  # label`, one entry per line, `#` comments and blank lines ignored. `role ∈ {reader, operator}`. The daemon and the console gateway both load it. `VERIPROC_AUTH_TOKENS` remains supported verbatim when no profile path is configured.

**Rationale.** It generalizes the existing parser instead of replacing it: `loadAuth` already splits `subject:role:token[:quota]` on `:` and feeds an `auth.StaticAuthenticator`. The file version is the same parse over `[]byte` from disk plus mtime-based reload. It also eliminates the daemon's current worst-case behaviour (unset env ⇒ *mutations* open to the world), because a deployment that enables credentials no longer has an "off switch" that silently opens writes.

**Alternative considered — console as identity provider (gateway holds a user DB, daemons trust the gateway's service token).** Rejected: it makes console availability a precondition for access decisions, contradicts the console's optional status, and requires a new remote-verification hop. It is strictly more machinery for the stated goal ("minimal user management").

**Alternative considered — per-instance token databases.** Rejected: N databases to keep in sync, and each daemon already has SQLite for control-plane state; putting credentials there would need migrations on every instance for a change that is a single file per group.

**Alternative considered — keep env only, add a restart-per-grant workflow.** Rejected: revoke-by-restart is operationally unusable across a group of containers, and "who has access" stays invisible.

### D2 — One shared file per group, no merging across sources

**Decision.** A process has exactly one credential source: the profile file if a path is configured, otherwise the legacy env list. They are never merged.

**Rationale.** Merging would create ambiguous precedence (which role wins when the same subject appears twice?) and makes "is this user still allowed?" unanswerable by reading one file. The spec scenario "Profile Replaces Local Token Definitions" makes the same call on the console side: once a profile is configured, the console's own inline legacy token list stops authenticating.

**Consequence.** Deployments migrate by pointing the daemon at the profile and removing the env var; the env list remains a supported fallback for existing installs (no breaking change).

### D3 — Tokens may be stored as SHA-256 digests; the file is the authority either way

**Decision.** A token field holds a literal token by default. An entry MAY instead carry the digest form by prefixing the field with `sha256:` (e.g. `alice:operator:sha256:9f2c…`), indicating the field is already the SHA-256 hex digest of the token. At load time each entry contributes one digest to the lookup map: for a literal entry, `sha256(literalToken)`; for a `sha256:` entry, the suffix itself. At verification time the presented bearer token is hashed once (`sha256(trimmedToken)`) and looked up — so both forms authenticate the same presented token, and the lookup is a single-path hash comparison.

**Rationale.** Storing digests means a leaked profile file yields no usable credential (the file is the credential store, not merely a pointer to one). The file is already mode `0600` and admin-only, but the digest form means that even a copy-pasted into a ticket or a world-readable directory backup, it is inert. The `sha256:` prefix keeps the interpretation explicit instead of guessing from field shape (a literal token could in principle be 64 hex chars). Both the daemon's `StaticAuthenticator` and the console's `HashToken` already compute exactly `sha256(trimmedToken)`, so this reuses existing primitives with no new crypto dependency.

**Non-crypto note.** SHA-256 without a salt is the right primitive here *because* the token is a high-entropy random string (≥128 bits), not a human password. It is deliberately not a password hashing scheme; the console spec's "Password storage" requirement stays satisfied because no passwords exist in this design.

### D4 — Reload on mtime change, keep last-known-good on invalid content

**Decision.** Each consumer keeps the loaded profile plus the file's mtime/size. A bounded interval (default 5 s, jittered) the consumer stats the file; on change it re-parses into a *new* map and swaps it in atomically under the existing lock. If parsing fails, the previously loaded map stays in effect and a warning naming the file and the offending line is logged.

**Rationale.** mtime+size is sufficient because writes are atomic renames (see D5) — a rename always changes mtime and inode. The last-known-good rule means a typo in the file never locks every operator out of the fleet, which is the single most likely operational accident with a hand-edited credential file.

**Alternative considered — filesystem watch (inotify).** Rejected: NFS (the actual deployment filesystem) inotify semantics are unreliable, and a 5 s poll is trivially cheap for a file that changes a few times a month.

### D5 — Writes are temp-file + rename, and validate before replacing

**Decision.** The CLI's `users add` / `users revoke` writes a temporary file in the same directory, validates the resulting content by re-parsing it, then renames it over the target. The temp file is created `0600`.

**Rationale.** `rename()` within a directory is atomic on POSIX and on NFS, so a concurrent reader sees either the whole old file or the whole new file — never a truncated one (which would parse as "no entries" and revoke everyone). Validating before the rename means a failed write leaves the previous file intact.

### D6 — Anonymous-reader flag exists at two scopes: the daemon and the console gateway

**Decision.** The same concept — "anonymous read-only access" — is configured independently at two scopes:

- **Daemon scope**: the daemon config gains `allow_unauthenticated_readers`. When credentials are configured and the flag is set, the daemon's `/api/v1` auth middleware treats a *missing* credential on a read endpoint as an anonymous `reader` principal. This is the authority for what a *direct* API consumer (CLI, script) can see without a token.
- **Console scope**: the console gateway config gains an anonymous-browsing flag. When set, the gateway admits credential-less calls to its own `/api/console/*` read endpoints as an anonymous read-only principal, which is what lets the browser open the dashboard without a login.

Both surfaces resolve anonymous access independently; neither implies the other. The gateway's anonymous reads still travel to each daemon *with the gateway's own service credential* (`ResolveInstanceToken`), so the console's anonymous mode does not depend on the daemon's anonymous-reader flag — it depends on the service credential the gateway holds.

A *present but invalid* credential is treated exactly like no credential at all: it is downgraded to the anonymous principal, and the rejection is still reported to the caller (see D14). Mutating requests are never downgraded — a request with no credential, or with a credential that does not resolve, is rejected at both scopes, with a message naming the required roles.

The anonymous principal uses the reserved subject `anonymous` and never carries a quota.

**Rationale.** The user wants one dial to turn on "view the state without logging in", applied to the API *and* the web console. Two flags rather than one because the two surfaces are separate processes with separate trust decisions: the daemon governs direct API consumers, the gateway governs browser sessions. A single flag would force one process's policy onto another (e.g. the console would have to be open because a daemon is open). Because the console reaches upstream through its service credential, the daemon flag stays a decision about *direct* access only — and the console still hides instances whose content it cannot retrieve, which is what makes the dashboard view meaningful.

Injecting the anonymous principal in the middleware (rather than weakening the authenticator) keeps the rest of the stack unchanged: handlers, rate limiting, and audit all see a normal `Principal`. Confining anonymous access to read endpoints means the flag can never grant write capability, which is exactly the user's "view the state, never admin privileges".

**Interaction with authentication-disabled mode.** Authentication-disabled remains only when no profile path and no env tokens are configured — that is today's "spec is empty" behaviour, a deliberate open-instance mode for sandboxes. The anonymous-reader flag is the *safe* alternative: read-only by construction.

### D14 — An invalid credential is downgraded to guest, and the rejection is reported rather than swallowed

**Decision.** When a request presents a credential that does not resolve to a profile entry, the
daemon and the console gateway treat it the same way as a request with no credential at all:
it is admitted as the anonymous principal on read endpoints when the respective anonymous flag
is enabled, and rejected on mutating endpoints regardless of that flag. The downgrade is never
silent — the response reports that the credential was rejected, so a caller can tell "you are
guests" apart from "your token was accepted and you are this subject".

- **Daemon (`/api/v1`)**: a rejected credential on a read endpoint is served as the anonymous
  `reader` principal, and the response carries the header `X-VeriProc-Auth: credential-rejected`
  alongside the request's outcome. On a mutating endpoint the request is rejected with `401`
  and the existing `invalidCredentialMessage`.
- **Console gateway (`/api/console/*`)**: the same downgrade applies to *content* reads when
  anonymous browsing is enabled, and the response carries the header
  `X-VeriProc-Auth: credential-rejected`. The **identity endpoint is the exception**: it
  always rejects an unmatched credential with `401`, because it is the endpoint the browser
  calls to *resolve* a login. Downgrading there would turn a failed sign-in into a silent
  success, which is exactly the failure mode the user called out: "the wrong credential
  should result in an error visible in console, and it should anyway not skip authentication
  on wrong credentials — just display an error and allow closing the sign-in dialog".
- **CLI**: a read command that was served as guest after a rejection prints a warning to
  stderr naming the fact that the supplied credential was rejected; the exit code stays `0`
  because the read did succeed and the caller asked only to look at the state. A mutating
  command still exits `6`.

**Rationale.** The user's requirement is that an instance configured for guest access must be
usable as a guest *without* a valid token, and that "if token is wrong, consider user as a
guest". Rejecting a mistyped token outright makes the guest mode fragile in exactly the
situations it is meant to cover: a stale `VERIPROC_TOKEN` left in a shell profile, a token
that was revoked, a token pasted with a trailing newline. In all of those cases the caller
simply wants to *look at the state*, and the anonymous-reader flag already states that this
is permitted — so denying the read adds no security while making the flag unreliable.

The important property is that the downgrade is **bounded and reported**. It can never widen
capability beyond what the anonymous flag already grants: the anonymous principal reads and
cannot mutate, carries no quota entitlement beyond that of a guest, and is attributed to the
reserved `anonymous` subject rather than to any named profile subject. So a rejected token
buys the caller exactly what sending no token buys — no more. Reporting the rejection
(`X-VeriProc-Auth` on the daemon and console content responses, a warning line in the CLI, a
`401` with an explicit message in the console login flow) preserves the diagnostic that a
strict 401 used to provide: the caller learns their credential is wrong instead of silently
operating with reduced rights, and the audit trail still shows an anonymous principal rather
than a named one.

**Where the downgrade deliberately does NOT apply.** The console login flow is an *explicit*
authentication attempt, not a content request. Answering it with a guest session would report
success for a credential that was refused, and the operator would never learn that their token
was revoked. The same argument applies to any future endpoint whose purpose is to resolve a
credential. The split is therefore not "reads versus writes" but "content versus
authentication": content degrades, authentication reports.

**Alternative considered — keep rejecting unknown credentials with 401.** Rejected: it makes
the guest mode conditional on holding a token that the deployment says is optional. A user
with a stale token in their shell would have to discover the flag, unset it, and retry before
they could read state the instance has already declared readable by anyone. The strict
version also fails closed in the *wrong* direction: it blocks reads the deployment policy
already permits.

**Alternative considered — downgrade the credential to guest but keep returning 401.** Meaningless
by construction: a 401 is a rejection, so the body could not carry the read the caller asked
for.

**Alternative considered — downgrade everywhere, including the console identity endpoint.**
Rejected: the identity endpoint answers the question "is this credential good?". Returning a
guest identity for a bad credential answers "no" with "yes, you are a guest", so the browser
cannot tell the operator that their token is wrong. This was the first implementation of D14
and it was corrected after the user pointed out that a wrong credential on the password
protected dashboard must produce a visible error.

**Consequence to document.** A revoked token is indistinguishable from a wrong token at the
authentication boundary, so revocation behaves like a downgrade rather than a hard lockout.
Hard lockout remains available by disabling the anonymous flag, which rejects every
credential-less and every unmatched credential alike.

### D7 — The console authenticates against the same profile; its own token table becomes a cache, not an authority

**Decision.** The console gateway loads the same profile file and verifies caller tokens against it. The gateway's `api_tokens` table is retained but no longer populated from user tokens; it is dropped as an authority in the same change (the spec says inline legacy tokens stop authenticating once a profile is configured). The console keeps its own SQLite for console-local state (preferences, hidden runs, audit) — that is unaffected.

**Rationale.** The console already needs a per-instance *service* token to talk to each daemon (`ResolveInstanceToken`: literal / env / file). That token is a machine credential for the gateway→daemon hop and is unrelated to human identities. Keeping the gateway as the human identity verifier, using the same file, preserves one identity namespace across CLI and console without making the console an authority.

### D8 — Effective console role derives from the profile role

**Decision.** `reader` maps to console `viewer`, `operator` maps to console `operator`, using the existing `console.NormalizeRole`. No new console role is introduced and the "Password storage" requirement is not exercised (no passwords).

**Rationale.** The user explicitly asked to keep the existing role names. The console already documents this mapping and already treats `admin` as an alias of `operator`, so "admin access" in operator vocabulary is `operator` — no capability gap, only vocabulary.

### D9 — Identity introspection endpoint replaces the client-declared role

**Decision.** Add `GET /api/console/identity` (gateway namespace) returning `{subject, role, anonymous}`. The frontend login dialog sends only the credential; the gateway resolves the identity and returns it; the client stores what the server says.

**Rationale.** Today the login form asks for token + subject + role and the client stores the self-declared role (`webapp/src/state/auth.tsx`), so the UI's role-gating is cosmetic until a mutating call 403s. Making the server authoritative costs one small endpoint and removes a class of confusing UX. It also gives the console the hook needed for "log in to upgrade privileges in place".

**Alternative considered — a `POST /api/console/login` that returns a session cookie.** Rejected: reintroduces cookie sessions and the CSRF requirement the console spec explicitly notes does not apply to token-based auth, for no benefit — the client already holds the token.

### D10 — Instance visibility comes from the probe; content rejection surfaces as an access-denied pane

**Decision.** The gateway records, per instance, whether the last upstream *probe* (health) was rejected as 401/403 or failed to connect. The instance list omits instances whose probe fails, so the grid only contains instances the gateway can talk to at all. A credential rejection that happens on *content* (the dashboard summary, run lists, task detail) is deliberately **not** a hiding condition: it is reported on that instance's own dashboard as a structured `access_denied` state, and the frontend renders a permission pane in place of the station rows. On successful login the gateway re-probes, and the caller's own credential is relayed upstream (see D15), so the pane turns into data without a reload.

**Rationale.** Hiding was the right instinct when the gateway held the only upstream credential: if the gateway could not read an instance, nobody could, so a card that always failed was noise. Once upstream calls carry the *caller's* credential (D15) that premise no longer holds — an instance that rejects an anonymous caller may well accept the same browser after login. Hiding it then hides the very signal the operator needs: that the instance exists and is readable with the right credential. The user's requirement is explicit — "show a warning the user has no permission to access that pane" — which presupposes the pane is *shown*, with a warning in it, rather than absent.

The probe stays the hiding condition because it answers a different question: "is this instance configured and reachable". The daemon's health endpoint is auth-exempt, so the probe never rejects on credentials in practice; a network failure still removes the card, while a credential rejection leaves it in place to explain itself.

**Consequence to document.** An instance whose content rejects the caller appears in the grid with a permission pane instead of disappearing. Operators who prefer hard hiding can still get it by pointing the console at instances it should never serve.

### D15 — Upstream calls carry the caller's credential when there is one, else the service credential

**Decision.** Every gateway→daemon request presents a bearer token chosen per call: if the console caller is authenticated, the caller's own credential is relayed upstream; if the caller is anonymous (or presented a credential that was rejected and downgraded to guest), the gateway's configured per-instance service credential (`instances[].token` / `token_env` / `token_file`) is used instead, and if no service credential is configured no `Authorization` header is sent at all. The relay token travels in the request context, so the `UpstreamClient` interface is unchanged and every existing call site picks it up automatically.

**Rationale.** The gateway previously sent only its own service credential, which made the console's effective upstream rights a property of the deployment rather than of the logged-in user: with no service credential configured, *nobody* — not even an operator who holds a valid token — could read any instance, and logging in changed nothing. The user's model is the opposite: signing in should make the panes load, because the daemon then sees a credential the profile accepts. Relaying also keeps the two anonymous scopes honest (D6): the daemon's `allow_unauthenticated_readers=false` still blocks anonymous *content* reads through the console, exactly as it blocks direct anonymous API calls, while the console's own anonymous-browsing flag continues to govern only whether the browser may open the dashboard at all.

**Alternative considered — always relay the caller's credential, never a service token.** Rejected: it would delete the documented anonymous-browsing path, where a deployment lets guests view state through the console using a machine credential, and it would break every existing configuration that relies on `instances[].token`.

**Alternative considered — keep service-token-only and just fix the UI message.** Rejected: the warning would have to tell the operator that logging in cannot help, because the console would still be unable to read the instance for anyone. That is a configuration dead end, not a permission model.

**Consequence to document.** The console's upstream rights are now the *caller's* rights when the caller is authenticated: a `reader` console session relays a `reader` credential, so operator-only upstream actions remain unavailable to it even if `instances[].token` is an operator token. The service credential remains the ceiling for anonymous browsing only.

### D11 — `veriproc users` is a local, file-gated profile editor

**Decision.** `veriproc users list|add|revoke [--profile <path>]` operates on a profile file chosen by `--profile`, else `VERIPROC_ACCESS_PROFILE`, else the CLI config default. `add` mints a token with `crypto/rand` (32 bytes → 64 hex chars), stores it digested (`sha256:` form), and prints the plaintext token exactly once. `list` prints subject, role, and label but never a token.

Mutating subcommands (`add`, `revoke`) require the caller's own credential — supplied via `--token` or `VERIPROC_TOKEN` — and the gate is resolved **against the same profile file being edited**: the credential must hash to an entry whose role is `operator`. No API call is made, and the web console is not involved. A missing credential, an unknown credential, or a `reader` entry all refuse the operation with exit code `6` and leave the file untouched.

**Rationale.** The user's model is that the profile file *is* the authority: whoever holds it already has the power to grant anything, so a separate network check would add a dependency (the API being up) without adding security. Resolving the operator role locally keeps `users` usable in exactly the situation it matters — a host where the daemon is unreachable or being redeployed — and keeps the model single-sourced: the same file that grants access decides who may grant access.

The `reader` refusal still has value: it catches the common mistake of an operator trying to manage users with a viewer token, and it documents the intended role boundary for shared filesystems.

**Alternative considered — verifying the operator credential against the API instead of the file.** Rejected: it makes user management depend on a running daemon, it fails exactly when access must be repaired, and it would reintroduce a network dependency for a purely local operation.

**Alternative considered — no CLI helper at all (pure `sed`/`echo` editing).** Rejected as the *only* option: it invites non-random tokens and non-atomic writes. But the file format is deliberately hand-editable, so the helper is a convenience, not a gate.

### D12 — The `?access_token=` query credential path is removed from the console

**Decision.** The console's `Authenticate` stops accepting the credential from a query parameter.

**Rationale.** It leaks into access logs, browser history, and `Referer` headers. The spec surface that motivated it (SSE/preview links) has no implementation requiring it in the current tree; if it becomes necessary, it should be a short-lived signed single-use URL rather than a reusable bearer token — which is out of scope here.

**Mitigation for image/file preview links in the browser.** The frontend fetches preview content through the authenticated client (it already does), so no feature regresses. If a direct `<img src>` needs a credential, the correct answer is an authenticated fetch + object URL, which the webapp already uses for other binary paths.

### D13 — The console exposes no user-management surface

**Decision.** The gateway serves no endpoint for listing, adding, or revoking access-profile entries, and no console endpoint mutates the profile file or the gateway's credential source. User management exists only as the CLI's local file operation (D11).

**Rationale.** The console is an optional client, and access management is a security-boundary operation. Serving it over HTTP would mean a browser session could alter who has access — an escalation path that the console's own role model does not otherwise grant, and one the user explicitly does not want. Keeping it CLI-only means the tooling that changes access is the same tooling that can run on the host holding the file, with no network exposure at all.

**Consequence.** The console UI offers no "users" page; operators manage access from a shell. The console spec states this as a normative constraint so a future change cannot quietly add such an endpoint.

## Risks / Trade-offs

- **Profile file becomes the single point of failure for access** → last-known-good reload (D4) means a bad edit never revokes everyone; the file is on shared storage and a copy can be kept in version control or a backup.
- **Every consumer polls the file** → 5 s stat of one small file is negligible; on NFS it is one metadata op.
- **Filesystem permission is the admin boundary** → no remote revoke path; a compromised host account can grant itself access. Accepted trade-off for a minimal, dependency-free model; documented in `docs/docs/`.
- **The `sha256:` digest form makes the file useless for "copy this token from the file" workflows** → intentional; the CLI prints the plaintext once at creation and that is the only copy.
- **Anonymous readers widen the unauthenticated surface** → scoped to read endpoints only, and only when explicitly enabled per instance; an invalid token is downgraded to the same anonymous scope and never widens it (D6, D14).
- **Rejecting a credential downgrades rather than locks out** → the caller keeps read access but is told their credential was rejected (`X-VeriProc-Auth` header, `credential_rejected` in the console identity body, a CLI warning), and the request is still attributed to the reserved `anonymous` subject rather than to a named one. Disable the anonymous flags to get hard lockout (D14).
- **Console gateway credential limits what all console users can do** → documented (D10); not worsened by this change.
- **CORS `*` combined with `Authorization`** → pre-existing and out of scope for this change, but worth a follow-up; noted in the proposal's impact section as a known weakness, not addressed here because it would change console behaviour beyond access management.
- **Two configuration vocabularies (`VERIPROC_AUTH_TOKENS` vs. profile path)** during migration → the profile path wins and the env var is ignored only when a profile is configured; the change is additive to the daemon CLI/config surface.

## Migration Plan

1. Land the loader, CLI helper, and console-side verification behind configuration that defaults to the current behaviour (no profile path ⇒ env var ⇒ unchanged).
2. Bootstrap a profile: `veriproc users add --profile /path/profile.conf alice:operator` for each existing subject, capturing the printed tokens.
3. Configure the daemon with the profile path and `allow_unauthenticated_readers` as desired; set the console gateway's profile path and anonymous-browsing flag (the two anonymous settings are independent — enable each where it applies).
4. Restart the daemons and console once, then distribute the minted tokens to operators.
5. Remove `VERIPROC_AUTH_TOKENS` from the env files once the profile path is live.
6. Rollback: unset the profile path and restore the env list (the profile file may remain on disk unused).

## Open Questions

- Should the profile path be a single global default (e.g. `/etc/veriproc/access.conf`) with per-instance overrides, or always explicit per instance? Deferred: it is a configuration ergonomics detail that does not change the format, the verification semantics, or the task breakdown.
- Should `reader` entries be able to restrict which instances a subject reaches (per-entry instance scoping)? Deferred: the current model is one profile per group of instances, and per-entry scoping would be a format change — worth revisiting only if one file must serve heterogeneous fleets.
- Should the profile support an expiry timestamp per entry? Deferred: expiry is a policy feature, and the format can grow an optional field later without changing existing entries.
