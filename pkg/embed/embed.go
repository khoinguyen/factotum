// Package embed is the port for turning text into vectors. Features that need
// semantic recall depend on this interface, never on a transport adapter, so the
// capability is fakeable in tests and disabled when no provider is configured.
//
// The default path stays lexical: with no provider the embedder is Disabled and
// callers fall back to their deterministic retrieval.
package embed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrUnavailable means no embedder is configured. Callers keep their lexical path.
var ErrUnavailable = errors.New("embedder unavailable")

// Input distinguishes a query from a stored document. Some models (nomic) embed
// the two with different instruction prefixes; others ignore it.
type Input string

const (
	// InputQuery embeds a search query.
	InputQuery Input = "query"
	// InputDocument embeds a stored document.
	InputDocument Input = "document"
)

// Embedder turns texts into vectors, one per text and in the same order.
// Implementations must be safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, input Input, texts []string) ([][]float32, error)
}

// Disabled is an embedder with no backend. It returns ErrUnavailable so callers
// keep their lexical path.
type Disabled struct{}

func (Disabled) Embed(context.Context, Input, []string) ([][]float32, error) {
	return nil, ErrUnavailable
}

// Provider builds an Embedder from provider-specific options. The embed capability
// does not know any provider by name; each provider parses its own options and reads
// its own environment variables (getenv), so adding a provider does not touch this port.
type Provider func(getenv func(string) string, options map[string]string) (Embedder, error)

var providers = map[string]Provider{}

// Register adds a provider by name. It panics on an empty or duplicate name, like the
// other plugin registries in the project, so misconfiguration fails at startup.
func Register(name string, provider Provider) {
	if name == "" || provider == nil {
		panic("embed: invalid provider registration")
	}
	if _, exists := providers[name]; exists {
		panic("embed: duplicate provider " + name)
	}
	providers[name] = provider
}

// New builds the named provider from its options. An empty name means no embedder is
// configured and returns Disabled; an unknown name is an error.
func New(name string, getenv func(string) string, options map[string]string) (Embedder, error) {
	if name == "" {
		return Disabled{}, nil
	}
	provider, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("embed: unknown provider %q", name)
	}
	return provider(getenv, options)
}

// Chain tries embedders in order and returns the first success. It is how the
// endpoint -> stdio fallback is expressed: an unreachable endpoint yields to the
// command, and only when every step fails does the caller drop to lexical. An empty
// chain is Disabled.
func Chain(embedders ...Embedder) Embedder {
	chain := make([]Embedder, 0, len(embedders))
	for _, e := range embedders {
		if e != nil {
			chain = append(chain, e)
		}
	}
	switch len(chain) {
	case 0:
		return Disabled{}
	case 1:
		return chain[0]
	default:
		return chainEmbedder(chain)
	}
}

type chainEmbedder []Embedder

func (c chainEmbedder) Embed(ctx context.Context, input Input, texts []string) ([][]float32, error) {
	var lastErr error
	for _, e := range c {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors, err := e.Embed(ctx, input, texts)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrUnavailable
	}
	return nil, lastErr
}

const (
	// DocumentBodyLimit caps the body slice used when a memory has no brief. The brief
	// is short and dense by design; long bodies are truncated explicitly rather than
	// relying on the model's silent truncation.
	DocumentBodyLimit = 1000
	// MaxTextLen caps the prepared text handed to a provider, in runes.
	MaxTextLen = 8000
)

// DocumentText builds the text to embed for one item: title plus brief, falling back
// to title plus the first DocumentBodyLimit characters of the body.
func DocumentText(title, brief, body string) string {
	title = strings.TrimSpace(title)
	if brief = strings.TrimSpace(brief); brief != "" {
		return title + "\n" + brief
	}
	return title + "\n" + truncate(strings.TrimSpace(body), DocumentBodyLimit)
}

// Prefix returns the model-specific instruction prepended to text. nomic-embed-text
// distinguishes queries from documents; models without one return "".
func Prefix(model string, input Input) string {
	if !strings.Contains(strings.ToLower(model), "nomic") {
		return ""
	}
	if input == InputQuery {
		return "search_query: "
	}
	return "search_document: "
}

// Prepare applies the model's prefix and the length cap to every text. Transports
// call it so prefix and truncation rules live in one place.
func Prepare(model string, input Input, texts []string) []string {
	prefix := Prefix(model, input)
	out := make([]string, len(texts))
	for i, text := range texts {
		text = prefix + text
		out[i] = truncate(text, MaxTextLen)
	}
	return out
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
