package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// maxMsgBytes bounds a message-transport request body so a runaway client
// cannot make the server buffer an unbounded payload. It is larger than the
// stored body limit because the sender's text rides inside a JSON envelope.
const maxMsgBytes = 64 << 10

// The message transport is a thin wrapper over app.MessageService: one endpoint
// per protocol verb, JSON in and out. Reads and writes both require the shared
// serve token, because private agent comms must not be world-readable to anyone
// who can reach the dashboard. The request keys (address, body, run_id, ...)
// match the `ft msg` flags, and an item is a superset of the `ft msg inbox`
// list entry (see msgWire); the envelopes match design section 9.

type msgLink struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type msgSendRequest struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Body    string    `json:"body"`
	ReplyTo string    `json:"reply_to"`
	Links   []msgLink `json:"links"`
}

type msgSendResponse struct {
	ID      string `json:"id"`
	Sent    bool   `json:"sent"`
	To      string `json:"to"`
	State   string `json:"state"`
	Project string `json:"project"`
}

type msgReadResponse struct {
	ID      string `json:"id"`
	Read    bool   `json:"read"`
	To      string `json:"to"`
	State   string `json:"state"`
	Project string `json:"project"`
}

// msgWire is the item shape for inbox/get/claim. It is a superset of the
// `ft msg inbox` list entry: it carries the same id/from/to/state/created_at/
// project plus body/task_id/reply_to, which claim needs and which a remote
// reader should not have to fetch separately. (The CLI's own get -o json is
// deliberately richer still to come; see t-ovcacn224s.)
type msgWire struct {
	ID        string `json:"id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	TaskID    string `json:"task_id,omitempty"`
	Body      string `json:"body"`
	ReplyTo   string `json:"reply_to,omitempty"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	Project   string `json:"project"`
}

type msgInboxResponse struct {
	Messages []msgWire `json:"messages"`
}

type msgGetResponse struct {
	Message msgWire `json:"message"`
}

type msgRegisterRequest struct {
	Actor     string `json:"actor"`
	Task      string `json:"task"`
	Harness   string `json:"harness"`
	Host      string `json:"host"`
	PID       int    `json:"pid"`
	CanInject bool   `json:"can_inject"`
	TTL       string `json:"ttl"`
}

type msgRegisterResponse struct {
	RunID      string `json:"run_id"`
	Project    string `json:"project"`
	Actor      string `json:"actor"`
	LeaseUntil string `json:"lease_until"`
}

type msgClaimRequest struct {
	RunID string `json:"run_id"`
	Actor string `json:"actor"`
	Task  string `json:"task"`
	Wait  string `json:"wait"`
	Lease string `json:"lease"`
}

type msgClaimResponse struct {
	Found   bool     `json:"found"`
	Message *msgWire `json:"message,omitempty"`
}

type msgAckRequest struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
	State string `json:"state"`
	Error string `json:"error"`
}

type msgNackRequest struct {
	ID     string `json:"id"`
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
}

type msgRunRequest struct {
	RunID string `json:"run_id"`
	TTL   string `json:"ttl"`
}

type msgIDRequest struct {
	ID string `json:"id"`
}

type msgOKResponse struct {
	OK bool `json:"ok"`
}

type msgErrorJSON struct {
	Error string `json:"error"`
}

