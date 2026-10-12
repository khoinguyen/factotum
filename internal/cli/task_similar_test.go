package cli

import (
	"fmt"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func similarIDs(tasks []*core.Ticket) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, string(task.ID))
	}
	return ids
}

func similarContains(tasks []*core.Ticket, id core.TicketID) bool {
	for _, task := range tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}

// TestRankSimilarStripsCommonTokensSoTrueMatchIsNotCrowded is the crowding
// fixture: a dozen distractors that each share the common tokens "fix"/"flow"
// with the query would out-score the true match on raw overlap and push it out
// of the bounded shortlist. With stopwords stripped and common tokens
// down-weighted, the true match ranks first.
func TestRankSimilarStripsCommonTokensSoTrueMatchIsNotCrowded(t *testing.T) {
	var tasks []*core.Ticket
	for i := 0; i < 12; i++ {
		tasks = append(tasks, &core.Ticket{
			ID:    core.TicketID(fmt.Sprintf("t-distractor-%02d", i)),
			Title: "Fix the flow",
		})
	}
	truth := &core.Ticket{ID: "t-truth", Title: "Rework sign authentication"}
	tasks = append(tasks, truth)

	got := rankSimilar("Fix sign-in flow", tasks, "", 8)
	if !similarContains(got, truth.ID) {
		t.Fatalf("true match %s was crowded out of the shortlist: %v", truth.ID, similarIDs(got))
	}
	if got[0].ID != truth.ID {
		t.Fatalf("shortlist[0] = %v, want the true match %s first", similarIDs(got), truth.ID)
	}
}

// TestRankSimilarDropsStopwordOnlyOverlap proves a candidate that shares only
// filler tokens (task/add/fix) does not enter the shortlist at all.
func TestRankSimilarDropsStopwordOnlyOverlap(t *testing.T) {
	tasks := []*core.Ticket{{ID: "t-noise", Title: "Add fix for the task"}}

	if got := rankSimilar("Fix the task", tasks, "", 8); len(got) != 0 {
		t.Fatalf("stopword-only overlap shortlisted %v, want none", similarIDs(got))
	}
}

// TestRankSimilarMorphologicalVariantIsShortlisted is the morphological fixture:
// the candidate shares no exact token with the query, only the variant
// "pagination"/"paginate"; a stem match still shortlists it.
func TestRankSimilarMorphologicalVariantIsShortlisted(t *testing.T) {
	tasks := []*core.Ticket{{ID: "t-pagination", Title: "Pagination for events"}}

	got := rankSimilar("Paginate audit log", tasks, "", 8)
	if !similarContains(got, "t-pagination") {
		t.Fatalf("morphological variant not shortlisted: %v", similarIDs(got))
	}
}

// TestRankSimilarShortlistIsBounded pins the two-stage contract: retrieval never
// hands the judge more than limit candidates.
func TestRankSimilarShortlistIsBounded(t *testing.T) {
	var tasks []*core.Ticket
	for i := 0; i < 20; i++ {
		tasks = append(tasks, &core.Ticket{
			ID:    core.TicketID(fmt.Sprintf("t-widget-%02d", i)),
			Title: fmt.Sprintf("Widget performance tuning %d", i),
		})
	}

	if got := rankSimilar("Widget performance", tasks, "", 8); len(got) != 8 {
		t.Fatalf("shortlist = %d tasks, want 8", len(got))
	}
}

// TestRankSimilarDeterministicAcrossInputOrders guards store independence: two
// stores may list the same tasks in different orders, so equal scores must break
// ties by a stable key, not by arrival order.
func TestRankSimilarDeterministicAcrossInputOrders(t *testing.T) {
	forward := []*core.Ticket{
		{ID: "t-a", Title: "Cache warmup"},
		{ID: "t-b", Title: "Cache warmup"},
		{ID: "t-c", Title: "Cache warmup"},
		{ID: "t-d", Title: "Cache warmup"},
	}
	reversed := []*core.Ticket{forward[3], forward[2], forward[1], forward[0]}

	first := similarIDs(rankSimilar("Cache warmup", forward, "", 8))
	second := similarIDs(rankSimilar("Cache warmup", reversed, "", 8))
	if len(first) != len(second) {
		t.Fatalf("shortlist lengths differ across orders: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("shortlist order depends on input order: %v vs %v", first, second)
		}
	}
}

// TestTaskCreateDuplicateCheckAsksJudgeOnce pins the other half of the two-stage
// contract: however retrieval ranks, the judge is asked exactly once.
func TestTaskCreateDuplicateCheckAsksJudgeOnce(t *testing.T) {
	r := newRunner(t)
	stub := fake.New(map[string]judge.Answer{})
	r.judge = stub
	projectID := firstField(t, r.run("project", "create", "Acme"))

	r.run("task", "create", "-p", projectID, "-t", "Rework sign-in flow")
	r.run("task", "create", "-p", projectID, "-t", "Fix sign-in flow")

	if got := len(stub.Requests()); got != 1 {
		t.Fatalf("judge requests = %d, want exactly 1", got)
	}
}
