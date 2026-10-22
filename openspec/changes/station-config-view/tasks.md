# Tasks

## 1. Upstream config assembly (`internal/stations`)

- [x] 1.1 Extend `Spec` and `Registry.Seed` to carry `station_name`-adjacent `description` from
  `Definition` through `SpecFromDefinition`, and verify existing loader/registry tests still pass
  (`go test ./internal/stations/...`).
- [x] 1.2 Implement `Service.ConfigView(ctx, stationID)` assembling the resolved config document from
  the registry revision record (unmarshal `DeclaredExecution`/`DeclaredJobOrder`/inputs/outputs/
  downstream/rolling_folders blobs) with `definitions`-only context resolution leaving runtime refs
  literal, plus secret-name-pattern value redaction using the startup env snapshot, and verify with
  table-driven tests covering: env-expanded image tag present (Station Config Endpoint → Container
  Image Exposed), definitions resolved / runtime refs literal (→ Resolved Configuration Contents),
  secret value redacted (→ Secret Environment Values Redacted), freshness metadata populated
  (→ Freshness Metadata), unknown station error (→ Unknown Station Config).
- [x] 1.3 Serialize the assembled document to YAML server-side (`config_yaml` field) and verify the
  YAML round-trips the resolved values in a unit test.

## 2. Upstream HTTP endpoint (`internal/httpapi`)

- [x] 2.1 Add `GET /api/v1/stations/{station_id}/config` route and handler wired to
  `stations.Service.ConfigView`, returning the response contract from design D5, and verify with a
  narrow handler test asserting 200 shape for a seeded station and 404 for an unknown station
  (Station Config Endpoint scenarios: Resolved Configuration Contents, Freshness Metadata, Unknown
  Station Config).
- [x] 2.2 Verify reader-credential access succeeds and no operator credential is required
  (→ Reader May Read Config) with an auth-enabled handler test.

## 3. Console gateway proxy (`internal/console`)

- [x] 3.1 Add `UpstreamClient.StationConfig()` and route
  `GET /api/console/instances/{instance_id}/stations/{station_id}/config` proxying upstream, and
  verify with a fake-upstream gateway test asserting pass-through of the config payload and error
  mapping (Gateway Minimum API Surface delta).
- [x] 3.2 Verify authorization: `viewer` may call the config endpoint and mutating endpoints remain
  denied for `viewer` (→ Viewer Reads Station Config, Server-side enforcement), with middleware tests.

## 4. WebUI (`webapp`)

- [x] 4.1 Add `client.stationConfig(instanceID, stationID)` to `webapp/src/api/client.ts` and the
  response type to `api/types.ts`, and verify with a client unit test against a mocked fetch.
- [x] 4.2 Add the `Config` button to the station page header next to Pause/Unpause and a
  `StationConfigDialog` (InfoDialog-style backdrop/header/close, loading and error states, scrollable
  read-only YAML body, freshness line with `content_hash`/`effective_at`), and verify with component
  tests covering: button opens dialog with resolved config including image tag (Station Config View →
  Config Button Opens Dialog), freshness line rendered (→ Freshness Indication), upstream error shown
  in dialog (→ Config Fetch Failure).
- [x] 4.3 Add dialog styles to `webapp/src/styles.css` reusing existing `.dialog` classes and verify
  the dialog is scrollable and non-editable in the component test (read-only assertion).

## 5. Integration and docs

- [x] 5.1 Run the full Go test suite (`go test ./...`) and webapp tests (`npm test` in `webapp/`) and
  verify all pass with no regressions.
- [x] 5.2 Update `docs/docs` station-page and API documentation to describe the config endpoint and
  dialog, and verify the documented endpoint paths match the implementation.
- [x] 5.3 Run `openspec validate --all --strict` and verify the change validates cleanly.

## Workflow follow-up

- Archive the change after implementation and review are complete (`openspec archive`).
- Verify the archived result merged the deltas into `openspec/specs/`.