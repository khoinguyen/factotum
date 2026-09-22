package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/embed"
	"github.com/khoinguyen/factotum/pkg/vector"
)

// keywordEmbedder maps a text to a vector by the topic it mentions, so tests can
// model paraphrase recall ("provisioning" finds "terraform") without a model. It
// never touches the network.
type keywordEmbedder struct {
	err error
}

func (e *keywordEmbedder) Embed(_ context.Context, _ embed.Input, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(lower, "terraform") || strings.Contains(lower, "infra") || strings.Contains(lower, "provision"):
			out = append(out, []float32{1, 0})
		case strings.Contains(lower, "kubernetes") || strings.Contains(lower, "cluster"):
			out = append(out, []float32{0, 1})
		default:
			out = append(out, []float32{0.5, 0.5})
		}
	}
	return out, nil
}

func vectorRunner(t *testing.T) *runner {
	t.Helper()
	r := newRunner(t)
	r.embedder = &keywordEmbedder{}
	r.vectors = vector.NewMemory()
	r.embedModel = "test-model"
	return r
}

func TestMemorySearchVectorAddsLexicalMiss(t *testing.T) {
	r := vectorRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "how we version infrastructure changes")
	r.run("memory", "create", "-p", projectID, "-t", "Kubernetes notes", "--brief", "cluster upgrade")

	// "provisioning" shares no token with the terraform memory, so lexical search
	// misses it; the vector retriever adds it back.
	out := r.run("memory", "search", "provisioning", "-p", projectID, "--no-rerank")
	if !strings.Contains(out, "Terraform notes") {
		t.Fatalf("vector recall did not add the semantic match:\n%s", out)
	}
	if strings.Contains(out, "Kubernetes notes") {
		t.Fatalf("vector recall added an unrelated memory:\n%s", out)
	}
}

func TestMemorySearchFallsBackToLexicalWhenEmbedderDown(t *testing.T) {
	r := vectorRunner(t)
	r.embedder = &keywordEmbedder{err: errors.New("endpoint unreachable")}
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "how we version infrastructure changes")

	out, stderr := r.runSplit("memory", "search", "terraform", "-p", projectID)
	if !strings.Contains(out, "Terraform notes") {
		t.Fatalf("search should still return the lexical hit:\n%s", out)
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "lexical") {
		t.Fatalf("a vector failure should warn once and fall back to lexical:\n%s", stderr)
	}
}

func TestMemoryWriteSucceedsWhenEmbedderDown(t *testing.T) {
	r := vectorRunner(t)
	r.embedder = &keywordEmbedder{err: errors.New("down")}
	projectID := firstField(t, r.run("project", "create", "Acme"))

	out, stderr := r.runSplit("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "infra")
	if !strings.Contains(out, "created: true") {
		t.Fatalf("a write must not fail because the embedder is down:\n%s", out)
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "reindex") {
		t.Fatalf("a skipped vector should warn and name the backfill:\n%s", stderr)
	}
}

func TestMemorySearchDetectsModelMismatch(t *testing.T) {
	r := vectorRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "how we version infrastructure changes")

	r.embedModel = "other-model"
	_, stderr := r.runSplit("memory", "search", "provisioning", "-p", projectID)
	if !strings.Contains(stderr, "model mismatch") || !strings.Contains(stderr, "reindex") {
		t.Fatalf("a model mismatch must be detected and name the fix:\n%s", stderr)
	}
}

func TestMemoryReindexRepairsModelMismatch(t *testing.T) {
	r := vectorRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "how we version infrastructure changes")

	r.embedModel = "other-model"
	out := r.run("memory", "reindex", "-p", projectID, "-o", "json")
	var result struct {
		Reindexed int    `json:"reindexed"`
		Model     string `json:"model"`
		Project   string `json:"project"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("reindex json: %v\n%s", err, out)
	}
	if result.Reindexed != 1 || result.Model != "other-model" || result.Project != projectID {
		t.Fatalf("reindex result = %+v", result)
	}

	// After reindexing, the vector path works again for the new model.
	search := r.run("memory", "search", "provisioning", "-p", projectID, "--no-rerank")
	if !strings.Contains(search, "Terraform notes") {
		t.Fatalf("reindex did not repair vector recall:\n%s", search)
	}
}

func TestMemoryReindexWithoutProviderIsUsageError(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	if err := r.runErr("memory", "reindex", "-p", projectID); !errors.Is(err, ErrUsage) {
		t.Fatalf("reindex without a provider error = %v, want ErrUsage", err)
	}
}

func TestMemorySearchIsLexicalWithoutProvider(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes")

	_, stderr := r.runSplit("memory", "search", "terraform", "-p", projectID)
	if strings.Contains(stderr, "warning") {
		t.Fatalf("no provider should mean no vector warning:\n%s", stderr)
	}
}

func TestMemoryDeleteRemovesVectorSoReindexRepairsMismatch(t *testing.T) {
	r := vectorRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	terraform := firstField(t, r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "infrastructure versioning"))
	r.run("memory", "create", "-p", projectID, "-t", "Kubernetes notes", "--brief", "cluster runbook")

	// Deleting a memory must remove its vector, or the orphan keeps the index on the
	// old model and reindex can never repair the mismatch.
	if out := r.run("memory", "delete", terraform); !strings.Contains(out, "deleted: true") {
		t.Fatalf("delete output:\n%s", out)
	}
	r.embedModel = "other-model"
	r.run("memory", "reindex", "-p", projectID)

	search, stderr := r.runSplit("memory", "search", "provisioning", "-p", projectID)
	if strings.Contains(stderr, "mismatch") {
		t.Fatalf("reindex should have cleared the orphaned model:\n%s", stderr)
	}
	if strings.Contains(search, "Terraform notes") {
		t.Fatalf("a deleted memory was returned:\n%s", search)
	}
}

func TestMemorySearchWarnsOnMisconfiguredProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{"command with no command", "command", "not usable"},
		{"unknown provider", "does-not-exist", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			body := "[embed]\nprovider = \"" + tt.provider + "\"\nmodel = \"m\"\n"
			if err := os.WriteFile(r.userPath, []byte(body), 0o600); err != nil {
				t.Fatalf("write user config: %v", err)
			}
			_, stderr := r.runSplit("memory", "search", "terraform", "-p", projectID)
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("misconfigured provider should warn %q:\n%s", tt.want, stderr)
			}
		})
	}
}

func TestOpenVectorsSkipsWhenModelEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.json")
	d := &Deps{Err: io.Discard, Embedder: &keywordEmbedder{}}
	cfg := config.Config{
		Embed: config.Embed{Provider: "command", Options: map[string]string{}},
		Store: config.Store{Backend: "jsonfile", Options: map[string]string{"path": path}},
	}
	if index := d.openVectors(cfg); index != nil {
		t.Fatal("openVectors should be nil with no model")
	}
	if _, err := os.Stat(path + ".vectors.db"); !os.IsNotExist(err) {
		t.Fatalf("openVectors created a side index with no model: err = %v", err)
	}
}
