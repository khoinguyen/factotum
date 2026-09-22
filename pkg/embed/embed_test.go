package embed_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/embed"
)

func TestDisabledReportsUnavailable(t *testing.T) {
	if _, err := (embed.Disabled{}).Embed(context.Background(), embed.InputQuery, []string{"x"}); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("Disabled.Embed error = %v, want ErrUnavailable", err)
	}
}

func TestNewEmptyNameIsDisabled(t *testing.T) {
	e, err := embed.New("", func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := e.(embed.Disabled); !ok {
		t.Fatal("empty provider name should be Disabled")
	}
}

func TestNewUnknownProviderErrors(t *testing.T) {
	if _, err := embed.New("not-registered", nil, nil); err == nil {
		t.Fatal("unknown provider should be an error")
	}
}

func TestRegisterAndNewPassesOptionsAndEnv(t *testing.T) {
	name := "test-provider-" + t.Name()
	embed.Register(name, func(getenv func(string) string, options map[string]string) (embed.Embedder, error) {
		if options["model"] != "nomic" {
			t.Errorf("options = %v, want model=nomic", options)
		}
		if getenv("SOME_KEY") != "value" {
			t.Errorf("getenv not passed through")
		}
		return embed.Disabled{}, nil
	})
	if _, err := embed.New(name, func(key string) string {
		if key == "SOME_KEY" {
			return "value"
		}
		return ""
	}, map[string]string{"model": "nomic"}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

type fakeEmbedder struct {
	vectors [][]float32
	err     error
	calls   int
	lastIn  embed.Input
}

func (f *fakeEmbedder) Embed(_ context.Context, in embed.Input, texts []string) ([][]float32, error) {
	f.calls++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, 0, len(texts))
	for range texts {
		out = append(out, f.vectors[0])
	}
	return out, nil
}

func TestChainFallsBackToNextSuccess(t *testing.T) {
	bad := &fakeEmbedder{err: errors.New("endpoint down")}
	good := &fakeEmbedder{vectors: [][]float32{{1, 0}}}
	got, err := embed.Chain(bad, good).Embed(context.Background(), embed.InputQuery, []string{"hello"})
	if err != nil {
		t.Fatalf("Chain() error = %v", err)
	}
	if len(got) != 1 || got[0][0] != 1 {
		t.Fatalf("Chain() = %v, want the second embedder's vector", got)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Fatalf("calls = %d/%d, want both tried in order", bad.calls, good.calls)
	}
}

func TestChainAllFailReturnsLastError(t *testing.T) {
	first := &fakeEmbedder{err: errors.New("first")}
	second := &fakeEmbedder{err: errors.New("second")}
	_, err := embed.Chain(first, second).Embed(context.Background(), embed.InputDocument, []string{"x"})
	if err == nil || err.Error() != "second" {
		t.Fatalf("Chain() error = %v, want the last error", err)
	}
}

func TestChainEmptyIsDisabled(t *testing.T) {
	if _, ok := embed.Chain().(embed.Disabled); !ok {
		t.Fatal("an empty chain should be Disabled")
	}
}

func TestDocumentTextPrefersBrief(t *testing.T) {
	got := embed.DocumentText("Terraform", "how we run infra as code", "a very long body")
	if got != "Terraform\nhow we run infra as code" {
		t.Fatalf("DocumentText() = %q", got)
	}
}

func TestDocumentTextFallsBackToCappedBody(t *testing.T) {
	body := strings.Repeat("x", 1500)
	got := embed.DocumentText("Terraform", "", body)
	if !strings.HasPrefix(got, "Terraform\n") {
		t.Fatalf("DocumentText() missing title: %q", got)
	}
	bodyPart := strings.TrimPrefix(got, "Terraform\n")
	if len(bodyPart) != embed.DocumentBodyLimit {
		t.Fatalf("body length = %d, want cap %d", len(bodyPart), embed.DocumentBodyLimit)
	}
}

func TestPrefixOnlyForNomic(t *testing.T) {
	if got := embed.Prefix("nomic-embed-text", embed.InputQuery); got != "search_query: " {
		t.Fatalf("nomic query prefix = %q", got)
	}
	if got := embed.Prefix("nomic-embed-text", embed.InputDocument); got != "search_document: " {
		t.Fatalf("nomic document prefix = %q", got)
	}
	if got := embed.Prefix("text-embedding-3-small", embed.InputQuery); got != "" {
		t.Fatalf("non-nomic prefix = %q, want empty", got)
	}
}
