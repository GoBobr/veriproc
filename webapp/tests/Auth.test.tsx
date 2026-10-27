import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/preact";
import { AuthProvider } from "../src/state/auth";
import { App } from "../src/App";
import { Login } from "../src/pages/Login";
import type { Identity } from "../src/api/types";

// ── fetch plumbing ────────────────────────────────────────────────────────

interface Routes {
  // identityFor returns the identity for the presented Authorization header
  // (undefined = no credential).
  identityFor: (auth?: string) => { status: number; body: unknown };
  // instancesFor returns the instance list for the presented credential.
  instancesFor: (auth?: string) => unknown;
}

const UI = {
  refresh_interval_ms: 0,
  visible_slot_count: 12,
  completed_visibility_ms: 30000,
  default_stats_since_ms: 3600000,
  preview_max_bytes: 1024,
  card_min_width_px: 300,
  card_max_width_px: 600,
  station_runs_page_size: 50,
};

const VP1 = { id: "vp1", title: "VP One", base_url: "http://a", working_root_base: "/tmp" };
const VP2 = { id: "vp2", title: "VP Two", base_url: "http://b", working_root_base: "/tmp" };

const DASHBOARD = {
  instance_id: "vp1",
  title: "VP One",
  status: "ok",
  since: "2026-05-28T12:00:00Z",
  stations: [],
  last_refresh: "2026-05-28T12:00:00Z",
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetch(routes: Routes) {
  const mock = vi.fn(async (input: RequestInfo, init?: RequestInit) => {
    const url = String(input);
    const auth = new Headers(init?.headers).get("Authorization") ?? undefined;
    if (url.includes("/api/console/identity")) {
      const r = routes.identityFor(auth);
      return json(r.body, r.status);
    }
    if (url.includes("/dashboard")) {
      return json(DASHBOARD);
    }
    if (url.includes("/api/console/instances")) {
      return json(routes.instancesFor(auth));
    }
    return json({});
  });
  globalThis.fetch = mock as unknown as typeof fetch;
  return mock;
}

const anonymousIdentity: Identity = { subject: "anonymous", role: "viewer", anonymous: true };
const operatorIdentity: Identity = { subject: "alice", role: "operator", anonymous: false };
const viewerIdentity: Identity = { subject: "bob", role: "viewer", anonymous: false };

function identityFor(map: Record<string, { status: number; body: unknown }>) {
  return (auth?: string) =>
    map[auth ?? ""] ?? { status: 401, body: { error: { code: "unauthenticated", message: "missing or invalid bearer token" } } };
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

// ── 7.1 Login Sends Only Credential ──────────────────────────────────────

describe("Login dialog", () => {
  it("has no role or subject field and submits only the credential", async () => {
    const fetchMock = installFetch({
      identityFor: identityFor({ "Bearer good-token": { status: 200, body: operatorIdentity } }),
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider initial={null}>
        <Login />
      </AuthProvider>
    );

    // No role or subject inputs exist.
    expect(screen.queryByLabelText(/role/i)).toBeNull();
    expect(screen.queryByLabelText(/subject/i)).toBeNull();
    expect(document.querySelector("select")).toBeNull();

    // Submitting sends only the credential: a GET to the identity endpoint
    // with an Authorization header and no body.
    const input = screen.getByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "good-token" } });
    fireEvent.submit(input.closest("form")!);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain("/api/console/identity");
    expect(init?.body).toBeUndefined();
    const headers = new Headers(init?.headers);
    expect(headers.get("Authorization")).toBe("Bearer good-token");
    expect(headers.get("Content-Type")).toBeNull();
  });
});

// ── 7.2 Role Derived From Server / rejected credential ───────────────────

