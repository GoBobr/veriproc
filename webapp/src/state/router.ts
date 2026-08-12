import { useEffect, useState } from "preact/hooks";

// Hash-based router suitable for an SPA served from a single index.html.
// Routes:
//   #/                                              → dashboard
//   #/instances/{id}/tasks/{taskId}                 → task detail
//   #/instances/{id}/stations/{stationId}           → station activity view
// Path segments are returned through useRoute().
export interface Route {
  page: "dashboard" | "task" | "station" | "login";
  instanceID?: string;
  taskID?: string;
  retryIndex?: number;
  stationID?: string;
}

function parseHash(): Route {
  const h = location.hash.startsWith("#") ? location.hash.slice(1) : location.hash;
  if (!h || h === "/" || h === "") return { page: "dashboard" };
  const parts = h.split("/").filter(Boolean);
  if (parts[0] === "instances" && parts[2] === "tasks") {
    return {
      page: "task",
      instanceID: decodeURIComponent(parts[1]),
      taskID: decodeURIComponent(parts[3] || ""),
      retryIndex: parts[5] !== undefined ? Number(parts[5]) : undefined,
    };
  }
  if (parts[0] === "instances" && parts[2] === "stations") {
    return {
      page: "station",
      instanceID: decodeURIComponent(parts[1]),
      stationID: decodeURIComponent(parts[3] || ""),
    };
  }
  return { page: "dashboard" };
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseHash());
  useEffect(() => {
    const fn = () => setRoute(parseHash());
    window.addEventListener("hashchange", fn);
    return () => window.removeEventListener("hashchange", fn);
  }, []);
  return route;
}

export function taskHref(instanceID: string, taskID: string, retryIndex?: number): string {
  let h = `#/instances/${encodeURIComponent(instanceID)}/tasks/${encodeURIComponent(taskID)}`;
  if (retryIndex !== undefined) h += `/runs/${retryIndex}`;
  return h;
}

export function stationHref(instanceID: string, stationID: string): string {
  return `#/instances/${encodeURIComponent(instanceID)}/stations/${encodeURIComponent(stationID)}`;
}
