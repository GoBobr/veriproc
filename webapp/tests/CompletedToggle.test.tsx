// Behavioral tests for the dashboard's completed-in-interval toggle
// (OpenSpec change console-completed-interval-toggle):
//  - checking the box sends completed=1 on the dashboard request and shows
//    aged-out-but-in-interval completed slots,
//  - unchecking hides them again,
//  - completed_visibility_ms = 0 disables the checkbox with an explanatory
//    tooltip,
//  - the toggle state survives a remount via localStorage.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/preact";
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

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// dashboardWith returns a healthy dashboard where one station has a single
// completed slot for task aged-out (terminal well past the 30s timeout).
function dashboardWith(completedVisible: boolean) {
  const slots = completedVisible
    ? [{ kind: "completed", run_id: "r1", task_id: "aged-out", retry_index: 0, state: "complete", terminal_at: "2026-05-28T10:00:00Z" }]
    : Array.from({ length: 12 }, () => ({ kind: "empty", retry_index: 0 }));
  return {
    instance_id: "vp1",
    title: "VP One",
    status: "ok",
    since: "2026-05-27T12:00:00Z",
    stations: [
      {
        station_id: "STA",
        paused: false,
        running_count: 0,
        queued_count: 0,
        counts: { success: 1, failure: 0 },
        slots,
        overflow: 0,
      },
    ],
    last_refresh: "2026-05-28T12:00:00Z",
  };
}

interface Options {
  ui?: typeof UI;
}

// installToggleFetch mocks the console API; onlyCompletedVisible is shared
// mutable state so the mock can serve different dashboard bodies depending
// on what completed= state the client last requested.
function installToggleFetch(opts: Options = {}) {
  let requestedCompleted = false;
  const dashboardCalls: string[] = [];
  const mock = vi.fn(async (input: RequestInfo, init?: RequestInit) => {
    const url = new URL(String(input), "http://console");
    if (url.pathname.includes("/api/console/identity")) {
      return json(anonymousIdentity);
    }
    if (url.pathname.includes("/dashboard")) {
      requestedCompleted = url.searchParams.get("completed") === "1";
      dashboardCalls.push(url.search + "");
      return json(dashboardWith(requestedCompleted));
    }
    if (url.pathname.includes("/api/console/instances")) {
      return json({ items: [VP1], ui: opts.ui ?? UI });
    }
    return json({});
  });
  globalThis.fetch = mock as unknown as typeof fetch;
  return { mock, dashboardCalls, wasRequestedCompleted: () => requestedCompleted };
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

function renderApp() {
  render(
    <AuthProvider>
      <App />
    </AuthProvider>
  );
}

// toggleInput returns the completed-toggle checkbox once rendered.
async function toggleInput(): Promise<HTMLInputElement> {
  await screen.findByText(/stations|OK|O K/, { selector: "h3" }).catch(() => {});
  const el = await waitFor(() => {
    const input = document.querySelector(".completed-toggle input[type=checkbox]") as HTMLInputElement | null;
    if (!input) throw new Error("completed toggle not rendered yet");
    return input;
  });
  return el;
}

// instancePane returns the instance block element.
async function instancePane(): Promise<HTMLElement> {
  return waitFor(() => {
    const pane = document.querySelector('[data-instance="vp1"]') as HTMLElement | null;
    if (!pane) throw new Error("instance pane not rendered yet");
    return pane;
  });
}

// ── toggle request + display behavior ────────────────────────────────────

describe("completed interval toggle", () => {
  it("sends completed=1 and shows an aged-out completed slot when checked", async () => {
    const { wasRequestedCompleted } = installToggleFetch();
    renderApp();

    const toggle = await toggleInput();
    expect(wasRequestedCompleted()).toBe(false); // default unchecked

    fireEvent.click(toggle);
    await waitFor(() => expect(wasRequestedCompleted()).toBe(true));
    const pane = await instancePane();
    await waitFor(() => {
      const slot = pane.querySelector('.slot[data-kind="completed"]') as HTMLElement | null;
      if (!slot || slot.getAttribute("data-task") !== "aged-out") {
        throw new Error("completed slot for aged-out not rendered yet");
      }
    });
  });

  it("does not show the completed slot while the mock answers without the flag", async () => {
    // The dashboard mock shows the completed slot only when completed=1 was
    // requested; with the toggle never touched it must stay hidden.
    const { wasRequestedCompleted } = installToggleFetch();
    renderApp();

    await instancePane();
    expect(wasRequestedCompleted()).toBe(false);
    const slot = document.querySelector('.slot[data-kind="completed"]');
    expect(slot).toBeNull();
  });

  it("is disabled with an explanatory title when completed_visibility_ms is 0", async () => {
    installToggleFetch({ ui: { ...UI, completed_visibility_ms: 0 } });
    renderApp();

    const toggle = await toggleInput();
    expect(toggle.disabled).toBe(true);
    const label = toggle.closest("label") as HTMLElement;
    expect(label.title).toMatch(/disabled in config/i);

    // A disabled checkbox must not be checked.
    expect(toggle.checked).toBe(false);
  });

  it("persists the checked state across a remount", async () => {
    const { wasRequestedCompleted } = installToggleFetch();
    renderApp();
    fireEvent.click(await toggleInput());
    await waitFor(() => expect(wasRequestedCompleted()).toBe(true));

    cleanup();
    renderApp();
    const toggle = await toggleInput();
    expect(toggle.checked).toBe(true);
    await waitFor(() => expect(wasRequestedCompleted()).toBe(true));
  });
});
