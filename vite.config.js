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
        // Prefixed so a chunk can never collide with the forced "app.js" entry
        // name. main.ts pulls app.ts in with a dynamic import(), and that chunk
        // is also named after its module ("app"); without the prefix Rollup
        // resolves the clash by emitting "app2.js", a name that shifts if the
        // module is ever renamed. go:embed and verify:web both reference these
        // files by fixed path, so the name has to be stable.
        //
        // Do NOT reach for manualChunks to force the split: main.ts and app.ts
        // share modules (vue, store, bridge, format), and assigning app.ts to
        // its own chunk drags those shared modules with it, leaving the entry
        // with a STATIC import of that chunk. Static imports are evaluated
        // before the entry's own body, which silently defeats the whole reason
        // the import is dynamic -- app.ts constructs a CodeMirror view against
        // the DOM at module scope and must not run before Vue has mounted.
        chunkFileNames: "chunk-[name].js",
        assetFileNames: (asset) => {
          const name = asset.names?.[0] ?? asset.name ?? "";
          return name.endsWith(".css") ? "styles.css" : "[name][extname]";
        },
      },
    },
  },
});
