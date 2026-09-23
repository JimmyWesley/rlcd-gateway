package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CompactReport is what Compact did (or, dry, would do).
type CompactReport struct {
	DryRun    bool `json:"dry_run"`
	Converted int  `json:"converted"`
	// Skipped counts legacy files that could not be read; they are left as
	// they are.
	Skipped []string `json:"skipped,omitempty"`
	// BytesBefore is the size of the legacy files converted; BytesAfter what
	// the requests and blobs directories hold afterwards.
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
}

// Compact rewrites legacy requests/<id>.json files into the deduplicated,
// compressed format. It is optional: legacy files are read as they are. The
// content is kept whatever the bodies policy says, and the new file keeps
// the old one's modification time. Safe to run next to a live gateway:
// the new file is written before the old one is removed, and Get prefers
// the new one.
func (s *Store) Compact(dryRun bool) (CompactReport, error) {
	rep := CompactReport{DryRun: dryRun}
	dir := filepath.Join(s.dir, "requests")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return rep, err
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, legacyExt) || strings.HasSuffix(name, recordExt) {
			continue
		}
		id := strings.TrimSuffix(name, legacyExt)
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil || !validID(id) {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			rep.Skipped = append(rep.Skipped, name+": "+err.Error())
			continue
		}
		var d Detail
		if err := json.Unmarshal(b, &d); err != nil {
			rep.Skipped = append(rep.Skipped, name+": "+err.Error())
			continue
		}
		if d.ID == "" {
			d.ID = id
		}
		if d.ID != id {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: holds request %q", name, d.ID))
			continue
		}
		rep.Converted++
		rep.BytesBefore += info.Size()
		if dryRun {
			continue
		}
		if err := s.writeDetail(&d); err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		mt := info.ModTime()
		_ = os.Chtimes(s.recordPath(id), mt, mt)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return rep, err
		}
	}
	if !dryRun {
		rep.BytesAfter = dirBytes(dir) + dirBytes(filepath.Join(s.dir, "blobs"))
	}
	return rep, nil
}

// dirBytes sums the sizes of the regular files under dir.
func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if info, err := e.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
