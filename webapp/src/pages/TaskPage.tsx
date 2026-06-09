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

const TERMINAL_STATES = new Set(["complete", "failed", "cancelled"]);
const POLL_INTERVAL_MS = 3000;

export function TaskPage({ instanceID, taskID, initialRetry }: Props) {
  const { client, session } = useAuth();
  // Poll while active; stop once the task reaches a terminal state.
  const [taskTerminal, setTaskTerminal] = useState(false);
  const pollInterval = taskTerminal ? 0 : POLL_INTERVAL_MS;
  const taskQ = usePoll(() => client.getTask(instanceID, taskID), pollInterval, [instanceID, taskID]);
  const runsQ = usePoll(() => client.listTaskRuns(instanceID, taskID), pollInterval, [instanceID, taskID]);

  const runs = (runsQ.data?.items ?? []) as Array<Record<string, unknown>>;
  const [retryIndex, setRetryIndex] = useState<number | null>(initialRetry ?? null);
  const [workingRoot, setWorkingRoot] = useState<string | null>(null);
  const taskState = String(taskQ.data?.state ?? "");
  const isOperator = session?.role === "operator";
  const canRetry = isOperator && (taskState === "failed" || taskState === "cancelled");

  // Stop polling once we observe a terminal state.
  useEffect(() => {
    if (taskState && TERMINAL_STATES.has(taskState)) setTaskTerminal(true);
    else if (taskState) setTaskTerminal(false);
  }, [taskState]);
  // Derive the state of the currently selected run so we can show Cancel.
  const selectedRun = retryIndex !== null
    ? runs.find((r) => Number(r.retry_index ?? 0) === retryIndex)
    : null;
  const selectedRunState = String(selectedRun?.state ?? "");
  const executionNode = String(selectedRun?.execution_node ?? "");
  const elapsedTime = String(selectedRun?.elapsed_time ?? "");
  const selectedRunFailure = String(selectedRun?.failure_summary ?? selectedRun?.failure_reason ?? "");
  const canCancel = isOperator && (selectedRunState === "running" || selectedRunState === "queued");
  // Task-level failure summary (e.g. a fatal preparation error that occurs
  // before any run is created). Surfaced only when no run carries its own
  // failure detail, so we don't duplicate the per-run reason below.
  const taskFailure = String(taskQ.data?.failure_summary ?? "");
  const showTaskFailure = taskState === "failed" && runs.length === 0 && taskFailure !== "";

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
            <div>created<span>{fmtTime(selectedRun?.created_at ?? taskQ.data.created_at)}</span></div>
            <div>elapsed<span>{elapsedTime || "—"}</span></div>
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
        {showTaskFailure && (
          <div class="run-failure-reason">
            {taskState}
            <span>{taskFailure}</span>
          </div>
        )}
        {workingRoot && (
          <div class="working-root">
            <span class="working-root-label">working root</span>
            <span class="working-root-path">{workingRoot}</span>
          </div>
        )}
      </div>
      {retryIndex !== null && runs.length > 0 && (
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
  const [previewIsImage, setPreviewIsImage] = useState(false);
  const [imageBlobURL, setImageBlobURL] = useState<string | null>(null);
  const prevBlobRef = useRef<string | null>(null);
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

  // Fetch image as blob URL (with auth header) whenever an image file is selected.
  useEffect(() => {
    if (prevBlobRef.current) {
      URL.revokeObjectURL(prevBlobRef.current);
      prevBlobRef.current = null;
      setImageBlobURL(null);
    }
    if (!previewPath || !previewIsImage) return;
    let cancelled = false;
    client.fetchImageBlob(instanceID, taskID, retryIndex, previewPath).then((url) => {
      if (cancelled) { URL.revokeObjectURL(url); return; }
      prevBlobRef.current = url;
      setImageBlobURL(url);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [previewPath, previewIsImage, retryIndex]);

  const prevQ = usePoll<PreviewResponse | null>(
    async () =>
      previewPath && !previewIsImage
        ? await client.previewFile(instanceID, taskID, retryIndex, previewPath, {
            mode: previewMode,
          })
        : null,
    follow ? 2000 : 0,
    [previewPath, previewIsImage, retryIndex, previewMode]
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
                  const isImg = IMAGE_EXTS.has(extOf(e.name));
                  setPreviewPath(e.path);
                  setPreviewIsImage(isImg);
                  if (!isImg) {
                    setPreviewMode(e.kind === "log" || e.size > 256 * 1024 ? "tail" : "head");
                    setFollow(false);
                  }
                }
              }}
            >
              <span>{e.name}</span>
              {!e.is_dir && <span class="size">{e.size}b</span>}
            </li>
          ))}
        </ul>
      </div>
      <div class={`preview-pane${!previewIsImage && wrap ? " wrap" : ""}`}>
        <div class="toolbar">
          <span>{previewPath || "(select a file)"}</span>
          {!previewIsImage && prevQ.data?.truncated && <span class="truncated">truncated</span>}
          {!previewIsImage && prevQ.data && previewMode === "tail" && (
            <span class="preview-offset">byte {prevQ.data.offset}+</span>
          )}
          {previewIsImage ? (
            <button
              class="secondary small"
              disabled={!previewPath}
              onClick={() => {
                // Re-trigger the image fetch by toggling.
                setImageBlobURL(null);
                if (previewPath)
                  client.fetchImageBlob(instanceID, taskID, retryIndex, previewPath).then((url) => {
                    if (prevBlobRef.current) URL.revokeObjectURL(prevBlobRef.current);
                    prevBlobRef.current = url;
                    setImageBlobURL(url);
                  }).catch(() => {});
              }}
            >
              refresh
            </button>
          ) : (
            <>
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
            </>
          )}
        </div>
        <div class="preview-body" ref={previewPaneRef}>
          {previewIsImage && imageBlobURL && (
            <img
              src={imageBlobURL}
              alt={previewPath ?? ""}
              style={{ maxWidth: "100%", maxHeight: "100%", objectFit: "contain", display: "block", margin: "auto" }}
            />
          )}
          {previewIsImage && !imageBlobURL && previewPath && (
            <div class="binary">loading image…</div>
          )}
          {!previewIsImage && prevQ.error && <div class="degraded-msg">preview: {prevQ.error.message}</div>}
          {!previewIsImage && prevQ.data && prevQ.data.kind === "binary" && (
            <div class="binary">{prevQ.data.reason || "binary file"}</div>
          )}
          {!previewIsImage && prevQ.data && (prevQ.data.kind === "text" || prevQ.data.kind === "log") && (
            <pre>{prevQ.data.content || ""}</pre>
          )}
        </div>
      </div>
    </div>
  );
}

const IMAGE_EXTS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".svg"]);

function extOf(name: string): string {
  const i = name.lastIndexOf(".");
  return i >= 0 ? name.slice(i).toLowerCase() : "";
}

function parentPath(p: string): string {
  if (!p) return "";
  const i = p.lastIndexOf("/");
  if (i < 0) return "";
  return p.slice(0, i);
}
