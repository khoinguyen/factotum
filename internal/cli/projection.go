package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

// parseFieldList splits a comma-separated field list into ordered, non-empty
// names. It is the shared parser behind `--fields` on task get and context.
func parseFieldList(value string) []string {
	var out []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// validateFieldList reports the first requested name that is not allowed.
func validateFieldList(requested, allowed []string) error {
	known := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		known[name] = true
	}
	for _, name := range requested {
		if !known[name] {
			return fmt.Errorf("%w: unknown field %q", core.ErrInvalid, name)
		}
	}
	return nil
}

// projectFields selects the requested keys from values after validating them
// against allowed. Absent keys are omitted from the result.
func projectFields(values map[string]any, allowed, requested []string) (map[string]any, error) {
	if err := validateFieldList(requested, allowed); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(requested))
	for _, name := range requested {
		if value, ok := values[name]; ok {
			out[name] = value
		}
	}
	return out, nil
}

// formatFieldValue renders one projected value as a single text line.
func formatFieldValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	case []string:
		return strings.Join(typed, ", ")
	case []checkResultDoc:
		return summarizeChecks(typed)
	default:
		return fmt.Sprintf("%v", value)
	}
}
