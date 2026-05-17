#!/usr/bin/env bash
# Station-D parallel worker: receives the full SCE_2__BIG______ product and
# processes only the sub-window assigned by station C's task-out.yaml.
# No intermediate granule files are staged; the big product is resolved
# directly from the product category and only the slice [WINDOW_START,
# WINDOW_END) is consumed. The result is written as SCE_2__PROCBIG__.
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RETRY_INDEX:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"
: "${VERIPROC_WINDOW_START:?}"
: "${VERIPROC_WINDOW_END:?}"

# Find the SCE_2__BIG______ product resolved for the parent window.
big=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*SCE_2__BIG______*' | sort | tail -n 1)
if [[ -z "${big}" ]]; then
    echo "ERROR: no SCE_2__BIG______ input found" >&2
    exit 1
fi

echo "station-D: processing sub-window ${VERIPROC_WINDOW_START} -> ${VERIPROC_WINDOW_END}"
echo "station-D: reading partial slice from $(basename "${big}")"

GEN_TIME=$(date -u +"%Y%m%dT%H%M%S")

# FILE_TYPE = SCE_2__PROCBIG__ (16 chars); pattern separator = _ON_
OUTFNAME="CDMA_SCE_2__PROCBIG___ON_${VERIPROC_WINDOW_START}_${VERIPROC_WINDOW_END}_${GEN_TIME}_018_093_EUM__VAL_T_NR_C00.nc"

cat > "${VERIPROC_RUN_DIR}/${OUTFNAME}" <<OUTPUT
station: STATION-D
product: processed slice
task_id: ${VERIPROC_TASK_ID}
retry_index: ${VERIPROC_RETRY_INDEX}
window_start: ${VERIPROC_WINDOW_START}
window_end: ${VERIPROC_WINDOW_END}
input_big: $(basename "${big}")
partial_processing: true
slice_window: ${VERIPROC_WINDOW_START} to ${VERIPROC_WINDOW_END}
OUTPUT

echo "wrote ${OUTFNAME}"

# The fan-in trigger (submitting station-E when the group completes) is
# handled automatically by the veriprocd GroupCompleteNotifier; no manual
# coordination needed here.

exit 0
