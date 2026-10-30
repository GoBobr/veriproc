# Design: Console Page Title

## Context

See proposal.md — Why. Current state: the top-bar heading is hardcoded in
`webapp/src/App.tsx` and the tab title in `webapp/index.html`; the gateway already ships a
`ui` object in the `GET /api/console/instances` response (refresh interval, page sizes, card
widths) that the webapp consumes.

## Goals / Non-Goals

- Goals: configurable console title (heading + tab) via `ui.page_title`; zero-config default
  identical to today's hardcoded string; no bundle rebuild per deployment.
- Non-Goals: per-instance titles (already exist as `instances[].title`), templating, i18n.

## Decisions

- **D1 — Config shape**: new optional `ui.page_title` string on `UIConfig`, defaulted to
  `VeriProc Operator Console` in `Config.applyDefaults()`. Optional-string-with-default matches the
  existing UI-defaults pattern; no validation beyond non-empty default.
- **D2 — Delivery to the webapp**: add `page_title` to the existing `ui` object of
  `GET /api/console/instances`. This endpoint is already fetched by the dashboard, is available to
  anonymous sessions when `allow_anonymous_browsing` is on, and avoids a new endpoint or templating
  `index.html` at serve time.
- **D3 — Webapp behavior**: `App.tsx` performs a one-shot `client.instances()` fetch independent of
  the dashboard poll and derives `pageTitle = ui.page_title ?? "VeriProc Operator Console"`. The
  heading renders `pageTitle`, and an effect sets `document.title` so the shell `index.html` title
  (kept as the default) is corrected once config is known. On auth-gated gateways without anonymous
  browsing, the built-in default shows until login — acceptable and covered by the Fallback scenario.
- **D4 — Docs**: document the key in `docs/docs/10-web-console.md` configuration reference.

## Risks / Trade-offs

- The title arrives after first paint (one fetch round-trip) — a brief default-to-configured
  swap; mitigated by the identical default, which makes it a no-op for unconfigured deployments.

## Migration Plan

None required: key optional, default preserves current behavior, API field additive.

## Open Questions

None.
