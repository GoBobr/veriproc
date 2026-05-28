import { useState } from "preact/hooks";
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

export function InstanceBlock({ instance, refreshIntervalMs }: Props) {
  const { client } = useAuth();
  const [presetMs, setPresetMs] = useState(24 * 3600 * 1000);
  const since = new Date(Date.now() - presetMs);
  const { data, error, loading, refresh } = usePoll(
    () => client.dashboard(instance.id, since),
    refreshIntervalMs,
    [instance.id, presetMs]
  );
  const degraded = data?.status === "degraded" || !!error;
  return (
    <div class={`instance-block${degraded ? " degraded" : ""}`} data-instance={instance.id}>
      <div class="instance-header">
        <h3>{instance.title}</h3>
        <span class={degraded ? "health-bad" : "health-ok"}>
          {degraded ? "DEGRADED" : "OK"}
        </span>
      </div>
      <div class="preset-row">
        <span>since</span>
        {PRESETS.map((p) => (
          <button
            key={p.label}
            class={p.ms === presetMs ? "active" : ""}
            onClick={() => setPresetMs(p.ms)}
          >
            {p.label}
          </button>
        ))}
        {data?.last_refresh && (
          <span style={{ marginLeft: "auto" }}>
            @ {new Date(data.last_refresh).toLocaleTimeString()}
          </span>
        )}
      </div>
      {error && <div class="degraded-msg">upstream unreachable: {error.message}</div>}
      {data?.status === "degraded" && (
        <div class="degraded-msg">degraded: {data.error || "upstream unavailable"}</div>
      )}
      {loading && !data && <div class="degraded-msg">loading…</div>}
      {data?.stations.map((row) => (
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
