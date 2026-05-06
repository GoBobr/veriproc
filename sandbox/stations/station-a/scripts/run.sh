#!/usr/bin/env bash
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RUN_ID:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"

primary=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*PRIMARY_A*' | sort | tail -n 1)
aux=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*AUX_A*' | sort | tail -n 1)

cat > "${VERIPROC_RUN_DIR}/result-a.json" <<JSON
{"station":"${VERIPROC_STATION_ID}","run_id":"${VERIPROC_RUN_ID}","task_id":"${VERIPROC_TASK_ID}","primary":"$(basename "${primary}")","aux":"$(basename "${aux}")"}
JSON
echo "wrote ${VERIPROC_RUN_DIR}/result-a.json from ${primary} and ${aux}"
