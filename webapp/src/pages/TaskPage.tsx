import { useEffect, useRef, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import { useFetch, usePoll } from "../state/poll";
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
  const { client, session } = useAuth();
  const taskQ = useFetch(() => client.getTask(instanceID, taskID), [instanceID, taskID]);
  const runsQ = useFetch(() => client.listTaskRuns(instanceID, taskID), [instanceID, taskID]);

  const runs = (runsQ.data?.items ?? []) as Array<Record<string, unknown>>;
  const [retryIndex, setRetryIndex] = useState<number | null>(initialRetry ?? null);
  const [workingRoot, setWorkingRoot] = useState<string | null>(null);
  const taskState = String(taskQ.data?.state ?? "");
  const isOperator = session?.role === "operator";
  const canRetry = isOperator && (taskState === "failed" || taskState === "cancelled");
  // Derive the state of the currently selected run so we can show Cancel.
  const selectedRun = retryIndex !== null
    ? runs.find((r) => Number(r.retry_index ?? 0) === retryIndex)
    : null;
  const selectedRunState = String(selectedRun?.state ?? "");
  const executionNode = String(selectedRun?.execution_node ?? "");
  const selectedRunFailure = String(selectedRun?.failure_summary ?? selectedRun?.failure_reason ?? "");
  const canCancel = isOperator && (selectedRunState === "running" || selectedRunState === "queued");

  useEffect(() => {
    if (retryIndex === null && runs.length > 0) {
      const latest = runs.reduce(
        (acc, r) => Math.max(acc, Number(r.retry_index ?? 0)),
        -1
      );
      if (latest >= 0) setRetryIndex(latest);
    }
  }, [runs, retryIndex]);

  // Clear working root whenever the selected run changes so we don't briefly
  // display the previous run's path while the new tree query is in flight.
  useEffect(() => { setWorkingRoot(null); }, [retryIndex]);

  return (
    <div class="task-page">
      <div class="task-header">
        <div class="task-title-row">
          <h2><span class="task-instance-prefix">{instanceID} / </span>{taskID}</h2>
          {canRetry && (
            <button
              class="primary small"
              onClick={() => {
                void client.retryTask(instanceID, taskID).then((out) => {
                  if (out.retry_index !== undefined) setRetryIndex(Number(out.retry_index));
                  taskQ.refresh();
                  runsQ.refresh();
                });
              }}
            >
              restart
            </button>
          )}
          {canCancel && retryIndex !== null && (
            <button
              class="secondary small"
              style={{ color: "var(--danger)", borderColor: "var(--slot-failed)" }}
              onClick={() => {
                void client.cancelRun(instanceID, taskID, retryIndex).then(() => {
                  taskQ.refresh();
                  runsQ.refresh();
                });
              }}
            >
              cancel
            </button>
          )}
        </div>
        {taskQ.error && <div class="degraded-msg">task: {taskQ.error.message}</div>}
        {taskQ.data && (
          <div class="task-meta">
            <div>station<span>{String(taskQ.data.station_id ?? "—")}</span></div>
            <div>node<span>{executionNode || "—"}</span></div>
            <div>state<span>{selectedRunState || String(taskQ.data.state ?? "—")}</span></div>
            <div>window<span>{fmtTime(taskQ.data.start)} – {fmtTime(taskQ.data.end)}</span></div>
            <div>created<span>{fmtTime(taskQ.data.created_at)}</span></div>
          </div>
        )}
        {runs.length > 0 && (
          <div class="run-selector">
            run:
            {runs.map((r) => {
              const idx = Number(r.retry_index ?? 0);
              const st = String(r.state ?? "");
              const isBad = st === "failed" || st === "cancelled";
              const cls = [retryIndex === idx ? "active" : "", isBad ? "run-failed" : ""].filter(Boolean).join(" ");
              return (
                <button
                  key={idx}
                  class={cls}
                  onClick={() => setRetryIndex(idx)}
                >
                  r{idx}
                </button>
              );
            })}
          </div>
        )}
        {selectedRun && (selectedRunState === "failed" || selectedRunState === "cancelled") && selectedRunFailure && (
          <div class="run-failure-reason">
            r{retryIndex} {selectedRunState}
            <span>{selectedRunFailure}</span>
          </div>
        )}
        {workingRoot && (
          <div class="working-root">
            <span class="working-root-label">working root</span>
            <span class="working-root-path">{workingRoot}</span>
          </div>
        )}
      </div>
      {retryIndex !== null && (
        <RunBrowser
          instanceID={instanceID}
          taskID={taskID}
          retryIndex={retryIndex}
          onWorkingRoot={setWorkingRoot}
        />
      )}
    </div>
  );
}

