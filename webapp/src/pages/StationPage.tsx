import { useEffect, useMemo, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import { useFetch, usePoll } from "../state/poll";
import { taskHref } from "../state/router";
import type { StationRun, StationRunsResponse } from "../api/types";

interface Props {
  instanceID: string;
  stationID: string;
}

type SortCol = "run_id" | "created_at" | "task_id" | "elapsed";
type SortDir = "ASC" | "DESC";

const POLL_INTERVAL_MS = 5000;
const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];

// Map a run state to the dashboard slot kind for colour reuse.
function stateToKind(state: string): string {
  switch (state) {
    case "running":
      return "running";
    case "queued":
    case "pending":
      return "queued";
    case "complete":
      return "completed";
    case "failed":
      return "failed";
    case "cancelled":
      return "cancelled";
    case "finalizing":
      return "running";
    default:
      return "queued";
  }
}

// Format an ISO-8601 timestamp to millisecond precision.
function fmtTime(raw: unknown): string {
  if (!raw || raw === "—") return "—";
  const s = String(raw);
  return s.replace(/(\.(\d{3}))\d+(Z|[+-]\d{2}:\d{2})?$/, "$1$3");
}

// Parse an elapsed-time string (e.g. "5m30s", "1:23:45", "2-12:00:00")
// to seconds for sorting. Returns -1 for empty/unparseable values so
// they sort last in ascending order.
function elapsedToSeconds(s: string): number {
  if (!s) return -1;
  // Go duration format: "5m30s", "1h2m3s", etc.
  const goMatch = s.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/);
  if (goMatch && s.includes("s")) {
    const h = parseInt(goMatch[1] || "0", 10);
    const m = parseInt(goMatch[2] || "0", 10);
    const sec = parseFloat(goMatch[3] || "0");
    return h * 3600 + m * 60 + sec;
  }
  // sacct format: "[DD-]HH:MM:SS"
  const parts = s.split(/[-:]/).map(Number);
  if (parts.every((n) => !isNaN(n))) {
    let mult = 1;
    let total = 0;
    for (let i = parts.length - 1; i >= 0; i--) {
      total += parts[i] * mult;
      mult *= 60;
      if (mult > 3600) mult = 24; // days
    }
    return total;
  }
  return -1;
}

