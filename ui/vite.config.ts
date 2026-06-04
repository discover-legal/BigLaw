import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Big Michael's REST API + SSE streams live on :3101. Proxy every backend
// route through Vite so the browser sees one origin (no CORS, SSE just works).
const target = process.env.BIG_MICHAEL_API ?? "http://localhost:3101";
const backendRoutes = ["/tasks", "/documents", "/agents", "/templates", "/audit", "/health", "/settings", "/profiles", "/me", "/auth", "/clients", "/cost", "/time-entries", "/analytics", "/plugins"];

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: Object.fromEntries(
      backendRoutes.map((route) => [route, { target, changeOrigin: true }]),
    ),
  },
});
