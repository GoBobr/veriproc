# Proposal

## Why

The web console's top-bar heading and browser tab title are hardcoded ("Veriproc Operator Console"),
so every deployment — including site- or project-specific gateway instances — shows the same name.
Deployments that aggregate a dedicated instance (e.g. an ADPP-TCE processing profile) want the console
to identify itself by that deployment, without forking the webapp bundle.

## What Changes

- **Console gateway config**: new optional `ui.page_title` key (`veriproc.console/v1`), defaulting to
  `VeriProc Operator Console` when unset.
- **Console gateway API**: `GET /api/console/instances` response's `ui` object gains a `page_title`
  string field carrying the configured (or default) title.
- **WebUI**: the top-bar heading and the browser tab title (`document.title`) render the value from
  `ui.page_title`, falling back to the built-in default until the instance list loads (e.g. on the
  login screen or before authentication resolves).

No breaking changes: the config key is optional with a default equal to today's hardcoded value, and
the API addition is a new response field.

## Capabilities

### New Capabilities

<!-- none -->

### Modified Capabilities

- `console`: `Gateway Configuration` dashboard-defaults rule gains the console page title; new
  `Console Page Title` requirement covering config default, API exposure, and UI rendering
  (heading + tab title, fallback behavior).

## Impact

- **Console gateway**: `internal/console/config.go` (new `UIConfig.PageTitle` + default),
  `internal/console/handlers.go` (`ui.page_title` in the instances response), tests in
  `internal/console/config_test.go` and `handlers_test.go`.
- **WebUI**: `webapp/src/App.tsx` (fetch + render), `webapp/src/api/types.ts` (type).
- **Docs**: `docs/docs/10-web-console.md` configuration reference gains `ui.page_title`.
- **Spec**: `openspec/specs/console/spec.md` adjusted via this change (spec-driven workflow).
