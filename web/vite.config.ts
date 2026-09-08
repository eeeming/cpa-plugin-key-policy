import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";

// CPA base URL used by the dev-server proxy. Override with VITE_CPA_BASE.
const cpaBase = process.env.VITE_CPA_BASE ?? "http://127.0.0.1:8317";

// When hosted inside CPA, the app is served at
// /v0/resource/plugins/cpa-key-quota/index.html. Production builds must use
// that base so inlined asset URLs resolve. In dev we keep "/" for convenience.
const base = process.env.VITE_HOSTED === "1"
  ? "/v0/resource/plugins/cpa-key-quota/"
  : "/";

export default defineConfig({
  base,
  plugins: [react(), viteSingleFile()],
  // Single-file build: inline everything into index.html.
  build: {
    assetsInlineLimit: 100000000,
    cssCodeSplit: false,
    rollupOptions: {
      output: { inlineDynamicImports: true },
    },
  },
  server: {
    port: 5174,
    proxy: {
      // Proxy management API calls to CPA during development to avoid CORS.
      "/v0/management": { target: cpaBase, changeOrigin: true },
      "/v1/models": { target: cpaBase, changeOrigin: true },
    },
  },
});
