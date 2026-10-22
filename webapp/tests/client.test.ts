import { describe, it, expect } from "vitest";
import { ConsoleClient } from "../src/api/client";

function mockFetch(handler: (input: RequestInfo, init?: RequestInit) => Response) {
  return (input: RequestInfo, init?: RequestInit) => Promise.resolve(handler(input, init));
}

function jsonOk(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ConsoleClient", () => {
  it("attaches the bearer token", async () => {
    let seen: Headers | null = null;
    globalThis.fetch = mockFetch((_input, init) => {
      seen = new Headers(init?.headers);
      return jsonOk({ items: [], ui: { refresh_interval_ms: 5000 } });
    }) as typeof fetch;
    const c = new ConsoleClient("tok");
    await c.instances();
    expect(seen!.get("Authorization")).toBe("Bearer tok");
  });

  it("throws on non-OK and surfaces server message", async () => {
    globalThis.fetch = mockFetch(() =>
      new Response(JSON.stringify({ error: { message: "boom" } }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      })
    ) as typeof fetch;
    const c = new ConsoleClient("tok");
    await expect(c.instances()).rejects.toThrow(/boom/);
  });

  it("encodes path segments", async () => {
    let url = "";
    globalThis.fetch = mockFetch((input) => {
      url = String(input);
      return jsonOk({});
    }) as typeof fetch;
    const c = new ConsoleClient("tok");
    await c.getTask("inst with space", "task/slash");
    expect(url).toContain("inst%20with%20space");
    expect(url).toContain("task%2Fslash");
  });

  it("stationConfig fetches the resolved config document", async () => {
    let url = "";
    let method = "";
    let auth: string | null = null;
    globalThis.fetch = mockFetch((input, init) => {
      url = String(input);
      method = init?.method ?? "GET";
      auth = new Headers(init?.headers).get("Authorization");
      return jsonOk({
        station_id: "scecli",
        content_hash: "sha256:abc",
        config_yaml: "execution:\n",
      });
    }) as typeof fetch;
    const c = new ConsoleClient("tok");
    const res = await c.stationConfig("eucent", "scecli");
    expect(method).toBe("GET");
    expect(auth).toBe("Bearer tok");
    expect(url).toBe("/api/console/instances/eucent/stations/scecli/config");
    expect(res.station_id).toBe("scecli");
    expect(res.config_yaml).toContain("execution");
  });
});
