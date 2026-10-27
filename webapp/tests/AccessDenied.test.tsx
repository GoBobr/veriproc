import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/preact";
import { AuthProvider } from "../src/state/auth";
import { App } from "../src/App";
import type { Identity } from "../src/api/types";

// ── fetch plumbing ────────────────────────────────────────────────────────

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

const anonymousIdentity: Identity = { subject: "anonymous", role: "viewer", anonymous: true };
const operatorIdentity: Identity = { subject: "alice", role: "operator", anonymous: false };

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

interface Routes {
  identityFor: (auth?: string) => { status: number; body: unknown };
  instancesFor: (auth?: string) => unknown;
  dashboardFor: (auth?: string) => unknown;
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
      return json(routes.dashboardFor(auth));
    }
    if (url.includes("/api/console/instances")) {
      return json(routes.instancesFor(auth));
    }
    return json({});
  });
  globalThis.fetch = mock as unknown as typeof fetch;
  return mock;
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

// ── 10.4 Permission notice rendered ──────────────────────────────────────

describe("access-denied instance pane", () => {
  it("shows a permission notice instead of crashing when the dashboard is access_denied", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 200, body: operatorIdentity },
      instancesFor: () => ({ items: [VP1], ui: UI }),
      dashboardFor: () => ({
        instance_id: "vp1",
        title: "VP One",
        status: "access_denied",
        error: "the credential presented to this instance was rejected",
        since: "2026-05-28T12:00:00Z",
        stations: [],
        last_refresh: "2026-05-28T12:00:00Z",
      }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );

    // The pane renders the permission notice — no "loading…" limbo, no crash.
    await screen.findByText(/No permission to view this instance/);
    expect(screen.getByText(/Sign in with a reader or operator token/)).toBeInTheDocument();
    expect(screen.getByText("NO ACCESS")).toBeInTheDocument();
    // The pane carries no login button of its own; the top-bar control is the
    // single way to sign in.
    const pane = document.querySelector('[data-instance="vp1"]')!;
    expect(pane.querySelector(".permission-msg")).not.toBeNull();
    expect(pane.querySelector(".permission-msg button")).toBeNull();
    expect(screen.getByRole("button", { name: "Log in" })).toBeInTheDocument();
  });

  it("does not offer login in the pane for an authenticated session", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 401, body: { error: { code: "unauthenticated", message: "missing" } } }
          : { status: 200, body: operatorIdentity },
      instancesFor: () => ({ items: [VP1], ui: UI }),
      dashboardFor: () => ({
        instance_id: "vp1",
        title: "VP One",
        status: "access_denied",
        error: "the credential presented to this instance was rejected",
        since: "2026-05-28T12:00:00Z",
        stations: [],
        last_refresh: "2026-05-28T12:00:00Z",
      }),
    });

    render(
      <AuthProvider initial={{ token: "op-token", subject: "alice", role: "operator" }}>
        <App />
      </AuthProvider>
    );

    await screen.findByText(/No permission to view this instance/);
    // An authenticated user is told to ask an operator, not to log in again.
    expect(screen.getByText(/Ask an operator to grant you access/)).toBeInTheDocument();
    const pane = document.querySelector('[data-instance="vp1"]')!;
    expect(pane.querySelector(".permission-msg button")).toBeNull();
  });

  it("opens the sign-in dialog from the top-bar login control", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 200, body: operatorIdentity },
      instancesFor: () => ({ items: [VP1], ui: UI }),
      dashboardFor: () => ({
        instance_id: "vp1",
        title: "VP One",
        status: "access_denied",
        error: "the credential presented to this instance was rejected",
        since: "2026-05-28T12:00:00Z",
        stations: [],
        last_refresh: "2026-05-28T12:00:00Z",
      }),
    });

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );

    await screen.findByText(/No permission to view this instance/);
    // The top-bar control is the single login affordance.
    fireEvent.click(screen.getByRole("button", { name: "Log in" }));
    await screen.findByLabelText(/API token/i);
  });
});

// ── 10.4 Null collections never crash the pane ───────────────────────────

describe("null collections", () => {
  it("renders a dashboard whose stations field is null without a client error", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 200, body: operatorIdentity },
      instancesFor: () => ({ items: [VP1], ui: UI }),
      dashboardFor: () => ({
        instance_id: "vp1",
        title: "VP One",
        status: "degraded",
        error: "upstream returned 500",
        since: "2026-05-28T12:00:00Z",
        stations: null,
        last_refresh: "2026-05-28T12:00:00Z",
      }),
    });

    const errors: unknown[] = [];
    const onError = (e: ErrorEvent) => errors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );

    // The pane still renders: the degraded message is shown, no crash.
    await screen.findByText(/degraded: upstream returned 500/);
    expect(screen.getByText("DEGRADED")).toBeInTheDocument();
    expect(errors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });

  it("renders a dashboard with a null stations field and status ok without a crash", async () => {
    installFetch({
      identityFor: (auth) =>
        auth === undefined
          ? { status: 200, body: anonymousIdentity }
          : { status: 200, body: operatorIdentity },
      instancesFor: () => ({ items: [VP1], ui: UI }),
      dashboardFor: () => ({
        instance_id: "vp1",
        title: "VP One",
        status: "ok",
        since: "2026-05-28T12:00:00Z",
        stations: null,
        last_refresh: "2026-05-28T12:00:00Z",
      }),
    });

    const errors: unknown[] = [];
    const onError = (e: ErrorEvent) => errors.push(e.error ?? e.message);
    window.addEventListener("error", onError);

    render(
      <AuthProvider>
        <App />
      </AuthProvider>
    );

    // The card header renders with OK and no station rows — no TypeError.
    await screen.findAllByText("VP One");
    expect(screen.getByText("OK")).toBeInTheDocument();
    expect(errors).toHaveLength(0);

    window.removeEventListener("error", onError);
  });
});