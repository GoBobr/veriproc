# Proposal

## Why

Operators viewing a station page (e.g. `#/instances/<instance>/stations/<station>`) cannot see which
container image version or effective configuration that station will run with. The fully resolved
station configuration already exists server-side — `station.yaml` is env-expanded once at daemon
startup and persisted to the `station_revisions` table — but no HTTP endpoint exposes it, so the only
way to check an image tag is to read YAML files and env files by hand. This change surfaces that data
through a read-only "Config" dialog on the station page.

## What Changes

- **REST API (upstream `veriprocd`)**: add `GET /api/v1/stations/{station_id}/config` returning the
  station's effective configuration as loaded at startup: env-expanded (`<env:VAR>` already
  substituted) values, with `<context>` references resolved against the instance `definitions` section
  only; per-run runtime keys (`start`, `end`, `working_root`, `prep.*`, …) remain as literal
  references. The response includes `content_hash`, `schema_version`, and `effective_at` so clients can
  tell whether the view reflects the current startup. Readable with reader or operator credentials.
- **Console gateway**: add `GET /api/console/instances/{instance_id}/stations/{station_id}/config`
  proxying the upstream endpoint (same pattern as the existing pause/unpause proxy). Readable by
  `viewer` and `operator`.
- **WebUI**: add a `Config` button in the station page header next to Pause/Unpause. Pressing it opens
  a modal dialog (styled like the existing System Info dialog) showing the fully resolved configuration
  in a read-only, scrollable field, including the container image version.
- **Spec gap**: the change documents the previously unspecified `<env:VAR>` expansion semantics as part
  of the config-view requirement (env expansion is currently implemented but has no requirement in
  `openspec/specs/`).

No breaking changes: all additions are new read-only endpoints and a new UI affordance.

## Capabilities

### New Capabilities

<!-- none -->

### Modified Capabilities

- `api`: new requirement for the station configuration endpoint (response shape, resolution semantics,
  authorization, errors), alongside the existing station status/summary and pause requirements.
- `console`: `Gateway Minimum API Surface` gains the proxied config endpoint; new requirement for the
  station Config button and read-only resolved-config dialog; `Authorization Enforcement` extended so
  `viewer` may call the config endpoint.

## Impact

- **REST API layer**: `internal/httpapi` (new route + handler), `internal/stations` (config view
  assembly from `Registry`/`station_revisions` + `definitions` context), possibly `internal/store`
  (read-only access to revision record fields already present — no migration).
- **Console layer**: `internal/console` (`router.go`, `handlers.go`, `upstream.go` proxy method).
- **WebUI**: `webapp/src/pages/StationPage.tsx` (header button), new dialog component,
  `webapp/src/api/client.ts`, `webapp/src/styles.css`.
- **CLI / executor**: unaffected.
- **Tests**: Go handler/service tests in `internal/httpapi`, `internal/console`, `internal/stations`;
  Preact component tests in `webapp/tests`.
- **Docs**: `docs/docs` station-page / API documentation updated when behavior lands.