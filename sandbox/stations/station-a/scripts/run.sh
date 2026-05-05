#!/usr/bin/env bash
# Sandbox station-A run script (illustrative — the stub executor performs
# the actual work in the M0–M7 profile and writes a placeholder artifact).
set -euo pipefail
WIN_START="${1:-unknown}"
WIN_END="${2:-unknown}"
OUT_DIR="${VERIPROC_RUN_DIR:-./out}"
mkdir -p "${OUT_DIR}"
cat > "${OUT_DIR}/result.json" <<JSON
{"station": "STATION-A", "window": {"start": "${WIN_START}", "end": "${WIN_END}"}, "rows": 42}
JSON
echo "wrote ${OUT_DIR}/result.json"
