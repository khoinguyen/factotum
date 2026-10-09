import { useCallback, useEffect, useState } from "react"

import {
  applyTheme,
  darkMedia,
  loadTheme,
  resolveTheme,
  saveTheme,
  type Theme,
} from "@/lib/theme"

// useTheme owns the current theme and keeps <html> in sync. While the choice is
// "system", it tracks the OS preference and re-applies on change.
export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(loadTheme)

  useEffect(() => {
    const media = darkMedia()
    const sync = () => applyTheme(resolveTheme(theme, media?.matches ?? false))
    sync()
    if (theme !== "system" || !media) return
    media.addEventListener("change", sync)
    return () => media.removeEventListener("change", sync)
  }, [theme])

  const setTheme = useCallback((next: Theme) => {
    saveTheme(next)
    setThemeState(next)
  }, [])

  return { theme, setTheme }
}
