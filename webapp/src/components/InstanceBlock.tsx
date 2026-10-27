import { useEffect, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import { usePoll } from "../state/poll";
import { StationRow } from "./StationRow";
import type { InstanceInfo } from "../api/types";

interface Props {
  instance: InstanceInfo;
  refreshIntervalMs: number;
}

interface Preset {
  label: string;
  ms: number;
}

const PRESETS: Preset[] = [
  { label: "6h", ms: 6 * 3600 * 1000 },
  { label: "24h", ms: 24 * 3600 * 1000 },
  { label: "3d", ms: 3 * 86400 * 1000 },
  { label: "7d", ms: 7 * 86400 * 1000 },
];

const DEFAULT_PRESET_MS = 24 * 3600 * 1000;

// Convert a Date to the value string expected by <input type="datetime-local">.
function toLocalInput(d: Date): string {
  const off = d.getTimezoneOffset() * 60_000;
  return new Date(d.getTime() - off).toISOString().slice(0, 16);
}

interface SinceStore {
  presetMs: number | null;  // non-null → relative preset is active
  customTs: number | null;  // epoch ms, only used when presetMs is null
}

function sinceKey(instanceId: string) {
  return `veriproc.console.since.${instanceId}`;
}

function loadSince(instanceId: string): { presetMs: number | null; sinceDate: Date } {
  try {
    const raw = localStorage.getItem(sinceKey(instanceId));
    if (raw) {
      const s = JSON.parse(raw) as SinceStore;
      if (s.presetMs !== null && s.presetMs > 0) {
        return { presetMs: s.presetMs, sinceDate: new Date(Date.now() - s.presetMs) };
      }
      if (s.customTs !== null && !isNaN(s.customTs)) {
        return { presetMs: null, sinceDate: new Date(s.customTs) };
      }
    }
  } catch { /* ignore corrupt storage */ }
  return { presetMs: DEFAULT_PRESET_MS, sinceDate: new Date(Date.now() - DEFAULT_PRESET_MS) };
}

function saveSince(instanceId: string, presetMs: number | null, sinceDate: Date) {
  try {
    const s: SinceStore = {
      presetMs,
      customTs: presetMs === null ? sinceDate.getTime() : null,
    };
    localStorage.setItem(sinceKey(instanceId), JSON.stringify(s));
  } catch { /* ignore quota errors */ }
}

export function InstanceBlock({ instance, refreshIntervalMs }: Props) {
  const { client, session } = useAuth();

  // activePresetMs tracks which preset button should appear highlighted.
  // null means the user typed a custom datetime in the picker.
  const [activePresetMs, setActivePresetMs] = useState<number | null>(
    () => loadSince(instance.id).presetMs
  );
  const [sinceDate, setSinceDate] = useState<Date>(
    () => loadSince(instance.id).sinceDate
  );

  // Persist whenever the selection changes.
  useEffect(() => {
    saveSince(instance.id, activePresetMs, sinceDate);
  }, [instance.id, activePresetMs, sinceDate]);

  const { data, error, loading, refresh } = usePoll(
    () => client.dashboard(instance.id, sinceDate),
    refreshIntervalMs,
    // Re-fetch on login/logout: the relayed upstream credential changes with
    // the session, so an access-denied pane must be re-evaluated immediately.
    [instance.id, sinceDate, session?.token]
  );

  const onPreset = (ms: number) => {
    const d = new Date(Date.now() - ms);
    setActivePresetMs(ms);
    setSinceDate(d);
  };

  const onPickerChange = (value: string) => {
    const d = new Date(value);
    if (!isNaN(d.getTime())) {
      setActivePresetMs(null); // custom value — clear preset highlight
      setSinceDate(d);
    }
  };

  // The upstream refused the credential relayed on this hop: the pane shows a
  // permission notice instead of data. Distinct from "degraded" (instance
  // down / unreachable) so the operator is told what to do about it.
  const accessDenied = data?.status === "access_denied";
  const degraded = !accessDenied && (data?.status === "degraded" || !!error);
  const stations = data?.stations ?? [];
  return (
    <div class={`instance-block${degraded || accessDenied ? " degraded" : ""}`} data-instance={instance.id}>
      <div class="instance-header">
        <h3>{instance.title}</h3>
        <span class={degraded || accessDenied ? "health-bad" : "health-ok"}>
          {accessDenied ? "NO ACCESS" : degraded ? "DEGRADED" : "OK"}
        </span>
      </div>
      <div class="preset-row">
        <span>since</span>
        {PRESETS.map((p) => (
          <button
            key={p.label}
            class={p.ms === activePresetMs ? "active" : ""}
            onClick={() => onPreset(p.ms)}
          >
            {p.label}
          </button>
        ))}
        <input
          type="datetime-local"
          class="since-picker"
          value={toLocalInput(sinceDate)}
          max={toLocalInput(new Date())}
          onInput={(e) => onPickerChange((e.target as HTMLInputElement).value)}
          title="Set a custom since date/time"
        />
        {data?.last_refresh && (
          <span class="last-refresh">
            @ {new Date(data.last_refresh).toLocaleTimeString()}
          </span>
        )}
      </div>
      {accessDenied && (
        <div class="permission-msg" role="alert">
          <strong>No permission to view this instance.</strong>
          <p>
            {session?.anonymous
              ? "This instance requires a credential. Sign in with a reader or operator token to see its stations."
              : "This instance rejected the credential presented to it. Ask an operator to grant you access."}
          </p>
        </div>
      )}
      {error && <div class="degraded-msg">upstream unreachable: {error.message}</div>}
      {!accessDenied && data?.status === "degraded" && (
        <div class="degraded-msg">degraded: {data.error || "upstream unavailable"}</div>
      )}
      {loading && !data && !accessDenied && <div class="degraded-msg">loading…</div>}
      {!accessDenied && stations.map((row) => (
        <StationRow
          key={row.station_id}
          instanceID={instance.id}
          row={row}
          onChanged={refresh}
        />
      ))}
    </div>
  );
}
