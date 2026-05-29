import { useEffect, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import { useFetch } from "../state/poll";
import type { PreviewResponse, TreeResponse } from "../api/types";

interface Props {
  instanceID: string;
  taskID: string;
  initialRetry?: number;
}

// Format an ISO-8601 timestamp to millisecond precision (drop sub-ms digits).
function fmtTime(raw: unknown): string {
  if (!raw || raw === "—") return "—";
  const s = String(raw);
  // Replace trailing nanoseconds (7-9 fractional digits) with ms (3 digits).
  return s.replace(/(\.(\d{3}))\d+(Z|[+-]\d{2}:\d{2})?$/, "$1$3");
}

export function TaskPage({ instanceID, taskID, initialRetry }: Props) {
  const { client } = useAuth();
  const taskQ = useFetch(() => client.getTask(instanceID, taskID), [instanceID, taskID]);
  const runsQ = useFetch(() => client.listTaskRuns(instanceID, taskID), [instanceID, taskID]);

  const runs = (runsQ.data?.items ?? []) as Array<Record<string, unknown>>;
  const [retryIndex, setRetryIndex] = useState<number | null>(initialRetry ?? null);

  useEffect(() => {
    if (retryIndex === null && runs.length > 0) {
      const latest = runs.reduce(
        (acc, r) => Math.max(acc, Number(r.retry_index ?? 0)),
        -1
      );
      if (latest >= 0) setRetryIndex(latest);
    }
  }, [runs, retryIndex]);

  return (
    <div class="task-page">
      <div class="task-header">
        <h2>{taskID}</h2>
        {taskQ.error && <div class="degraded-msg">task: {taskQ.error.message}</div>}
        {taskQ.data && (
          <div class="task-meta">
            <div>station<span>{String(taskQ.data.station_id ?? "—")}</span></div>
            <div>state<span>{String(taskQ.data.state ?? "—")}</span></div>
            <div>created<span>{fmtTime(taskQ.data.created_at)}</span></div>
            <div>window start<span>{fmtTime(taskQ.data.start)}</span></div>
            <div>window end<span>{fmtTime(taskQ.data.end)}</span></div>
            <div>instance<span>{instanceID}</span></div>
            {taskQ.data.failure_summary && (
              <div class="task-failure-summary">
                failure summary
                <span>{String(taskQ.data.failure_summary)}</span>
              </div>
            )}
          </div>
        )}
        {runs.length > 0 && (
          <div class="run-selector">
            run:
            {runs.map((r) => {
              const idx = Number(r.retry_index ?? 0);
              return (
                <button
                  key={idx}
                  class={retryIndex === idx ? "active" : ""}
                  onClick={() => setRetryIndex(idx)}
                >
                  r{idx}
                </button>
              );
            })}
          </div>
        )}
      </div>
      {retryIndex !== null && (
        <RunBrowser instanceID={instanceID} taskID={taskID} retryIndex={retryIndex} />
      )}
    </div>
  );
}

function RunBrowser({
  instanceID,
  taskID,
  retryIndex,
}: {
  instanceID: string;
  taskID: string;
  retryIndex: number;
}) {
  const { client } = useAuth();
  const [path, setPath] = useState("");
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [wrap, setWrap] = useState(false);
  const treeQ = useFetch<TreeResponse>(
    () => client.listTree(instanceID, taskID, retryIndex, path),
    [instanceID, taskID, retryIndex, path]
  );
  const prevQ = useFetch<PreviewResponse | null>(
    async () => (previewPath ? await client.previewFile(instanceID, taskID, retryIndex, previewPath) : null),
    [previewPath, retryIndex]
  );

  return (
    <div class="split-panes">
      {treeQ.data?.working_root && (
        <div class="working-root" style={{ gridColumn: "1 / -1" }}>
          <span class="working-root-label">working root</span>
          <span class="working-root-path">{treeQ.data.working_root}</span>
        </div>
      )}
      <div class="tree-pane">
        <div class="path">/{path}</div>
        {path && (
          <button
            class="secondary"
            onClick={() => setPath(parentPath(path))}
            style={{ marginBottom: "6px" }}
          >
            ↑ up
          </button>
        )}
        {treeQ.error && <div class="degraded-msg">tree: {treeQ.error.message}</div>}
        <ul>
          {treeQ.data?.entries.map((e) => (
            <li
              key={e.path}
              class={e.is_dir ? "dir" : ""}
              onClick={() => {
                if (e.is_dir) setPath(e.path);
                else setPreviewPath(e.path);
              }}
            >
              <span>{e.name}</span>
              {!e.is_dir && <span class="size">{e.size}b</span>}
            </li>
          ))}
        </ul>
      </div>
      <div class={`preview-pane${wrap ? " wrap" : ""}`}>
        <div class="toolbar">
          <span>{previewPath || "(select a file)"}</span>
          {prevQ.data?.truncated && <span class="truncated">truncated</span>}
          <label style={{ marginLeft: "auto", fontSize: "11px" }}>
            <input
              type="checkbox"
              checked={wrap}
              onChange={(e) => setWrap((e.target as HTMLInputElement).checked)}
            />{" "}
            wrap
          </label>
        </div>
        {prevQ.error && <div class="degraded-msg">preview: {prevQ.error.message}</div>}
        {prevQ.data && prevQ.data.kind === "binary" && (
          <div class="binary">{prevQ.data.reason || "binary file"}</div>
        )}
        {prevQ.data && (prevQ.data.kind === "text" || prevQ.data.kind === "log") && (
          <pre>{prevQ.data.content || ""}</pre>
        )}
      </div>
    </div>
  );
}

function parentPath(p: string): string {
  if (!p) return "";
  const i = p.lastIndexOf("/");
  if (i < 0) return "";
  return p.slice(0, i);
}
