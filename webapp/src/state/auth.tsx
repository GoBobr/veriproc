import { createContext } from "preact";
import { useContext, useEffect, useState } from "preact/hooks";
import { ConsoleClient } from "../api/client";
import type { Identity, Role } from "../api/types";

// Session is the client-side view of the caller's identity. subject and role
// are always derived from the server's identity response - never chosen by
// the user - and `anonymous` marks a read-only session admitted without a
// credential under the gateway's anonymous-browsing mode.
//
// credentialRejected marks a guest session that exists because a *stored*
// credential was refused, as opposed to one that arrived without a credential
// at all. The session stays read-only either way. It is never set on an
// explicit sign-in: the gateway rejects an unmatched credential outright, so
// the sign-in fails visibly and the previous session is kept.
interface Session {
  token: string;
  subject: string;
  role: Role;
  anonymous?: boolean;
  credentialRejected?: boolean;
}

interface AuthCtx {
  session: Session | null;
  client: ConsoleClient;
  // ready is false until the startup identity probe completes, so the app can
  // avoid flashing the login screen before it learns whether anonymous
  // browsing is enabled.
  ready: boolean;
  // login resolves the credential against the server identity endpoint and
  // adopts the identity the server reports. It throws when the credential is
  // rejected, leaving the previous session untouched.
  login: (token: string) => Promise<void>;
  logout: () => void;
}

const STORAGE_KEY = "veriproc.console.session";

const ctx = createContext<AuthCtx>({
  session: null,
  client: new ConsoleClient(""),
  ready: false,
  login: async () => {},
  logout: () => {},
});

// useAuth returns the current session, a token-bound client, and login/logout.
// Sessions are persisted in localStorage so reloads stay logged in.
export function useAuth() {
  return useContext(ctx);
}

interface ProviderProps {
  children?: preact.ComponentChildren;
  // initial overrides the persisted session (used by tests).
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
  // Tests that inject an initial session skip the startup probe.
  const [ready, setReady] = useState(initial !== undefined);

  useEffect(() => {
    if (ready) return;
    let cancelled = false;
    bootstrapSession(new ConsoleClient(""), session).then((s) => {
      if (cancelled) return;
      setSession(s);
      setReady(true);
    });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready]);

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
    ready,
    login: async (token: string) => {
      // Resolve the identity server-side: only the credential is sent, and
      // the role/subject come back from the gateway. The gateway resolves
      // login credentials strictly, so a credential that matches no profile
      // entry fails here with the server's message and the session is left
      // untouched - the user sees the error instead of being silently
      // downgraded to a guest.
      const identity = await new ConsoleClient(token).identity(token);
      setSession({
        token,
        subject: identity.subject,
        role: identity.role,
        anonymous: identity.anonymous,
      });
    },
    logout: () => {
      // Clear immediately, then re-probe: with anonymous browsing enabled the
      // gateway admits the credential-less identity again, returning the
      // session to its anonymous state.
      setSession(null);
      bootstrapSession(new ConsoleClient(""), null).then((s) => {
        if (s) setSession(s);
      });
    },
  };

  return <ctx.Provider value={value}>{children}</ctx.Provider>;
}

// credentialRejectedMessage is shown when the gateway refuses a credential the
// user explicitly submitted.
export const credentialRejectedMessage =
  "credential rejected: no access-profile entry matches this token";

// bootstrapSession probes the gateway for the caller's identity at startup.
// It returns:
//   - an anonymous session when the gateway admits credential-less reads
//     (anonymous browsing enabled),
//   - null when no credential is stored and the gateway requires one,
//   - a restored session for a stored credential the gateway still accepts,
//   - the stored session when the probe fails for a non-auth reason, so a
//     transient outage does not log the operator out.
//
// A stored credential the gateway rejects (401) is resolved by retrying
// without it: the gateway then either admits an anonymous session - the
// dashboard stays usable, flagged via credentialRejected so the UI can say
// the stored credential was refused - or rejects the credential-less probe
// too, in which case the session is dropped and the login dialog is shown.
export async function bootstrapSession(
  client: ConsoleClient,
  stored: Session | null
): Promise<Session | null> {
  try {
    const identity = await client.identity(stored?.token);
    if (identity.anonymous) {
      return { token: "", subject: identity.subject, role: identity.role, anonymous: true };
    }
    return {
      token: stored?.token ?? "",
      subject: identity.subject,
      role: identity.role,
      anonymous: false,
    };
  } catch (err) {
    const message = String((err as Error)?.message ?? err);
    if (stored && /unauthenticated|missing|invalid|401|credential rejected/i.test(message)) {
      // The stored credential was refused. Retry without it: if the gateway
      // admits guests, keep browsing as a guest and tell the user their
      // credential was rejected; otherwise drop the session entirely.
      try {
        const anonymous = await client.identity("");
        if (anonymous.anonymous) {
          return {
            token: "",
            subject: anonymous.subject,
            role: anonymous.role,
            anonymous: true,
            credentialRejected: true,
          };
        }
      } catch {
        // The gateway requires a credential: fall through to the login screen.
      }
      return null;
    }
    return stored;
  }
}
