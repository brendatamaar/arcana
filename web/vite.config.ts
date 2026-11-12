import { defineConfig } from "vitest/config";
import tailwindcss from "@tailwindcss/vite";

// The build goes straight into the Go package that embeds it. `base` matches where the server mounts it.
export default defineConfig({
  base: "/ui/",
  plugins: [tailwindcss()],
  // JSX is compiled by esbuild's automatic runtime; no React plugin is needed.
  esbuild: { jsx: "automatic" },
  build: {
    outDir: "../internal/webui/dist",
    // scripts/clean-dist.mjs empties the directory but keeps .gitkeep, which a plain empty would remove.
    emptyOutDir: false,
    sourcemap: false,
    target: "es2022",
    chunkSizeWarningLimit: 400,
    rollupOptions: {
      onwarn(warning, warn) {
        // TanStack Query ships React Server Component directives that are meaningless in a client bundle.
        if (warning.code === "MODULE_LEVEL_DIRECTIVE") return;
        warn(warning);
      },
    },
  },
  server: {
    // `make web-dev` serves the SPA with hot reload and sends the API to a local Arcana.
    proxy: {
      "/v1": { target: process.env.ARCANA_URL ?? "http://127.0.0.1:7070", changeOrigin: false },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
