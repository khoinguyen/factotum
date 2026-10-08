package render

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
)

type TreeNode struct {
	Ticket   core.Ticket
	Class    Class
	Children []*TreeNode
	Refs     []core.TicketID
}

type TreeModel struct {
	Roots []*TreeNode
}

func (v View) BuildTree() *TreeModel {
	derived := v.info()

	children := make(map[core.TicketID][]core.TicketID)
	hasParent := make(map[core.TicketID]bool)
	for child, parent := range derived.forest {
		children[parent] = append(children[parent], child)
		hasParent[child] = true
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool { return children[parent][i] < children[parent][j] })
	}

	var roots []core.TicketID
	for _, id := range derived.ids {
		if !hasParent[id] {
			roots = append(roots, id)
		}
	}

	seen := make(map[core.TicketID]bool)
	var build func(core.TicketID) *TreeNode
	build = func(id core.TicketID) *TreeNode {
		task := derived.tasks[id]
		node := &TreeNode{Ticket: task, Class: v.Classify(task)}
		for _, child := range children[id] {
			if seen[child] {
				continue
			}
			seen[child] = true
			node.Children = append(node.Children, build(child))
		}
		for _, dependent := range v.Graph.Dependents(id) {
			if derived.forest[dependent] != id {
				node.Refs = append(node.Refs, dependent)
			}
		}
		return node
	}

	model := &TreeModel{}
	for _, id := range roots {
		if seen[id] {
			continue
		}
		seen[id] = true
		model.Roots = append(model.Roots, build(id))
	}
	return model
}

type Tree struct{}

func (Tree) Format() string { return "tree" }

func (Tree) Render(_ context.Context, w io.Writer, view View) error {
	var b strings.Builder
	model := view.BuildTree()
	writeTreeNode(&b, model, 0)
	return writeString(w, b.String())
}

func writeTreeNode(b *strings.Builder, model *TreeModel, depth int) {
	var walk func(node *TreeNode, level int)
	walk = func(node *TreeNode, level int) {
		indent := strings.Repeat("  ", level)
		fmt.Fprintf(b, "%s- %s [%s] %s\n", indent, node.Ticket.ID, node.Class, node.Ticket.Title)
		for _, ref := range node.Refs {
			fmt.Fprintf(b, "%s  ↳ %s\n", indent, ref)
		}
		for _, child := range node.Children {
			walk(child, level+1)
		}
	}
	for _, root := range model.Roots {
		walk(root, depth)
	}
}
