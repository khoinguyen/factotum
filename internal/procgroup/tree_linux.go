//go:build linux

package procgroup

import (
	"os"
	"strconv"
	"strings"
)

// children returns every live process keyed by parent id, read from /proc. The
// parent is the second field of /proc/<pid>/stat after the command name, which
// is parenthesised and may itself contain spaces or parentheses, so the parse
// starts at the last ')'.
func children() (map[int][]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	kids := make(map[int][]int, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			// The process exited between the listing and the read.
			continue
		}
		rest := string(stat)
		if idx := strings.LastIndexByte(rest, ')'); idx >= 0 {
			rest = rest[idx+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		kids[ppid] = append(kids[ppid], pid)
	}
	return kids, nil
}
