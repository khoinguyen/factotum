package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// messageSendResult is the single-result shape of `ft msg send`.
type messageSendResult struct {
	ID      string `json:"id" yaml:"id"`
	Sent    bool   `json:"sent" yaml:"sent"`
	To      string `json:"to" yaml:"to"`
	State   string `json:"state" yaml:"state"`
	Project string `json:"project" yaml:"project"`
}

// messageReadResult is the single-result shape of `ft msg read`.
type messageReadResult struct {
	ID      string `json:"id" yaml:"id"`
	Read    bool   `json:"read" yaml:"read"`
	To      string `json:"to" yaml:"to"`
	State   string `json:"state" yaml:"state"`
	Project string `json:"project" yaml:"project"`
}

// messageEntry is one row of `ft msg inbox`.
type messageEntry struct {
	ID      string `json:"id" yaml:"id"`
	From    string `json:"from,omitempty" yaml:"from,omitempty"`
	To      string `json:"to" yaml:"to"`
	TaskID  string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	State   string `json:"state" yaml:"state"`
	Created string `json:"created_at" yaml:"created_at"`
	Project string `json:"project" yaml:"project"`
}

func newMessageCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "msg", Short: "Send and read durable agent messages"}

	var sendProject, sendFrom, sendBody, sendReplyTo string
	var sendLinks []string
	send := &cobra.Command{
		Use:   "send <address|ticket-id> [text]",
		Short: "Send a message to an actor, run, or task",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := deps.resolveProject(sendProject)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			body := sendBody
			if len(args) == 2 {
				body = args[1]
			}
			if body == "-" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read message body: %w", err)
				}
				body = string(data)
			}
			if strings.TrimSpace(body) == "" {
				return usageError(cmd, "message body is required (positional text, --body, or -)")
			}
			links, err := parseMessageLinks(sendLinks)
			if err != nil {
				return err
			}
			from, err := deps.resolveMessageFrom(cmd.Context(), sendFrom)
			if err != nil {
				return err
			}
			input := app.SendMessageInput{
				ProjectID: project,
				From:      from,
				Target:    args[0],
				Body:      body,
				Links:     links,
			}
			if sendReplyTo != "" {
				replyTo := core.MessageID(sendReplyTo)
				input.ReplyTo = &replyTo
			}
			message, err := deps.Messages.Send(cmd.Context(), input)
			if err != nil {
				return err
			}
			result := messageSendResult{ID: string(message.ID), Sent: true, To: string(message.To), State: string(message.State), Project: string(message.ProjectID)}
			return deps.emit(result, func() {
				deps.printFields(f("message_id", message.ID), f("sent", true), f("to", message.To), f("state", message.State), f("project", message.ProjectID))
			}, hint{Command: fmt.Sprintf("ft msg inbox --project %s", message.ProjectID), About: "see the inbox"})
		},
	}
	send.Flags().StringVarP(&sendProject, "project", "p", "", "project id (required)")
	send.Flags().StringVar(&sendFrom, "from", "", "sender actor (id or name; defaults to the configured actor)")
	send.Flags().StringVarP(&sendBody, "body", "b", "", "message body (use - to read stdin)")
	send.Flags().StringArrayVar(&sendLinks, "link", nil, "attach a link as kind=url (repeatable)")
	send.Flags().StringVar(&sendReplyTo, "reply-to", "", "message id this message replies to")

	var inboxProject, inboxAddress, inboxActor, inboxRun string
	var inboxStates []string
	var inboxLimit int
	inbox := &cobra.Command{
		Use:   "inbox",
		Short: "List messages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(inboxProject)
			states, err := parseMessageStates(inboxStates)
			if err != nil {
				return err
			}
			query := app.InboxQuery{ProjectID: project, States: states, Limit: inboxLimit}
			if inboxAddress != "" {
				address := core.Address(inboxAddress)
				if err := core.ValidateAddress(address); err != nil {
					return err
				}
				query.To = &address
			}
			if inboxActor != "" {
				actor := core.ActorID(inboxActor)
				query.Actor = &actor
			}
			if inboxRun != "" {
				run := core.RunID(inboxRun)
				query.Run = &run
			}
			messages, err := deps.Messages.Inbox(cmd.Context(), query)
			if err != nil {
				return err
			}
			entries := make([]messageEntry, 0, len(messages))
			for _, message := range messages {
				entries = append(entries, messageEntryFrom(message))
			}
			return deps.emit(entries, func() {
				rows := make([][]string, 0, len(entries))
				for _, entry := range entries {
					rows = append(rows, []string{entry.ID, entry.From, entry.To, entry.State, entry.Created, entry.Project})
				}
				deps.printTable([]string{"ID", "FROM", "TO", "STATE", "CREATED", "PROJECT"}, rows)
			})
		},
	}
	inbox.Flags().StringVarP(&inboxProject, "project", "p", "", "filter by project id")
	inbox.Flags().StringArrayVar(&inboxStates, "state", nil, "filter by state (queued, delivered, read, failed; repeatable)")
	inbox.Flags().StringVar(&inboxAddress, "address", "", "exact stored address to match")
	inbox.Flags().StringVar(&inboxActor, "for", "", "messages claimable by this actor")
	inbox.Flags().StringVar(&inboxRun, "run", "", "messages claimable by this run")
	inbox.Flags().IntVar(&inboxLimit, "limit", 0, "maximum number of messages")

	get := &cobra.Command{
		Use:   "get <message>",
		Short: "Show a message without changing its state",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := deps.Messages.Get(cmd.Context(), core.MessageID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(messageEntryFrom(message), func() { printMessageBlock(deps, message) })
		},
	}

	read := &cobra.Command{
		Use:   "read <message>",
		Short: "Show a message and mark it read",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := deps.Messages.Read(cmd.Context(), core.MessageID(args[0]))
			if err != nil {
				return err
			}
			result := messageReadResult{ID: string(message.ID), Read: true, To: string(message.To), State: string(message.State), Project: string(message.ProjectID)}
			return deps.emit(result, func() {
				deps.printFields(f("message_id", message.ID), f("read", true), f("to", message.To), f("state", message.State), f("project", message.ProjectID))
			})
		},
	}

	cmd.AddCommand(send, inbox, get, read, newMessageAgentCommand(deps))
	return cmd
}

