import { useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import { useFetch } from "../state/poll";
import { InstanceBlock } from "../components/InstanceBlock";
import type { InstanceInfo } from "../api/types";

const VISIBILITY_KEY = "veriproc.console.instanceVisibility";

/** Load the saved visibility map from sessionStorage. */
function loadVisibility(): Record<string, boolean> {
  try {
    const raw = sessionStorage.getItem(VISIBILITY_KEY);
    if (raw) return JSON.parse(raw) as Record<string, boolean>;
  } catch { /* ignore corrupt storage */ }
  return {};
}

/** Persist the visibility map to sessionStorage. */
function saveVisibility(map: Record<string, boolean>) {
  try {
    sessionStorage.setItem(VISIBILITY_KEY, JSON.stringify(map));
  } catch { /* ignore quota errors */ }
}

export function DashboardPage() {
  const { client } = useAuth();
  const { data, error, loading } = useFetch(() => client.instances(), []);
  const [hidden, setHidden] = useState<Record<string, boolean>>(loadVisibility);

  const toggleInstance = (id: string) => {
    setHidden((prev) => {
      const next = { ...prev, [id]: !prev[id] };
      saveVisibility(next);
      return next;
    });
  };

  const instances = data?.items ?? [];
  const visibleInstances = instances.filter((inst) => !hidden[inst.id]);

  return (
    <div>
      <Legend
        instances={instances}
        hidden={hidden}
        onToggle={toggleInstance}
      />
      {error && <div class="degraded-msg">failed to load instances: {error.message}</div>}
      {loading && !data && <div class="degraded-msg">loading instances…</div>}
      {data && (
        <div
          class="grid"
          style={{
            "--card-min-width": `${data.ui.card_min_width_px}px`,
            "--card-max-width": `${data.ui.card_max_width_px}px`,
          }}
        >
          {visibleInstances.map((inst) => (
            <InstanceBlock
              key={inst.id}
              instance={inst}
              refreshIntervalMs={data.ui.refresh_interval_ms}
            />
          ))}
        </div>
      )}
    </div>
  );
}

interface LegendProps {
  instances: InstanceInfo[];
  hidden: Record<string, boolean>;
  onToggle: (id: string) => void;
}

function Legend({ instances, hidden, onToggle }: LegendProps) {
  return (
    <div class="legend">
      <span><span class="swatch" style={{ background: "var(--slot-running)" }} />running</span>
      <span><span class="swatch" style={{ background: "var(--slot-queued)" }} />queued</span>
      <span><span class="swatch" style={{ background: "var(--slot-completed)" }} />completed</span>
      <span><span class="swatch" style={{ background: "var(--slot-failed)" }} />failed</span>
      <span><span class="swatch" style={{ background: "var(--slot-empty)" }} />empty</span>
      {instances.length > 0 && <span class="legend-separator" />}
      {instances.map((inst) => (
        <label class="instance-toggle" key={inst.id} title={`Show/hide ${inst.title}`}>
          <input
            type="checkbox"
            checked={!hidden[inst.id]}
            onChange={() => onToggle(inst.id)}
          />
          {inst.title}
        </label>
      ))}
    </div>
  );
}
