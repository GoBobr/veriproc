import { useAuth } from "./state/auth";
import { useRoute } from "./state/router";
import { Login } from "./pages/Login";
import { DashboardPage } from "./pages/DashboardPage";
import { TaskPage } from "./pages/TaskPage";

export function App() {
  const { session, logout } = useAuth();
  const route = useRoute();

  return (
    <div class="app">
      <header class="topbar">
        <h1>
          <a href="#/" style={{ color: "inherit", textDecoration: "none" }}>
            Veriproc Operator Console
          </a>
        </h1>
        {session && (
          <div class="user">
            <span>{session.subject}</span>
            <span class={`role role-${session.role}`}>{session.role}</span>
            <button class="secondary" onClick={logout}>
              Sign out
            </button>
          </div>
        )}
      </header>
      <main>
        {!session && <Login />}
        {session && route.page === "dashboard" && <DashboardPage />}
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
