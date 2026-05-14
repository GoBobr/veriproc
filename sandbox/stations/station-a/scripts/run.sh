#!/usr/bin/env bash
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RETRY_INDEX:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"
: "${VERIPROC_WINDOW_START:?}"
: "${VERIPROC_WINDOW_END:?}"

primary=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*CLI_1B_RAD______*' | sort | tail -n 1)
aux=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*CO2_1A_GEO______*' | sort | tail -n 1)

GEN_TIME=$(date -u +"%Y%m%dT%H%M%S")
OUTFNAME="CDMA_SCE_2__ICM_______ON_${VERIPROC_WINDOW_START}_${VERIPROC_WINDOW_END}_${GEN_TIME}_018_093_EUM__VAL_T_NR_C00.nc"
cat > "${VERIPROC_RUN_DIR}/${OUTFNAME}" <<OUTPUT
Here is the output product: ${OUTFNAME}
run_id: ${VERIPROC_RETRY_INDEX}
task_id: ${VERIPROC_TASK_ID}
primary input: ${primary}
aux input: ${aux}
OUTPUT
echo "wrote ${VERIPROC_RUN_DIR}/${OUTFNAME} from ${primary} and ${aux}"

echo "sample error" 1>&2
exit 0