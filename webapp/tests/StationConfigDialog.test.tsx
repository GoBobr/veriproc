import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/preact";
import { StationConfigDialog } from "../src/components/StationConfigDialog";
import { AuthProvider } from "../src/state/auth";

const okConfig = {
  station_id: "scecli",
  station_name: "Scenario Clim",
  content_hash: "sha256:abc123",
  schema_version: "veriproc.station/v1",
  effective_at: "2026-10-22T10:00:00Z",
  config: {},
  config_yaml:
    "execution:\n  container:\n    image: harbor.example/co2m.swlib_scene:mt.3.0-1\n",
};

function mockFetch(handler: (input: RequestInfo) => Response) {
  return (input: RequestInfo) => Promise.resolve(handler(input));
}

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function wrap(ui: preact.ComponentChildren) {
  return <AuthProvider initial={{ token: "tok", subject: "alice", role: "viewer" }}>{ui}</AuthProvider>;
}

beforeEach(() => {
  globalThis.fetch = mockFetch(() => jsonOk(okConfig)) as typeof fetch;
});

describe("StationConfigDialog", () => {
  it("renders the resolved YAML including the image tag", async () => {
    render(
      wrap(
        <StationConfigDialog
          instanceID="eucent"
          stationID="scecli"
          onClose={() => {}}
        />
      )
    );
    const body = await screen.findByTestId("station-config-yaml");
    expect(body.textContent).toContain("harbor.example/co2m.swlib_scene:mt.3.0-1");
  });

  it("renders the freshness line with content_hash and effective_at", async () => {
    render(
      wrap(
        <StationConfigDialog
          instanceID="eucent"
          stationID="scecli"
          onClose={() => {}}
        />
      )
    );
    const meta = await screen.findByTestId("station-config-meta");
    expect(meta.textContent).toContain("sha256:abc123");
    expect(meta.textContent).toContain("2026-10-22T10:00:00.000Z");
  });

  it("renders a read-only, non-editable body", async () => {
    render(
      wrap(
        <StationConfigDialog
          instanceID="eucent"
          stationID="scecli"
          onClose={() => {}}
        />
      )
    );
    const body = await screen.findByTestId("station-config-yaml");
    // Scrollable read-only display: a pre element styled with overflow
    // scrolling, never an input or textarea.
    expect(body.tagName).toBe("PRE");
    expect(body.className).toContain("station-config-yaml");
  });

  it("shows the upstream error reason when the fetch fails", async () => {
    globalThis.fetch = mockFetch(
      () =>
        new Response(JSON.stringify({ error: { message: "upstream down" } }), {
          status: 502,
          headers: { "Content-Type": "application/json" },
        })
    ) as typeof fetch;
    render(
      wrap(
        <StationConfigDialog
          instanceID="eucent"
          stationID="scecli"
          onClose={() => {}}
        />
      )
    );
    expect(await screen.findByText(/upstream down/)).toBeInTheDocument();
  });

  it("closes via the Close button", async () => {
    const onClose = vi.fn();
    render(
      wrap(
        <StationConfigDialog
          instanceID="eucent"
          stationID="scecli"
          onClose={onClose}
        />
      )
    );
    await screen.findByTestId("station-config-yaml");
    fireEvent.click(screen.getByText("Close"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
