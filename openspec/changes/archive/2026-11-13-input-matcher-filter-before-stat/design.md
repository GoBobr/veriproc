# Design

## Context

`internal/runs/service.go::inputCandidatesInFolder` has two scanning branches that
disagree on stat failures (see proposal.md - Why):

- **Structured branch** (effective `filename_pattern` present): `os.ReadDir` then
  `os.Stat` every entry; any stat failure returns an error that `resolveManifest`
  promotes to `ErrFatalPrepare`. No name filtering happens before the stat — the
  pattern parse and window filter run later in `classicalSelectCandidates`.
- **Unstructured branch** (glob-based): `os.Stat` failures are silently skipped
  (`if info, err := os.Stat(match); err == nil && …`), even for name-matching entries.

`resolveManifest` creates the working root first and `os.RemoveAll`s it on manifest
error, which is why cascading prepare failures leave no working roots behind.

## Goals / Non-Goals

**Goals:**
- One coherent inspection rule for both branches: name contract first, filesystem
  inspection second.
- Unrelated broken entries in shared folders can never fail a task.
- A broken entry the task actually needs fails loudly with path + file type.
- Fatal error text feeds the existing `ErrFatalPrepare` path and operator logs; no
  new error model.

**Non-Goals:**
- Changing `source: upstream` staging semantics (explicit contract, stays as-is).
- Changing winner selection, grouping, window matching, or manifest metadata.
- Auto-repair of broken symlinks or retries for prepare failures.

## Decisions

1. **Filter by name before statting.** In the structured branch, run the effective
   pattern parse (`policy.ParseFilename`) on `entry.Name()` before `os.Stat`; entries
   that fail the parse are skipped exactly as today's caller-side parse failures are.
   In the unstructured branch the glob already encodes the name contract, so the
   ordering is inherent. The parse may be carried back to the caller to avoid
   double-parsing (implementation detail).
   - *Alternative:* stat everything and ignore errors whose entry name doesn't match.
     Rejected: it still touches (and can trip over) irrelevant entries and leaves the
     ordering rule unspecified.
2. **Unreadable candidates are excluded; only an unreadable *winner* is fatal.**
   A name-matching entry whose `os.Stat` fails is dropped from the candidate set
   (trace-logged with the input `file_type` and path) in both branches, so it can
   never be selected, manifested, or staged. If the group's selected winner is
   unreadable, the scan returns a wrapped error naming the input `file_type` and
   entry path, which `resolveManifest` promotes to `ErrFatalPrepare`; a mandatory
   input left with no readable candidate fails through the existing
   missing-input path. This absorbs populate-while-running races (a readable
   candidate in another folder — e.g. the products-dir symlink vs. the rolling
   archive copy — wins) while still failing loudly when the file the task
   actually needs is broken. The unstructured branch's silent skip is retained
   but now records the entry as unreadable so winner validation applies
   uniformly.
   - *Alternative (earlier revision, rejected by field evidence):* fatal on any
     matching unreadable entry. Rejected: it fails tasks whose winner is
     perfectly readable merely because a losing candidate (or a symlink being
     copied concurrently) is momentarily unreadable — observed on both
     eucent-3.0 and eucent-v3-gaps while fixture archives were being
     populated.
3. **Keep `os.Stat` (follows symlinks).** A dangling symlink must fail; `os.Lstat`
   would report it as a healthy symlink object.
4. **Error wrapping stays `fmt.Errorf("%w", …)`** at the scan site with context
   `input <file_type> candidate <path>`; no new error types, no API surface change.

## Risks / Trade-offs

- [A losing candidate that is unreadable is silently absent from the winner pool]
  → It is trace-logged with input file type and path; it can never be staged, so
  absence is safe. If it was the only candidate, preparation fails loudly.
- [Permission-denied entries (not just dangling links) are excluded the same way]
  → Correct per spec ("cannot be inspected"); if such an entry is the winner the
  error message distinguishes the syscall error.
- [Name-parse moved before stat changes which entries get parse-trace logs] → Keep
  the existing trace wording; log ordering is not contract.

## Migration Plan

Code-only change, no data or API migration. Deploy as a normal release; rollback by
revert. Existing `docs/docs` behavior notes, if any describe the silent-skip, are
updated in the apply phase.

## Open Questions

None.
