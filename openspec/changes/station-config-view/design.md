# Design: Station Config View

## Context

See proposal.md — Why. Relevant current state:

- `stations.LoadDir` → `ReadDefinition` expands `<env:VAR>` tokens once at daemon startup
  (`envexpand.Strict`, fail-fast on undefined vars) and resolves relative `execution.executable`
  paths against the station directory. The resulting `Definition` is converted to a `Spec`
  (`SpecFromDefinition`) and seeded into the in-memory `Registry` plus the `station_revisions` SQLite
  table (`store.StationRevisionRecord`), which persists JSON blobs for `DeclaredExecution`,
  `DeclaredJobOrder`, `DeclaredInputs`, `DeclaredOutputs`, `DeclaredDownstream`, `RollingFolders`,
  plus `ContentHash`, `SchemaVersion`, `EffectiveAt`.
- Per-run `<context>` references (`<start>`, `<working_root>`, `prep.*`, …) are resolved later by
  `stations.ResolveString` against a per-run context built in `runs.Service.buildRunContext`
  (instance `definitions` + reserved runtime keys).
- The console gateway is a pure REST proxy (`UpstreamClient` in `internal/console/upstream.go`) with
  no filesystem access to station YAML. The webapp station page header
  (`webapp/src/pages/StationPage.tsx`) already renders Pause/Unpause buttons for operators, and
  `InfoDialog.tsx` provides the modal pattern to reuse.
- `Spec`/`Registry` do not retain `Definition.Description`, and the daemon does not currently keep the
  env snapshot beyond startup wiring.

## Goals / Non-Goals

- Goals: one read-only endpoint chain (upstream → gateway → dialog) exposing the effective station
  config; deterministic, testable resolution semantics; secret-value redaction; freshness metadata.
- Non-Goals: editing or hot-reloading station config; exposing the raw pre-expansion YAML; resolving
  per-run runtime keys; CLI surface (`veriproc station config` can be added later without spec change
  only if it reuses the same endpoint — out of scope here); audit logging (read-only GET).

## Decisions

**D1 — Data source: re-assemble from `Registry` + `definitions`, not re-read YAML files.**
The daemon's `Registry` holds the startup-loaded revision records; the instance `definitions` map is
in `config.Config`. Assembling the view from these keeps the endpoint consistent with what the daemon
actually loaded (single source of truth) and needs no filesystem access or re-expansion.
*Alternative:* re-read `station.yaml` and re-run `envexpand` — rejected: risks drift from the loaded
revision (env may have changed since startup), duplicates loader logic, and requires the config root
path to be plumbed into the handler.

**D2 — Resolution semantics: env expansion is already baked in; resolve `definitions` refs only.**
The stored `DeclaredExecution`/`DeclaredJobOrder` JSON blobs contain env-expanded values (expansion
happened before `SpecFromDefinition`). For `<context>` refs, run `stations.ResolveString`-style
resolution against a context containing only instance `definitions` (option b from the proposal
discussion). References that fail to resolve against definitions-only context — i.e. runtime keys like
`<start>`, `<working_root>`, `prep.*` — are left as literal `<key>` text rather than erroring.
*Alternative:* full per-run resolution — impossible without a concrete run; definitions-only
resolution gives operators the meaningful values (facility, mission ids, roots from definitions) while
clearly marking runtime-dependent parts.

**D3 — Secret redaction: retain the startup env snapshot for value-level redaction.**
`config.EnvSnapshot()` is already computed in `cmd/veriprocd/main.go`; pass it (or a filtered
name→value map of only the variables referenced by station configs) into the config-view assembly.
Before returning, replace occurrences of values whose variable *name* matches a secret pattern
(`(?i)(token|password|secret|key|credential)`) with a redaction marker. This satisfies the CLI spec's
"avoid printing secret values" principle (`cli/spec.md`) at the API layer.
*Alternative:* redact nothing — rejected: resolved configs may embed deployment secrets and the
endpoint is readable by `viewer`. *Alternative:* redact by value only for env vars — that is exactly
this decision; name-pattern matching avoids needing to classify values heuristically.

**D4 — Endpoint shape: new upstream route + thin gateway proxy, mirroring pause/unpause.**
`GET /api/v1/stations/{station_id}/config` in `internal/httpapi/router.go` handled via a small
`stations.Service` method (e.g. `ConfigView(ctx, stationID)`) that reads the registry record,
unmarshals the declared blobs, applies D2/D3, and returns a JSON document. Gateway adds
`UpstreamClient.StationConfig()` and `GET /api/console/instances/{id}/stations/{sid}/config` —
read-only, allowed for `viewer` per the modified Authorization Enforcement requirement.
*Alternative:* gateway reads station YAML directly — rejected: gateway has no config-root access by
design (Gateway Filesystem Access Contract scopes it to working roots).

**D5 — Response contract.**
Top-level: `station_id`, `station_name`, `content_hash`, `schema_version`, `effective_at`, `config`
(the resolved document: `execution`, `joborder`, `inputs`, `outputs`, `downstream`,
`rolling_folders`, plus `description`), and `config_yaml` — the same document re-serialized to YAML
server-side (Go `yaml.v3` is already a dependency via the loader) so the dialog can render it
read-only without adding a YAML library to the webapp. Field names follow the existing
`Definition` JSON tags so the frontend can render YAML-like structure directly. Unknown-field tolerance
per `Stable Field Names And Compatibility`.

**D6 — Frontend: header button + modal reusing dialog styles.**
`Config` button next to Unpause in `StationPage.tsx` (visible to all roles — read-only). New
`StationConfigDialog` component modeled on `InfoDialog` (backdrop, header with close, error state,
loading state) but with a scrollable read-only body rendering the resolved config as formatted YAML,
plus a freshness line (`content_hash`, `effective_at`). Fetch on open via new
`client.stationConfig(instanceID, stationID)` in `webapp/src/api/client.ts`.

## Risks / Trade-offs

- [Registry drops `Definition.Description` (and `Spec` has no description field)] → resolved: extend
  `Spec` and `Registry.Seed` to carry `description` through into the assembled config document; no DB
  column needed since it travels inside the response payload.
- [Redaction is name-pattern based; a secret env var named e.g. `SCENE_PATH` would not be redacted] →
  acceptable: station configs reference operational vars; document the pattern in docs. Operators
  control var naming.
- [Definitions-only resolution may surprise users expecting full expansion] → the dialog labels
  unresolved runtime refs implicitly (they remain `<key>` text) and the freshness line explains the
  view is the startup snapshot; documented in docs/docs.
- [New read endpoint increases surface for config disclosure] → read-only, authenticated, redacted
  (D3), no audit entry needed (no mutation); consistent with existing read endpoints.
- [Gateway proxy adds one upstream call per dialog open] → on-demand only (dialog open), no polling;
  within existing per-instance cache/TTL behavior.

## Migration Plan

No DB migration (all data already persisted). Deploy order: veriprocd first (endpoint), then console
gateway, then webapp bundle — older gateways/webapps simply never call the new route, so rollout is
backward-compatible in both directions. Rollback = redeploy previous artifacts; no state to unwind.

## Open Questions

None. Both previously open questions were resolved with the requester:

- Dialog body renders **YAML** (re-serialized from the resolved document) — matches the operator's
  mental model of `station.yaml`.
- The station `description` **is carried through**: extend `Spec` and registry seeding to retain it so
  it appears in the config view (no DB column needed — it travels inside the assembled config
  document).