import { useState } from "preact/hooks";
import { useAuth } from "../state/auth";
import type { Role } from "../api/types";

// Login collects the bearer token, declared subject, and the role the user
// expects (only used to hide irrelevant menus client-side; the server is
// authoritative).
export function Login() {
  const { login } = useAuth();
  const [token, setToken] = useState("");
  const [subject, setSubject] = useState("");
  const [role, setRole] = useState<Role>("viewer");

  return (
    <div class="dialog-backdrop" style={{ position: "static" }}>
      <form
        class="dialog"
        onSubmit={(e) => {
          e.preventDefault();
          if (!token || !subject) return;
          login(token.trim(), subject.trim(), role);
        }}
      >
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
        <label for="login-subject">Subject (your name / id)</label>
        <input
          id="login-subject"
          type="text"
          value={subject}
          onInput={(e) => setSubject((e.target as HTMLInputElement).value)}
          required
        />
        <label for="login-role">Role</label>
        <select
          id="login-role"
          value={role}
          onChange={(e) => setRole((e.target as HTMLSelectElement).value as Role)}
        >
          <option value="viewer">viewer</option>
          <option value="operator">operator</option>
        </select>
        <div class="actions">
          <button type="submit" class="primary">
            Sign in
          </button>
        </div>
      </form>
    </div>
  );
}
