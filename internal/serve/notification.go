package serve

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// notificationView is one completed prompt grooming the web app announces: the
// prompt artifact's id and title, the page to open (always the prompt artifact
// page), and how many captures linked back to it.
type notificationView struct {
	ID          core.ArtifactID `json:"id"`
	Title       string          `json:"title"`
	URL         string          `json:"url"`
	Captures    int             `json:"captures"`
	CompletedAt string          `json:"completed_at,omitempty"`
}

// notificationsDoc is the /api/notifications document: the completed prompt
// groomings, oldest first, so a client can toast each new one once.
type notificationsDoc struct {
	Notifications []notificationView `json:"notifications"`
}

// handleNotifications serves the completed prompt groomings. A prompt's grooming
// is complete once at least one capture links back to it; until then the prompt
// is still in flight and is not announced.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	notifications, err := s.completedPromptGroomings(r.Context())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	s.writeJSON(w, notificationsDoc{Notifications: notifications})
}

// completedPromptGroomings projects every groomed prompt artifact into a
// notification. It reads each prompt's linked captures through the same reverse
// lookup the prompt artifact page uses, so the toast and the page never
// disagree. Both reads span one project, or every project on an all-projects
// server.
func (s *Server) completedPromptGroomings(ctx context.Context) ([]notificationView, error) {
	kind := core.ArtifactPrompt
	artifactFilter := store.ArtifactFilter{Kind: &kind}
	ticketFilter := store.TicketFilter{}
	if !s.options.All {
		artifactFilter.ProjectID = s.options.Project
		ticketFilter.ProjectID = s.options.Project
	}
	prompts, err := s.options.Backend.Artifacts().List(ctx, artifactFilter)
	if err != nil {
		return nil, err
	}
	out := make([]notificationView, 0, len(prompts))
	if len(prompts) == 0 {
		return out, nil
	}
	tickets, err := s.options.Backend.Tickets().List(ctx, ticketFilter)
	if err != nil {
		return nil, err
	}
	byPage := capturesByPagePath(tickets)
	for _, prompt := range prompts {
		captures := byPage[artifactURL(prompt)]
		if len(captures) == 0 {
			continue
		}
		out = append(out, notificationView{
			ID:          prompt.ID,
			Title:       prompt.Title,
			URL:         artifactURL(prompt),
			Captures:    len(captures),
			CompletedAt: latestUpdate(captures),
		})
	}
	// Oldest first, with the id as a stable tiebreak, so a client that records
	// what it has shown sees new completions in order and never re-orders.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CompletedAt != out[j].CompletedAt {
			return out[i].CompletedAt < out[j].CompletedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// linkedCaptures projects the captures that link back to a prompt artifact for
// its page. It reads the artifact's own project, so a scoped and an
// all-projects server agree for the same prompt.
func (s *Server) linkedCaptures(ctx context.Context, artifact *core.Artifact) ([]taskLink, error) {
	tickets, err := s.options.Backend.Tickets().List(ctx, store.TicketFilter{ProjectID: artifact.ProjectID})
	if err != nil {
		return nil, err
	}
	captures := capturesByPagePath(tickets)[artifactURL(artifact)]
	out := make([]taskLink, 0, len(captures))
	for _, capture := range captures {
		out = append(out, taskRefLink(capture))
	}
	return out, nil
}

// capturesByPagePath indexes the captures that link back to a prompt artifact by
// the page path their note links name: a capture links back with a doc link to
// the artifact's page, the provenance the grooming agent records. Captures under
// one page are sorted by id for a stable count and page.
func capturesByPagePath(tickets []*core.Ticket) map[string][]*core.Ticket {
	byPage := make(map[string][]*core.Ticket)
	for _, ticket := range tickets {
		if !ticket.Kind.CapturedByHuman() {
			continue
		}
		seen := make(map[string]bool)
		for _, note := range ticket.Notes {
			for _, link := range note.Links {
				if link.Kind != core.LinkDoc {
					continue
				}
				page := linkPagePath(link.URL)
				if page == "" || seen[page] {
					continue
				}
				seen[page] = true
				byPage[page] = append(byPage[page], ticket)
			}
		}
	}
	for page := range byPage {
		captures := byPage[page]
		sort.Slice(captures, func(i, j int) bool { return captures[i].ID < captures[j].ID })
	}
	return byPage
}

// linkPagePath returns the path a doc link points at, dropping any scheme, host,
// or query, so an absolute or query-carrying link still matches the artifact's
// page path.
func linkPagePath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		return raw
	}
	return parsed.Path
}

// latestUpdate returns the most recent UpdatedAt among tickets as an RFC3339
// timestamp, or "" when there are none.
func latestUpdate(tickets []*core.Ticket) string {
	var latest time.Time
	for _, ticket := range tickets {
		if ticket.UpdatedAt.After(latest) {
			latest = ticket.UpdatedAt
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
}
