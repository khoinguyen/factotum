package cli

import (
	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/core"
)

func newActorCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "actor", Short: "Manage humans and agents"}

	var kind string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Register a human or agent",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := deps.Actors.Add(cmd.Context(), core.ActorKind(kind), args[0])
			if err != nil {
				return err
			}
			return deps.emit(actor, func() { deps.printf("%s\t%s\t%s\n", actor.ID, actor.Kind, actor.Name) },
				hint{Command: "ft actor list", About: "see all actors"})
		},
	}
	create.Flags().StringVarP(&kind, "kind", "k", string(core.ActorHuman), "actor kind: human or agent")

	list := &cobra.Command{
		Use:   "list",
		Short: "List registered actors",
		RunE: func(cmd *cobra.Command, _ []string) error {
			actors, err := deps.Actors.List(cmd.Context())
			if err != nil {
				return err
			}
			return deps.emit(actors, func() {
				rows := make([][]string, 0, len(actors))
				for _, actor := range actors {
					rows = append(rows, []string{string(actor.ID), string(actor.Kind), actor.Name})
				}
				deps.printTable([]string{"ID", "KIND", "NAME"}, rows)
			}, actorListHints(actors)...)
		},
	}

	cmd.AddCommand(create, list)
	return cmd
}
