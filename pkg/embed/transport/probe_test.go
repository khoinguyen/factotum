package transport

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/khoinguyen/factotum/pkg/doctor"
)

func TestProbeReachableUsesProtocolListPath(t *testing.T) {
	tests := []struct {
		protocol string
		wantPath string
	}{
		{"openai", "/v1/models"},
		{"ollama", "/api/tags"},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			doer := &fakeDoer{status: http.StatusNotFound, body: "nope"}
			probe := NewProbe(doer)
			if err := probe.Reachable(context.Background(), doctor.Endpoint{Protocol: tt.protocol, URL: "http://127.0.0.1:8080"}); err != nil {
				t.Fatalf("any HTTP response is reachable, got %v", err)
			}
			if got := doer.req.URL.Path; got != tt.wantPath {
				t.Fatalf("path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestProbeReachableTransportError(t *testing.T) {
	probe := NewProbe(&fakeDoer{err: errors.New("connection refused")})
	if err := probe.Reachable(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x"}); err == nil {
		t.Fatal("a transport error should be unreachable")
	}
}

func TestProbeHasModelOpenAI(t *testing.T) {
	doer := &fakeDoer{body: `{"data":[{"id":"nomic-embed-text-v1.5"},{"id":"other"}]}`}
	probe := NewProbe(doer)
	if err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x", Model: "nomic-embed-text-v1.5"}); err != nil {
		t.Fatalf("HasModel() error = %v", err)
	}
	if err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x", Model: "missing"}); err == nil {
		t.Fatal("an absent model should error")
	}
}

func TestProbeHasModelOllamaMatchesTag(t *testing.T) {
	doer := &fakeDoer{body: `{"models":[{"name":"nomic-embed-text:latest"}]}`}
	probe := NewProbe(doer)
	if err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "ollama", URL: "http://x", Model: "nomic-embed-text"}); err != nil {
		t.Fatalf("ollama model without a tag should match the tagged name: %v", err)
	}
}

func TestProbeHasModelSendsAPIKey(t *testing.T) {
	doer := &fakeDoer{body: `{"data":[{"id":"m"}]}`}
	probe := NewProbe(doer)
	if err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x", APIKey: "sk-1", Model: "m"}); err != nil {
		t.Fatalf("HasModel() error = %v", err)
	}
	if got := doer.req.Header.Get("Authorization"); got != "Bearer sk-1" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestProbeHasModelRejectsErrorStatus(t *testing.T) {
	probe := NewProbe(&fakeDoer{status: http.StatusInternalServerError, body: "boom"})
	if err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x", Model: "m"}); err == nil {
		t.Fatal("a non-2xx response should error")
	}
}

func TestProbeHasModelReportsAuthFailure(t *testing.T) {
	probe := NewProbe(&fakeDoer{status: http.StatusUnauthorized, body: `{"error":"bad key"}`})
	err := probe.HasModel(context.Background(), doctor.Endpoint{Protocol: "openai", URL: "http://x", APIKey: "bad", Model: "m"})
	if !errors.Is(err, doctor.ErrAuth) {
		t.Fatalf("a 401 should be ErrAuth, got %v", err)
	}
}

func TestProbeResolvable(t *testing.T) {
	probe := newProbe(&fakeDoer{}, func(name string) (string, error) {
		if name == "on-path" {
			return "/usr/bin/on-path", nil
		}
		return "", errors.New("not found")
	})
	if err := probe.Resolvable(context.Background(), "on-path --flag"); err != nil {
		t.Fatalf("Resolvable() error = %v", err)
	}
	if err := probe.Resolvable(context.Background(), "not-on-path"); err == nil {
		t.Fatal("an absent executable should error")
	}
	if err := probe.Resolvable(context.Background(), "   "); err == nil {
		t.Fatal("an empty command should error")
	}
}