// routeMessage dispatches the /api/msg/* transport. Auth runs first: the
// transport is disabled unless a scoped project and a token are configured, and
// an unauthenticated request is rejected before it can touch the store.
func (s *Server) routeMessage(w http.ResponseWriter, r *http.Request) {
	if !s.msgEnabled() {
		writeMsgJSON(w, http.StatusForbidden, msgErrorJSON{Error: "msg: transport is not configured"})
		return
	}
	if !s.tokenOK(captureToken(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factotum msg"`)
		writeMsgJSON(w, http.StatusUnauthorized, msgErrorJSON{Error: "msg: invalid token"})
		return
	}

	verb := strings.TrimPrefix(r.URL.Path, "/api/msg/")
	switch {
	case verb == "send":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgSend(w, r)
		}
	case verb == "inbox":
		if msgMethod(w, r, http.MethodGet) {
			s.handleMsgInbox(w, r)
		}
	case strings.HasPrefix(verb, "get/"):
		if msgMethod(w, r, http.MethodGet) {
			s.handleMsgGet(w, r, strings.TrimPrefix(verb, "get/"))
		}
	case verb == "read":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgRead(w, r)
		}
	case verb == "ack":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgAck(w, r)
		}
	case verb == "nack":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgNack(w, r)
		}
	case verb == "register":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgRegister(w, r)
		}
	case verb == "claim":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgClaim(w, r)
		}
	case verb == "heartbeat":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgHeartbeat(w, r)
		}
	case verb == "deregister":
		if msgMethod(w, r, http.MethodPost) {
			s.handleMsgDeregister(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

// msgEnabled reports whether the transport can accept a request: the message
// service to run it, a configured token, and one scoped project. All-projects
// serving has no single backend to message through, so it is disabled.
func (s *Server) msgEnabled() bool {
	return s.options.Messages != nil && s.options.Token != "" && !s.options.All && s.options.Project != ""
}

// msgMethod rejects any method other than want with a 405 and an Allow header.
func msgMethod(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method == want {
		return true
	}
	w.Header().Set("Allow", want)
	writeMsgJSON(w, http.StatusMethodNotAllowed, msgErrorJSON{Error: "msg: method not allowed"})
	return false
}

func (s *Server) handleMsgSend(w http.ResponseWriter, r *http.Request) {
	var req msgSendRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.To) == "" {
		s.msgError(w, fmt.Errorf("%w: to is required", core.ErrInvalid))
		return
	}
	input := app.SendMessageInput{
		ProjectID: s.options.Project,
		From:      msgActor(req.From),
		Target:    req.To,
		Body:      req.Body,
		Links:     msgLinks(req.Links),
	}
	if req.ReplyTo != "" {
		replyTo := core.MessageID(req.ReplyTo)
		input.ReplyTo = &replyTo
	}
	message, err := s.options.Messages.Send(r.Context(), input)
	if err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusCreated, msgSendResponse{
		ID:      string(message.ID),
		Sent:    true,
		To:      string(message.To),
		State:   string(message.State),
		Project: string(message.ProjectID),
	})
}

func (s *Server) handleMsgInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := app.InboxQuery{ProjectID: s.options.Project}
	if address := q.Get("address"); address != "" {
		a := core.Address(address)
		if err := core.ValidateAddress(a); err != nil {
			s.msgError(w, err)
			return
		}
		query.To = &a
	}
	if actor := q.Get("for"); actor != "" {
		a := core.ActorID(actor)
		query.Actor = &a
	}
	if run := q.Get("run"); run != "" {
		id := core.RunID(run)
		query.Run = &id
	}
	if limit := q.Get("limit"); limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil || parsed < 0 {
			s.msgError(w, fmt.Errorf("%w: limit must be a non-negative integer", core.ErrInvalid))
			return
		}
		query.Limit = parsed
	}
	states, err := msgStates(q["state"])
	if err != nil {
		s.msgError(w, err)
		return
	}
	query.States = states

	messages, err := s.options.Messages.Inbox(r.Context(), query)
	if err != nil {
		s.msgError(w, err)
		return
	}
	out := msgInboxResponse{Messages: make([]msgWire, 0, len(messages))}
	for _, message := range messages {
		out.Messages = append(out.Messages, msgWireOf(message))
	}
	writeMsgJSON(w, http.StatusOK, out)
}

func (s *Server) handleMsgGet(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		http.NotFound(w, r)
		return
	}
	message, err := s.options.Messages.Get(r.Context(), core.MessageID(id))
	if err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgGetResponse{Message: msgWireOf(message)})
}

func (s *Server) handleMsgRead(w http.ResponseWriter, r *http.Request) {
	var req msgIDRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	message, err := s.options.Messages.Read(r.Context(), core.MessageID(req.ID))
	if err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgReadResponse{
		ID:      string(message.ID),
		Read:    true,
		To:      string(message.To),
		State:   string(message.State),
		Project: string(message.ProjectID),
	})
}

func (s *Server) handleMsgAck(w http.ResponseWriter, r *http.Request) {
	var req msgAckRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	state := core.MessageState(req.State)
	if state != core.MessageRead && state != core.MessageFailed {
		s.msgError(w, fmt.Errorf("%w: state must be read or failed", core.ErrInvalid))
		return
	}
	if err := s.options.Messages.Ack(r.Context(), app.AckInput{
		ID:    core.MessageID(req.ID),
		RunID: core.RunID(req.RunID),
		State: state,
		Error: req.Error,
	}); err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgOKResponse{OK: true})
}

func (s *Server) handleMsgNack(w http.ResponseWriter, r *http.Request) {
	var req msgNackRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	if err := s.options.Messages.Nack(r.Context(), core.MessageID(req.ID), core.RunID(req.RunID), req.Reason); err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgOKResponse{OK: true})
}

