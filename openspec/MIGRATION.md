# OpenSpec Migration Mapping

Mapping from the original files in `docs/specs/` to the requirement headers in
`openspec/specs/<domain>/spec.md`. `docs/specs/1.introduction.md` was folded into the
context in `openspec/config.yaml` (it contained no normative requirements).
Diagrams and tables that OpenSpec could not represent were moved to
`docs/docs/02-architecture.md` (appendix) and `docs/docs/14-data-model-reference.md`.

Notes:

- `docs/specs/9.known_issues.md` was relocated verbatim to `docs/docs/15-known-issues.md`
  (all KI entries intact); the reference in `openspec/config.yaml` points to the new path.
- `docs/specs/1.introduction.md` contained no normative requirements.

## 2.system_architecture.md -> specs/architecture/spec.md

67 requirements, 96 scenarios.

1. Architectural layers and authorities
2. Control-plane background loop cadence
3. High-level component responsibilities
4. Authoritative records
5. Versioning boundaries
6. Instance configuration pre-flight validation
7. Default generator selection
8. Definitions section semantics
9. Execution env section rules
10. Naming policy scope
11. Task id timestamp source
12. Working-root path template
13. Working-root collision suffix
14. Structured filename pattern contract
15. Fixed positional component extraction
16. Compact timestamp formatting
17. Integrity checksum policy
18. Executor registry
19. Executor types
20. Station configuration model
21. Station context references
22. Split-group policy declaration
23. Downstream routing modes
24. Station execution mode binding
25. Task/run/job/joborder cardinality
26. Run identity is task-scoped
27. Task model and submission
28. Input resolution determinism
29. Input selection rules
30. Half-open interval semantics
31. Input filter rules
32. Recorded manifest metadata
33. Frozen execution context
34. Job order evaluation preconditions
35. Job order formats
36. Job order renderers
37. Default renderer document content
38. Joborder include merging and reserved names
39. Flat joborder inputs list
40. Working-root layout
41. Working-root path is not run identity
42. Runtime environment contract
43. Run lifecycle states
44. Asynchronous recovery expectations
45. Scheduler adapter operations
46. SLURM executor modes
47. SLURM submission construction
48. SLURM wrapper contract
49. SLURM polling state mapping
50. SLURM cancellation
51. Output validation completion conditions
52. Artifact records and directory outputs
53. Rolling archive publication declaration
54. Publication target and modes
55. Multi-object output publication
56. Publication idempotency and metadata
57. Publication is not grouping authority
58. Task-out routing and validation
59. Split-group representation
60. Fan-in policy definition
61. Aggregation task creation
62. Processing fingerprint composition
63. Canonical and non-canonical runs
64. Mandatory crash-recovery cases
65. Idempotency and uniqueness
66. API query semantics
67. Operational constraints

## 3.conceptual_model.md -> specs/domain-model/spec.md

40 requirements, 43 scenarios.

1. Identity Model Foundation
2. Station Entity
3. Station Context and Structured Execution
4. Execution Mode Resolution
5. Station Revision Entity
6. Task Entity
7. Task Identifier Grammar
8. Submission Idempotency
9. Routing History
10. Provenance Links
11. Job Order Entity
12. Job Order Reserved Names and Root Layout
13. Job Order Flat Inputs
14. Template Renderer
15. Resolved Input Manifest
16. Manifest Integrity and Checksum Policy
17. Run Entity and Composite Identity
18. Run Retry Isolation
19. Run Canonicality and Downstream Routing
20. Job Entity
21. Runtime Environment Contract
22. Working Root Entity
23. Task Output Descriptor
24. Split Group Definition
25. Split Group Membership and Closure
26. Split Group Readiness and Aggregation
27. Split Group Aggregation Inputs
28. Split Group Source of Policy
29. Artifact Entity
30. Processing Fingerprint
31. Deduplication Record
32. Facility Entity
33. State Store
34. Instance Configuration
35. Instance Configuration Validation
36. Generation Hook
37. Rolling Archive Entity
38. Executor Entity
39. Shared Storage
40. Glossary Distinctions

