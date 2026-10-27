import { useState } from "preact/hooks";
import { useAuth } from "../state/auth";

// Login collects the bearer token only. The subject and role are never
// supplied by the user: the gateway resolves them from the credential and
// the session adopts whatever the server reports.
//
// It doubles as the in-place upgrade control: when the console opened into an
// anonymous session, submitting a credential upgrades that session without a
// page reload.
//
// The gateway resolves login credentials strictly: a token that matches no
// access-profile entry is rejected with 401 rather than downgraded to a guest.
// The rejection is therefore always visible - the message is shown inside the
// dialog and handed to onRejected so the console can also surface it outside
// the dialog, and the dialog can be dismissed so the operator keeps browsing in
// whatever session state they already had.
//
// onCancel renders the dismiss control. It is optional: when the gateway
// requires a credential (no guest browsing at all) the parent decides whether
// dismissing leaves a "sign in required" placeholder or is not offered.
export function Login(props: {
  onCancel?: () => void;
  onRejected?: (message: string) => void;
}) {
  const { login } = useAuth();
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: Event) => {
    e.preventDefault();
    const value = token.trim();
    if (!value) return;
    setBusy(true);
    setError(null);
    try {
      await login(value);
      setToken("");
    } catch (err) {
      // Rejected credential: report it and keep the current session state
      // (anonymous or previously authenticated) untouched. The user is never
      // silently turned into a guest here - the gateway already refused the
      // credential, and this makes that refusal obvious in the UI.
      const message = String((err as Error)?.message ?? err);
      setError(message);
      props.onRejected?.(message);
    } finally {
      setBusy(false);
    }
  };

  const cancel = () => {
    setError(null);
    setToken("");
    props.onCancel?.();
  };

  return (
    <div class="dialog-backdrop" style={{ position: "static" }}>
      <form class="dialog" onSubmit={submit}>
        <h3>Sign in to Veriproc Console</h3>
        <label for="login-token">API token</label>
        <input
          id="login-token"
          type="password"
          autocomplete="off"
          value={token}
          onInput={(e) => setToken((e.target as HTMLInputElement).value)}
          required
        />
        {error && (
          <div class="error-msg" role="alert">
            <strong>Sign-in failed.</strong> {error}
          </div>
        )}
        <div class="actions">
          <button type="submit" class="primary" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </button>
          {props.onCancel && (
            <button type="button" class="secondary" onClick={cancel}>
              Close
            </button>
          )}
        </div>
      </form>
    </div>
  );
}
