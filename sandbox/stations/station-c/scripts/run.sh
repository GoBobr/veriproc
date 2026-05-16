#!/usr/bin/env bash
# Station-C fan-out script: splits SCE_2__BIG______ into SLICES micro-granules,
# then writes task-out.yaml so the backend creates one station-D task per slice
# in a shared split group.
set -euo pipefail

: "${VERIPROC_WORKING_ROOT:?}"
: "${VERIPROC_STATION_ID:?}"
: "${VERIPROC_RETRY_INDEX:?}"
: "${VERIPROC_TASK_ID:?}"
: "${VERIPROC_JOBORDER_PATH:?}"
: "${VERIPROC_RUN_DIR:?}"
: "${VERIPROC_WINDOW_START:?}"
: "${VERIPROC_WINDOW_END:?}"

# ---------------------------------------------------------------------------
# Find the mandatory primary input.
# ---------------------------------------------------------------------------
primary=$(find "${VERIPROC_WORKING_ROOT}/input" -type l -name '*SCE_2__BIG______*' | sort | tail -n 1)
if [[ -z "${primary}" ]]; then
    echo "ERROR: no SCE_2__BIG______ input found" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Portable timestamp helpers (macOS / GNU Linux).
# ---------------------------------------------------------------------------
compact_to_epoch() {
    local ts="$1"
    if [[ "$(uname)" == "Darwin" ]]; then
        date -j -u -f "%Y%m%dT%H%M%S" "${ts}" "+%s"
    else
        date -u -d "${ts:0:4}-${ts:4:2}-${ts:6:2}T${ts:9:2}:${ts:11:2}:${ts:13:2}" "+%s"
    fi
}

epoch_to_compact() {
    local epoch="$1"
    if [[ "$(uname)" == "Darwin" ]]; then
        date -j -u -r "${epoch}" "+%Y%m%dT%H%M%S"
    else
        date -u -d "@${epoch}" "+%Y%m%dT%H%M%S"
    fi
}

epoch_to_rfc3339() {
    local epoch="$1"
    if [[ "$(uname)" == "Darwin" ]]; then
        date -j -u -r "${epoch}" "+%Y-%m-%dT%H:%M:%SZ"
    else
        date -u -d "@${epoch}" "+%Y-%m-%dT%H:%M:%SZ"
    fi
}

# ---------------------------------------------------------------------------
# Split configuration.
# ---------------------------------------------------------------------------
SLICES=10
WIN_EPOCH=$(compact_to_epoch "${VERIPROC_WINDOW_START}")
WIN_END_EPOCH=$(compact_to_epoch "${VERIPROC_WINDOW_END}")
WIN_DURATION=$(( WIN_END_EPOCH - WIN_EPOCH ))
# Derive slice length from actual window duration so all slices fit inside.
# Integer division; any remainder is absorbed by the last slice.
SLICE_SECS=$(( WIN_DURATION / SLICES ))
if [[ "${SLICE_SECS}" -lt 1 ]]; then
    echo "ERROR: window too short to split into ${SLICES} slices (${WIN_DURATION}s)" >&2
    exit 1
fi
GROUP_ID="sg-${VERIPROC_TASK_ID}"
GEN_TIME=$(date -u +"%Y%m%dT%H%M%S")

echo "station-C: splitting ${VERIPROC_WINDOW_START}->${VERIPROC_WINDOW_END} into ${SLICES} slices of ${SLICE_SECS}s"
echo "station-C: group_id=${GROUP_ID}, primary_input=$(basename "${primary}")"

# ---------------------------------------------------------------------------
# Pass 1 - produce MICROBIG output files. The backend validates every matching
# output artifact and publishes them to micro-ra during finalization before it
# creates the descriptor-declared station-D tasks.
# FILE_TYPE = SCE_2__MICROBIG_ (16 chars); pattern separator = _ON_
# ---------------------------------------------------------------------------
for i in $(seq 0 $((SLICES - 1))); do
    SLICE_START_EPOCH=$((WIN_EPOCH + i * SLICE_SECS))
    if [[ "${i}" -eq $((SLICES - 1)) ]]; then
        SLICE_END_EPOCH=${WIN_END_EPOCH}
    else
        SLICE_END_EPOCH=$((WIN_EPOCH + (i + 1) * SLICE_SECS))
    fi
    SLICE_START=$(epoch_to_compact "${SLICE_START_EPOCH}")
    SLICE_END=$(epoch_to_compact "${SLICE_END_EPOCH}")
    SLICE_IDX=$(printf "%02d" "${i}")

    # SCE_2__MICROBIG_ = 16 chars; +_+ON+_ = __ON_ in filename
    OUTFNAME="CDMA_SCE_2__MICROBIG__ON_${SLICE_START}_${SLICE_END}_${GEN_TIME}_${SLICE_IDX}0_093_EUM__VAL_T_NR_C00.nc"

    cat > "${VERIPROC_RUN_DIR}/${OUTFNAME}" <<OUTPUT
station: STATION-C
product: micro-granule
slice: ${i} of ${SLICES}
group_id: ${GROUP_ID}
task_id: ${VERIPROC_TASK_ID}
retry_index: ${VERIPROC_RETRY_INDEX}
window_start: ${SLICE_START}
window_end: ${SLICE_END}
input_big: $(basename "${primary}")
OUTPUT

    echo "  wrote slice ${i}: ${OUTFNAME}"
done

# ---------------------------------------------------------------------------
# Pass 2 - write task-out.yaml. This descriptor is the authoritative routing
# instruction consumed by the backend during run finalization; the script does
# not submit downstream tasks itself.
# ---------------------------------------------------------------------------
TASK_OUT="${VERIPROC_WORKING_ROOT}/task-out.yaml"
cat > "${TASK_OUT}" <<YAML
schema_version: veriproc.task-out/v1
split_groups:
  - group_id: ${GROUP_ID}
    mode: aggregation
    aggregation_station_id: statE
    expected_members: ${SLICES}
    closure: closed
    label: station-C microgranule fan-out
    description: statD microgranules for ${VERIPROC_TASK_ID}
downstream:
YAML

echo "station-C: writing task-out.yaml with ${SLICES} station-D tasks in group ${GROUP_ID}"
for i in $(seq 0 $((SLICES - 1))); do
    SLICE_START_EPOCH=$((WIN_EPOCH + i * SLICE_SECS))
    if [[ "${i}" -eq $((SLICES - 1)) ]]; then
        SLICE_END_EPOCH=${WIN_END_EPOCH}
    else
        SLICE_END_EPOCH=$((WIN_EPOCH + (i + 1) * SLICE_SECS))
    fi
    SLICE_START_RFC3339=$(epoch_to_rfc3339 "${SLICE_START_EPOCH}")
    SLICE_END_RFC3339=$(epoch_to_rfc3339 "${SLICE_END_EPOCH}")
    SLICE_KEY=$(printf "slice-%02d" "${i}")

    cat >> "${TASK_OUT}" <<YAML
  - key: ${SLICE_KEY}
    station_id: statD
    split_group_id: ${GROUP_ID}
    role: member
    window:
      start: ${SLICE_START_RFC3339}
      end: ${SLICE_END_RFC3339}
YAML
    echo "  declared ${SLICE_KEY}: start=${SLICE_START_RFC3339} end=${SLICE_END_RFC3339}"
done

echo "station-C: done"
echo "  descriptor: ${TASK_OUT}"
