#!/usr/bin/env bash
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RUN_ID:?}"
: "${VERIPROC_RUN_DIR:?}"

input=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*A_RESULT*' | sort | tail -n 1)

cat > "${VERIPROC_RUN_DIR}/track.csv" <<CSV
run_id,station,input_file,confidence
${VERIPROC_RUN_ID},${VERIPROC_STATION_ID},$(basename "${input}"),0.91
CSV
echo "wrote ${VERIPROC_RUN_DIR}/track.csv from ${input}"