// agentRegisterResult is the JSON shape `ft msg agent register` returns.
type agentRegisterResult struct {
	RunID      string `json:"run_id" yaml:"run_id"`
	Project    string `json:"project" yaml:"project"`
	Actor      string `json:"actor" yaml:"actor"`
	LeaseUntil string `json:"lease_until" yaml:"lease_until"`
}

// agentMessage is the wire shape of a claimed message for the receiver verbs.
type agentMessage struct {
	ID        string `json:"id" yaml:"id"`
	From      string `json:"from,omitempty" yaml:"from,omitempty"`
	To        string `json:"to" yaml:"to"`
	TaskID    string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Body      string `json:"body" yaml:"body"`
	ReplyTo   string `json:"reply_to,omitempty" yaml:"reply_to,omitempty"`
	State     string `json:"state" yaml:"state"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}

// agentClaimResult is the JSON shape `ft msg agent claim` returns. Found is
// false (exit 0) when nothing is claimable within the wait, so a poll loop
// distinguishes "empty" from "error".
type agentClaimResult struct {
	Found   bool          `json:"found" yaml:"found"`
	Message *agentMessage `json:"message,omitempty" yaml:"message,omitempty"`
}

// agentOKResult is the JSON shape of the finalizing agent verbs.
type agentOKResult struct {
	OK    bool   `json:"ok" yaml:"ok"`
	ID    string `json:"id,omitempty" yaml:"id,omitempty"`
	State string `json:"state,omitempty" yaml:"state,omitempty"`
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
}

// newMessageAgentCommand is the hidden, stable adapter interface the receiver
// plugins shell out to when they cannot speak HTTP. Every verb is one store
// protocol call, with JSON-first output so a plugin parses one shape.
func newMessageAgentCommand(deps *Deps) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Receiver protocol verbs (register, claim, ack, nack, heartbeat, deregister)",
		Hidden: true,
	}
	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "project id (defaults to the configured project)")

	var regActor, regTask, regHarness, regHost string
	var regPID int
	var regTTL time.Duration
	var regCanInject bool
	register := &cobra.Command{
		Use:   "register",
		Short: "Register a receiver session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID := deps.resolveProject(project)
			if err := requireProject(cmd, projectID); err != nil {
				return err
			}
			if regActor == "" {
				return usageError(cmd, "--actor is required")
			}
			in := app.RegisterRunInput{
				ProjectID: projectID,
				ActorID:   core.ActorID(regActor),
				Harness:   regHarness,
				Host:      regHost,
				PID:       regPID,
				CanInject: regCanInject,
				TTL:       regTTL,
			}
			if regTask != "" {
				task := core.TicketID(regTask)
				in.TaskID = &task
			}
			run, err := deps.Messages.RegisterRun(cmd.Context(), in)
			if err != nil {
				return err
			}
			result := agentRegisterResult{
				RunID:      string(run.ID),
				Project:    string(run.ProjectID),
				Actor:      string(run.ActorID),
				LeaseUntil: run.LeaseUntil.UTC().Format(time.RFC3339),
			}
			return deps.emit(result, func() {
				deps.printFields(f("run_id", result.RunID), f("actor", result.Actor), f("lease_until", result.LeaseUntil), f("project", result.Project))
			})
		},
	}
	register.Flags().StringVar(&regActor, "actor", "", "actor id (required)")
	register.Flags().StringVar(&regTask, "task", "", "task this session is working (optional)")
	register.Flags().StringVar(&regHarness, "harness", "", "harness name (opencode, pi, ...)")
	register.Flags().StringVar(&regHost, "host", defaultHost(), "machine identity")
	register.Flags().IntVar(&regPID, "pid", os.Getpid(), "process id")
	register.Flags().BoolVar(&regCanInject, "can-inject", false, "the adapter can accept an out-of-band injection")
	register.Flags().DurationVar(&regTTL, "ttl", 0, "run lease (default 90s)")

	var claimRun, claimActor, claimTask string
	var claimWait, claimLease time.Duration
	claim := &cobra.Command{
		Use:   "claim",
		Short: "Claim the next message for a run (long-poll)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID := deps.resolveProject(project)
			if err := requireProject(cmd, projectID); err != nil {
				return err
			}
			if claimRun == "" {
				return usageError(cmd, "--run is required")
			}
			in := app.ClaimInput{ProjectID: projectID, RunID: core.RunID(claimRun), Wait: claimWait, Lease: claimLease}
			if claimActor != "" {
				actor := core.ActorID(claimActor)
				in.ActorID = &actor
			}
			if claimTask != "" {
				task := core.TicketID(claimTask)
				in.TaskID = &task
			}
			message, err := deps.Messages.Claim(cmd.Context(), in)
			if errors.Is(err, core.ErrNotFound) {
				return deps.emit(agentClaimResult{Found: false}, func() { deps.printFields(f("found", false)) })
			}
			if err != nil {
				return err
			}
			result := agentClaimResult{Found: true, Message: agentMessageFrom(message)}
			return deps.emit(result, func() {
				deps.printFields(f("found", true), f("message_id", message.ID), f("to", message.To), f("state", message.State))
			})
		},
	}
	claim.Flags().StringVar(&claimRun, "run", "", "run id (required)")
	claim.Flags().StringVar(&claimActor, "actor", "", "the run's actor address")
	claim.Flags().StringVar(&claimTask, "task", "", "the run's task address")
	claim.Flags().DurationVar(&claimWait, "wait", 0, "long-poll window (default none)")
	claim.Flags().DurationVar(&claimLease, "lease", 0, "delivered lease (default 30s)")

	var ackRun, ackState, ackError string
	ack := &cobra.Command{
		Use:   "ack <message>",
		Short: "Finalize a delivered message (read|failed)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ackRun == "" {
				return usageError(cmd, "--run is required")
			}
			state := core.MessageState(ackState)
			if state != core.MessageRead && state != core.MessageFailed {
				return usageError(cmd, "--state must be read or failed")
			}
			in := app.AckInput{ID: core.MessageID(args[0]), RunID: core.RunID(ackRun), State: state, Error: ackError}
			if err := deps.Messages.Ack(cmd.Context(), in); err != nil {
				return err
			}
			result := agentOKResult{OK: true, ID: args[0], State: string(state), Error: ackError}
			return deps.emit(result, func() { deps.printFields(f("ok", true), f("message_id", args[0]), f("state", state)) })
		},
	}
	ack.Flags().StringVar(&ackRun, "run", "", "run id (required)")
	ack.Flags().StringVar(&ackState, "state", "read", "read or failed")
	ack.Flags().StringVarP(&ackError, "error", "e", "", "failure reason when --state failed")

	var nackRun, nackError string
	nack := &cobra.Command{
		Use:   "nack <message>",
		Short: "Requeue (or park failed) a delivered message",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if nackRun == "" {
				return usageError(cmd, "--run is required")
			}
			if err := deps.Messages.Nack(cmd.Context(), core.MessageID(args[0]), core.RunID(nackRun), nackError); err != nil {
				return err
			}
			result := agentOKResult{OK: true, ID: args[0], Error: nackError}
			return deps.emit(result, func() { deps.printFields(f("ok", true), f("message_id", args[0])) })
		},
	}
	nack.Flags().StringVar(&nackRun, "run", "", "run id (required)")
	nack.Flags().StringVarP(&nackError, "error", "e", "", "reason")

	var beatRun string
	var beatTTL time.Duration
	heartbeat := &cobra.Command{
		Use:   "heartbeat",
		Short: "Renew a run lease",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if beatRun == "" {
				return usageError(cmd, "--run is required")
			}
			if err := deps.Messages.HeartbeatRun(cmd.Context(), core.RunID(beatRun), beatTTL); err != nil {
				return err
			}
			return deps.emit(agentOKResult{OK: true}, func() { deps.printFields(f("ok", true)) })
		},
	}
	heartbeat.Flags().StringVar(&beatRun, "run", "", "run id (required)")
	heartbeat.Flags().DurationVar(&beatTTL, "ttl", 0, "lease to renew (default 90s)")

	var dropRun string
	deregister := &cobra.Command{
		Use:   "deregister",
		Short: "Remove a run on clean shutdown",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dropRun == "" {
				return usageError(cmd, "--run is required")
			}
			if err := deps.Messages.DeregisterRun(cmd.Context(), core.RunID(dropRun)); err != nil {
				return err
			}
			return deps.emit(agentOKResult{OK: true}, func() { deps.printFields(f("ok", true)) })
		},
	}
	deregister.Flags().StringVar(&dropRun, "run", "", "run id (required)")

	cmd.AddCommand(register, claim, ack, nack, heartbeat, deregister)
	return cmd
}

func agentMessageFrom(message *core.Message) *agentMessage {
	m := &agentMessage{
		ID:        string(message.ID),
		To:        string(message.To),
		Body:      message.Body,
		State:     string(message.State),
		CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339),
	}
	if message.From != nil {
		m.From = string(*message.From)
	}
	if message.TaskID != nil {
		m.TaskID = string(*message.TaskID)
	}
	if message.ReplyTo != nil {
		m.ReplyTo = string(*message.ReplyTo)
	}
	return m
}

// defaultHost names the machine a run registered from. A hostname failure is
// not fatal: the empty string still registers.
func defaultHost() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

func messageEntryFrom(message *core.Message) messageEntry {
	entry := messageEntry{
		ID:      string(message.ID),
		To:      string(message.To),
		State:   string(message.State),
		Created: message.CreatedAt.UTC().Format(time.RFC3339),
		Project: string(message.ProjectID),
	}
	if message.From != nil {
		entry.From = string(*message.From)
	}
	if message.TaskID != nil {
		entry.TaskID = string(*message.TaskID)
	}
	return entry
}

func printMessageBlock(deps *Deps, message *core.Message) {
	deps.printf("(message) %s: %s\n", message.ID, message.State)
	if message.From != nil {
		deps.printf("from: %s\n", *message.From)
	}
	deps.printf("to: %s\n", message.To)
	if message.TaskID != nil {
		deps.printf("task: %s\n", *message.TaskID)
	}
	if message.ReplyTo != nil {
		deps.printf("reply_to: %s\n", *message.ReplyTo)
	}
	deps.printf("project: %s\n", message.ProjectID)
	deps.printf("created_at: %s\n", message.CreatedAt.UTC().Format(time.RFC3339))
	if message.Body != "" {
		deps.printf("\n=== Body ===\n%s\n", message.Body)
	}
}

// resolveMessageFrom resolves the sender actor: the flag when set, else the
// configured actor. An empty result means a system message.
func (d *Deps) resolveMessageFrom(ctx context.Context, flag string) (*core.ActorID, error) {
	ref := flag
	if ref == "" {
		ref = d.ActorRef
	}
	if ref == "" {
		return nil, nil
	}
	actor, err := d.Actors.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("sender actor: %w", err)
	}
	id := actor.ID
	return &id, nil
}

func parseMessageLinks(values []string) ([]core.Link, error) {
	links := make([]core.Link, 0, len(values))
	for _, value := range values {
		kind, url, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("%w: link %q must be kind=url", core.ErrInvalid, value)
		}
		links = append(links, core.Link{Kind: core.LinkKind(kind), URL: url})
	}
	return links, nil
}

func parseMessageStates(values []string) ([]core.MessageState, error) {
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
