// tokenKey is where the browser remembers the shared capture token, so a phone
// captures without retyping it. It is shared by the capture entry, the prompt
// modal, and the standalone capture page, so a token entered in either place is
// remembered everywhere.
const tokenKey = "factotum.capture.token"

export function loadCaptureToken(): string {
  try {
    return window.localStorage.getItem(tokenKey) ?? ""
  } catch {
    return ""
  }
}

export function saveCaptureToken(token: string): void {
  try {
    window.localStorage.setItem(tokenKey, token)
  } catch {
    // a private-mode browser may refuse storage; the write still works.
  }
}

export function clearCaptureToken(): void {
  try {
    window.localStorage.removeItem(tokenKey)
  } catch {
    // best-effort, like the write.
  }
}
