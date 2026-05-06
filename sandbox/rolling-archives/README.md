# Sandbox Rolling Archive Fixtures

`hot/` is the configured rolling archive used by `sandbox/instance.yaml`.

Fixture purpose:

- `20250703T110000_PRIMARY_A_v1.txt`: older primary candidate for STATION-A precedence testing.
- `20250703T110000_PRIMARY_A_v2.txt`: newer lexical primary candidate selected by the default tie-breaker.
- `20250703T110000_AUX_A_v1.txt`: auxiliary input required by STATION-A.
- `20250703T111500_AUX_A_v2.txt`: newer auxiliary candidate selected by the default tie-breaker.
- `20250703T110000_QC_B_v1.txt`: second file type not consumed by the default flow, kept to make archive scans non-trivial.
- `20250703T110000_SPARE_v1.txt`: irrelevant product that should not be selected.

When STATION-A completes, VeriProc publishes `STATION-A/result-a.json` into this archive. STATION-B then resolves that published artifact as its `A_RESULT` input.
