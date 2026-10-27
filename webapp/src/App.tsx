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
  const { session, ready, logout } = useAuth();
  const route = useRoute();
  const [theme, setTheme] = useState<Theme>(readTheme);
  const [showInfo, setShowInfo] = useState(false);
  const [showLogin, setShowLogin] = useState(false);
  // Set when the gateway refused a credential the user submitted. It outlives
  // the dialog so the rejection stays visible after the dialog is closed.
  const [authError, setAuthError] = useState<string | null>(null);

  useEffect(() => {
    applyTheme(theme);
    try { localStorage.setItem("veriproc.console.theme", theme); } catch { /* ignore */ }
  }, [theme]);

  const toggleTheme = () => setTheme((t) => (t === "dark" ? "light" : "dark"));

  // The dashboard renders whenever a session exists - anonymous or
  // authenticated. The login dialog is only a blocking screen when the
  // gateway requires a credential (no session at all); with anonymous
  // browsing it is an optional upgrade control.
  const showDashboard = ready && !!session;
  const showLoginScreen = ready && !session;
  // Two independent dialog states:
  //  - showLogin is the guest *upgrade* dialog, opened from the top bar.
  //  - loginDismissed records that the *blocking* dialog (no session at all)
  //    was closed; without it the console has nothing to show, so a
  //    "sign-in required" placeholder stands in for it.
  const [loginDismissed, setLoginDismissed] = useState(false);
  const blockingLoginOpen = showLoginScreen && !loginDismissed;
  const upgradeLoginOpen = showDashboard && !!session?.anonymous && showLogin;
  const loginOpen = blockingLoginOpen || upgradeLoginOpen;

  // Once a login upgrade lands, dismiss the upgrade dialog in place.
  useEffect(() => {
    if (session && !session.anonymous) {
      setShowLogin(false);
      setAuthError(null);
    }
  }, [session]);

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
          {session && !session.anonymous && (
            <div class="user">
              <span>{session.subject}</span>
              <span class={`role role-${session.role}`}>{session.role}</span>
              <button class="secondary" onClick={logout}>
                Sign out
              </button>
            </div>
          )}
          {session && session.anonymous && (
            <div class="user">
              <span class="role role-anonymous">anonymous</span>
              <button class="secondary" onClick={() => setShowLogin((v) => !v)}>
                Log in
              </button>
            </div>
          )}
        </div>
      </header>
      {session && session.credentialRejected && (
        <div class="banner banner-warning" role="status">
          Your credential was rejected, so you are browsing as a guest with read-only access.{" "}
          <button class="secondary" onClick={() => setShowLogin(true)}>
            Log in
          </button>
        </div>
      )}
      {authError && !loginOpen && (
        <div class="banner banner-warning" role="alert">
          Sign-in failed: {authError}{" "}
          <button class="secondary" onClick={() => setShowLogin(true)}>
            Try again
          </button>
          <button class="secondary" onClick={() => setAuthError(null)}>
            Dismiss
          </button>
        </div>
      )}
      {showInfo && session && <InfoDialog onClose={() => setShowInfo(false)} />}
      <main>
        {blockingLoginOpen && (
          <Login
            onRejected={setAuthError}
            onCancel={() => setLoginDismissed(true)}
          />
        )}
        {showLoginScreen && loginDismissed && (
          <div class="login-required">
            <h2>Sign-in required</h2>
            <p>
              This console requires a reader or operator credential. The sign-in dialog was
              dismissed, so there is nothing to show until you sign in.
            </p>
            <button class="primary" onClick={() => setLoginDismissed(false)}>
              Sign in
            </button>
          </div>
        )}
        {upgradeLoginOpen && (
          <Login
            onRejected={setAuthError}
            onCancel={() => setShowLogin(false)}
          />
        )}
        {showDashboard && route.page === "dashboard" && <DashboardPage />}
        {showDashboard && route.page === "station" && route.instanceID && route.stationID && (
          <StationPage instanceID={route.instanceID} stationID={route.stationID} />
        )}
        {showDashboard && route.page === "task" && route.instanceID && route.taskID && (
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
