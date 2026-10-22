import { useEffect, useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import type { StationConfigResponse } from "../api/types";

interface Props {
  instanceID: string;
  stationID: string;
  onClose: () => void;
}

// StationConfigDialog shows the station's fully resolved configuration
// (env-expanded, definitions-resolved) in a read-only, scrollable YAML view
// (Station Config View requirement). The content is a startup snapshot; the
// freshness line carries content_hash and effective_at.
export function StationConfigDialog({ instanceID, stationID, onClose }: Props) {
  const { client } = useAuth();
  const [data, setData] = useState<StationConfigResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    client
      .stationConfig(instanceID, stationID)
      .then(setData)
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [client, instanceID, stationID]);

  return (
    <div class="dialog-backdrop" onClick={onClose}>
      <div class="dialog station-config-dialog" onClick={(e) => e.stopPropagation()}>
        <div class="info-dialog-header">
          <h3>Station Config — {stationID}</h3>
          <button class="icon-btn" onClick={onClose} title="Close">✕</button>
        </div>

        {error && <div class="err">{error}</div>}
        {!data && !error && <div class="info-loading">Loading…</div>}

        {data && (
          <>
            <div class="station-config-meta" data-testid="station-config-meta">
              revision <code>{data.content_hash}</code>
              {data.effective_at && (
                <> · effective {new Date(data.effective_at).toISOString()}</>
              )}
            </div>
            <pre class="station-config-yaml" data-testid="station-config-yaml" tabIndex={-1}>
              {data.config_yaml ?? JSON.stringify(data.config, null, 2)}
            </pre>
          </>
        )}

        <div class="actions">
          <button class="secondary" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}
