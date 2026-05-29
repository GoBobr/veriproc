import type {
  DashboardInstance,
  InstancesResponse,
  PreviewResponse,
  SystemInfo,
  TreeResponse,
} from "./types";

// Lightweight typed HTTP client targeting the console gateway. Tokens are
// passed explicitly so the same client can be reused outside React.
export class ConsoleClient {
  constructor(private readonly token: string, private readonly base = "") {}

  private async send<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(this.base + path, {
      method,
      headers: {
        ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const ct = res.headers.get("Content-Type") || "";
    let data: unknown = null;
    if (ct.includes("application/json")) {
      data = await res.json();
    } else {
      data = await res.text();
    }
    if (!res.ok) {
      const msg = (data as { error?: { message?: string } } | string) || "request failed";
      const message =
        typeof msg === "string"
          ? msg
          : msg.error?.message || `HTTP ${res.status}`;
      throw new Error(message);
    }
    return data as T;
  }

  instances = (): Promise<InstancesResponse> =>
    this.send("GET", "/api/console/instances");

  systemInfo = (): Promise<SystemInfo> =>
    this.send("GET", "/api/console/info");

  dashboard = (instanceID: string, since?: Date): Promise<DashboardInstance> => {
    const q = since ? `?since=${encodeURIComponent(since.toISOString())}` : "";
    return this.send("GET", `/api/console/instances/${encodeURIComponent(instanceID)}/dashboard${q}`);
  };

  pauseStation = (instanceID: string, stationID: string) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/stations/${enc(stationID)}/pause`,
      {}
    );

  unpauseStation = (instanceID: string, stationID: string) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/stations/${enc(stationID)}/unpause`,
      {}
    );

  hideStationFailures = (instanceID: string, stationID: string) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/stations/${enc(stationID)}/hide-failed`,
      {}
    );

  submitTask = (
    instanceID: string,
    stationID: string,
    body: { start: string; end: string; force?: boolean; client?: Record<string, unknown> }
  ) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/stations/${enc(stationID)}/submissions`,
      body
    );

  cancelRun = (instanceID: string, taskID: string, retryIndex: number) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/runs/${retryIndex}/cancel`,
      {}
    );

  hideRun = (instanceID: string, taskID: string, retryIndex: number, stationID: string) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/runs/${retryIndex}/hide?station_id=${enc(stationID)}`,
      {}
    );

  retryTask = (instanceID: string, taskID: string) =>
    this.send<Record<string, unknown>>(
      "POST",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/retry`,
      {}
    );

  getTask = (instanceID: string, taskID: string) =>
    this.send<Record<string, unknown>>(
      "GET",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}`
    );

  listTaskRuns = (instanceID: string, taskID: string) =>
    this.send<{ items: Record<string, unknown>[] }>(
      "GET",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/runs`
    );

  listTree = (instanceID: string, taskID: string, retryIndex: number, path: string) =>
    this.send<TreeResponse>(
      "GET",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/runs/${retryIndex}/tree?path=${enc(path)}`
    );

  previewFile = (
    instanceID: string,
    taskID: string,
    retryIndex: number,
    path: string,
    opts?: { mode?: "head" | "tail"; limit?: number }
  ) => {
    const params = new URLSearchParams({ path });
    if (opts?.mode) params.set("mode", opts.mode);
    if (opts?.limit) params.set("limit", String(opts.limit));
    return this.send<PreviewResponse>(
      "GET",
      `/api/console/instances/${enc(instanceID)}/tasks/${enc(taskID)}/runs/${retryIndex}/file?${params}`
    );
  };
}

function enc(s: string) {
  return encodeURIComponent(s);
}
