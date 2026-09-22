// Package transport holds the embedder adapters: ollama and openai-compatible
// HTTP endpoints, and a one-shot stdio command. It is the only package that knows
// the wire formats; the rest of the code depends on pkg/embed.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/embed"
)

const (
	defaultTimeout = 5 * time.Second
	maxErrorBody   = 200
)

func init() {
	embed.Register("ollama", providerFor("ollama"))
	embed.Register("openai", providerFor("openai"))
	embed.Register("command", providerFor("command"))
}

// providerFor builds a provider that uses the given wire protocol. The provider
// name in config is the protocol; options select the transports.
func providerFor(protocol string) embed.Provider {
	return func(_ func(string) string, options map[string]string) (embed.Embedder, error) {
		return fromOptions(protocol, options)
	}
}

// fromOptions builds the transport chain for a protocol. The endpoint is the
// preferred transport when configured and reachable; the stdio command is the
// fallback. With neither, the embedder is Disabled and callers stay lexical.
func fromOptions(protocol string, options map[string]string) (embed.Embedder, error) {
	endpoint := options["endpoint"]
	command := options["command"]
	model := options["model"]
	timeout := parseTimeout(options["timeout"])
	var chain []embed.Embedder
	if protocol != "command" && endpoint != "" {
		chain = append(chain, NewHTTP(protocol, endpoint, model, options["api_key"], nil, timeout))
	}
	if command != "" {
		chain = append(chain, NewCommand(command, model, nil))
	}
	if len(chain) == 0 {
		return embed.Disabled{}, nil
	}
	return embed.Chain(chain...), nil
}

func parseTimeout(value string) time.Duration {
	if value == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return defaultTimeout
	}
	return d
}

// Doer performs one HTTP request. It is an interface so tests inject a fake
// transport and never open a socket.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTP embeds through an HTTP endpoint. protocol is "ollama" or "openai".
type HTTP struct {
	protocol string
	endpoint string
	model    string
	apiKey   string
	client   Doer
}

// NewHTTP builds an HTTP adapter. A nil client uses http.DefaultTransport with the
// configured timeout.
func NewHTTP(protocol, endpoint, model, apiKey string, client Doer, timeout time.Duration) *HTTP {
	if client == nil {
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	return &HTTP{protocol: protocol, endpoint: strings.TrimRight(endpoint, "/"), model: model, apiKey: apiKey, client: client}
}

func (h *HTTP) Embed(ctx context.Context, input embed.Input, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	prepared := embed.Prepare(h.model, input, texts)
	if h.protocol == "openai" {
		return h.embedOpenAI(ctx, prepared)
	}
	return h.embedOllama(ctx, prepared)
}

func (h *HTTP) embedOllama(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": h.model, "input": texts})
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}
	data, err := h.post(ctx, h.endpoint+"/api/embed", body, "")
	if err != nil {
		return nil, err
	}
	var wire struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	return checkVectors(wire.Embeddings, len(texts))
}

func (h *HTTP) embedOpenAI(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": h.model, "input": texts})
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}
	data, err := h.post(ctx, h.endpoint+"/v1/embeddings", body, h.apiKey)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	vectors := make([][]float32, len(texts))
	for _, item := range wire.Data {
		if item.Index >= 0 && item.Index < len(vectors) {
			vectors[item.Index] = item.Embedding
		}
	}
	return checkVectors(vectors, len(texts))
}

func (h *HTTP) post(ctx context.Context, url string, body []byte, apiKey string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("embed: %s: %s", response.Status, truncate(data))
	}
	return data, nil
}

// Runner executes a one-shot command with stdin and returns stdout.
type Runner func(ctx context.Context, command string, stdin []byte) ([]byte, error)

// Command embeds by running a one-shot stdio command once per call.
type Command struct {
	command string
	model   string
	run     Runner
}

// NewCommand builds a stdio adapter. A nil runner executes the command with sh -c.
func NewCommand(command, model string, run Runner) *Command {
	if run == nil {
		run = execRunner
	}
	return &Command{command: command, model: model, run: run}
}

func (c *Command) Embed(ctx context.Context, input embed.Input, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	prepared := embed.Prepare(c.model, input, texts)
	stdin, err := json.Marshal(prepared)
	if err != nil {
		return nil, fmt.Errorf("embed: encode request: %w", err)
	}
	out, err := c.run(ctx, c.command, stdin)
	if err != nil {
		return nil, err
	}
	var vectors [][]float32
	if err := json.Unmarshal(out, &vectors); err != nil {
		return nil, fmt.Errorf("embed: decode command output: %w", err)
	}
	return checkVectors(vectors, len(texts))
}

func execRunner(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("embed: command failed: %w: %s", err, truncate(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

func checkVectors(vectors [][]float32, want int) ([][]float32, error) {
	if len(vectors) != want {
		return nil, fmt.Errorf("embed: got %d vectors for %d texts", len(vectors), want)
	}
	for _, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embed: empty vector in response")
		}
	}
	return vectors, nil
}

func truncate(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) > maxErrorBody {
		return text[:maxErrorBody] + "..."
	}
	return text
}
