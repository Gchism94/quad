import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go control plane serves its API at the root (/classrooms, /assignments, …).
// In development we proxy those prefixes to it so the browser stays same-origin
// and no CORS configuration is required. Override the target with CAIRN_API_URL.
const API_PREFIXES = ["/classrooms", "/assignments", "/auth", "/healthz"];
const target = process.env.CAIRN_API_URL || "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: Object.fromEntries(
      API_PREFIXES.map((p) => [p, { target, changeOrigin: true }]),
    ),
  },
});
