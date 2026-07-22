package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Plan is the complete state record for one upgrade.
type Plan struct {
	ID         string       `json:"id"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	State      string       `json:"state"`
	Current    Release      `json:"current"`
	Target     Release      `json:"target"`
	BlueAddr   string       `json:"blue_addr"`   // previously active instance
	GreenAddr  string       `json:"green_addr"`  // newly staged instance
	ActiveAddr string       `json:"active_addr"` // proxy's current target
	History    []StateEvent `json:"history"`
	Audit      []AuditEntry `json:"audit,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// ActivePointer is the proxy's current target, persisted separately so
// the daemon can restore the forwarding target on restart without
// depending on any specific plan's state.
type ActivePointer struct {
	Addr    string    `json:"addr"`
	Version string    `json:"version"`
	Updated time.Time `json:"updated"`
}

// Store persists plans to <dir>/plans/<id>.json and active pointer to <dir>/active.json.
type Store struct{ dir string }

func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) plansDir() string { return filepath.Join(s.dir, "plans") }

// Save writes the plan JSON. Updates UpdatedAt. Creates plans dir if needed.
// Atomic: writes to a temp file then renames (audit I2: previously a plain
// WriteFile, so a crash mid-write left a truncated/corrupt plan file that
// LoadAll then silently skipped — losing the in-flight upgrade state).
func (s *Store) Save(p *Plan) error {
	if err := os.MkdirAll(s.plansDir(), 0o755); err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.plansDir(), p.ID+".json"), data, 0o644)
}

// atomicWrite writes data to path via a temp file + rename. POSIX rename
// is atomic, so readers see either the old or the new file, never a
// partial write. Audit I13: cleans up the .tmp file on rename failure so
// a stale tmp doesn't accumulate across repeated disk-full situations.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return err
	}
	return nil
}

// SweepStaleTmp removes any leftover .tmp files in the plans directory.
// Audit I13: called at daemon startup to clean up tmp files that
// atomicWrite failed to clean (e.g. process killed between WriteFile and
// Rename).
func (s *Store) SweepStaleTmp() {
	entries, err := os.ReadDir(s.plansDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json.tmp") {
			_ = os.Remove(filepath.Join(s.plansDir(), e.Name()))
		}
	}
}

// Load reads a plan by ID.
func (s *Store) Load(id string) (*Plan, error) {
	data, err := os.ReadFile(filepath.Join(s.plansDir(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", id, err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns all plan IDs sorted by file modification time (newest first).
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.plansDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type info struct {
		id string
		mt time.Time
	}
	var infos []info
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue // not a plan file
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		name = name[:len(name)-len(".json")]
		infos = append(infos, info{id: name, mt: fi.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].mt.After(infos[j].mt) })
	ids := make([]string, len(infos))
	for i, in := range infos {
		ids[i] = in.id
	}
	return ids, nil
}

// LoadAll returns all plans sorted newest-first. Used by the daemon for
// checker dedup (I1) and in-progress-plan restore on restart (I3). A
// corrupt single plan file is skipped with its error logged, not fatal —
// one bad file shouldn't break the daemon's view of the rest.
func (s *Store) LoadAll() ([]*Plan, error) {
	ids, err := s.List()
	if err != nil {
		return nil, err
	}
	var plans []*Plan
	for _, id := range ids {
		p, err := s.Load(id)
		if err != nil {
			// Skip corrupt plan rather than failing the whole listing.
			continue
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// LoadActive returns the active pointer, or nil if none saved.
func (s *Store) LoadActive() (*ActivePointer, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "active.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ap ActivePointer
	if err := json.Unmarshal(data, &ap); err != nil {
		return nil, err
	}
	return &ap, nil
}

// SaveActive writes the active pointer atomically (audit I2).
func (s *Store) SaveActive(ap *ActivePointer) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	ap.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(ap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal active: %w", err)
	}
	return atomicWrite(filepath.Join(s.dir, "active.json"), data, 0o644)
}
