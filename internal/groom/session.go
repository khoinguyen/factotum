package groom

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SessionManifestFileName is the deterministic name of a session's manifest:
// the small record `ft groom list` and `ft groom show` read to report a
// session's scope, outputs, and produced tasks.
const SessionManifestFileName = "session.json"

// SessionRecord is the durable summary of one grooming session: what it scoped,
// which artifacts hold its outputs, and which tasks it produced. `ft groom`
// writes it at capture; the read surface lists and shows it.
type SessionRecord struct {
	ID        string      `json:"id" yaml:"id"`
	Project   string      `json:"project" yaml:"project"`
	Mode      string      `json:"mode" yaml:"mode"`
	CreatedAt time.Time   `json:"created_at" yaml:"created_at"`
	Scope     []ScopeItem `json:"scope" yaml:"scope"`
	Report    string      `json:"report" yaml:"report"`
	Deferred  string      `json:"deferred" yaml:"deferred"`
	Produced  []string    `json:"produced" yaml:"produced"`
}

// SessionManifestPath returns the path of a session's manifest.
func SessionManifestPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), SessionManifestFileName)
}

// WriteSession writes rec to its session directory atomically, so a reader never
// sees a half-written manifest.
func WriteSession(dataDir string, rec SessionRecord) error {
	path := SessionManifestPath(dataDir, rec.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create grooming session dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode grooming session %s: %w", rec.ID, err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), SessionManifestFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("write grooming session %s: %w", rec.ID, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write grooming session %s: %w", rec.ID, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write grooming session %s: %w", rec.ID, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write grooming session %s: %w", rec.ID, err)
	}
	return nil
}

// ReadSession reads one session's manifest. A missing session returns an error
// wrapping fs.ErrNotExist.
func ReadSession(dataDir, sessionID string) (SessionRecord, error) {
	path := SessionManifestPath(dataDir, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("read grooming session %s: %w", sessionID, err)
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return SessionRecord{}, fmt.Errorf("parse grooming session %s: %w", sessionID, err)
	}
	return rec, nil
}

// ListSessions returns every session with a manifest under dataDir, oldest
// first. A directory without a manifest (an interrupted or unrelated entry) is
// skipped, not an error.
func ListSessions(dataDir string) ([]SessionRecord, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, SessionsDirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list grooming sessions: %w", err)
	}
	var sessions []SessionRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rec, err := ReadSession(dataDir, entry.Name())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		sessions = append(sessions, rec)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
		}
		return sessions[i].ID < sessions[j].ID
	})
	return sessions, nil
}
