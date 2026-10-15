/// <reference types="vitest/config" />
import { writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

const outDir = fileURLToPath(new URL("./dist", import.meta.url));

// web/dist is embedded by the Go binary (web/embed.go). Vite empties outDir on
// build, so re-create the tracked placeholder that keeps go:embed compiling.
function keepGitkeep(): Plugin {
  return {
    name: "rdpanel-keep-gitkeep",
    apply: "build",
    closeBundle() {
      writeFileSync(`${outDir}/.gitkeep`, "");
    },
  };
}

export default defineConfig({
  plugins: [react(), keepGitkeep()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir,
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    rollupOptions: {
      output: {
        // Stable vendor chunks: better long-term caching of /assets/*.
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return "react";
          if (id.includes("@tanstack")) return "tanstack";
          if (id.includes("@radix-ui") || id.includes("lucide-react")) return "ui";
          if (id.includes("i18next")) return "i18n";
          if (/[\\/]node_modules[\\/](zod|react-hook-form|@hookform)[\\/]/.test(id)) return "forms";
          return "vendor";
        },
      },
    },
  },
  server: {
    port: 5173,
    // `npm run dev` proxies the API to a locally running `rdpanel serve`.
    proxy: {
      "/api": "http://127.0.0.1:21114",
      "/healthz": "http://127.0.0.1:21114",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
