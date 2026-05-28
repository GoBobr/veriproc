import { createContext } from "preact";
import { useContext, useEffect, useState } from "preact/hooks";
import { ConsoleClient } from "../api/client";
import type { Role } from "../api/types";

interface Session {
  token: string;
  subject: string;
  role: Role;
}

interface AuthCtx {
  session: Session | null;
  client: ConsoleClient;
  login: (token: string, subject: string, role: Role) => void;
  logout: () => void;
}

const STORAGE_KEY = "veriproc.console.session";

const ctx = createContext<AuthCtx>({
  session: null,
  client: new ConsoleClient(""),
  login: () => {},
  logout: () => {},
});

// useAuth returns the current session, a token-bound client, and login/logout.
// Sessions are persisted in localStorage so reloads stay logged in.
export function useAuth() {
  return useContext(ctx);
}

interface ProviderProps {
  children?: preact.ComponentChildren;
  // Tokens declared in the gateway can carry an explicit role; we mirror the
  // subject/role on the client side from the login dialog. The role is also
  // re-verified by every mutating endpoint server-side.
  initial?: Session | null;
}

export function AuthProvider({ children, initial }: ProviderProps) {
  const [session, setSession] = useState<Session | null>(() => {
    if (initial !== undefined) return initial;
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      return raw ? (JSON.parse(raw) as Session) : null;
    } catch {
      return null;
    }
  });

  useEffect(() => {
    if (session) {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  }, [session]);

  const value: AuthCtx = {
    session,
    client: new ConsoleClient(session?.token || ""),
    login: (token, subject, role) => setSession({ token, subject, role }),
    logout: () => setSession(null),
  };

  return <ctx.Provider value={value}>{children}</ctx.Provider>;
}
