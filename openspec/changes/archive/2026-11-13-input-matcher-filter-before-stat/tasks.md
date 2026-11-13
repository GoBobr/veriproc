# Tasks

## 1. Candidate entry inspection order (spec: architecture — Candidate entry inspection order)

- [x] 1.1 In `internal/runs/service.go::inputCandidatesInFolder`, structured branch: run the effective-pattern name parse (`policy.ParseFilename`) on `entry.Name()` before `os.Stat`; skip entries failing the parse with the existing parse-trace log (implements Requirement: Candidate entry inspection order)
- [x] 1.2 Test — Scenario: Unrelated dangling symlink is ignored: folder with a dangling symlink failing the name contract plus a good matching candidate resolves normally and the dangling entry is never stat'ed (verifies Scenario: Unrelated dangling symlink is ignored)
- [x] 1.3 Test — Scenario: Non-matching entry cannot fail preparation: folder containing only a dangling non-matching symlink yields no error for any declared input (verifies Scenario: Non-matching entry cannot fail preparation)

## 2. Winner readability validation (spec: architecture — Winner readability validation)

- [x] 2.1 Structured branch: on `os.Stat` failure of a name-matching entry, record it as an unreadable candidate (warn-log input `file_type` + path) instead of returning an error from the scan (implements Requirement: Winner readability validation)
- [x] 2.2 Unstructured branch: same — record the glob-matching unreadable entry as an unreadable candidate instead of silently skipping or failing (implements Requirement: Winner readability validation)
- [x] 2.3 After winner selection per group, validate the selected winner's readability; unreadable winner returns a wrapped error naming the declared `file_type` and entry path, promoted to `ErrFatalPrepare` by `resolveManifest`. Selection itself stays metadata-only (no filesystem state in the tie-break) (implements Requirement: Winner readability validation)
- [x] 2.4 Test — Scenario: Unreadable non-winner is ignored: group with a readable winner plus a dangling candidate prepares successfully and stages only the winner (verifies Scenario: Unreadable non-winner is ignored)
- [x] 2.5 Test — Scenario: Unreadable winner is fatal: group whose only candidate is dangling fails via `ErrFatalPrepare` with the offending path and file type (verifies Scenario: Unreadable winner is fatal)
- [x] 2.6 Test — Scenario: Unreadable winner with readable alternative: metadata-preferred candidate dangling while another group candidate is readable → preparation fails, no substitution (verifies Scenario: Unreadable winner with readable alternative)
- [x] 2.7 Test — Scenario: Unreadable candidate never staged: manifest contains no entry and working root no symlink for the unreadable entry (verifies Scenario: Unreadable candidate never staged)
- [x] 2.8 Test — Scenario: Upstream-declared inputs unchanged: `source: upstream` input referencing a missing path keeps its existing failure path and does not consult folder-scanning rules (verifies Scenario: Upstream-declared inputs unchanged)

## 3. Regression and suite

- [x] 3.1 Run the full `internal/runs` test suite (and `go test ./...`) to confirm no existing candidate-selection, grouping, or manifest-metadata behavior regressed
- [x] 3.2 Check `docs/docs` for any documented behavior of the unstructured branch's silent skip and update it to match the new rule (Known-Issue references in `docs/docs/15-known-issues.md` preserved per project context)

## Workflow follow-up (reference only, post-implementation)

- Archive the change (`openspec archive`) after apply is complete and verified.
