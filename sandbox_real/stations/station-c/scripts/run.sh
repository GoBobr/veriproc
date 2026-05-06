#!/usr/bin/env bash
set -euo pipefail
WIN_START="${1:-unknown}"
WIN_END="${2:-unknown}"
OUT_DIR="${VERIPROC_RUN_DIR:-./out}"
mkdir -p "${OUT_DIR}"
cat > "${OUT_DIR}/daily.json" <<JSON
{"station": "STATION-C", "window": {"start": "${WIN_START}", "end": "${WIN_END}"}, "summary": {"events": 1234}}
JSON
echo "wrote ${OUT_DIR}/daily.json"
