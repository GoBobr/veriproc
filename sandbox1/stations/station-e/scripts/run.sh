#!/usr/bin/env bash
# Station-E aggregator: collects available SCE_2__PROCBIG__ granules resolved
# from micro-ra and combines them into a single SCE_2__FINAL____
# product spanning the full parent processing window.
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RETRY_INDEX:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"
: "${VERIPROC_WINDOW_START:?}"
: "${VERIPROC_WINDOW_END:?}"

# Collect all PROCBIG granule symlinks resolved from micro-ra.
procbig_files=()
while IFS= read -r f; do
    [[ -n "$f" ]] && procbig_files+=("$f")
done < <(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*SCE_2__PROCBIG__*' | sort)
granule_count="${#procbig_files[@]}"

if [[ "${granule_count}" -eq 0 ]]; then
    echo "ERROR: no SCE_2__PROCBIG__ inputs found" >&2
    exit 1
fi

echo "station-E: aggregating ${granule_count} granule(s) for window ${VERIPROC_WINDOW_START}->${VERIPROC_WINDOW_END}"
for f in "${procbig_files[@]}"; do
    echo "  input: $(basename "${f}")"
done

GEN_TIME=$(date -u +"%Y%m%dT%H%M%S")

# FILE_TYPE = SCE_2__FINAL____ (16 chars); pattern separator = _ON_
# Results in five underscores before ON in the filename (____ from FILE_TYPE + _ separator).
OUTFNAME="CDMA_SCE_2__FINAL_____ON_${VERIPROC_WINDOW_START}_${VERIPROC_WINDOW_END}_${GEN_TIME}_018_093_EUM__VAL_T_NR_C00.nc"

{
    echo "station: STATION-E"
    echo "product: aggregated final"
    echo "task_id: ${VERIPROC_TASK_ID}"
    echo "retry_index: ${VERIPROC_RETRY_INDEX}"
    echo "window_start: ${VERIPROC_WINDOW_START}"
    echo "window_end: ${VERIPROC_WINDOW_END}"
    echo "granule_count: ${granule_count}"
    echo "granules:"
    for f in "${procbig_files[@]}"; do
        echo "  - $(basename "${f}")"
    done
} > "${VERIPROC_RUN_DIR}/${OUTFNAME}"

echo "wrote ${OUTFNAME} from ${granule_count} granule(s)"
sleep 15
exit 0