export function StationPage({ instanceID, stationID }: Props) {
  const { client, session } = useAuth();
  const isOperator = session?.role === "operator";

  // Fetch instance list for UI config (page size default, refresh interval).
  const instancesQ = useFetch(() => client.instances(), []);
  const uiConfig = instancesQ.data?.ui;
  const defaultPageSize = uiConfig?.station_runs_page_size ?? 50;
  const refreshInterval = uiConfig?.refresh_interval_ms
    ? Math.max(uiConfig.refresh_interval_ms, 2000)
    : POLL_INTERVAL_MS;

  // Sorting + pagination state.
  const [sortCol, setSortCol] = useState<SortCol>("run_id");
  const [sortDir, setSortDir] = useState<SortDir>("DESC");
  const [pageSize, setPageSize] = useState(50);
  const [cursor, setCursor] = useState<string | null>(null);
  const [cursorStack, setCursorStack] = useState<(string | null)[]>([]);

  // Sync page size once UI config is loaded.
  useEffect(() => {
    if (defaultPageSize > 0 && pageSize === 50 && defaultPageSize !== 50) {
      setPageSize(defaultPageSize);
    }
  }, [defaultPageSize]); // eslint-disable-line react-hooks/exhaustive-deps

  // Fetch runs with polling. When sorting by "elapsed" (a computed field),
  // we fetch with the default run_id sort and re-sort client-side.
  const serverSort = sortCol === "elapsed" ? "run_id" : sortCol;
  const runsQ = usePoll(
    () =>
      client.listStationRuns(instanceID, stationID, {
        limit: pageSize,
        cursor: cursor ?? undefined,
        sort: serverSort,
        order: sortDir,
      }),
    refreshInterval,
    [instanceID, stationID, pageSize, cursor, serverSort, sortDir]
  );

  const data = runsQ.data as StationRunsResponse | null;
  const rawRuns = data?.items ?? [];
  const hasNext = !!data?.next_cursor;

  // Elapsed is a computed field (from jobs, not a DB column), so sorting by
  // it is done client-side within the current page. All other sort columns
  // are handled server-side.
  const runs = useMemo(() => {
    if (sortCol !== "elapsed") return rawRuns;
    const sorted = [...rawRuns];
    sorted.sort((a, b) => {
      const cmp = elapsedToSeconds(a.elapsed_time || "") - elapsedToSeconds(b.elapsed_time || "");
      return sortDir === "ASC" ? cmp : -cmp;
    });
    return sorted;
  }, [rawRuns, sortCol, sortDir]);

  const toggleSort = (col: SortCol) => {
    if (sortCol === col) {
      setSortDir((d) => (d === "ASC" ? "DESC" : "ASC"));
    } else {
      setSortCol(col);
      setSortDir("DESC");
    }
    // Reset to first page on sort change.
    setCursor(null);
    setCursorStack([]);
  };
  const goNext = () => {
    if (!data?.next_cursor) return;
    setCursorStack((s) => [...s, cursor]);
    setCursor(data.next_cursor);
  };

  const goPrev = () => {
    if (cursorStack.length === 0) return;
    const prev = cursorStack[cursorStack.length - 1];
    setCursorStack((s) => s.slice(0, -1));
    setCursor(prev);
  };

  const changePageSize = (size: number) => {
    setPageSize(size);
    setCursor(null);
    setCursorStack([]);
  };

  const pageTitle = useMemo(
    () => `${instanceID} / ${stationID}`,
    [instanceID, stationID]
  );

  return (
    <div class="station-page">
      <div class="station-page-header">
        <div class="station-page-title-row">
          <a href="#/" class="back-link">← Dashboard</a>
          <h2>{pageTitle}</h2>
          {isOperator && (
            <button
              class="secondary small"
              onClick={() => {
                void client.pauseStation(instanceID, stationID).then(() => runsQ.refresh());
              }}
            >
              Pause
            </button>
          )}
          {isOperator && (
            <button
              class="secondary small"
              onClick={() => {
                void client.unpauseStation(instanceID, stationID).then(() => runsQ.refresh());
              }}
            >
              Unpause
            </button>
          )}
        </div>
        {runsQ.error && <div class="degraded-msg">error: {runsQ.error.message}</div>}
      </div>

      <div class="station-table-wrap">
        {runs.length === 0 && !runsQ.loading ? (
          <div class="station-empty">No runs found for this station.</div>
        ) : (
          <table class="station-table">
            <thead>
              <tr>
                <th
                  class={sortCol === "task_id" ? "sort-active" : "sortable"}
                  onClick={() => toggleSort("task_id")}
                >
                  Run ID {sortCol === "task_id" && (sortDir === "ASC" ? "▲" : "▼")}
                </th>
                <th
                  class={sortCol === "created_at" ? "sort-active" : "sortable"}
                  onClick={() => toggleSort("created_at")}
                >
                  Created {sortCol === "created_at" && (sortDir === "ASC" ? "▲" : "▼")}
                </th>
                <th>Working Root</th>
                <th>Node</th>
                <th
                  class={sortCol === "elapsed" ? "sort-active" : "sortable"}
                  onClick={() => toggleSort("elapsed")}
                >
                  Elapsed {sortCol === "elapsed" && (sortDir === "ASC" ? "▲" : "▼")}
                </th>
                <th
                  class={sortCol === "run_id" ? "sort-active" : "sortable"}
                  onClick={() => toggleSort("run_id")}
                >
                  Status {sortCol === "run_id" && (sortDir === "ASC" ? "▲" : "▼")}
                </th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => {
                const kind = stateToKind(run.state);
                const ref = run.run_ref || `${run.task_id}/r${run.retry_index}`;
                return (
                  <tr key={run.run_id}>
                    <td class="run-id-cell">{ref}</td>
                    <td class="mono">{fmtTime(run.created_at)}</td>
                    <td class="mono working-root-cell" title={run.working_root || ""}>
                      {run.working_root || "—"}
                    </td>
                    <td class="mono">{run.execution_node || "—"}</td>
                    <td class="mono">{run.elapsed_time || "—"}</td>
                    <td>
                      <button
                        class="status-btn"
                        data-kind={kind}
                        title={`${run.state} — click to open task page`}
                        onClick={() =>
                          window.open(taskHref(instanceID, run.task_id, run.retry_index), "_blank")
                        }
                      >
                        {run.state}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <div class="station-pagination">
        <div class="pagination-info">
          {data?.ordering && <span class="ordering">sorted: {data.ordering}</span>}
        </div>
        <div class="pagination-controls">
          <button
            class="secondary small"
            disabled={cursorStack.length === 0}
            onClick={goPrev}
          >
            ← Prev
          </button>
          <span class="page-num">
            {cursorStack.length + 1}
          </span>
          <button
            class="secondary small"
            disabled={!hasNext}
            onClick={goNext}
          >
            Next →
          </button>
          <select
            class="page-size-select"
            value={pageSize}
            onChange={(e) => changePageSize(Number((e.target as HTMLSelectElement).value))}
          >
            {PAGE_SIZE_OPTIONS.map((n) => (
              <option key={n} value={n}>{n} / page</option>
            ))}
          </select>
        </div>
      </div>
    </div>
  );
}
