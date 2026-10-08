package cli

import (
	"context"
	"fmt"
	"io"
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

	var sendProject, sendFrom, sendBody string
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
			message, err := deps.Messages.Send(cmd.Context(), app.SendMessageInput{
				ProjectID: project,
				From:      from,
				Target:    args[0],
				Body:      body,
				Links:     links,
			})
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
			result := messageSendResult{ID: string(message.ID), To: string(message.To), State: string(message.State), Project: string(message.ProjectID)}
			return deps.emit(result, func() {
				deps.printFields(f("message_id", message.ID), f("read", true), f("to", message.To), f("state", message.State), f("project", message.ProjectID))
			})
		},
	}

	cmd.AddCommand(send, inbox, get, read)
	return cmd
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
