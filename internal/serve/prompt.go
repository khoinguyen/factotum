package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// promptRequest is the JSON body of a prompt write: the free-form prompt the
// human typed into the web app.
type promptRequest struct {
	Text string `json:"text"`
}

// promptResult is the /api/prompt write response: the stored document artifact
// and the doc URL the app opens.
type promptResult struct {
	ID  core.ArtifactID `json:"id"`
	URL string          `json:"url"`
}

// routePrompt dispatches the prompt write. Only POST is accepted: unlike the
// capture form, there is no config read (the app reuses /api/capture's).
func (s *Server) routePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeCaptureJSON(w, http.StatusMethodNotAllowed, captureErrorJSON{Error: "prompt: method not allowed"})
		return
	}
	s.handlePromptSubmit(w, r)
}

// promptEnabled reports whether a prompt can be stored and announced: a scoped
// project to attribute the artifact to, a configured token, and both the
// artifact and message services to store it and tell the chief. All-projects
// serving has no single target, so it disables prompt.
func (s *Server) promptEnabled() bool {
	return s.options.Artifacts != nil && s.options.Messages != nil &&
		s.options.Token != "" && !s.options.All && s.options.Project != ""
}

// handlePromptSubmit stores one free-form prompt as a document artifact and
// ft-msgs the chief, naming that artifact. The artifact is durable before the
// message is sent, so the chief can always resolve what it is told about; a
// failed send is reported but leaves the artifact stored for a manual retry.
func (s *Server) handlePromptSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.promptEnabled() {
		writeCaptureJSON(w, http.StatusForbidden, captureErrorJSON{Error: "prompt is not configured"})
		return
	}
	if !s.tokenOK(captureToken(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factotum prompt"`)
		writeCaptureJSON(w, http.StatusUnauthorized, captureErrorJSON{Error: "prompt: invalid token"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCaptureBytes)
	var req promptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCaptureJSON(w, http.StatusBadRequest, captureErrorJSON{Error: "prompt: bad request"})
		return
	}
	title, _ := ideaFromSentence(req.Text)
	body := strings.TrimSpace(req.Text)
	if title == "" || body == "" {
		writeCaptureJSON(w, http.StatusBadRequest, captureErrorJSON{Error: "prompt: text is required"})
		return
	}
	artifact, err := s.options.Artifacts.Add(r.Context(), app.ArtifactInput{
		ProjectID: s.options.Project,
		Kind:      core.ArtifactPrompt,
		Title:     title,
		Body:      body,
	})
	if err != nil {
		writeCaptureJSON(w, http.StatusInternalServerError, captureErrorJSON{Error: "prompt: " + err.Error()})
		return
	}
	url := artifactURL(artifact)
	if _, err := s.options.Messages.Send(r.Context(), app.SendMessageInput{
		ProjectID: s.options.Project,
		Target:    "chief",
		Body:      fmt.Sprintf("Web prompt captured: %s (%s)", artifact.Title, artifact.ID),
		Links:     []core.Link{{Kind: core.LinkDoc, URL: url, Title: artifact.Title}},
	}); err != nil {
		writeCaptureJSON(w, http.StatusInternalServerError, captureErrorJSON{Error: "prompt: notify chief: " + err.Error()})
		return
	}
	w.Header().Set("Location", url)
	writeCaptureJSON(w, http.StatusCreated, promptResult{ID: artifact.ID, URL: url})
}
