import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { playwright } from "@vitest/browser-playwright"
import { defineConfig } from "vitest/config"

// The browser config is separate from vite.config.ts so the jsdom unit suite
// (`pnpm test`) stays fast and browser-free. It runs the *.browser.test.tsx
// files in headless Chromium so tests can assert real layout (line wrapping,
// overflow, computed styles), which jsdom cannot evaluate.
export default defineConfig({
  // The Tailwind plugin must generate the utilities the components use, or the
  // layout under test is not the layout that ships.
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  test: {
    include: ["src/**/*.browser.test.{ts,tsx}"],
    browser: {
      enabled: true,
      provider: playwright(),
      headless: true,
      instances: [{ browser: "chromium" }],
    },
  },
})
