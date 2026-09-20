package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestSkillList(t *testing.T) {
	r := newRunner(t)
	out := r.run("skill", "list")
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "DESCRIPTION") || !strings.Contains(out, "ft") {
		t.Fatalf("skill list missing header or default skill:\n%s", out)
	}

	jsonOut := r.run("skill", "list", "-o", "json")
	var entries []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &entries); err != nil {
		t.Fatalf("skill list json: %v\n%s", err, jsonOut)
	}
	if len(entries) == 0 || entries[0].Name != "ft" || entries[0].Description == "" {
		t.Fatalf("skill list json = %+v", entries)
	}
}

func TestSkillGetPrintsRawMarkdown(t *testing.T) {
	r := newRunner(t)
	out := r.run("skill", "get")
	if !strings.Contains(out, "#") {
		t.Fatalf("skill get did not print markdown:\n%s", out)
	}
	if named := r.run("skill", "get", "ft"); named != out {
		t.Fatalf("skill get ft differs from the default:\n%s", named)
	}
}

func TestSkillGetUnknown(t *testing.T) {
	r := newRunner(t)
	err := r.runErr("skill", "get", "nope")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("skill get nope error = %v, want ErrNotFound", err)
	}
}
