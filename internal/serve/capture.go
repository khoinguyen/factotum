package serve

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"strings"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

const (
	// maxCaptureBytes bounds a capture body, so a runaway client cannot make the
	// server buffer an unbounded form.
	maxCaptureBytes = 64 << 10
	// captureTitleMax caps the derived title at a readable length. The raw
	// sentence is always kept in full as the idea body.
	captureTitleMax = 100
)

// captureData is the state the capture page renders.
type captureData struct {
	CSS        template.CSS
	Title      string
	Project    string
	Configured bool
}

// routeCapture dispatches the capture page (GET/HEAD) and the capture write
// (POST). Any other method is rejected.
func (s *Server) routeCapture(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.handleCapturePage(w, r)
	case http.MethodPost:
		s.handleCaptureSubmit(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "capture: method not allowed", http.StatusMethodNotAllowed)
	}
}

// captureEnabled reports whether the write side can accept a capture: a scoped
// project to attribute the idea to, a configured token, and the app service to
// store it. All-projects serving has no single target, so it disables capture.
func (s *Server) captureEnabled() bool {
	return s.options.Tasks != nil && s.options.Token != "" && !s.options.All && s.options.Project != ""
}

func (s *Server) handleCapturePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "capture.html", &captureData{
		CSS:        dashboardCSS,
		Title:      "Capture an idea",
		Project:    string(s.options.Project),
		Configured: s.captureEnabled(),
	})
}

// handleCaptureSubmit stores one natural-language idea. It authenticates with
// the shared token, derives a title from the first line, and keeps the raw
// sentence as the body. Enrichment is deferred to grooming, so the capture
// returns as soon as the idea is stored.
func (s *Server) handleCaptureSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.captureEnabled() {
		http.Error(w, "capture is not configured", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCaptureBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "capture: bad form", http.StatusBadRequest)
		return
	}
	if !s.tokenOK(captureToken(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factotum capture"`)
		http.Error(w, "capture: invalid token", http.StatusUnauthorized)
		return
	}
	title, body := ideaFromSentence(r.FormValue("text"))
	if title == "" {
		http.Error(w, "capture: idea text is required", http.StatusBadRequest)
		return
	}
	idea, err := s.options.Tasks.Add(r.Context(), app.TaskInput{
		ProjectID:   s.options.Project,
		Kind:        core.KindIdea,
		Title:       title,
		Description: body,
	})
	if err != nil {
		http.Error(w, "capture: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, ideaURL(idea.ID), http.StatusSeeOther)
}

// captureToken reads the shared token from the Authorization header first, then
// the form field. A script uses the header; a browser form uses the field.
func captureToken(r *http.Request) string {
	const prefix = "Bearer "
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, prefix) {
		if token := strings.TrimSpace(header[len(prefix):]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(r.FormValue("token"))
}

// tokenOK compares the offered token to the configured one in constant time, so
// a wrong token reveals nothing about the right one. An empty configured token
// never matches: capture is disabled, not open.
func (s *Server) tokenOK(offered string) bool {
	if s.options.Token == "" || offered == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.options.Token), []byte(offered)) == 1
}

// ideaFromSentence splits a captured sentence into an idea title and body. The
// first line becomes the title, whitespace-collapsed and length-capped; the rest
// is kept verbatim as the body. A single-line capture keeps the whole sentence
// as its body, so a long one-liner is never truncated away.
func ideaFromSentence(raw string) (title, body string) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if raw == "" {
		return "", ""
	}
	first, rest, _ := strings.Cut(raw, "\n")
	title = truncateTitle(collapseSpaces(first))
	if rest = strings.TrimSpace(rest); rest == "" {
		rest = raw
	}
	return title, rest
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncateTitle caps a title at captureTitleMax runes, marking the cut with an
// ellipsis so the full sentence stays discoverable in the body.
func truncateTitle(s string) string {
	runes := []rune(s)
	if len(runes) <= captureTitleMax {
		return s
	}
	return string(runes[:captureTitleMax-1]) + "…"
}
