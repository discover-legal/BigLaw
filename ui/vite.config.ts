import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import backendRoutes from "./backend-routes.json";

// BigLaw's REST API + SSE streams live on :3101. Proxy every backend
// route through Vite so the browser sees one origin (no CORS, SSE just works).
const target = process.env.BIG_MICHAEL_API ?? "http://localhost:3101";
// The list lives in backend-routes.json so the Go suite can check it against
// the routes the API actually registers (TestViteProxyCoversAllRoutes in
// biglaw-go/internal/api) — a missing prefix makes the dev server answer with
// index.html and the page dies on "Unexpected token '<'".

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: Object.fromEntries(
      backendRoutes.map((route) => [route, { target, changeOrigin: true }]),
    ),
  },
});