func (s *Server) handleMsgRegister(w http.ResponseWriter, r *http.Request) {
	var req msgRegisterRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	ttl, err := msgDuration(req.TTL)
	if err != nil {
		s.msgError(w, err)
		return
	}
	in := app.RegisterRunInput{
		ProjectID: s.options.Project,
		ActorID:   core.ActorID(strings.TrimSpace(req.Actor)),
		Harness:   req.Harness,
		Host:      req.Host,
		PID:       req.PID,
		CanInject: req.CanInject,
		TTL:       ttl,
	}
	if req.Task != "" {
		task := core.TicketID(req.Task)
		in.TaskID = &task
	}
	run, err := s.options.Messages.RegisterRun(r.Context(), in)
	if err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgRegisterResponse{
		RunID:      string(run.ID),
		Project:    string(run.ProjectID),
		Actor:      string(run.ActorID),
		LeaseUntil: run.LeaseUntil.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleMsgClaim(w http.ResponseWriter, r *http.Request) {
	var req msgClaimRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	wait, err := msgDuration(req.Wait)
	if err != nil {
		s.msgError(w, err)
		return
	}
	lease, err := msgDuration(req.Lease)
	if err != nil {
		s.msgError(w, err)
		return
	}
	in := app.ClaimInput{
		ProjectID: s.options.Project,
		RunID:     core.RunID(strings.TrimSpace(req.RunID)),
		Wait:      wait,
		Lease:     lease,
	}
	if req.Actor != "" {
		actor := core.ActorID(req.Actor)
		in.ActorID = &actor
	}
	if req.Task != "" {
		task := core.TicketID(req.Task)
		in.TaskID = &task
	}
	message, err := s.options.Messages.Claim(r.Context(), in)
	if errors.Is(err, core.ErrNotFound) {
		writeMsgJSON(w, http.StatusOK, msgClaimResponse{Found: false})
		return
	}
	if err != nil {
		s.msgError(w, err)
		return
	}
	wire := msgWireOf(message)
	writeMsgJSON(w, http.StatusOK, msgClaimResponse{Found: true, Message: &wire})
}

func (s *Server) handleMsgHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req msgRunRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	ttl, err := msgDuration(req.TTL)
	if err != nil {
		s.msgError(w, err)
		return
	}
	if err := s.options.Messages.HeartbeatRun(r.Context(), core.RunID(strings.TrimSpace(req.RunID)), ttl); err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgOKResponse{OK: true})
}

func (s *Server) handleMsgDeregister(w http.ResponseWriter, r *http.Request) {
	var req msgRunRequest
	if !decodeMsgBody(w, r, &req) {
		return
	}
	if err := s.options.Messages.DeregisterRun(r.Context(), core.RunID(strings.TrimSpace(req.RunID))); err != nil {
		s.msgError(w, err)
		return
	}
	writeMsgJSON(w, http.StatusOK, msgOKResponse{OK: true})
}

// decodeMsgBody reads a bounded JSON body into v, writing a 400 and returning
// false on a malformed or oversized payload.
func decodeMsgBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxMsgBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeMsgJSON(w, http.StatusBadRequest, msgErrorJSON{Error: "msg: bad request"})
		return false
	}
	return true
}

// msgError maps a service error to an HTTP status: invalid input is 400, a
// missing entity is 404, and anything else is an internal error.
func (s *Server) msgError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, core.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, core.ErrNotFound):
		status = http.StatusNotFound
	}
	writeMsgJSON(w, status, msgErrorJSON{Error: "msg: " + err.Error()})
}

func writeMsgJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func msgWireOf(message *core.Message) msgWire {
	out := msgWire{
		ID:        string(message.ID),
		To:        string(message.To),
		Body:      message.Body,
		State:     string(message.State),
		CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339),
		Project:   string(message.ProjectID),
	}
	if message.From != nil {
		out.From = string(*message.From)
	}
	if message.TaskID != nil {
		out.TaskID = string(*message.TaskID)
	}
	if message.ReplyTo != nil {
		out.ReplyTo = string(*message.ReplyTo)
	}
	return out
}

// msgActor reads a sender actor id. An empty value means a system message.
func msgActor(raw string) *core.ActorID {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	id := core.ActorID(raw)
	return &id
}

func msgLinks(links []msgLink) []core.Link {
	out := make([]core.Link, 0, len(links))
	for _, link := range links {
		out = append(out, core.Link{Kind: core.LinkKind(link.Kind), URL: link.URL})
	}
	return out
}

func msgStates(values []string) ([]core.MessageState, error) {
	states := make([]core.MessageState, 0, len(values))
	for _, value := range values {
		state := core.MessageState(value)
		if !state.Valid() {
			return nil, fmt.Errorf("%w: unknown message state %q", core.ErrInvalid, value)
		}
		states = append(states, state)
	}
	return states, nil
}

// msgDuration parses a duration field. Empty means "use the default", which the
// app service applies.
func msgDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%w: invalid duration %q", core.ErrInvalid, raw)
	}
	return d, nil
}
