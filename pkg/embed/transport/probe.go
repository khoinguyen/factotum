package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/khoinguyen/factotum/pkg/doctor"
)

// Probe is the real doctor.Prober: it checks an embedding endpoint over HTTP and
// resolves a stdio command on PATH. The HTTP client is a Doer so tests inject a
// fake and never open a socket.
type Probe struct {
	client   Doer
	lookPath func(string) (string, error)
}

// NewProbe builds a probe over the given client, defaulting to the shared
// transport client when nil.
func NewProbe(client Doer) *Probe {
	if client == nil {
		client = httpDoer(defaultTimeout)
	}
	return newProbe(client, exec.LookPath)
}

func newProbe(client Doer, lookPath func(string) (string, error)) *Probe {
	return &Probe{client: client, lookPath: lookPath}
}

// Reachable reports whether the endpoint answers. Any HTTP response counts; only a
// transport failure (DNS, refused connection, timeout) does not. A 404 or 401
// still means the server is up, which is what this check asks.
func (p *Probe) Reachable(ctx context.Context, ep doctor.Endpoint) error {
	response, err := p.get(ctx, ep)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	return nil
}

// HasModel reports whether the endpoint serves the configured model. It lists the
// models the way the embed transport speaks (ollama /api/tags, openai /v1/models)
// and requires a 2xx response.
func (p *Probe) HasModel(ctx context.Context, ep doctor.Endpoint) error {
	response, err := p.get(ctx, ep)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: %s: %s", doctor.ErrAuth, response.Status, truncate(data))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("embed: %s: %s", response.Status, truncate(data))
	}
	models, err := parseModels(ep.Protocol, data)
	if err != nil {
		return err
	}
	for _, listed := range models {
		if matchModel(listed, ep.Model) {
			return nil
		}
	}
	return fmt.Errorf("embed: model %q not served", ep.Model)
}

// Resolvable reports whether the command's executable is on PATH. It inspects the
// first shell word, so a command with flags or arguments is still resolvable.
func (p *Probe) Resolvable(_ context.Context, command string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("embed: empty command")
	}
	if _, err := p.lookPath(fields[0]); err != nil {
		return fmt.Errorf("embed: %s: %w", fields[0], err)
	}
	return nil
}

func (p *Probe) get(ctx context.Context, ep doctor.Endpoint) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL(ep), nil)
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	if ep.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	return response, nil
}

// listURL is the endpoint's model-listing path, matching the embed transport's
// wire shapes.
func listURL(ep doctor.Endpoint) string {
	base := strings.TrimRight(ep.URL, "/")
	if ep.Protocol == "ollama" {
		return base + "/api/tags"
	}
	return base + "/v1/models"
}

func parseModels(protocol string, data []byte) ([]string, error) {
	if protocol == "ollama" {
		var wire struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return nil, fmt.Errorf("embed: decode model list: %w", err)
		}
		models := make([]string, 0, len(wire.Models))
		for _, model := range wire.Models {
			models = append(models, model.Name)
		}
		return models, nil
	}
	var wire struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("embed: decode model list: %w", err)
	}
	models := make([]string, 0, len(wire.Data))
	for _, model := range wire.Data {
		models = append(models, model.ID)
	}
	return models, nil
}

// matchModel reports whether a listed model name satisfies the configured one.
// Ollama lists tagged names (`nomic-embed-text:latest`) for an untagged
// configuration, so a bare configured name matches any tag of the same model.
func matchModel(listed, want string) bool {
	if want == "" {
		return false
	}
	if listed == want {
		return true
	}
	return strings.HasPrefix(listed, want+":")
}