describe("server-derived identity", () => {
  it("adopts the role reported by the server, not one chosen by the user", async () => {
    installFetch({
      identityFor: identityFor({ "Bearer good-token": { status: 200, body: operatorIdentity } }),
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider initial={null}>
        <App />
      </AuthProvider>
    );
    await screen.findByText(/Sign in to Veriproc Console/);

    const input = screen.getByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "good-token" } });
    fireEvent.submit(input.closest("form")!);

    // The header reflects the server-reported subject and role.
    await screen.findAllByText("alice");
    expect(screen.getAllByText("operator").length).toBeGreaterThan(0);
  });

  it("reports a rejected credential and preserves the previous session", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : auth === "Bearer good-token"
            ? { status: 200, body: operatorIdentity }
            : { status: 401, body: { error: { code: "unauthenticated", message: "credential rejected" } } },
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );
    // Opens anonymously (anonymous browsing enabled).
    await screen.findAllByText("anonymous");

    // Open the upgrade dialog and submit a bad credential.
    fireEvent.click(screen.getByText("Log in"));
    const input = await screen.findByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "bad-token" } });
    fireEvent.submit(input.closest("form")!);

    // The rejection is surfaced and the session stays anonymous.
    await screen.findByText(/credential rejected/);
    expect(screen.getByText("anonymous")).toBeInTheDocument();
  });

  it("never turns a rejected sign-in into a guest session", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 401, body: { error: { code: "unauthenticated", message: "no access-profile entry matches this credential" } } },
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );
    await screen.findAllByText("anonymous");

    fireEvent.click(screen.getByText("Log in"));
    const input = await screen.findByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "bad-token" } });
    fireEvent.submit(input.closest("form")!);

    // The failure is visible, and no authenticated identity was adopted.
    await screen.findByText(/no access-profile entry matches/);
    expect(screen.getByText("anonymous")).toBeInTheDocument();
    expect(screen.queryByText("Log in")).toBeInTheDocument();
  });

  it("lets the operator close the sign-in dialog after a rejection", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 401, body: { error: { code: "unauthenticated", message: "no access-profile entry matches this credential" } } },
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );
    await screen.findAllByText("anonymous");

    fireEvent.click(screen.getByText("Log in"));
    const input = await screen.findByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "bad-token" } });
    fireEvent.submit(input.closest("form")!);
    await screen.findByText(/no access-profile entry matches/);

    // The dialog can be dismissed, and the console stays usable.
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByText(/Sign in to Veriproc Console/)).toBeNull()
    );
    expect(screen.getByText("anonymous")).toBeInTheDocument();
  });

  it("keeps the rejection visible after the dialog is closed", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 401, body: { error: { code: "unauthenticated", message: "no access-profile entry matches this credential" } } },
      instancesFor: () => ({ items: [], ui: UI }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );
    await screen.findAllByText("anonymous");

    fireEvent.click(screen.getByText("Log in"));
    const input = await screen.findByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "bad-token" } });
    fireEvent.submit(input.closest("form")!);
    await screen.findByText(/no access-profile entry matches/);

    fireEvent.click(screen.getByRole("button", { name: "Close" }));

    // The error stays on screen as a banner so it is not lost with the
    // dialog, and it can be dismissed explicitly.
    await screen.findByText(/no access-profile entry matches/);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() =>
      expect(screen.queryByText(/no access-profile entry matches/)).toBeNull()
    );
  });
});

// ── 7.3 Anonymous session lifecycle ──────────────────────────────────────

describe("anonymous console session", () => {
  it("opens without login when anonymous browsing is enabled", async () => {
    installFetch({
      identityFor: identityFor({ "": { status: 200, body: anonymousIdentity } }),
      instancesFor: () => ({ items: [VP1], ui: UI }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );

    // Dashboard content is present without a login screen.
    await screen.findAllByText("VP One");
    expect(screen.queryByText(/Sign in to Veriproc Console/)).toBeNull();
    expect(screen.getByText("anonymous")).toBeInTheDocument();
  });

  it("upgrades the session in place on login", async () => {
    installFetch({
      identityFor: identityFor({
        "": { status: 200, body: anonymousIdentity },
        "Bearer op-token": { status: 200, body: operatorIdentity },
      }),
      instancesFor: (auth) =>
        auth ? { items: [VP1, VP2], ui: UI } : { items: [VP1], ui: UI },
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );
    await screen.findAllByText("VP One");
    expect(screen.queryAllByText("VP Two")).toHaveLength(0);

    // Log in: the session upgrades without a reload and the stronger
    // instance set appears.
    fireEvent.click(screen.getByText("Log in"));
    const input = await screen.findByLabelText(/API token/i) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "op-token" } });
    fireEvent.submit(input.closest("form")!);

    await screen.findAllByText("alice");
    expect(screen.getAllByText("operator").length).toBeGreaterThan(0);
    await screen.findAllByText("VP Two");
  });

  it("returns to the anonymous state on logout", async () => {
    installFetch({
      identityFor: identityFor({
        "Bearer op-token": { status: 200, body: operatorIdentity },
        "": { status: 200, body: anonymousIdentity },
      }),
      instancesFor: (auth) =>
        auth ? { items: [VP1, VP2], ui: UI } : { items: [VP1], ui: UI },
    });

    render(
      <AuthProvider initial={{ token: "op-token", subject: "alice", role: "operator" }}>
        <App />
      </AuthProvider>
    );
    await screen.findAllByText("alice");

    fireEvent.click(screen.getByText("Sign out"));

    // Back to anonymous: operator controls are gone.
    await screen.findAllByText("anonymous");
    expect(screen.queryAllByText("alice")).toHaveLength(0);
    expect(screen.queryAllByText("operator")).toHaveLength(0);
    expect(screen.queryByText("Sign out")).toBeNull();
  });
});

