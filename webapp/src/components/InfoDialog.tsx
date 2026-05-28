import { useEffect, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import type { SystemInfo } from "../api/types";

interface Props {
  onClose: () => void;
}

// Vite injects the version and git commit at build time (set by Makefile).
// Falls back to "dev" / "unknown" in vitest / un-built environments.
const WEBAPP_VERSION: string =
  typeof import.meta.env !== "undefined" && import.meta.env.VITE_APP_VERSION
    ? (import.meta.env.VITE_APP_VERSION as string)
    : "dev";

const WEBAPP_COMMIT: string =
  typeof import.meta.env !== "undefined" && import.meta.env.VITE_APP_COMMIT
    ? (import.meta.env.VITE_APP_COMMIT as string)
    : "unknown";

export function InfoDialog({ onClose }: Props) {
  const { client } = useAuth();
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    client
      .systemInfo()
      .then(setInfo)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  return (
    <div class="dialog-backdrop" onClick={onClose}>
      <div class="dialog info-dialog" onClick={(e) => e.stopPropagation()}>
        <div class="info-dialog-header">
          <h3>System Info</h3>
          <button class="icon-btn" onClick={onClose} title="Close">✕</button>
        </div>

        {error && <div class="err">{error}</div>}
        {!info && !error && <div class="info-loading">Loading…</div>}

        {info && (
          <>
            <table class="info-table">
              <tbody>
                <tr>
                  <th colSpan={2} class="info-section-head">Console Gateway</th>
                </tr>
                <tr>
                  <td>Version</td>
                  <td><code>{info.console.version} ({info.console.commit})</code></td>
                </tr>
                <tr>
                  <td>Protocol</td>
                  <td><code>{info.console.api_version}</code></td>
                </tr>
                <tr>
                  <th colSpan={2} class="info-section-head">Web App</th>
                </tr>
                <tr>
                  <td>Version</td>
                  <td><code>{WEBAPP_VERSION} ({WEBAPP_COMMIT})</code></td>
                </tr>
                <tr>
                  <th colSpan={2} class="info-section-head">Upstream Instances</th>
                </tr>
                {info.instances.map((inst) => (
                  <>
                    <tr key={inst.id + "-header"}>
                      <td colSpan={2} class="info-instance-name">
                        <span
                          class={`status-dot ${inst.status === "up" ? "dot-up" : "dot-down"}`}
                          title={inst.status}
                        />
                        {inst.title}
                        {inst.instance_id && inst.instance_id !== inst.id && (
                          <span class="muted"> ({inst.instance_id})</span>
                        )}
                      </td>
                    </tr>
                    {inst.status === "up" ? (
                      <>
                        <tr key={inst.id + "-ver"}>
                          <td class="indent">Version</td>
                          <td><code>{inst.version || "—"}{inst.commit ? ` (${inst.commit})` : ""}</code></td>
                        </tr>
                        <tr key={inst.id + "-api"}>
                          <td class="indent">Protocol</td>
                          <td><code>{inst.api_version || "—"}</code></td>
                        </tr>
                      </>
                    ) : (
                      <tr key={inst.id + "-err"}>
                        <td class="indent">Error</td>
                        <td class="err-cell">{inst.error}</td>
                      </tr>
                    )}
                  </>
                ))}
              </tbody>
            </table>
          </>
        )}

        <div class="actions">
          <button class="secondary" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}
