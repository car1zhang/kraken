// defineConfig comes from vitest/config rather than vite: it is vite's own
// config type widened with the `test` block below. Importing vite's would
// typecheck everything except `test`, which is exactly the field this file
// needs it for.
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The board is served two ways and this config has to satisfy both:
//
//   production — `npm run build` writes ./dist, which web/embed.go pulls in
//     with `//go:embed all:_ui/dist` and web/serve.go serves off the same mux
//     as /api. Same origin, so the API base is "" and no proxy is involved.
//
//   dev — `npm run dev` serves the bundle from :5173 and reverse-proxies
//     /api/* to the real `kraken serve` on :7777. There are no mocks and no
//     fixtures: the dev server talks to the live daemon reading the real
//     kraken.db, so what you see is what the board sees.
//
// The output directory is ./dist (i.e. web/_ui/dist) because go:embed cannot
// reach above its own package directory — the same constraint that pins
// skills_embed.go at the repo root. The leading underscore on _ui is load
// bearing for a different reason: `go build ./...` walks into node_modules
// and one stray .go file in any npm package breaks the build, and the go tool
// skips `_`-prefixed directories but NOT node_modules. That is also why the
// embed directive needs `all:` — embed patterns skip `_` paths too.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Hashed filenames are what let web/serve.go serve assets as immutable
    // while keeping index.html on no-cache.
    assetsDir: "assets",
  },
  server: {
    port: 5173,
    // Bind 0.0.0.0 so the dev board is reachable over tailscale, the same way
    // :7777 is. Vite's DNS-rebinding guard rejects unrecognized Host headers,
    // hence allowedHosts; hmr.host so the HMR socket dials back to the host
    // the page was actually loaded from rather than localhost.
    host: true,
    allowedHosts: true,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:7777",
        changeOrigin: false,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
