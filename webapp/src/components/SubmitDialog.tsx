import { useState } from "preact/hooks";

interface Props {
  stationID: string;
  defaultStart?: Date;
  defaultEnd?: Date;
  onSubmit: (body: { start: string; end: string; force: boolean }) => Promise<void>;
  onClose: () => void;
}

// SubmitDialog mirrors the operator submission flow described in §8.4.3.
export function SubmitDialog({ stationID, defaultStart, defaultEnd, onSubmit, onClose }: Props) {
  const [start, setStart] = useState(toLocal(defaultStart ?? new Date(Date.now() - 3600 * 1000)));
  const [end, setEnd] = useState(toLocal(defaultEnd ?? new Date()));
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      const startIso = new Date(start).toISOString();
      const endIso = new Date(end).toISOString();
      if (new Date(endIso) <= new Date(startIso)) {
        throw new Error("end must be after start");
      }
      await onSubmit({ start: startIso, end: endIso, force });
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
        <h3>Submit task — {stationID}</h3>
        <label for="sub-start">Start (local)</label>
        <input
          id="sub-start"
          type="datetime-local"
          value={start}
          onInput={(e) => setStart((e.target as HTMLInputElement).value)}
          required
        />
        <label for="sub-end">End (local)</label>
        <input
          id="sub-end"
          type="datetime-local"
          value={end}
          onInput={(e) => setEnd((e.target as HTMLInputElement).value)}
          required
        />
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
          <button type="submit" class="primary" disabled={busy}>
            {busy ? "Submitting…" : "Submit"}
          </button>
        </div>
      </form>
    </div>
  );
}

function toLocal(d: Date): string {
  const off = d.getTimezoneOffset() * 60_000;
  return new Date(d.getTime() - off).toISOString().slice(0, 16);
}
