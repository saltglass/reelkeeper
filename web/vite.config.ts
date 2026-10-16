/// <reference types="vitest/config" />
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// The Go server replaces this marker with <base href="{URL base}/">. The dev
// server does the same with "/", so relative URLs (brand images) work on
// deep links such as /settings/security.
const devBase: Plugin = {
  name: "reelkeeper-dev-base",
  apply: "serve",
  transformIndexHtml: (html) => html.replace("<!--reelkeeper:base-->", '<base href="/">'),
};

// In development (`just dev`) Vite serves the UI on :7676 and forwards API
// calls to the Go server on :7677. In production the Go binary serves the
// built files itself (they're embedded from internal/ui/dist).
const backend = "http://localhost:7677";

// Vite empties the output folder on every build, including the committed
// placeholder that lets `go build` work before the UI has been built.
const keepPlaceholder: Plugin = {
  name: "reelkeeper-keep-placeholder",
  apply: "build",
  async writeBundle(options) {
    const { writeFile } = await import("node:fs/promises");
    await writeFile(`${options.dir}/.gitkeep`, "");
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), devBase, keepPlaceholder],
  // Relative asset URLs plus a <base href> injected by the Go server make
  // the same build work under any URL base (such as /reelkeeper).
  base: "./",
  resolve: { alias: { "@": decodeURIComponent(new URL("./src", import.meta.url).pathname) } },
  server: {
    port: 7676,
    strictPort: true,
    proxy: {
      "/api": { target: backend, changeOrigin: false },
      "/ping": backend,
    },
  },
  build: {
    outDir: "../internal/ui/dist",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // Room for findBy…'s 5 s wait (src/test/setup.ts) on the slow CI runner.
    testTimeout: 15000,
  },
});