// ── 7.4 Instance visibility per session ──────────────────────────────────

describe("instance visibility", () => {
  const cases: Array<{
    name: string;
    stored: { token: string; subject: string; role: "viewer" | "operator" } | null;
    identity: Identity;
    auth?: string;
    wantVisible: boolean;
  }> = [
    {
      name: "anonymous sees only reachable instances",
      stored: null,
      identity: anonymousIdentity,
      auth: undefined,
      wantVisible: false,
    },
    {
      name: "viewer sees only reachable instances",
      stored: { token: "viewer-token", subject: "bob", role: "viewer" },
      identity: viewerIdentity,
      auth: "Bearer viewer-token",
      wantVisible: false,
    },
    {
      name: "operator sees all reachable instances",
      stored: { token: "operator-token", subject: "alice", role: "operator" },
      identity: operatorIdentity,
      auth: "Bearer operator-token",
      wantVisible: true,
    },
  ];

  for (const c of cases) {
    it(c.name, async () => {
      installFetch({
        identityFor: identityFor({
          [c.auth ?? ""]: { status: 200, body: c.identity },
        }),
        // The gateway hides vp2 from sessions that cannot reach it.
        instancesFor: (auth) =>
          auth === "Bearer operator-token"
            ? { items: [VP1, VP2], ui: UI }
            : { items: [VP1], ui: UI },
      });

      render(
        <AuthProvider initial={c.stored ?? undefined}>
          <App />
        </AuthProvider>
      );

      await screen.findAllByText("VP One");
      if (c.wantVisible) {
        expect(screen.getAllByText("VP Two").length).toBeGreaterThan(0);
      } else {
        expect(screen.queryAllByText("VP Two")).toHaveLength(0);
      }
    });
  }
});

// ── 7.5 No user management in the UI ─────────────────────────────────────

describe("no user management surface", () => {
  const states: Array<{
    name: string;
    stored: { token: string; subject: string; role: "viewer" | "operator" } | null;
    identity: Identity;
    auth?: string;
  }> = [
    { name: "anonymous", stored: null, identity: anonymousIdentity },
    {
      name: "viewer",
      stored: { token: "viewer-token", subject: "bob", role: "viewer" },
      identity: viewerIdentity,
      auth: "Bearer viewer-token",
    },
    {
      name: "operator",
      stored: { token: "operator-token", subject: "alice", role: "operator" },
      identity: operatorIdentity,
      auth: "Bearer operator-token",
    },
  ];

  for (const s of states) {
    it(`offers no user management in the ${s.name} session`, async () => {
      installFetch({
        identityFor: identityFor({ [s.auth ?? ""]: { status: 200, body: s.identity } }),
        instancesFor: () => ({ items: [VP1], ui: UI }),
      });

      const { container } = render(
        <AuthProvider initial={s.stored ?? undefined}>
          <App />
        </AuthProvider>
      );
      await screen.findAllByText("VP One");

      // No control or route for listing, adding, or revoking users.
      expect(screen.queryAllByText(/users/i)).toHaveLength(0);
      expect(screen.queryAllByText(/add user/i)).toHaveLength(0);
      expect(screen.queryAllByText(/revoke/i)).toHaveLength(0);
      expect(container.querySelector('[href*="users"]')).toBeNull();
      expect(container.querySelector('[href*="tokens"]')).toBeNull();
    });
  }
});