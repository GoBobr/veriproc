# Design

## Context

Completed-slot pruning already happens in exactly one live place: the console gateway's `ExpandStationRow` (`internal/console/dashboard.go`), driven by `ui.completed_visibility_timeout` (a `FlexDuration` whose zero value with `Set: true` means "explicitly no cutoff"). The daemon-side cutoff in `internal/stations/service.go` (`SetCompletedVisibilityTimeout`, the `completedVisibilityTimeout` field, and the cutoff branch in `summaryForStation`) is never invoked — no caller, no test, no doc beyond a stale mention in the spec's upstream-test-coverage list. The webapp already persists the per-instance since selection (`presetMs`/`customTs`) in `localStorage` and passes `since` to `GET /api/console/instances/{id}/dashboard?since=...`; the gateway's `parseSince` and `BuildInstanceDashboard` already thread that timestamp down to `ExpandStationRow`. The instances config endpoint already exposes `completed_visibility_ms` to the frontend (currently unused by the webapp). See proposal.md for motivation.

## Goals / Non-Goals

**Goals:**
- Let operators see all completed runs inside the selected since interval without loosening the default linger behavior for live monitoring.
- Make "no visibility pruning" (`completed_visibility_timeout: 0s`) explicit in the UI instead of leaving the timeout semantics implicit.
- Remove the unreachable daemon-side cutoff so the upstream contract (summary returns completed runs unpruned) matches the code.

**Non-Goals:**
- Changing slot ordering (completed slots are currently sorted alphabetically by task ID; the spec's "newest first by terminal timestamp" wording vs. the implementation is a pre-existing discrepancy, untouched here).
- Changing the upstream `/api/v1` station-summary shape or its since/counter semantics.
- Making the toggle per-station or role-gated; it is a per-instance display preference like the since picker.

## Decisions

1. **Gateway-owned pruning via request flag, not config.** The dashboard handler gains an optional `completed=1` query parameter (parsed alongside `parseSince`, truthy for `1`/`true`/`yes`, absent/false otherwise). `BuildInstanceDashboard` passes it down; `ExpandStationRow` takes the since time plus the flag and applies exactly one of two pruning rules per completed slot: visibility-timeout cutoff (default, flag off) or since-window cutoff (flag on). A completed slot missing `terminal_at` is kept in both modes, mirroring the existing counter behavior in the stations service. Alternative considered: a second config key switching global behavior — rejected because the operator asked for an interactive per-view toggle, not a deployment-wide mode.

2. **Flag off by default; zero timeout still means "never prune".** When `completed` is absent the behavior is byte-for-byte today's, so existing API consumers and the unchecked default are unaffected. When `ui.completed_visibility_timeout` is set to `0s`, the gateway applies no age pruning (existing behavior of `ExpandStationRow` for zero durations), and the frontend renders the checkbox disabled — nothing to toggle because completed runs are always shown.

3. **Frontend wiring.** `DashboardPage` already receives `data.ui.completed_visibility_ms` from the instances endpoint; it passes it to `InstanceBlock` as a new prop. The checkbox state is persisted with the since selection in the same per-instance `localStorage` entry (`SinceStore` gains a `completed` field, default `false`), so a reload preserves both. When checked, `client.dashboard(id, sinceDate)` appends `&completed=1`; when `completed_visibility_ms === 0`, the checkbox renders `disabled` with a `title` tooltip "disabled in config (completed_visibility_timeout: 0)". The gateway already returns `completed_visibility_ms` in the instances response; no config-endpoint change is needed.

4. **Hidden-run and acknowledge interplay unchanged.** The existing hidden-run suppression in `ExpandStationRow` runs before kind-based handling and independently of age pruning; the toggle only replaces the age rule. This preserves acknowledgment semantics without new code paths.

5. **Daemon cleanup is pure deletion.** Remove `defaultCompletedVisibilityTimeout`, the `completedVisibilityTimeout` field, `SetCompletedVisibilityTimeout`, and the cutoff computation/branch in `summaryForStation` (`internal/stations/service.go`). The commented aged-out exclusion and the `base.Counts.Success` double-check collapse to the simple in-window counting path. No daemon tests referenced the cutoff, so no test rewrites are required; the summary tests keep verifying that completed runs are included regardless of age (now an explicitly specified property — see the delta's "Upstream pruning independence" scenario).

6. **Spec alignment.** The console spec delta modifies the affected requirements (instance block contents, slot semantics, gateway API surface, station summary contract, upstream capability testing) rather than inventing a parallel capability, because the toggle is a refinement of existing dashboard/summary requirements.

## Risks / Trade-offs

- [With the toggle on and a wide interval, completed slots can crowd out running/queued slots within `visible_slot_count`] → Same pre-existing overflow behavior applies: the overflow badge reports excluded occupants; the operator can switch the toggle off. Counters remain exact.
- [Upstream summary lists only the 200 most recent runs per station] → Very old completions inside a wide since window may be absent from the summary entirely; the count is still reflected in `counts.success` (computed from the same page). Documented in `docs/docs/10-web-console.md` as a known bound, not fixed here.
- [Removing the daemon cutoff could surprise a hypothetical external caller of `SetCompletedVisibilityTimeout`] → Grep confirms zero references anywhere in the repo (code, tests, docs); the method is not part of any published contract.
- [`complete`-state runs without `terminal_at` are always shown when the flag is on] → Mirrors existing counter logic; acceptable and deterministic.

## Migration Plan

Deploy console gateway + webapp together with the daemon image in one release (they ship in the same tag pipeline today). No data migration; `localStorage` gains an optional field with a safe default. Rollback: redeploy the previous tag — the query parameter is ignored by old gateways, and old frontends never send it.

## Open Questions

(none)
