import { writeFileSync } from "node:fs"
import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, type Plugin } from "vite"

// keepGitkeep re-adds web/dist/.gitkeep after emptyOutDir wipes the directory,
// so a fresh clone always has something for `//go:embed all:dist` to match and
// git sees no spurious deletion after a build.
function keepGitkeep(): Plugin {
  return {
    name: "factotum-keep-gitkeep",
    closeBundle() {
      writeFileSync(
        path.resolve(import.meta.dirname, "dist/.gitkeep"),
        "# Placeholder so go:embed finds web/dist on a fresh clone.\n# `mise run build-web` replaces its contents with the built Vite bundle.\n",
      )
    },
  }
}

// The Go binary serves the built app under /app, so Vite must emit asset URLs
// with that base and write into web/dist, which web/web.go embeds.
export default defineConfig({
  base: "/app/",
  plugins: [react(), tailwindcss(), keepGitkeep()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8484",
      "/events": "http://127.0.0.1:8484",
    },
  },
})
