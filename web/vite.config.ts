import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.STRIKE_API_ORIGIN || "http://127.0.0.1:8787";
  return {
    plugins: [react()],
    build: {
      outDir: path.resolve(__dirname, "../internal/server/static"),
      emptyOutDir: true,
      assetsDir: "assets",
    },
    server: {
      host: env.VITE_HOST || "127.0.0.1",
      port: Number(env.VITE_PORT || 5173),
      strictPort: true,
      proxy: {
        "/health": { target, changeOrigin: true },
        "/v1": { target, changeOrigin: true, ws: true },
      },
    },
    test: {
      environment: "jsdom",
      setupFiles: "./src/test/setup.ts",
    },
  };
});
