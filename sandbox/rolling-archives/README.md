# Sandbox Rolling Archive Fixtures

`hot/` is the configured rolling archive used by `sandbox/instance.yaml`.

Fixture purpose:

- `CO2M_PRIMARY_A_20250703T110000000Z_20250703T111500000Z_20250703T112000000Z_v1.txt`: older primary candidate for STATION-A precedence testing.
- `CO2M_PRIMARY_A_20250703T110000000Z_20250703T111500000Z_20250703T113000000Z_v2.txt`: newer primary candidate selected by the default tie-breaker.
- `CO2M_AUX_A_20250703T110000000Z_20250703T111500000Z_20250703T112500000Z_v1.txt`: auxiliary input required by STATION-A.
- `CO2M_AUX_A_20250703T110000000Z_20250703T111500000Z_20250703T113500000Z_v2.txt`: newer auxiliary candidate selected by the default tie-breaker.
- `CO2M_QC_B_20250703T110000000Z_20250703T111500000Z_20250703T112500000Z_v1.txt`: second file type not consumed by the default flow, kept to make archive scans non-trivial.
- `CO2M_SPARE_20250703T110000000Z_20250703T111500000Z_20250703T112500000Z_v1.txt`: irrelevant product that should not be selected.

When STATION-A completes, VeriProc publishes `STATION-A/result-a.json` into this archive. STATION-B then resolves that published artifact as its `A_RESULT` input.
