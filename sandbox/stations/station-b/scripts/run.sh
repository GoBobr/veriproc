#!/usr/bin/env bash
set -euo pipefail
WIN_START="${1:-unknown}"
WIN_END="${2:-unknown}"
OUT_DIR="${VERIPROC_RUN_DIR:-./out}"
mkdir -p "${OUT_DIR}"
cat > "${OUT_DIR}/track.csv" <<CSV
ts,track_id,confidence
${WIN_START},T1,0.91
${WIN_START},T2,0.87
CSV
echo "wrote ${OUT_DIR}/track.csv"
