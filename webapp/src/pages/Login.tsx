import { useState } from "preact/hooks";
import { useAuth } from "../state/auth";

// Login collects the bearer token only. The subject and role are never
// supplied by the user: the gateway resolves them from the credential and
// the session adopts whatever the server reports.
//
// It doubles as the in-place upgrade control: when the console opened into an
// anonymous session, submitting a credential upgrades that session without a
// page reload.
export function Login() {
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
      // (anonymous or previously authenticated) untouched.
      setError(String((err as Error)?.message ?? err));
    } finally {
      setBusy(false);
    }
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
        {error && <div class="error-msg" role="alert">{error}</div>}
        <div class="actions">
          <button type="submit" class="primary" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </button>
        </div>
      </form>
    </div>
  );
}
