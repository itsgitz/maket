import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The Go server routes by Host header, so the dev proxy must keep the
// browser's Host (app.localhost:5173) rather than rewriting it.
export default defineConfig({
  plugins: [svelte()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    allowedHosts: ["app.localhost"],
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
  build: {
    outDir: "../api/internal/webui/dist",
    emptyOutDir: true,
  },
});
