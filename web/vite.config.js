import { defineConfig, loadEnv } from "vite";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const cockpit = path.resolve(__dirname, "../internal/server/static/index.html");
const apiTarget = process.env.STRIKE_API_ORIGIN || "http://127.0.0.1:8787";

/** Dev server: serve the Go-embedded cockpit and proxy API to strike serve. */
function serveCockpit() {
  return {
    name: "strike-cockpit",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url?.split("?")[0] || "";
        if (url === "/" || url === "/index.html" || url === "/attach") {
          res.setHeader("Content-Type", "text/html; charset=utf-8");
          res.setHeader("Cache-Control", "no-store");
          fs.createReadStream(cockpit).pipe(res);
          return;
        }
        next();
      });
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, __dirname, "");
  const target = env.STRIKE_API_ORIGIN || apiTarget;
  const host = env.VITE_HOST || "127.0.0.1";
  return {
    publicDir: false,
    plugins: [serveCockpit()],
    server: {
      host,
      port: Number(env.VITE_PORT || 5173),
      strictPort: true,
      proxy: {
        "/health": { target, changeOrigin: true },
        "/v1": { target, changeOrigin: true, ws: true },
      },
    },
    preview: {
      host,
      port: Number(env.VITE_PREVIEW_PORT || 4173),
      proxy: {
        "/health": { target, changeOrigin: true },
        "/v1": { target, changeOrigin: true, ws: true },
      },
    },
  };
});
