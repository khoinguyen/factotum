package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/embed"
)

type fakeDoer struct {
	status int
	body   string
	err    error
	req    *http.Request
	raw    []byte
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.req = req
	if req.Body != nil {
		f.raw, _ = io.ReadAll(req.Body)
	}
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

func TestHTTPOllamaRequestAndResponse(t *testing.T) {
	doer := &fakeDoer{body: `{"embeddings":[[1,2],[3,4]]}`}
	client := NewHTTP("ollama", "http://localhost:11434", "nomic-embed-text", "", doer, 0)

	vectors, err := client.Embed(context.Background(), embed.InputQuery, []string{"terraform", "kubernetes"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 4 {
		t.Fatalf("Embed() = %v", vectors)
	}
	if got := doer.req.URL.Path; got != "/api/embed" {
		t.Fatalf("request path = %q, want /api/embed", got)
	}
	var body struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(doer.raw, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if body.Model != "nomic-embed-text" {
		t.Fatalf("model = %q", body.Model)
	}
	if len(body.Input) != 2 || body.Input[0] != "search_query: terraform" || body.Input[1] != "search_query: kubernetes" {
		t.Fatalf("input = %v, want the nomic query prefix on every text", body.Input)
	}
}

func TestHTTPOpenAIRequestAndResponse(t *testing.T) {
	doer := &fakeDoer{body: `{"data":[{"index":0,"embedding":[9,8]}]}`}
	client := NewHTTP("openai", "https://api.openai.com", "text-embedding-3-small", "sk-secret", doer, 0)

	vectors, err := client.Embed(context.Background(), embed.InputDocument, []string{"body"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 1 || vectors[0][0] != 9 {
		t.Fatalf("Embed() = %v", vectors)
	}
	if got := doer.req.URL.Path; got != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", got)
	}
	if auth := doer.req.Header.Get("Authorization"); auth != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q", auth)
	}
	if strings.Contains(string(doer.raw), "search_document") {
		t.Fatalf("non-nomic model must not get a prefix: %s", doer.raw)
	}
}

func TestHTTPErrorStatusFails(t *testing.T) {
	doer := &fakeDoer{status: http.StatusInternalServerError, body: "boom"}
	client := NewHTTP("ollama", "http://localhost:11434", "m", "", doer, 0)
	if _, err := client.Embed(context.Background(), embed.InputQuery, []string{"x"}); err == nil {
		t.Fatal("a non-2xx response should error")
	}
}

type fakeRunner struct {
	stdin []byte
	out   []byte
	err   error
	calls int
}

func (f *fakeRunner) run(_ context.Context, _ string, stdin []byte) ([]byte, error) {
	f.calls++
	f.stdin = stdin
	return f.out, f.err
}

func TestCommandWritesJSONAndParsesVectors(t *testing.T) {
	runner := &fakeRunner{out: []byte(`[[1,2],[3,4]]`)}
	client := NewCommand("llamafile --embedding", "", runner.run)

	vectors, err := client.Embed(context.Background(), embed.InputDocument, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 || vectors[1][1] != 4 {
		t.Fatalf("Embed() = %v", vectors)
	}
	var stdin []string
	if err := json.Unmarshal(runner.stdin, &stdin); err != nil {
		t.Fatalf("stdin is not a JSON string array: %s", runner.stdin)
	}
	if len(stdin) != 2 || stdin[0] != "a" {
		t.Fatalf("stdin = %v", stdin)
	}
}

func TestCommandErrorsOnBadJSON(t *testing.T) {
	runner := &fakeRunner{out: []byte("not json")}
	client := NewCommand("embed", "", runner.run)
	if _, err := client.Embed(context.Background(), embed.InputQuery, []string{"x"}); err == nil {
		t.Fatal("malformed command output should error")
	}
}

func TestCommandErrorsOnCountMismatch(t *testing.T) {
	runner := &fakeRunner{out: []byte(`[[1,2]]`)}
	client := NewCommand("embed", "", runner.run)
	if _, err := client.Embed(context.Background(), embed.InputQuery, []string{"x", "y"}); err == nil {
		t.Fatal("a vector count mismatch should error")
	}
}

func TestCommandPropagatesRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("no such command")}
	client := NewCommand("embed", "", runner.run)
	if _, err := client.Embed(context.Background(), embed.InputQuery, []string{"x"}); err == nil {
		t.Fatal("a runner error should propagate")
	}
}

func TestProvidersFromOptions(t *testing.T) {
	getenv := func(string) string { return "" }

	if e, err := embed.New("ollama", getenv, map[string]string{"endpoint": "http://x", "model": "m"}); err != nil {
		t.Fatalf("ollama: %v", err)
	} else if _, ok := e.(embed.Disabled); ok {
		t.Fatal("ollama with an endpoint should be enabled")
	}
	if e, err := embed.New("command", getenv, map[string]string{"command": "embed"}); err != nil {
		t.Fatalf("command: %v", err)
	} else if _, ok := e.(embed.Disabled); ok {
		t.Fatal("command with a command should be enabled")
	}
	if e, err := embed.New("command", getenv, nil); err != nil {
		t.Fatalf("command empty: %v", err)
	} else if _, ok := e.(embed.Disabled); !ok {
		t.Fatal("command provider with no command should be Disabled")
	}
	if e, err := embed.New("ollama", getenv, nil); err != nil {
		t.Fatalf("ollama empty: %v", err)
	} else if _, ok := e.(embed.Disabled); !ok {
		t.Fatal("ollama with neither endpoint nor command should be Disabled")
	}
}
