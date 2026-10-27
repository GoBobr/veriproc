# Proposal

## Why

Access to VeriProc is currently all-or-nothing: tokens come from a single environment variable parsed once at daemon start (revoke = restart, who-has-what is unrecorded), an instance configured with tokens denies anonymous read-only use even for harmless dashboards, and the web console always forces a login page where the user must manually type a subject and pick a role the server later re-checks anyway. Operators need a minimal, file-based way to grant and revoke reader/operator access across a group of instances without a user database or an admin API, plus anonymous read-only viewing where an instance opts in.

## What Changes

- **Access profile file** (new): a passwd-like file shared by a group of instances (`subject:role:token`, role ∈ `reader`|`operator`, `#` comments for human-readable labels), read by both `veriprocd` and the console gateway, hot-reloaded on mtime change so grant/revoke takes effect in seconds without restarting anything. Tokens in the file MAY be stored as SHA-256 hex digests; verification hashes the presented bearer token before lookup. The existing `VERIPROC_AUTH_TOKENS` environment variable keeps working as a legacy fallback (no breaking change).
- **Anonymous read-only access** (new): an `allow_unauthenticated_readers` flag at two scopes. On the **daemon** it governs the `/api/v1` surface of that instance; on the **console gateway** it governs whether the browser-facing console API admits credential-less read-only sessions. When a daemon's flag is set, requests without a token on read endpoints act as an anonymous `reader`; mutating or unauthorized requests receive a clear 401 explaining that a reader/operator token is required (no more raw error dumps). The console flag additionally gates the dashboard: with both scopes opted in, the console opens its dashboard for anonymous visitors; the console hides instances it cannot serve read content for.
- **Console anonymous browser + token-only login** (new): console config flag controlling anonymous content browsing; when set, the frontend opens the dashboard without a login prompt. The login form accepts a token only and queries a new identity endpoint for the effective `subject`/`role` (removing the client-declared role). The role taxonomy is unchanged: `reader`/`operator` on the daemon, existing `viewer`/`operator` console mapping via `NormalizeRole`.
- **Instance visibility** (new): the console gateway reports per-instance access for the bounded principal and hides instances the anonymous or logged-in user cannot reach; instances appear/disappear after login/logout.
- **Invalid credential falls back to guest** (new): a request presenting a credential that matches no profile entry is handled exactly like a request with no credential — served as the anonymous `reader`/read-only principal on read endpoints when the corresponding anonymous flag is enabled, and still rejected on mutating endpoints. The downgrade is reported so the caller learns their credential was rejected instead of silently losing rights, and the request is still attributed to the reserved `anonymous` subject: a daemon or console content response carries the header `X-VeriProc-Auth: credential-rejected`, and the CLI prints a warning to stderr while still exiting `0` for a read. The console *login* flow is the deliberate exception — the identity endpoint always rejects an unmatched credential with `401`, so a wrong password produces a visible error in a dismissible sign-in dialog rather than a silent guest session. Disabling the anonymous flags restores hard lockout.
- **CLI access diagnostics** (new): 401/403 responses produce actionable messages ("set VERIPROC_TOKEN", "requires operator access") while preserving exit code `6` and never echoing token values.
- **CLI user management** (new): `veriproc users list|add|revoke`, a local helper that edits the access profile file (atomic write via temp file + rename), printing a freshly minted token exactly once. Mutating subcommands are gated on the caller's own credential resolving to an `operator` entry **in that profile file** — no API call, no console involvement. The web console gains no user-management capability whatsoever.

No executor-layer change: authorization is enforced at the HTTP boundary only.

## Capabilities

### New Capabilities

- `access-profiles`: the shared access-profile file — format, hashing rules, loading precedence (file vs. legacy env), mtime-based hot reload, grant/revoke semantics, and the anonymous-reader profile behavior that both `veriprocd` and the console gateway consume.

### Modified Capabilities

- `api`: authentication modes for `/api/v1` (tokens configured vs. anonymous readers enabled vs. authentication disabled), anonymous reader scoping to read endpoints, and the required diagnostic message on rejected access.
- `cli`: `users list|add|revoke` command semantics (operator-gated, local profile edit, single-time token print) and actionable access-denied diagnostics tied to the existing exit-code model.
- `console`: authentication source becomes the shared access profile, anonymous browsing mode gated by config, identity introspection endpoint replacing the client-declared role, and per-instance visibility filtering of the instance list.

## Impact

- **REST API layer**: `internal/httpapi/auth_middleware.go`, `internal/auth`, `cmd/veriprocd/main.go` (`loadAuth` → profile loader), daemon instance config validation (`internal/config`, `internal/domain-model` instance configuration).
- **Console layer**: `internal/console` (`config.go` auth fields, `auth.go`, `router.go` middleware, `gateway.go` seeding replaced by profile reload, `handlers.go` `/me` + instance listing), `webapp/src` (`state/auth.tsx`, `pages/Login.tsx`, instance list filtering, `api/client.ts`).
- **CLI layer**: `cmd/veriproc/main.go` (access-denied rendering, new `users` subcommand group).
- **Specs**: new `access-profiles` capability; deltas in `api`, `cli`, `console` (`openspec/specs/<domain>` untouched until archive).
- **Dependencies/systems**: none new (stdlib only); file-permission hygiene (`0600`, atomic rename) becomes an operational contract; `docs/docs/` gets an access-profile how-to.