## 4.relational_data_model.md -> specs/persistence/spec.md

33 requirements, 70 scenarios.

1. Database Authority Boundaries
2. Single Canonical Identity Model
3. Run Composite Canonical Key
4. Internal Run Key Constraint
5. Routing-Content and Task Immutability
6. Task Creation and Downstream Coupling
7. Join-Mode Downstream Identity
8. Deterministic Downstream-Creation Key
9. Task Summary State Consistency
10. Provenance Link Persistence
11. Station Revision Immutability
12. Configuration Freezing on Runs
13. Run Field Immutability
14. Run Lifecycle State Vocabulary
15. Terminal-State Immutability and No Revival
16. Retry-Safe Run Creation
17. Jobs Persistence and Scheduler-Id Timing
18. Manifest Freezing
19. Manifest Entry Persistence
20. Artifact Recording
21. Publication Persistence
22. Split-Group and Membership Persistence
23. Deduplication Ownership
24. Idempotency Records
25. Fingerprint Prerequisite
26. Canonicality Classification Persistence
27. Cardinality and Referential Rules
28. Recoverability of Side-Effect Transitions
29. Normalization and Projections
30. Scheduler Submission-Uncertainty Persistence
31. Retention and Deletion Constraints
32. Schema Evolution and Migration Rules
33. Implementation Schema Extras

## 5.rest_api.md -> specs/api/spec.md

53 requirements, 101 scenarios.

1. API Scope And Control-Plane Exclusivity
2. API Version And Documentation
3. Identity Distinction Preservation
4. Canonical Resource Identifiers
5. Readable Context Fields
6. Validated State-Change Operations
7. Language-Neutral Contract
8. Task Submission Endpoint
9. Submission Destination Rules
10. Submission Window Rules
11. Force And Parent Rules
12. Idempotency Key Semantics
13. Submission Validation Scope
14. Submission Response Semantics
15. Get Task By Identifier
16. List Or Query Tasks
17. Get Run
18. List Or Query Runs
19. Get Jobs For A Run
20. Artifacts Content And Metadata
21. Log Access
22. Split Group Endpoints
23. Provenance Query Availability
24. Retry And Promote Endpoints
25. Cancellation Request Semantics
26. Station Status And Summary Endpoints
27. Station Pause And Unpause
28. Task Deletion Cascade
29. Deletion Dry Run And Report
30. Deletion Transactional And Filesystem Semantics
31. Maintenance Clean Endpoint
32. Task Summary Representation
33. Task Detail Representation
34. Run Summary Representation
35. Run Detail Representation
36. Job Summary And Detail Representation
37. Artifact Representation
38. Canonical And Non-Canonical Visibility
39. Consistency Semantics Observed State
40. Latest Canonical Active Job Determination
41. Paginated List Envelope
42. Additional Resources Bounded
43. Error Envelope Fields
44. Invalid Request Errors
45. Unknown Station Errors
46. Resource Not Found Errors
47. Invalid State Transition Errors
48. Cancellation Unsupported Errors
49. Reconciliation In Progress Errors
50. Dependency Unavailable Errors
51. Internal Error Handling
52. Stable Field Names And Compatibility
53. Health And Readiness Endpoints

## 6.command_line_interface.md -> specs/cli/spec.md

66 requirements, 107 scenarios.

