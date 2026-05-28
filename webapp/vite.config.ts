import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

export default defineConfig({
  plugins: [preact()],
  define: {
    // Expose the package.json version at build time so the UI can display it.
    // Access via: import.meta.env.VITE_APP_VERSION / VITE_APP_COMMIT
    // Priority: VITE_APP_VERSION env (set by Makefile) > package.json version > "dev"
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(
      process.env.VITE_APP_VERSION ?? process.env.npm_package_version ?? "dev"
    ),
    "import.meta.env.VITE_APP_COMMIT": JSON.stringify(
      process.env.VITE_APP_COMMIT ?? "unknown"
    ),
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8090"
    }
  },
  build: {
    outDir: "dist",
    sourcemap: true
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./tests/setup.ts"]
  }
});
