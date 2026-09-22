package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

type taskSearchResult struct {
	ID    string
	Title string
}

func searchIDs(t *testing.T, out string) []string {
	t.Helper()
	var results []taskSearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("task search json = %v\n%s", err, out)
	}
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTaskSearchRanksTitleBodyNotes(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	title := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Terraform notes", "-b", "apply in devops"))
	body := firstField(t, r.run("task", "create", "-p", projectID, "-t", "run scripts", "-b", "terraform then kubectl"))
	note := firstField(t, r.run("task", "create", "-p", projectID, "-t", "unrelated", "-b", "nothing here"))
	r.run("task", "note", "create", note, "-b", "terraform in the notes")

	out := r.run("task", "search", "terraform", "-p", projectID, "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{title, body, note}) {
		t.Fatalf("task search order = %v, want title, body, note", got)
	}
}

func TestTaskSearchExcludesSystemNotes(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	task := firstField(t, r.run("task", "create", "-p", projectID, "-t", "unrelated", "-b", "nothing here"))
	r.run("task", "note", "create", task, "-b", "sysprobe triage noise", "--system")

	out := r.run("task", "search", "sysprobe", "-p", projectID, "-o", "json")
	if got := searchIDs(t, out); len(got) != 0 {
		t.Fatalf("task search = %v, want no hits for a system note", got)
	}

	// The generated note stays on the task; only search skips it.
	out = r.run("task", "get", task, "-o", "json")
	if !strings.Contains(out, "sysprobe triage noise") {
		t.Fatalf("task get should retain the system note:\n%s", out)
	}
	if !strings.Contains(out, `"system": true`) {
		t.Fatalf("task get should expose the system marker:\n%s", out)
	}
}

func TestTaskSearchFiltersByProjectAndStatus(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	todo := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Terraform notes"))
	started := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Terraform scripts"))
	r.run("task", "start", started)

	out := r.run("task", "search", "terraform", "-p", projectID, "--status", "todo", "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{todo}) {
		t.Fatalf("task search --status todo = %v, want [%s]", got, todo)
	}
}

func TestTaskSearchReranksWithJudge(t *testing.T) {
	r := newRunner(t)
	r.judge = fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-b", Probabilities: map[string]float64{"t-a": 0.1, "t-b": 0.9}, Confidence: 0.9},
		"exists": {Probability: 0.9},
	})
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-a", "-t", "Terraform notes"))
	second := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-b", "-t", "Terraform scripts"))

	out := r.run("task", "search", "terraform", "-p", projectID, "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{second, first}) {
		t.Fatalf("task search rerank = %v, want [%s %s]", got, second, first)
	}

	out = r.run("task", "search", "terraform", "-p", projectID, "--no-rerank", "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{first, second}) {
		t.Fatalf("task search --no-rerank = %v, want [%s %s]", got, first, second)
	}
}

func TestTaskSearchFallsBackWithoutJudge(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-a", "-t", "Terraform notes"))
	second := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-b", "-t", "Terraform scripts"))

	out := r.run("task", "search", "terraform", "-p", projectID, "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{first, second}) {
		t.Fatalf("task search without a judge = %v, want lexical order", got)
	}
}

func TestTaskSearchEmptyQueryKeepsAllWithJudge(t *testing.T) {
	r := newRunner(t)
	// A judge that claims nothing answers must not hide an empty-query listing.
	r.judge = fake.New(map[string]judge.Answer{
		"which":  {Confidence: 0},
		"exists": {Probability: 0},
	})
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-a", "-t", "Terraform notes"))
	second := firstField(t, r.run("task", "create", "-p", projectID, "--id", "t-b", "-t", "Terraform scripts"))

	out := r.run("task", "search", "", "-p", projectID, "-o", "json")
	if got := searchIDs(t, out); !equalIDs(got, []string{first, second}) {
		t.Fatalf("task search empty query with a judge = %v, want [%s %s]", got, first, second)
	}
}

func TestTaskSearchRequiresQuery(t *testing.T) {
	r := newRunner(t)
	_, stderr := r.runSplit("task", "search")
	if !strings.Contains(stderr, "accepts 1 arg") && !strings.Contains(stderr, "arg") {
		t.Fatalf("task search without a query should be a usage error:\n%s", stderr)
	}
}
