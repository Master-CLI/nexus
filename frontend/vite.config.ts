import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Inject Wails v3 runtime.js as a module script before any app scripts.
// /wails/runtime.js only exists at runtime (served by Wails), so we inject
// it via plugin to avoid Vite trying to resolve it during build.
function wailsRuntime(): Plugin {
  return {
    name: "wails-runtime",
    transformIndexHtml: {
      order: "post",
      handler(html) {
        return html.replace(
          '<script type="module"',
          '<script src="/wails/runtime.js" type="module"></script>\n    <script type="module"',
        );
      },
    },
  };
}

export default defineConfig({
  plugins: [wailsRuntime(), react()],
  build: {
    outDir: "dist",
  },
  server: {
    port: 5176,
  },
});
