import { render } from "preact";
import { AuthProvider } from "./state/auth";
import { App } from "./App";
import "./styles.css";

const root = document.getElementById("app");
if (!root) throw new Error("missing #app");
render(
  <AuthProvider>
    <App />
  </AuthProvider>,
  root
);
