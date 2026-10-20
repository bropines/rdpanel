/// <reference types="vitest/config" />
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Connect, type Plugin } from "vite";

const outDir = fileURLToPath(new URL("./dist", import.meta.url));
const mockOutDir = fileURLToPath(new URL("./dist-mock", import.meta.url));
// Second entry: the Swagger UI page served by the backend at /api/v1/docs.
// Third: the public share page served by the backend at /s/<token>.
const pages = {
  main: fileURLToPath(new URL("./index.html", import.meta.url)),
  "api-docs": fileURLToPath(new URL("./api-docs.html", import.meta.url)),
  share: fileURLToPath(new URL("./share.html", import.meta.url)),
};
const workerSrc = fileURLToPath(new URL("./node_modules/msw/lib/mockServiceWorker.js", import.meta.url));

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

// Dev and preview servers: /s/<token> is the public share page (the backend
// serves it in production); its data (/s/<token>/info) comes from MSW or the
// proxied backend.
function sharePage(): Plugin {
  const rewrite: Connect.NextHandleFunction = (req, _res, next) => {
    if (req.method === "GET" && /^\/s\/[A-Za-z0-9_-]+\/?(\?.*)?$/.test(req.url ?? "")) req.url = "/share.html";
    next();
  };
  return {
    name: "rdpanel-share-page",
    configureServer(server) {
      server.middlewares.use(rewrite);
    },
    configurePreviewServer(server) {
      server.middlewares.use(rewrite);
    },
  };
}

// Mock mode only: serve/emit MSW's service worker (never part of production builds,
// so it is not copied from public/).
function mockServiceWorker(): Plugin {
  return {
    name: "rdpanel-msw-worker",
    configureServer(server) {
      server.middlewares.use("/mockServiceWorker.js", (_req, res) => {
        res.setHeader("Content-Type", "application/javascript");
        res.end(readFileSync(workerSrc));
      });
    },
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "mockServiceWorker.js", source: readFileSync(workerSrc, "utf8") });
    },
  };
}

export default defineConfig(({ mode }) => {
  // `--mode mock` (npm run dev:mock / build:mock) or VITE_MOCK=1 enables the MSW mock API.
  const mock = mode === "mock" || process.env.VITE_MOCK === "1";
  return {
    plugins: [react(), sharePage(), ...(mock ? [mockServiceWorker()] : [keepGitkeep()])],
    define: mock ? { "import.meta.env.VITE_MOCK": JSON.stringify("1") } : {},
    resolve: {
      alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
    },
    build: {
      // Mock builds never land in web/dist (which is embedded into the binary).
      outDir: mock ? mockOutDir : outDir,
      emptyOutDir: true,
      assetsDir: "assets",
      sourcemap: false,
      // The api-docs entry is one prebuilt Swagger UI bundle (~1.5 MB); the SPA chunks stay far below.
      chunkSizeWarningLimit: 1600,
      rollupOptions: {
        input: pages,
        output: {
          // Stable vendor chunks: better long-term caching of /assets/*.
          manualChunks(id) {
            if (!id.includes("node_modules")) return undefined;
            // Swagger UI: only the api-docs entry imports it; let Rollup keep it there
            // (a manual chunk would also capture shared CJS helpers the SPA imports).
            if (id.includes("swagger-ui-dist")) return undefined;
            // xterm.js and its icon font (web client terminal) and the Opus fallback decoder load on demand.
            if (/[\\/]node_modules[\\/](@xterm|opus-decoder|@wasm-audio-decoders)[\\/]/.test(id)) return undefined;
            if (/[\\/]node_modules[\\/]rdweb[\\/]dist[\\/]react[\\/]fonts[\\/]/.test(id)) return undefined;
            if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return "react";
            if (id.includes("@tanstack")) return "tanstack";
            // Radix with its own dependencies: "ui" must not import "vendor" (rdweb in
            // "vendor" imports Radix, and a vendor <-> ui cycle breaks depending on
            // which entry loads first).
            if (id.includes("@radix-ui") || id.includes("lucide-react")) return "ui";
            if (/[\\/]node_modules[\\/](@floating-ui|aria-hidden|react-remove-scroll|react-remove-scroll-bar|react-style-singleton|use-callback-ref|use-sidecar|get-nonce|detect-node-es|tslib)[\\/]/.test(id)) return "ui";
            if (id.includes("i18next")) return "i18n";
            if (/[\\/]node_modules[\\/](zod|react-hook-form|@hookform)[\\/]/.test(id)) return "forms";
            if (/[\\/]node_modules[\\/](marked|dompurify)[\\/]/.test(id)) return "markdown";
            if (mock && /[\\/]node_modules[\\/](msw|@mswjs|@bundled-es-modules|graphql|headers-polyfill|outvariant|strict-event-emitter|@open-draft)[\\/]/.test(id)) return "msw";
            return "vendor";
          },
        },
      },
    },
    server: {
      port: 5173,
      // `npm run dev` proxies the API to a locally running `rdpanel serve`.
      proxy: mock
        ? undefined
        : {
            "/api": "http://127.0.0.1:21114",
            "/healthz": "http://127.0.0.1:21114",
            "^/s/[^/]+/(info|windows/.*)$": "http://127.0.0.1:21114",
          },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test/setup.ts"],
      css: false,
      exclude: ["node_modules", "dist", "dist-mock", "e2e"],
    },
  };
});
