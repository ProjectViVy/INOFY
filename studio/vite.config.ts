import { defineConfig } from "vite";

export default defineConfig({
  // Dev only: the App's /api/v1 is reached same-origin through this
  // proxy, so the SameSite=Strict session cookie is sent and the
  // Origin/CSRF guard sees one origin (Host/Origin are forwarded
  // unchanged — do not set changeOrigin here).
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8377",
        // Keep the browser's Host so the App sees one single origin:
        // its CSRF guard requires Origin to match Host, and rewriting
        // Host to the target would make every session mutation 403.
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: "dist",
    assetsDir: "assets",
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
  },
});
