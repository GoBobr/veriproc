// Shared frontend types mirroring the console gateway JSON shapes.
export type Role = "viewer" | "operator";

// Identity is the response of GET /api/console/identity: the server-derived
// effective identity for the presented credential. The client never supplies
// a subject or role; it stores what the server reports.
//
// The endpoint resolves credentials strictly: a credential that matches no
// access-profile entry is rejected with 401, so a failed login is always
// reported rather than silently downgraded to a guest session.
export interface Identity {
  subject: string;
  role: Role;
  anonymous: boolean;
}

export interface InstanceInfo {
  id: string;
  title: string;
  base_url: string;
  working_root_base: string;
}

export interface InstancesResponse {
  items: InstanceInfo[];
  ui: {
    refresh_interval_ms: number;
    visible_slot_count: number;
    completed_visibility_ms: number;
    default_stats_since_ms: number;
    preview_max_bytes: number;
    card_min_width_px: number;
    card_max_width_px: number;
    station_runs_page_size: number;
    page_title: string;
  };
}

export type SlotKind = "empty" | "running" | "queued" | "completed" | "failed" | "cancelled";

export interface DashboardSlot {
  kind: SlotKind;
  run_id?: string;
  task_id?: string;
  retry_index: number;
  state?: string;
  terminal_at?: string;
}

export interface DashboardStationRow {
  station_id: string;
  station_name?: string;
  paused: boolean;
  running_count: number;
  queued_count: number;
  counts: { success: number; failure: number };
  slots: DashboardSlot[] | null;
  overflow: number;
  last_refresh?: string;
  /** Station IDs declared as downstream targets in the station definition. */
  downstream?: string[];
}

export interface DashboardInstance {
  instance_id: string;
  title: string;
  /**
   * ok — data present; degraded — upstream failed; access_denied — the
   * credential presented to the instance was rejected (rendered as a
   * permission notice).
   */
  status: "ok" | "degraded" | "access_denied";
  error?: string;
  since: string;
  /** Always an array in a well-formed response, but the server may send null. */
  stations: DashboardStationRow[] | null;
  last_refresh: string;
}

export interface InstanceHealthInfo {
  id: string;
  title: string;
  status: "up" | "down";
  version?: string;
  commit?: string;
  api_version?: string;
  instance_id?: string;
  error?: string;
}

export interface SystemInfo {
  console: {
    version: string;
    commit: string;
    build_date: string;
    api_version: string;
  };
  instances: InstanceHealthInfo[] | null;
}

export interface TreeEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mod_time: string;
  kind: string;
}

export interface TreeResponse {
  working_root: string;
  path: string;
  entries: TreeEntry[] | null;
}

export interface PreviewResponse {
  working_root: string;
  path: string;
  kind: string;
  size: number;
  offset: number;
  mode?: string;
  truncated: boolean;
  bytes_returned: number;
  content?: string;
  reason?: string;
}

// --- Station activity view types ---

export interface StationRun {
  run_id: string;
  run_ref: string;
  task_id: string;
  station_id?: string;
  state: string;
  internal_state?: string;
  canonicality?: string;
  retry_index: number;
  working_root?: string;
  failure_reason?: string;
  failure_summary?: string;
  created_at: string;
  terminal_at?: string;
  executor_type?: string;
  execution_node?: string;
  elapsed_time?: string;
}

export interface StationRunsResponse {
  items: StationRun[] | null;
  page_size: number;
  next_cursor?: string;
  ordering: string;
  filters: Record<string, string>;
}

// --- Station config view types ---

// StationConfigResponse mirrors the resolved station configuration served by
// GET /api/console/instances/{id}/stations/{sid}/config (proxied from the
// upstream config endpoint).
export interface StationConfigResponse {
  station_id: string;
  station_name?: string;
  content_hash: string;
  schema_version?: string;
  effective_at?: string;
  config?: Record<string, unknown>;
  config_yaml?: string;
}
