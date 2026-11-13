# Spec Delta

## ADDED Requirements

### Requirement: Candidate entry inspection order
Folder scanning MUST filter candidate entries by the declared input's name contract before any filesystem inspection of the entry. Entries whose names fail the effective `filename_pattern` or declared pattern for the input MUST NOT be stat'ed, opened, or otherwise inspected, and MUST NOT influence input resolution, error reporting, or run preparation of any task.

#### Scenario: Unrelated dangling symlink is ignored
- **GIVEN** a shared product folder containing a dangling symlink whose name does not satisfy the declared input's name contract, plus a valid candidate that does
- **WHEN** inputs are resolved for a task whose declared inputs do not include the dangling entry's file type
- **THEN** preparation succeeds and the dangling entry is never inspected

#### Scenario: Non-matching entry cannot fail preparation
- **GIVEN** a folder containing only a dangling symlink whose name fails the name contract of every declared input of the task
- **WHEN** run preparation scans the folder
- **THEN** no error arises from that entry regardless of its filesystem state

### Requirement: Winner readability validation
Winner selection MUST depend only on parsed filename metadata and configured folder priority, so filesystem state never changes which candidate is selected. After a group's winner is selected, the backend MUST verify the winner's filesystem object can be inspected; if it cannot, run preparation MUST fail with a fatal error identifying the offending path and declared file type. Non-selected candidates MUST NOT fail preparation and MUST NOT be manifested or staged.

#### Scenario: Unreadable non-winner is ignored
- **GIVEN** a logical interval group whose selected winner is readable and another candidate whose object cannot be inspected
- **WHEN** the winner is selected
- **THEN** preparation succeeds and only the winner is staged

#### Scenario: Unreadable winner is fatal
- **GIVEN** a logical interval group whose only candidate cannot be inspected
- **WHEN** run preparation resolves that input
- **THEN** preparation fails with a fatal error identifying the offending path and file type

#### Scenario: Unreadable winner with readable alternative
- **GIVEN** a logical interval group where the candidate preferred by the tie-break rules is unreadable and another candidate in the group is readable
- **WHEN** the winner is selected
- **THEN** preparation fails with the fatal error rather than substituting the readable candidate

#### Scenario: Unreadable candidate never staged
- **GIVEN** an input whose candidate set contains an unreadable entry and a readable winner
- **WHEN** run preparation freezes the manifest
- **THEN** the unreadable entry appears in no manifest entry and no working-root input symlink

#### Scenario: Upstream-declared inputs unchanged
- **GIVEN** an input with `source: upstream` whose declared file list references a missing path
- **WHEN** run preparation resolves that input
- **THEN** the existing upstream missing-path failure applies and this requirement's folder-scanning rules are not consulted