function RunBrowser({
  instanceID,
  taskID,
  retryIndex,
  onWorkingRoot,
}: {
  instanceID: string;
  taskID: string;
  retryIndex: number;
  onWorkingRoot: (root: string) => void;
}) {
  const { client } = useAuth();
  const [path, setPath] = useState("");
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const [previewMode, setPreviewMode] = useState<"head" | "tail">("head");
  const [follow, setFollow] = useState(false);
  const [wrap, setWrap] = useState(false);
  const previewPaneRef = useRef<HTMLDivElement | null>(null);
  const treeQ = useFetch<TreeResponse>(
    () => client.listTree(instanceID, taskID, retryIndex, path),
    [instanceID, taskID, retryIndex, path]
  );

  useEffect(() => {
    if (treeQ.data?.working_root) onWorkingRoot(treeQ.data.working_root);
  }, [treeQ.data?.working_root]);
  const prevQ = usePoll<PreviewResponse | null>(
    async () =>
      previewPath
        ? await client.previewFile(instanceID, taskID, retryIndex, previewPath, {
            mode: previewMode,
          })
        : null,
    follow ? 2000 : 0,
    [previewPath, retryIndex, previewMode]
  );

  useEffect(() => {
    if (follow && previewPaneRef.current) {
      previewPaneRef.current.scrollTop = previewPaneRef.current.scrollHeight;
    }
  }, [follow, prevQ.data?.content]);

  return (
    <div class="split-panes">
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
                else {
                  setPreviewPath(e.path);
                  setPreviewMode(e.kind === "log" || e.size > 256 * 1024 ? "tail" : "head");
                  setFollow(false);
                }
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
          {prevQ.data && previewMode === "tail" && (
            <span class="preview-offset">byte {prevQ.data.offset}+</span>
          )}
          <button
            class="secondary small"
            disabled={!previewPath}
            onClick={() => prevQ.refresh()}
          >
            refresh
          </button>
          <button
            class={`secondary small${previewMode === "tail" ? " active" : ""}`}
            disabled={!previewPath}
            onClick={() => {
              if (previewMode === "tail") {
                setFollow(false);
                setPreviewMode("head");
              } else {
                setPreviewMode("tail");
              }
            }}
          >
            tail
          </button>
          <button
            class={`secondary small${follow ? " active" : ""}`}
            disabled={!previewPath}
            onClick={() => {
              setPreviewMode("tail");
              setFollow((v) => !v);
            }}
          >
            follow
          </button>
          <label style={{ fontSize: "11px" }}>
            <input
              type="checkbox"
              checked={wrap}
              onChange={(e) => setWrap((e.target as HTMLInputElement).checked)}
            />{" "}
            wrap
          </label>
        </div>
        <div class="preview-body" ref={previewPaneRef}>
          {prevQ.error && <div class="degraded-msg">preview: {prevQ.error.message}</div>}
          {prevQ.data && prevQ.data.kind === "binary" && (
            <div class="binary">{prevQ.data.reason || "binary file"}</div>
          )}
          {prevQ.data && (prevQ.data.kind === "text" || prevQ.data.kind === "log") && (
            <pre>{prevQ.data.content || ""}</pre>
          )}
        </div>
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
