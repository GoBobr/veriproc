# Proposal

## Why

A single broken (dangling) symlink in a shared product folder currently fails run
preparation for *every* task, including tasks whose declared inputs have nothing to
do with the broken entry. Field evidence (e2e instance `eucent-v3-gaps`, task
`no2gap-20250703T111239000-8e7351`, 2026-11-13): 15 dangling `SCE_2__CSM` links in
the products directory caused all `no2gap` tasks to die with `ErrFatalPrepare: stat
… no such file or directory`, although the `no2gap` station declares only
`NO2_1A_GEO` / `NO2_1B_RAD` inputs. The structured-branch folder scanner stats every
directory entry and treats any stat failure as fatal, and it applies no name/pattern
filter before statting; the unstructured-branch scanner in the same function silently
skips stat failures — the two branches are inconsistent. The spec is silent on how
unreadable folder entries are treated, so the failure mode is unspecified.

## What Changes

- Candidate discovery in the classical input matcher applies the entry's name/pattern
  contract **before** `os.Stat`: entries that cannot belong to the declared input are
  never stat'ed and cannot influence the task.
- A candidate whose name satisfies the input contract but cannot be stat'ed (dangling
  symlink, permission error) is **excluded from selection** — it can never be
  manifested or staged — and only fails run preparation when it is the *winner* of
  its logical interval group (fatal error identifying the offending path and file
  type), or when it leaves a mandatory input with no readable candidate (existing
  missing-input path). This absorbs populate-while-running races where a readable
  candidate in another folder wins, and replaces the unstructured branch's silent
  skip with the same winner-validation rule.
- `source: upstream` inputs are out of scope: upstream-declared file lists are an
  explicit contract and keep failing on missing paths.

## Capabilities

### New Capabilities

- None.

### Modified Capabilities

- `architecture`: input candidate scanning rules gain normative treatment of
  name-based pre-filtering and unreadable candidate entries (matching + unreadable =
  fatal; non-matching = never inspected). Affects "Input resolution determinism" and
  "Input selection rules".

## Impact

- Affected layer: backend daemon input resolution (no REST API, CLI, console, or
  executor contract changes; failure messages change shape for the unreadable-candidate
  case).
- Code: `internal/runs/service.go` (`inputCandidatesInFolder`, error path in
  `resolveManifest`), tests in `internal/runs/`.
- Fixes the field failure above: unrelated broken symlinks in shared product folders no
  longer block task preparation, and working roots are no longer created-then-removed
  by cascading fatal prepares.
