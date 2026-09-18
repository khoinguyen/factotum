package cli

import (
	"os"
	"strconv"
	"strings"
)

// textWidth returns the wrapping width for human-readable text output. It
// honours $COLUMNS and otherwise defaults to 100.
func textWidth() int {
	if value := os.Getenv("COLUMNS"); value != "" {
		if width, err := strconv.Atoi(value); err == nil && width >= 20 {
			return width
		}
	}
	return 100
}

// wrapText word-wraps each line to width, preserving explicit line breaks and
// blank lines. Words longer than the width are left intact rather than broken.
func wrapText(s string, width int) string {
	s = strings.TrimRight(s, "\n")
	if width < 20 {
		width = 20
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) []string {
	if len(line) <= width {
		return []string{line}
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	hanging := indent
	switch trimmed := strings.TrimLeft(line, " \t"); {
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
		hanging = indent + "  "
	default:
		if n := numberedPrefix(trimmed); n > 0 {
			hanging = indent + strings.Repeat(" ", n)
		}
	}

	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}
	var body []string
	current := ""
	for _, word := range words {
		switch {
		case current == "":
			current = word
		case len(current)+1+len(word) <= width:
			current += " " + word
		default:
			body = append(body, current)
			current = word
		}
	}
	if current != "" {
		body = append(body, current)
	}

	lines := make([]string, 0, len(body))
	for i, text := range body {
		if i == 0 {
			lines = append(lines, indent+text)
			continue
		}
		lines = append(lines, hanging+text)
	}
	return lines
}

// numberedPrefix returns the length of a leading "N. " list marker, or 0.
func numberedPrefix(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(s) && s[i] == '.' && s[i+1] == ' ' {
		return i + 2
	}
	return 0
}
