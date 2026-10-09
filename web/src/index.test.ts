import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { expect, test } from "vitest"

// Khoi 2026-10-08: the corner radius was too round. Lower the --radius token to
// a slight rounding - rounded, not pointed, not too round.
test("the --radius token is a slight rounding", () => {
  const css = readFileSync(resolve(process.cwd(), "src/index.css"), "utf8")
  const match = css.match(/--radius:\s*([0-9.]+)rem/)
  expect(match).not.toBeNull()
  const radius = Number(match![1])
  expect(radius).toBeGreaterThanOrEqual(0.25)
  expect(radius).toBeLessThanOrEqual(0.5)
})
