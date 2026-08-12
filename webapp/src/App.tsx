import { useEffect, useState } from "preact/hooks";
import { useAuth } from "./state/auth";
import { useRoute } from "./state/router";
import { Login } from "./pages/Login";
import { DashboardPage } from "./pages/DashboardPage";
import { TaskPage } from "./pages/TaskPage";
import { StationPage } from "./pages/StationPage";
import { InfoDialog } from "./components/InfoDialog";

type Theme = "dark" | "light";

function readTheme(): Theme {
  try {
    const v = localStorage.getItem("veriproc.console.theme");
    if (v === "light" || v === "dark") return v;
  } catch { /* ignore */ }
  return "dark";
}

function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t === "light" ? "light" : "";
}

export function App() {
  const { session, logout } = useAuth();
  const route = useRoute();
  const [theme, setTheme] = useState<Theme>(readTheme);
  const [showInfo, setShowInfo] = useState(false);

  useEffect(() => {
    applyTheme(theme);
    try { localStorage.setItem("veriproc.console.theme", theme); } catch { /* ignore */ }
  }, [theme]);

  const toggleTheme = () => setTheme((t) => (t === "dark" ? "light" : "dark"));

  return (
    <div class="app">
      <header class="topbar">
        <h1>
          <a href="#/" style={{ color: "inherit", textDecoration: "none" }}>
            Veriproc Operator Console
          </a>
        </h1>
        <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
          <button class="theme-toggle" onClick={toggleTheme} title="Toggle light/dark theme">
            {theme === "dark" ? "☀ Light" : "☾ Dark"}
          </button>
          <button class="info-btn" onClick={() => setShowInfo(true)} title="System info">ℹ Info</button>
          {session && (
            <div class="user">
              <span>{session.subject}</span>
              <span class={`role role-${session.role}`}>{session.role}</span>
              <button class="secondary" onClick={logout}>
                Sign out
              </button>
            </div>
          )}
        </div>
      </header>
      {showInfo && session && <InfoDialog onClose={() => setShowInfo(false)} />}
      <main>
        {!session && <Login />}
        {session && route.page === "dashboard" && <DashboardPage />}
        {session && route.page === "station" && route.instanceID && route.stationID && (
          <StationPage instanceID={route.instanceID} stationID={route.stationID} />
        )}
        {session && route.page === "task" && route.instanceID && route.taskID && (
          <TaskPage
            instanceID={route.instanceID}
            taskID={route.taskID}
            initialRetry={route.retryIndex}
          />
        )}
      </main>
    </div>
  );
}
