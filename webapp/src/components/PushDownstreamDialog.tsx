import { useEffect, useState } from "preact/hooks";
import { useAuth } from "../state/auth";

interface Props {
  instanceID: string;
  /** Task whose window is copied to the new submission. */
  taskID: string;
  /** The downstream station to submit to (determined from station config). */
  targetStationID: string;
  /** Pre-set the force flag (true for the "force" variant). */
  defaultForce?: boolean;
  onClose: () => void;
  onSubmit: (stationID: string, body: { start: string; end: string; force: boolean }) => Promise<void>;
}

// PushDownstreamDialog submits the same processing window to a known downstream
// station. The target station is determined from the source station's definition;
// the operator only confirms and optionally enables force.
export function PushDownstreamDialog({ instanceID, taskID, targetStationID, defaultForce, onClose, onSubmit }: Props) {
  const { client } = useAuth();
  const [fetchErr, setFetchErr] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [force, setForce] = useState(!!defaultForce);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Fetch the source task to get the window times.
  useEffect(() => {
    client.getTask(instanceID, taskID).then((task) => {
      const w = task["window"] as Record<string, string> | undefined;
      const s = String(w?.start ?? task["start"] ?? "");
      const e = String(w?.end ?? task["end"] ?? "");
      if (s) setStart(s);
      if (e) setEnd(e);
    }).catch((err: Error) => {
      setFetchErr(err.message);
    });
  }, [instanceID, taskID]);

  const submit = async (ev: Event) => {
    ev.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await onSubmit(targetStationID, { start, end, force });
      onClose();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="dialog-backdrop" role="dialog" aria-modal="true">
      <form class="dialog" onSubmit={submit}>
        <h3>Push downstream execution</h3>
        <p class="dialog-hint">
          Submit <code>{taskID}</code> window to <strong>{targetStationID}</strong>.
        </p>

        {fetchErr && <div class="err">Failed to load source task: {fetchErr}</div>}

        <label>Window start</label>
        <input type="text" value={start || "loading…"} readOnly class="readonly" />

        <label>Window end</label>
        <input type="text" value={end || "loading…"} readOnly class="readonly" />

        <label>
          <input
            type="checkbox"
            checked={force}
            onChange={(e) => setForce((e.target as HTMLInputElement).checked)}
          />{" "}
          force (ignore idempotency)
        </label>

        {err && <div class="err">{err}</div>}
        <div class="actions">
          <button type="button" class="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" class="primary" disabled={busy || !start}>
            {busy ? "Submitting…" : "Submit"}
          </button>
        </div>
      </form>
    </div>
  );
}
