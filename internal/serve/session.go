package serve

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// deliveredView is the work one chief session drove, read from the events
// attributed to it: the tasks it carried to done, the pull requests recorded on
// those tasks, and the captures advanced by delivering their promoted work.
type deliveredView struct {
	TasksDone        int        `json:"tasks_done"`
	PRsMerged        int        `json:"prs_merged"`
	CapturesAdvanced int        `json:"captures_advanced"`
	Tasks            []taskLink `json:"tasks"`
	PRs              []linkView `json:"prs"`
}

// sessionView is one chief session projected for the sessions page: its
// lifecycle (alive or retired, when it started and stopped, why) and the work it
// delivered. A session is defined by the events that carry its id, so a session
// whose events exist but whose start was not recorded still appears.
type sessionView struct {
	ID              string        `json:"id"`
	State           string        `json:"state"`
	StartedAt       string        `json:"started_at,omitempty"`
	StoppedAt       string        `json:"stopped_at,omitempty"`
	DurationSeconds int           `json:"duration_seconds"`
	Reason          string        `json:"reason,omitempty"`
	Successor       string        `json:"successor,omitempty"`
	Delivered       deliveredView `json:"delivered"`
}

// sessionsJSON is the /api/sessions document the app renders.
type sessionsJSON struct {
	Sessions []sessionView `json:"sessions"`
}

// sessionAcc accumulates one session's events while the log is folded.
type sessionAcc struct {
	started *core.Event
	retired *core.Event
	first   time.Time
	done    map[core.TicketID]bool
}

// handleSessions serves the chief-session timeline as JSON. Like every read
// route it answers GET and HEAD only.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.sessions(r.Context())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, fmt.Errorf("sessions: %w", err))
		return
	}
	s.writeJSON(w, sessionsJSON{Sessions: sessions})
}

// sessions folds the event log into one projection per chief session. Every
// event carrying a Session belongs to that session's timeline; the session
// events bracket it (start, retire), and the work events inside it are the
// delivered work. The snapshot resolves the done tasks' titles and pull-request
// links, which the events carry only by ticket id.
func (s *Server) sessions(ctx context.Context) ([]sessionView, error) {
	now := s.options.Clock.Now()
	scope := core.ProjectID("")
	if !s.options.All {
		scope = s.options.Project
	}
	events, err := s.options.Backend.Events().List(ctx, store.EventFilter{ProjectID: scope})
	if err != nil {
		return nil, err
	}
	var snapshot *app.Snapshot
	if s.options.All {
		snapshot, err = app.LoadAllSnapshot(ctx, s.options.Backend, now)
	} else {
		snapshot, err = app.LoadSnapshot(ctx, s.options.Backend, scope, now)
	}
	if err != nil {
		return nil, err
	}
	vs := s.buildViews(ctx, snapshot, nil)

	byID := make(map[string]*sessionAcc)
	order := make([]string, 0)
	for _, event := range events {
		sid := event.Session
		if sid == "" {
			continue
		}
		acc := byID[sid]
		if acc == nil {
			acc = &sessionAcc{done: make(map[core.TicketID]bool)}
			byID[sid] = acc
			order = append(order, sid)
		}
		if acc.first.IsZero() || event.CreatedAt.Before(acc.first) {
			acc.first = event.CreatedAt
		}
		switch event.Kind {
		case core.EventSessionStarted:
			if acc.started == nil {
				acc.started = event
			}
		case core.EventSessionRetired:
			if acc.retired == nil {
				acc.retired = event
			}
		case core.EventTaskStatusChanged:
			if event.TicketID != nil && eventDataString(event.Data, "to") == string(core.StatusDone) {
				acc.done[*event.TicketID] = true
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return sessionStart(byID[order[i]]).After(sessionStart(byID[order[j]]))
	})
	out := make([]sessionView, 0, len(order))
	for _, sid := range order {
		out = append(out, s.sessionViewOf(ctx, sid, byID[sid], vs, now))
	}
	return out, nil
}

// sessionViewOf projects one accumulated session. start is the recorded start,
// falling back to the earliest event for a session whose start was not logged;
// duration runs to now while the session is alive and to its retirement once it
// stopped.
func (s *Server) sessionViewOf(ctx context.Context, sid string, acc *sessionAcc, vs viewSet, now time.Time) sessionView {
	start := sessionStart(acc)
	view := sessionView{ID: sid, State: "alive"}
	if !start.IsZero() {
		view.StartedAt = start.UTC().Format(time.RFC3339)
	}
	end := now
	if acc.retired != nil {
		view.State = "retired"
		end = acc.retired.CreatedAt
		view.StoppedAt = end.UTC().Format(time.RFC3339)
		if successor := eventDataString(acc.retired.Data, "successor"); successor != "" {
			view.Successor = successor
			view.Reason = "retired for " + successor
		} else {
			view.Reason = "retired"
		}
	}
	if !start.IsZero() && end.After(start) {
		view.DurationSeconds = int(end.Sub(start).Seconds())
	}
	view.Delivered = s.deliveredOf(ctx, acc.done, vs)
	return view
}

// deliveredOf resolves a session's done tickets into the delivered-work
// projection: each done task as a link, its pull-request links deduplicated, and
// the distinct captures those tasks were promoted from.
func (s *Server) deliveredOf(ctx context.Context, done map[core.TicketID]bool, vs viewSet) deliveredView {
	out := deliveredView{Tasks: []taskLink{}, PRs: []linkView{}}
	ids := make([]core.TicketID, 0, len(done))
	for id := range done {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out.TasksDone = len(ids)

	captures := make(map[core.TicketID]bool)
	seenPR := make(map[string]bool)
	for _, id := range ids {
		link, task, ok := s.deliveredTicket(ctx, id, vs)
		if !ok {
			continue
		}
		out.Tasks = append(out.Tasks, link)
		if origin, ok := vs.origin[id]; ok {
			captures[origin.ID] = true
		}
		for _, note := range task.Notes {
			for _, edge := range note.Links {
				if edge.Kind != core.LinkPR || edge.URL == "" || seenPR[edge.URL] {
					continue
				}
				seenPR[edge.URL] = true
				out.PRs = append(out.PRs, linkView{Kind: string(edge.Kind), URL: edge.URL, Title: edge.Title})
			}
		}
	}
	out.CapturesAdvanced = len(captures)
	out.PRsMerged = len(out.PRs)
	return out
}

// deliveredTicket resolves a ticket the session drove to done to its snapshot
// record and link. A ticket the scoped snapshot omitted (for example a deleted
// one) is fetched so its notes still contribute; an unresolvable id is skipped.
func (s *Server) deliveredTicket(ctx context.Context, id core.TicketID, vs viewSet) (taskLink, core.Ticket, bool) {
	if task, ok := vs.byID[id]; ok {
		return taskLinkOf(vs.views[id]), task, true
	}
	task, err := s.options.Backend.Tickets().Get(ctx, id)
	if err != nil {
		return taskLink{}, core.Ticket{}, false
	}
	return taskRefLink(task), *task, true
}

// sessionStart is the session's start time: the recorded start event, or the
// earliest event carrying the session when no start was logged.
func sessionStart(acc *sessionAcc) time.Time {
	if acc.started != nil {
		return acc.started.CreatedAt
	}
	return acc.first
}

// eventDataString reads a string field from an event's Data map. Backends that
// persist events round-trip the map through JSON, so a value may already be a
// string or still be the Go value written in-process; both are handled.
func eventDataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
