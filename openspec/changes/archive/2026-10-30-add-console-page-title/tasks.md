# Tasks

## 1. Console gateway (`internal/console`)

- [x] 1.1 Add `UIConfig.PageTitle` (`ui.page_title`) with default `VeriProc Operator Console` in
  `Config.applyDefaults()`, and verify with a `config_test.go` default assertion plus an explicit
  override case (Gateway Configuration → Default page title).
- [x] 1.2 Include `page_title` in the `ui` object of the `GET /api/console/instances` response, and
  verify with a `handlers_test.go` assertion on the served value (Console Page Title → Page title
  served with instance list).

## 2. WebUI (`webapp`)

- [x] 2.1 Add `page_title: string` to the `InstancesResponse.ui` type.
- [x] 2.2 In `App.tsx`, fetch the instance list once, render the top-bar heading from
  `ui.page_title` (fallback `VeriProc Operator Console`), and set `document.title` from the same
  value (Console Page Title → Configured page title, Fallback before load).

## 3. Verification and deployment

- [x] 3.1 Run the Go suite (`go test ./...`) and webapp build/tests, and verify no regressions.
- [x] 3.2 Update `docs/docs/10-web-console.md` configuration reference with `ui.page_title`
  (default and effect).
- [x] 3.3 Rebuild the webapp bundle and the local `veriproc-console` binary, redeploy the local
  instance, and verify the custom title appears in the header and tab (manual check, no commits or
  pushes).
- [x] 3.4 Run `openspec validate add-console-page-title --strict` and verify it validates cleanly.

## Workflow follow-up

- [x] Archive via `openspec archive` only after the user approves commits.
