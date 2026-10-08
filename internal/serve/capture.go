package serve

import (
	"crypto/subtle"
	"encoding/json"
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
	// captureTokenPrefix is the Authorization scheme the write side accepts.
	captureTokenPrefix = "Bearer "
)

// captureConfig is the /api/capture config document: whether the app should
// show the capture form, and the project a capture would be attributed to.
type captureConfig struct {
	Enabled bool   `json:"enabled"`
	Project string `json:"project,omitempty"`
}

// captureRequest is the JSON body of a capture write. Kind defaults to an idea
// when omitted, so a bare sentence is a valid capture.
type captureRequest struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// captureResult is the /api/capture write response: the stored capture and the
// detail URL the app navigates to.
type captureResult struct {
	ID   core.TicketID   `json:"id"`
	Kind core.TicketKind `json:"kind"`
	URL  string          `json:"url"`
}

// captureErrorJSON is the body of a rejected capture write.
type captureErrorJSON struct {
	Error string `json:"error"`
}

// routeCapture dispatches the capture config read (GET/HEAD) and the capture
// write (POST). Any other method is rejected.
func (s *Server) routeCapture(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.handleCaptureConfig(w, r)
	case http.MethodPost:
		s.handleCaptureSubmit(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeCaptureJSON(w, http.StatusMethodNotAllowed, captureErrorJSON{Error: "capture: method not allowed"})
	}
}

// captureEnabled reports whether the write side can accept a capture: a scoped
// project to attribute the idea to, a configured token, and the app service to
// store it. All-projects serving has no single target, so it disables capture.
func (s *Server) captureEnabled() bool {
	return s.options.Tasks != nil && s.options.Token != "" && !s.options.All && s.options.Project != ""
}

// handleCaptureConfig tells the app whether to show the form and which project
// it would write to. Reads are open, like the rest of the read side.
func (s *Server) handleCaptureConfig(w http.ResponseWriter, _ *http.Request) {
	enabled := s.captureEnabled()
	project := ""
	if enabled {
		project = string(s.options.Project)
	}
	writeCaptureJSON(w, http.StatusOK, captureConfig{Enabled: enabled, Project: project})
}

// handleCaptureSubmit stores one natural-language capture. It authenticates with
// the shared token, derives a title from the first line, and keeps the raw
// sentence as the body. Enrichment is deferred to grooming, so the capture
// returns as soon as it is stored.
func (s *Server) handleCaptureSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.captureEnabled() {
		writeCaptureJSON(w, http.StatusForbidden, captureErrorJSON{Error: "capture is not configured"})
		return
	}
	if !s.tokenOK(captureToken(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factotum capture"`)
		writeCaptureJSON(w, http.StatusUnauthorized, captureErrorJSON{Error: "capture: invalid token"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCaptureBytes)
	var req captureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCaptureJSON(w, http.StatusBadRequest, captureErrorJSON{Error: "capture: bad request"})
		return
	}
	kind, ok := captureKind(req.Kind)
	if !ok {
		writeCaptureJSON(w, http.StatusBadRequest, captureErrorJSON{Error: "capture: unknown kind"})
		return
	}
	title, body := ideaFromSentence(req.Text)
	if title == "" {
		writeCaptureJSON(w, http.StatusBadRequest, captureErrorJSON{Error: "capture: text is required"})
		return
	}
	capture, err := s.options.Tasks.Add(r.Context(), app.TicketInput{
		ProjectID:   s.options.Project,
		Kind:        kind,
		Title:       title,
		Description: body,
	})
	if err != nil {
		writeCaptureJSON(w, http.StatusInternalServerError, captureErrorJSON{Error: "capture: " + err.Error()})
		return
	}
	w.Header().Set("Location", taskOrIdeaURL(*capture))
	writeCaptureJSON(w, http.StatusCreated, captureResult{
		ID:   capture.ID,
		Kind: capture.Kind,
		URL:  taskOrIdeaURL(*capture),
	})
}

// captureKind validates the requested capture kind. Empty defaults to an idea;
// only human captures are accepted, so a client cannot store executable work
// through the write side.
func captureKind(raw string) (core.TicketKind, bool) {
	switch raw {
	case "", string(core.KindIdea):
		return core.KindIdea, true
	case string(core.KindBug):
		return core.KindBug, true
	default:
		return "", false
	}
}

// captureToken reads the shared token from the Authorization header. It is the
// only credential: a token in the query string or body is ignored, so it cannot
// leak through logs, a Referer header, or a form field.
func captureToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, captureTokenPrefix) {
		return ""
	}
	return strings.TrimSpace(header[len(captureTokenPrefix):])
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

// writeCaptureJSON writes a capture response with an explicit status, disabling
// caching like the other JSON projections.
func writeCaptureJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
