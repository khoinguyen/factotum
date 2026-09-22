package typesafe_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/typesafe"
)

const canned = `{
  "model": "jev-test",
  "answers": {
    "q":    {"type": "noul",   "noul": 0.77},
    "pick": {"type": "choice", "choice": "c1", "probabilities": {"c0": 0.2, "c1": 0.8}, "confidence": 0.7},
    "band": {"type": "score",  "score": 1.5, "legend": {"0": "low", "1": "high"},
             "probabilities": {"0": 0.3, "1": 0.7}, "confidence": 0.6}
  },
  "usage": {"input_tokens": 42, "output_tokens": 7}
}`

func testRequest() judge.Request {
	return judge.Request{
		State: "the state",
		Questions: map[string]judge.Question{
			"q":    {Kind: judge.KindYesNo, Instructions: "ready?"},
			"pick": {Kind: judge.KindChoice, Instructions: "which?", Criteria: map[string]any{"c0": nil, "c1": nil}},
			"band": {Kind: judge.KindRating, Instructions: "how much?", Criteria: []string{"low", "high"}},
		},
	}
}

func TestAskSendsTypedRequestAndParsesResponse(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(canned))
	}))
	defer srv.Close()

	c := typesafe.New(typesafe.Config{APIKey: "test-key", BaseURL: srv.URL, Model: "test-model"})
	resp, err := c.Ask(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}

	if auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", auth)
	}
	if got := body["model"]; got != "test-model" {
		t.Errorf("model = %v, want test-model", got)
	}
	if got := body["state"]; got != "the state" {
		t.Errorf("state = %v, want the state", got)
	}
	questions, ok := body["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions = %T, want object", body["questions"])
	}
	if got := questions["q"].(map[string]any)["type"]; got != "noul" {
		t.Errorf("q.type = %v, want noul", got)
	}
	if _, ok := questions["pick"].(map[string]any)["criteria"]; !ok {
		t.Error("pick.criteria missing, want it sent for a choice")
	}

	if got := resp.Answers["q"].Probability; got != 0.77 {
		t.Errorf("q.Probability = %v, want 0.77", got)
	}
	if got := resp.Answers["pick"].Choice; got != "c1" {
		t.Errorf("pick.Choice = %q, want c1", got)
	}
	if got := resp.Answers["band"].Rating; got != 1.5 {
		t.Errorf("band.Score = %v, want 1.5", got)
	}
	if resp.InputTokens != 42 || resp.OutputTokens != 7 {
		t.Errorf("usage = %d/%d, want 42/7", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "jev-test" {
		t.Errorf("resp.Model = %q, want jev-test", resp.Model)
	}
}

func TestRetriesOn429ThenSucceeds(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(canned))
	}))
	defer srv.Close()

	c := typesafe.New(typesafe.Config{
		APIKey: "k", BaseURL: srv.URL, Model: "m",
		MaxRetries: 2, Backoff: time.Millisecond,
	})
	if _, err := c.Ask(context.Background(), testRequest()); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one 429, one success)", attempts)
	}
}

func TestHTTPErrorIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"bad question"}`))
	}))
	defer srv.Close()

	c := typesafe.New(typesafe.Config{APIKey: "k", BaseURL: srv.URL, Model: "m"})
	_, err := c.Ask(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Ask() error = nil, want an HTTP error")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error = %v, want it to name the status 422", err)
	}
}

func TestNoKeyDoesNotCallServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server was called with no API key")
	}))
	defer srv.Close()

	c := typesafe.New(typesafe.Config{BaseURL: srv.URL})
	_, err := c.Ask(context.Background(), testRequest())
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Ask() error = %v, want ErrUnavailable", err)
	}
}

func TestResolveAPIKeyPrefersEnv(t *testing.T) {
	env := func(string) string { return "from-env" }
	if got := typesafe.ResolveAPIKey(env, "from-config"); got != "from-env" {
		t.Errorf("ResolveAPIKey = %q, want from-env", got)
	}
	empty := func(string) string { return "" }
	if got := typesafe.ResolveAPIKey(empty, "from-config"); got != "from-config" {
		t.Errorf("ResolveAPIKey = %q, want from-config", got)
	}
}
