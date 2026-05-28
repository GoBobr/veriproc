import { useAuth } from "../state/auth";
import { useFetch } from "../state/poll";
import { InstanceBlock } from "../components/InstanceBlock";

export function DashboardPage() {
  const { client } = useAuth();
  const { data, error, loading } = useFetch(() => client.instances(), []);
  return (
    <div>
      <Legend />
      {error && <div class="degraded-msg">failed to load instances: {error.message}</div>}
      {loading && !data && <div class="degraded-msg">loading instances…</div>}
      {data && (
        <div class="grid">
          {data.items.map((inst) => (
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

function Legend() {
  return (
    <div class="legend">
      <span><span class="swatch" style={{ background: "var(--slot-running)" }} />running</span>
      <span><span class="swatch" style={{ background: "var(--slot-queued)" }} />queued</span>
      <span><span class="swatch" style={{ background: "var(--slot-completed)" }} />completed</span>
      <span><span class="swatch" style={{ background: "var(--slot-failed)" }} />failed</span>
      <span><span class="swatch" style={{ background: "var(--slot-empty)" }} />empty</span>
    </div>
  );
}
