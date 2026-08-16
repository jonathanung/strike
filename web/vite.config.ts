import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { injectStockTokens } from "./src/stockTokens";

/** Inject schemas/ui-tokens.json hexes at strike-stock dark/light markers. */
function strikeStockPlugin(): Plugin {
  return {
    name: "strike-stock-tokens",
    transform(code, id) {
      const file = id.split("?", 1)[0];
      if (!file.endsWith("styles.css") || !code.includes("strike-stock:")) return;
      return { code: injectStockTokens(code), map: null };
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.STRIKE_API_ORIGIN || "http://127.0.0.1:8787";
  return {
    plugins: [strikeStockPlugin(), react()],
    build: {
      outDir: path.resolve(__dirname, "../internal/frontend/server/static"),
      emptyOutDir: true,
      assetsDir: "assets",
    },
    server: {
      host: env.VITE_HOST || "127.0.0.1",
      port: Number(env.VITE_PORT || 5173),
      strictPort: true,
      fs: { allow: [path.resolve(__dirname, "..")] },
      proxy: {
        "/health": { target, changeOrigin: true },
        "/v1": { target, changeOrigin: true, ws: true },
      },
    },
    test: {
      environment: "jsdom",
      setupFiles: "./src/test/setup.ts",
      // Playwright specs live under e2e/ and must not run under Vitest.
      exclude: ["**/node_modules/**", "**/e2e/**", "**/e2e-results/**", "**/e2e-report/**"],
    },
  };
});
