import vue from "@vitejs/plugin-vue";
import path from "node:path";
import { defineConfig } from "vite";

const projectRoot = import.meta.dirname;

// Output filenames must stay deterministic: internal/app/static.go embeds web/* and
// index.html references the assets by fixed path, so hashed names would break both.
export default defineConfig({
  root: path.join(projectRoot, "internal/app/web-src"),
  base: "/",
  plugins: [vue()],
  // License compliance is handled by web/vendor/THIRD_PARTY_NOTICES.txt, generated
  // from the pnpm dependency tree in scripts/build-web.mjs.
  build: {
    outDir: path.join(projectRoot, "internal/app/web"),
    // web/vendor/THIRD_PARTY_NOTICES.txt lives in the output dir and is generated
    // separately by scripts/build-web.mjs.
    emptyOutDir: false,
    cssCodeSplit: false,
    assetsDir: ".",
    target: "es2020",
    rollupOptions: {
      output: {
        entryFileNames: "app.js",
        chunkFileNames: "[name].js",
        assetFileNames: (asset) => {
          const name = asset.names?.[0] ?? asset.name ?? "";
          return name.endsWith(".css") ? "styles.css" : "[name][extname]";
        },
      },
    },
  },
});
