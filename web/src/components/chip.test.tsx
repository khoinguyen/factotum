import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"

import { Chip } from "@/components/chip"

afterEach(cleanup)

// QA of the human-next work (art-jgcmwjwoz7) found the same needs-human state
// rendered red in `ft graph render --format html` and violet here. Violet won:
// red is the cycle tone, while needs-human is a handoff to a person. This pins
// the web side of that one-tone rule; pkg/render asserts the other side.
test("a needs-human chip is violet, not an alert red", () => {
  render(<Chip label="needs human" toneKey="needs-human" />)

  const chip = screen.getByText("needs human")
  expect(chip.className).toContain("violet")
  expect(chip.className).not.toContain("red")
})