1. CLI Scope And API Only Access
2. CLI Optional For Server Conformance
3. Identity Distinction Preservation
4. Opaque Canonical Identifiers
5. Local Convenience Checks
6. Thin Client Behavior
7. Stable Command Semantics
8. No Hidden Mutation
9. Machine Readable Output Modes
10. Human Readable Defaults
11. Stdout Stderr Separation
12. Timestamps And Nulls In Output
13. Quiet Mode
14. Exit Code Model
15. Indeterminate Outcome Safety
16. Most Specific Exit Code Wins
17. Configuration Implementation Status
18. Configuration Precedence Model
19. Configuration Sources And Local File Rules
20. Secrets Handling In Errors
21. Submit Command
22. Submit Idempotency Key Handling
23. Submit Force Semantics
24. Submit Parent And Destination Handling
25. Submit Timestamp Acceptance
26. Submit Error Surfacing
27. No Local Semantics Invention
28. Document Flag Merge Behavior
29. Task Get Command
30. Task List Command
31. Run Get Command
32. Run List Command
33. Job Get Command
34. Job Listing
35. Artifact Listing
36. Artifact Get Not Implemented
37. Content Denial Distinction
38. Logs Command
39. Relationships Command
40. Cancel Command
41. Cancel Confirmation Policy
42. Cancel Outcome Distinctions
43. Health Command
44. Readiness Command
45. Version Command
46. Unsupported API Version Failure
47. Config Validate Command
48. Task Delete Command
49. Run Delete Command
50. Clean Command Basis Semantics
51. Clean Timestamp Formats
52. Clean Dry Run Force And Output
53. Clean Help Without Cutoffs
54. Clean Station Restriction
55. Listing Filters Backend Alignment
56. Pagination Mapping
57. Interactive Table Paging
58. Empty Versus Failed Distinction
59. Latest And Canonical Distinct Fields
60. Unknown Field Tolerance
61. Summary And Detail Views
62. Default Ordering
63. Streaming And Decorated Log Output
64. Consistency Not Oversold
65. Additive Implemented Commands
66. Non Goals And No Second Control Path

## 7.verification_and_conformance.md -> specs/verification/spec.md

45 requirements, 71 scenarios.

1. Conformance Scope and Evidence Basis
2. Specification Conformance Model
3. MVP Conformance Profiles
4. Optional, Deferred, and Deviation Handling
5. Test Taxonomy Coverage
6. Database and Migration Tests
7. API Contract Tests
8. End-to-End Workflow Tests
9. Executor-Adapter Contract Tests
10. Identity and Immutability Invariants
11. Station Identity and Uniqueness
12. Opaque Identifier Invariants
13. Working Root Uniqueness
14. Working Root Template Invariants
15. Frozen Execution Context
16. Filename Pattern Parsing
17. Interval Matching and Timestamp Parsing
18. Classical Matcher Invariants
19. Integrity Policy and Directory Inputs
20. Manifest Traceability
21. Matching Decision Logging
22. Context Reference Resolution
23. Execution Environment and Arguments
24. SLURM Config Merging
25. Joborder Default Renderer
26. Joborder Template Renderer
27. Joborder Include Merge
28. Preprocess Script
29. Local Log Capture
30. Completion and Output Validation
31. Output Directory Resolution
32. Publication Rules
33. Canonicality and Deduplication Invariants
34. Idempotency and Retry Invariants
35. Scheduler Uncertainty and Recovery Invariants
36. Provenance and Split-Group Invariants
37. CLI Thin-Client Invariants
38. MVP Submission and Preparation Scenarios
39. MVP Dispatch, Finalization, and API Scenarios
40. MVP CLI and Cancellation Scenarios
41. API Compliance Requirements
42. Persistence and Migration Compliance
43. Recovery and Reconciliation Compliance
44. CLI Compliance Requirements
45. Evidence and Reporting

## 8.operator_web_console.md -> specs/console/spec.md

29 requirements, 47 scenarios.

1. Goals and Scope
2. Three-Layer Architecture
3. Multi-Instance Dashboard Grid
4. Station Row Rendering
5. Hide Failure Acknowledgment
6. Station Context Menu
7. Task-Box Context Menu
8. Task Detail Page Layout
9. Working-Root Tree Browser
10. Quick-View Pane
11. Station Activity Table
12. Gateway Configuration
13. Console SQLite Data Model
14. Users and Roles
15. Audit Log
16. Gateway Minimum API Surface
17. Stable Console Identity
18. Upstream Capability Slice
19. Station Summary Contract
20. Upstream Capability Testing
21. Gateway Filesystem Access Contract
22. Authorization Enforcement
23. Refresh and Performance
24. Visual Design Essentials
25. Console Gateway Unit Tests
26. Frontend Unit Tests
27. Unit Testing Edge Cases
28. Additional Required Test Coverage
29. Test Isolation

