#!/usr/bin/env bash
# Station-D parallel worker: processes one SCE_2__MICROBIG_ granule and
# writes a SCE_2__PROCBIG__ product covering the same 90-second sub-window.
# The backend publishes validated outputs to micro-ra and handles fan-in
# readiness for the split group.
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RETRY_INDEX:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"
: "${VERIPROC_WINDOW_START:?}"
: "${VERIPROC_WINDOW_END:?}"

# Find the single MICROBIG granule resolved for this sub-window.
microbig=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*SCE_2__MICROBIG_*' | sort | tail -n 1)
if [[ -z "${microbig}" ]]; then
    echo "ERROR: no SCE_2__MICROBIG_ input found" >&2
    exit 1
fi

GEN_TIME=$(date -u +"%Y%m%dT%H%M%S")

# FILE_TYPE = SCE_2__PROCBIG__ (16 chars); pattern separator = _ON_
# Results in two underscores before ON in the filename (__ from FILE_TYPE + _ separator).
OUTFNAME="CDMA_SCE_2__PROCBIG___ON_${VERIPROC_WINDOW_START}_${VERIPROC_WINDOW_END}_${GEN_TIME}_018_093_EUM__VAL_T_NR_C00.nc"

cat > "${VERIPROC_RUN_DIR}/${OUTFNAME}" <<OUTPUT
station: STATION-D
product: processed granule
task_id: ${VERIPROC_TASK_ID}
retry_index: ${VERIPROC_RETRY_INDEX}
window_start: ${VERIPROC_WINDOW_START}
window_end: ${VERIPROC_WINDOW_END}
input_microbig: $(basename "${microbig}")
OUTPUT

echo "wrote ${OUTFNAME} from $(basename "${microbig}")"

# The fan-in trigger (submitting station-E when the group completes) is
# handled automatically by the veriprocd GroupCompleteNotifier; no manual
# coordination needed here.

exit 0
