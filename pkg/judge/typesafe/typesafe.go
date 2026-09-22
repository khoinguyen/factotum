// Package typesafe is the HTTP adapter for the TypeSafe System One API. It is the
// only package that knows the wire format; the rest of the code depends on pkg/judge.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/khoinguyen/factotum/pkg/judge"
)

const (
	defaultBaseURL = "https://api.typesafe.ai"
	defaultModel   = "jev-latest"
	defaultRetries = 3
	defaultBackoff = 200 * time.Millisecond
	maxErrorBody   = 200
)

// Config configures the client. An empty APIKey yields a disabled judge.
type Config struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	MaxRetries int
	Backoff    time.Duration
}

// Client is the TypeSafe HTTP adapter.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	http       *http.Client
	maxRetries int
	backoff    time.Duration
}

// New returns a client, filling in defaults. With no API key it still returns a
// client, but Ask reports ErrUnavailable without making a request.
func New(cfg Config) *Client {
	client := &Client{
		apiKey:     cfg.APIKey,
		baseURL:    firstNonEmpty(cfg.BaseURL, defaultBaseURL),
		model:      firstNonEmpty(cfg.Model, defaultModel),
		http:       cfg.HTTPClient,
		maxRetries: cfg.MaxRetries,
		backoff:    cfg.Backoff,
	}
	if client.http == nil {
		client.http = &http.Client{Timeout: 60 * time.Second}
	}
	if client.maxRetries == 0 {
		client.maxRetries = defaultRetries
	}
	if client.backoff == 0 {
		client.backoff = defaultBackoff
	}
	return client
}

// ResolveAPIKey returns the environment key when set, otherwise the configured key,
// so TYPESAFE_API_KEY wins over the config table.
func ResolveAPIKey(getenv func(string) string, configKey string) string {
	if value := getenv("TYPESAFE_API_KEY"); value != "" {
		return value
	}
	return configKey
}

func init() { judge.Register("typesafe", fromOptions) }

// fromOptions builds the TypeSafe judge from its [typesafe] options, with the
// TYPESAFE_* environment variables winning. With no key it returns Disabled.
func fromOptions(getenv func(string) string, options map[string]string) (judge.Judge, error) {
	key := ResolveAPIKey(getenv, options["secret_api_key"])
	if key == "" {
		return judge.Disabled{}, nil
	}
	return New(Config{
		APIKey:  key,
		Model:   firstNonEmpty(getenv("TYPESAFE_MODEL"), options["model"]),
		BaseURL: firstNonEmpty(getenv("TYPESAFE_BASE_URL"), options["base_url"]),
	}), nil
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireRequest struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireAnswer struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Noul          float64            `json:"noul"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Ask sends one evaluation. It retries 429 and 529 responses with exponential
// backoff; other failures are returned.
func (c *Client) Ask(ctx context.Context, req judge.Request) (judge.Response, error) {
	if c.apiKey == "" {
		return judge.Response{}, judge.ErrUnavailable
	}
	payload := wireRequest{State: req.State, Model: c.model,
		Questions: make(map[string]wireQuestion, len(req.Questions))}
	for id, question := range req.Questions {
		payload.Questions[id] = wireQuestion{
			Type: string(question.Kind), Instructions: question.Instructions, Criteria: question.Criteria,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return judge.Response{}, fmt.Errorf("judge: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.backoff<<(attempt-1)); err != nil {
				return judge.Response{}, err
			}
		}
		response, retry, err := c.do(ctx, body)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retry {
			return judge.Response{}, err
		}
	}
	return judge.Response{}, lastErr
}

func (c *Client) do(ctx context.Context, body []byte) (judge.Response, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return judge.Response{}, false, fmt.Errorf("judge: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return judge.Response{}, false, fmt.Errorf("judge: request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)

	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 529 {
		return judge.Response{}, true, fmt.Errorf("judge: %s: %s", response.Status, truncate(data))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return judge.Response{}, false, fmt.Errorf("judge: %s: %s", response.Status, truncate(data))
	}

	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return judge.Response{}, false, fmt.Errorf("judge: decode response: %w", err)
	}
	out := judge.Response{
		Model:        wire.Model,
		Answers:      make(map[string]judge.Answer, len(wire.Answers)),
		InputTokens:  wire.Usage.InputTokens,
		OutputTokens: wire.Usage.OutputTokens,
	}
	for id, answer := range wire.Answers {
		out.Answers[id] = judge.Answer{
			Choice: answer.Choice, Probabilities: answer.Probabilities,
			Noul: answer.Noul, Score: answer.Score, Confidence: answer.Confidence,
		}
	}
	return out, false, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncate(data []byte) string {
	if len(data) > maxErrorBody {
		return string(data[:maxErrorBody]) + "..."
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
