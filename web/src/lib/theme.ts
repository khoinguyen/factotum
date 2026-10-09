// Theme is the user's choice; "system" defers to the OS preference.
export type Theme = "light" | "dark" | "system"

// themeStorageKey is where the browser remembers the choice across reloads.
export const themeStorageKey = "factotum.theme"

// darkMedia is the OS dark-mode query; null where matchMedia is unavailable
// (jsdom, very old browsers), so callers can fall back to light.
export function darkMedia(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return null
  }
  return window.matchMedia("(prefers-color-scheme: dark)")
}

function isTheme(value: unknown): value is Theme {
  return value === "light" || value === "dark" || value === "system"
}

// loadTheme reads the stored choice, defaulting to "system" (follow the OS).
export function loadTheme(): Theme {
  try {
    const stored = window.localStorage.getItem(themeStorageKey)
    return isTheme(stored) ? stored : "system"
  } catch {
    return "system"
  }
}

// saveTheme records the choice; a private-mode browser may refuse the write.
export function saveTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(themeStorageKey, theme)
  } catch {
    // Storage is best-effort; the in-memory theme still applies this session.
  }
}

// resolveTheme collapses a Theme and the OS preference to the actual palette.
export function resolveTheme(theme: Theme, prefersDark: boolean): "light" | "dark" {
  if (theme === "system") return prefersDark ? "dark" : "light"
  return theme
}

// applyTheme toggles the .dark class on <html>, which the token set keys off.
export function applyTheme(theme: "light" | "dark"): void {
  document.documentElement.classList.toggle("dark", theme === "dark")
}

// initTheme applies the stored theme before the first paint to avoid a flash.
export function initTheme(): Theme {
  const theme = loadTheme()
  applyTheme(resolveTheme(theme, darkMedia()?.matches ?? false))
  return theme
}
