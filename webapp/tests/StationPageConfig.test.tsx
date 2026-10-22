import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/preact";
import { StationPage } from "../src/pages/StationPage";
import { AuthProvider } from "../src/state/auth";

const okConfig = {
  station_id: "scecli",
  content_hash: "sha256:abc123",
  effective_at: "2026-10-22T10:00:00Z",
  config_yaml: "execution:\n  container:\n    image: harbor.example/scene:mt.3.0-1\n",
};

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  const handler = (input: RequestInfo): Response => {
    const url = String(input);
    if (url.endsWith("/stations/scecli/config")) return jsonOk(okConfig);
    if (url.endsWith("/stations/scecli/runs") || url.includes("/runs?"))
      return jsonOk({ items: [], page_size: 50, ordering: "", filters: {} });
    if (url.endsWith("/api/console/instances"))
      return jsonOk({ items: [], ui: { refresh_interval_ms: 5000 } });
    return jsonOk({});
  };
  globalThis.fetch = ((input: RequestInfo) => Promise.resolve(handler(input))) as typeof fetch;
});

describe("StationPage Config button", () => {
  it("opens the config dialog with the resolved config including the image tag", async () => {
    render(
      <AuthProvider initial={{ token: "tok", subject: "alice", role: "viewer" }}>
        <StationPage instanceID="eucent" stationID="scecli" />
      </AuthProvider>
    );
    // Config button is available to viewers (read-only).
    const btn = screen.getByText("Config");
    fireEvent.click(btn);
    const body = await screen.findByTestId("station-config-yaml");
    expect(body.textContent).toContain("harbor.example/scene:mt.3.0-1");
    const meta = await screen.findByTestId("station-config-meta");
    expect(meta.textContent).toContain("sha256:abc123");
  });
});
