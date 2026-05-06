# Sandbox Rolling Archive Fixtures

`hot/` is the configured rolling archive used by `sandbox/instance.yaml`.

Fixture purpose:

- `CO2M_PRIMARY_A________20250703T110000Z_20250703T111500Z_20250703T112000Z_v1.txt`: older primary candidate for STATION-A precedence testing.
- `CO2M_PRIMARY_A________20250703T110000Z_20250703T111500Z_20250703T113000Z_v2.txt`: newer primary candidate selected by the default tie-breaker.
- `CO2M_AUX_A____________20250703T110000Z_20250703T111500Z_20250703T112500Z_v1.txt`: auxiliary input required by STATION-A.
- `CO2M_AUX_A____________20250703T110000Z_20250703T111500Z_20250703T113500Z_v2.txt`: newer auxiliary candidate selected by the default tie-breaker.
- `CO2M_QC_B_____________20250703T110000Z_20250703T111500Z_20250703T112500Z_v1.txt`: second file type not consumed by the default flow, kept to make archive scans non-trivial.
- `CO2M_SPARE____________20250703T110000Z_20250703T111500Z_20250703T112500Z_v1.txt`: irrelevant product that should not be selected.

When STATION-A completes, VeriProc publishes `result-a.json` at this archive root. STATION-B then resolves that published artifact as its `A_RESULT________` input.
