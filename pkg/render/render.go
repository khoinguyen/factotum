// Package render provides the pluggable output renderers for task graphs and
// reports. Every renderer is deterministic: the same view renders the same
// bytes.
package render

import (
	"context"
	"io"

	"github.com/khoinguyen/factotum/pkg/registry"
)

type Renderer interface {
	Format() string
	Render(ctx context.Context, w io.Writer, view View) error
}

func Builtins() *registry.Registry[Renderer] {
	reg := registry.New[Renderer]()
	for _, renderer := range []Renderer{Agent{}, JSON{}, Tree{}, HTML{}, DOT{}, Mermaid{}} {
		if err := reg.Register(renderer.Format(), renderer); err != nil {
			panic(err)
		}
	}
	return reg
}

func writeString(w io.Writer, s string) error {
	_, err := io.WriteString(w, s)
	return err
}
