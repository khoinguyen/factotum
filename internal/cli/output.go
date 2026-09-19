package cli

import "fmt"

// field is one `key: value` line of single-result text output.
type field struct {
	Key   string
	Value string
}

func f(key string, value any) field {
	return field{Key: key, Value: fmt.Sprintf("%v", value)}
}

// printFields writes yaml-like `key: value` lines. The project, when relevant,
// is always the last field.
func (d *Deps) printFields(fields ...field) {
	for _, field := range fields {
		d.printf("%s: %s\n", field.Key, field.Value)
	}
}
