package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/embed"
)

// These tests exercise the real embedding path end to end: an HTTP provider
// (a llama.cpp / Ollama / OpenAI-compatible server) and the SQLite side index.
// They are opt-in so `go test ./...` and the CI suite stay hermetic.
//
// Run the real model through the containerized harness:
//
//	mise run test-embed
//
// Or against an endpoint you already have (Ollama, llama-server, OpenAI):
//
//	FACTOTUM_EMBED_TEST_ENDPOINT=http://127.0.0.1:8080 \
//	FACTOTUM_EMBED_TEST_PROVIDER=openai \
//	FACTOTUM_EMBED_TEST_MODEL=nomic-ai/nomic-embed-text-v1.5-GGUF:Q4_K_M \
//	go test -run TestRealEmbedding -v ./internal/cli/
//
// FACTOTUM_EMBED_TEST_PROVIDER defaults to openai and FACTOTUM_EMBED_TEST_MODEL to
// the pinned nomic model, so only the endpoint is required.

const defaultEmbedTestModel = "nomic-ai/nomic-embed-text-v1.5-GGUF:Q4_K_M"

// realEmbedConfig reads the opt-in gate and skips when it is unset. The endpoint is
// the gate; provider and model default so a local llama-server needs one variable.
func realEmbedConfig(t *testing.T) (provider, endpoint, model string) {
	t.Helper()
	endpoint = os.Getenv("FACTOTUM_EMBED_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set FACTOTUM_EMBED_TEST_ENDPOINT to run the real-embedding integration test")
	}
	provider = envOr("FACTOTUM_EMBED_TEST_PROVIDER", "openai")
	model = envOr("FACTOTUM_EMBED_TEST_MODEL", defaultEmbedTestModel)
	return provider, endpoint, model
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// preflightEmbed embeds one text directly, so an unreachable or misconfigured
// endpoint fails with its own error instead of a confusing "recall missed".
func preflightEmbed(t *testing.T, provider, endpoint, model string) {
	t.Helper()
	e, err := embed.New(provider, func(string) string { return "" }, map[string]string{
		"endpoint": endpoint,
		"model":    model,
	})
	if err != nil {
		t.Fatalf("build %s embedder: %v", provider, err)
	}
	vectors, err := e.Embed(context.Background(), embed.InputQuery, []string{"preflight"})
	if err != nil {
		t.Fatalf("real endpoint %s is not usable: %v", endpoint, err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		t.Fatalf("real endpoint %s returned no vector", endpoint)
	}
}

// realEmbedRunner builds a runner whose [embed] table points at the real endpoint.
// Unlike vectorRunner it injects no fake: the CLI builds the HTTP transport and the
// SQLite side index from config, so the whole real path runs.
func realEmbedRunner(t *testing.T, provider, endpoint, model string) *runner {
	t.Helper()
	r := newRunner(t)
	body := fmt.Sprintf("[embed]\nprovider = %q\nendpoint = %q\nmodel = %q\n", provider, endpoint, model)
	if err := os.WriteFile(r.userPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	return r
}

// realEmbedRunnerOn points a runner at an existing store and side index, so
// persistence and the model-mismatch path can be exercised across invocations.
func realEmbedRunnerOn(t *testing.T, path, provider, endpoint, model string) *runner {
	t.Helper()
	r := realEmbedRunner(t, provider, endpoint, model)
	r.path = path
	return r
}

// lexicalRunnerOn shares an existing store but configures no embedder, so it is the
// lexical baseline the vector path is measured against.
func lexicalRunnerOn(t *testing.T, path string) *runner {
	t.Helper()
	r := newRunner(t)
	r.path = path
	return r
}

type memoryHit struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// searchTitles runs a memory search in JSON and returns the ranked titles. The
// judge rerank is off, so the order is the fusion of lexical and vector only.
func searchTitles(t *testing.T, r *runner, project, query string) []string {
	t.Helper()
	out := r.run("memory", "search", query, "-p", project, "--no-rerank", "-o", "json")
	var hits []memoryHit
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatalf("search %q json: %v\n%s", query, err, out)
	}
	titles := make([]string, len(hits))
	for i, hit := range hits {
		titles[i] = hit.Title
	}
	return titles
}

func rankOf(titles []string, want string) int {
	for i, title := range titles {
		if title == want {
			return i
		}
	}
	return -1
}

func TestRealEmbeddingSmoke(t *testing.T) {
	provider, endpoint, model := realEmbedConfig(t)
	preflightEmbed(t, provider, endpoint, model)
	r := realEmbedRunner(t, provider, endpoint, model)

	project := firstField(t, r.run("project", "create", "Embed Smoke"))
	r.run("memory", "create", "-p", project, "-t", "Terraform notes", "--brief", "how we version infrastructure changes")
	r.run("memory", "create", "-p", project, "-t", "Kubernetes notes", "--brief", "cluster upgrade runbook")

	// The premise: "provisioning" shares no token with the Terraform memory, so the
	// lexical baseline misses it. Proving that with a lexical-only runner over the
	// same store is what makes the vector hit below genuinely semantic.
	lexical := lexicalRunnerOn(t, r.path)
	if titles := searchTitles(t, lexical, project, "provisioning"); rankOf(titles, "Terraform notes") >= 0 {
		t.Fatalf("premise broken: lexical search matched a tokenless query: %v", titles)
	}

	titles := searchTitles(t, r, project, "provisioning")
	if rank := rankOf(titles, "Terraform notes"); rank < 0 {
		t.Fatalf("real embedding recall missed the semantic match: %v", titles)
	} else if rank != 0 {
		t.Errorf("semantic match ranked %d, want first: %v", rank, titles)
	}
	if rank := rankOf(titles, "Kubernetes notes"); rank == 0 {
		t.Errorf("unrelated memory ranked first: %v", titles)
	}

	// Vectors land in the SQLite side index beside the store, so a later invocation
	// (already the case above) and reindex both see them.
	if _, err := os.Stat(r.path + ".vectors.db"); err != nil {
		t.Fatalf("vector side index not persisted: %v", err)
	}

	var reindexed struct {
		Reindexed int    `json:"reindexed"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal([]byte(r.run("memory", "reindex", "-p", project, "-o", "json")), &reindexed); err != nil {
		t.Fatalf("reindex json: %v", err)
	}
	if reindexed.Reindexed != 2 || reindexed.Model != model {
		t.Fatalf("reindex = %+v, want 2 on %s", reindexed, model)
	}

	// A different configured model is a mismatch: the vectors are not comparable, so
	// the search warns and stays lexical rather than mixing models.
	other := realEmbedRunnerOn(t, r.path, provider, endpoint, "some-other-model")
	_, stderr := other.runSplit("memory", "search", "provisioning", "-p", project, "--no-rerank")
	if !strings.Contains(stderr, "model mismatch") {
		t.Fatalf("a mismatched model must be reported:\n%s", stderr)
	}

	// Reindexing with the real model repairs the index and recall returns.
	r.run("memory", "reindex", "-p", project)
	if titles := searchTitles(t, r, project, "provisioning"); rankOf(titles, "Terraform notes") < 0 {
		t.Fatalf("recall did not return after reindex: %v", titles)
	}
}

// realEmbedEval is a small labeled set: each query paraphrases its memory without
// sharing a token, so lexical recall is expected to be zero and vector recall is
// what is measured.
var realEmbedEval = []struct {
	title string
	brief string
	query string
}{
	{"Terraform state", "how we version cloud infrastructure", "provisioning"},
	{"Kubernetes runbook", "cluster upgrade procedure", "container orchestration"},
	{"Postgres backups", "nightly database dump and restore", "disaster recovery"},
	{"Auth tokens", "rotating service credentials", "secret management"},
	{"CI pipeline", "build and deploy automation", "continuous delivery"},
}

// TestRealEmbeddingRecallEval puts a number on "does vector recall actually help":
// lexical recall@1 versus vector recall@1 and MRR over labeled paraphrases.
func TestRealEmbeddingRecallEval(t *testing.T) {
	provider, endpoint, model := realEmbedConfig(t)
	preflightEmbed(t, provider, endpoint, model)
	r := realEmbedRunner(t, provider, endpoint, model)

	project := firstField(t, r.run("project", "create", "Embed Eval"))
	for _, c := range realEmbedEval {
		r.run("memory", "create", "-p", project, "-t", c.title, "--brief", c.brief)
	}
	lexical := lexicalRunnerOn(t, r.path)

	var lexicalTop1, lexicalTop3, vectorTop1, vectorTop3 int
	var lexicalRR, vectorRR float64
	for _, c := range realEmbedEval {
		lexRank := rankOf(searchTitles(t, lexical, project, c.query), c.title)
		vecRank := rankOf(searchTitles(t, r, project, c.query), c.title)
		if lexRank == 0 {
			lexicalTop1++
		}
		if lexRank >= 0 && lexRank < 3 {
			lexicalTop3++
		}
		if vecRank == 0 {
			vectorTop1++
		}
		if vecRank >= 0 && vecRank < 3 {
			vectorTop3++
		}
		lexicalRR += reciprocalRank(lexRank)
		vectorRR += reciprocalRank(vecRank)
		t.Logf("query %-24q lexical=%d vector=%d", c.query, lexRank, vecRank)
	}

	n := float64(len(realEmbedEval))
	t.Logf("recall@1 lexical=%.2f vector=%.2f | recall@3 lexical=%.2f vector=%.2f | MRR lexical=%.3f vector=%.3f (n=%d)",
		float64(lexicalTop1)/n, float64(vectorTop1)/n,
		float64(lexicalTop3)/n, float64(vectorTop3)/n,
		lexicalRR/n, vectorRR/n, len(realEmbedEval))

	if vectorTop3 < lexicalTop3 {
		t.Fatalf("vector recall@3 (%d) is worse than lexical (%d)", vectorTop3, lexicalTop3)
	}
	if vectorTop3 != len(realEmbedEval) {
		t.Fatalf("vector recall@3 = %d/%d; the real model missed labeled paraphrases", vectorTop3, len(realEmbedEval))
	}
}

// reciprocalRank is 0 when the target is absent, else 1/(rank+1) with rank 0-based.
func reciprocalRank(rank int) float64 {
	if rank < 0 {
		return 0
	}
	return 1 / float64(rank+1)
}
