// Shared frontend types mirroring the console gateway JSON shapes.
export type Role = "viewer" | "operator";

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
  slots: DashboardSlot[];
  overflow: number;
  last_refresh?: string;
  /** Station IDs declared as downstream targets in the station definition. */
  downstream?: string[];
}

export interface DashboardInstance {
  instance_id: string;
  title: string;
  status: "ok" | "degraded";
  error?: string;
  since: string;
  stations: DashboardStationRow[];
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
  instances: InstanceHealthInfo[];
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
  entries: TreeEntry[];
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
