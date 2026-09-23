package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Filler fills in what detection can on a record written before that
// detection existed (client, provider, model vendor). headers are the
// stored (masked) request headers, nil when the detail is gone. It reports
// whether it changed anything, and must leave fields already set alone, so
// running it again changes nothing.
type Filler func(r *Record, headers map[string]string) bool

// BackfillReport is what Backfill did (or, dry, would do).
type BackfillReport struct {
	DryRun         bool `json:"dry_run"`
	DetailsScanned int  `json:"details_scanned"`
	DetailsUpdated int  `json:"details_updated"`
	// SummariesUpdated counts index lines changed, in IndexFiles.
	SummariesUpdated int      `json:"summaries_updated"`
	IndexFiles       []string `json:"index_files_rewritten"`
	Skipped          []string `json:"skipped,omitempty"`
}

// Backfill runs fill over every detail and summary. A changed detail is
// rewritten atomically in the current format (a legacy file is converted,
// as Compact does, keeping its modification time); a changed index file
// is rewritten through a temp file and a rename. Summaries take the values
// their detail got, or fill(nil headers) when the detail is gone.
//
// Run it with the gateway stopped when possible: an index file is checked
// for appends just before it is replaced and re-read if it grew, but a
// gateway appending in that instant could lose a summary line.
func (s *Store) Backfill(fill Filler, dryRun bool) (BackfillReport, error) {
	rep := BackfillReport{DryRun: dryRun}
	recs, err := s.scanRecords()
	if err != nil {
		return rep, err
	}
	filled := map[string]Record{}
	for _, ri := range recs {
		rep.DetailsScanned++
		d, err := s.readForBackfill(ri)
		if err != nil {
			rep.Skipped = append(rep.Skipped, filepath.Base(ri.path)+": "+err.Error())
			continue
		}
		if !fill(&d.Record, d.RequestHeaders) {
			continue
		}
		filled[ri.id] = d.Record
		rep.DetailsUpdated++
		if dryRun {
			continue
		}
		info, statErr := os.Stat(ri.path)
		if err := s.writeDetail(d); err != nil {
			return rep, fmt.Errorf("%s: %w", filepath.Base(ri.path), err)
		}
		if statErr == nil {
			mt := info.ModTime()
			_ = os.Chtimes(s.recordPath(ri.id), mt, mt)
		}
		if ri.legacy {
			if err := os.Remove(s.legacyPath(ri.id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return rep, err
			}
		}
	}

	apply := func(r *Record) bool {
		if f, ok := filled[r.ID]; ok {
			changed := false
			if r.Client == nil && f.Client != nil {
				r.Client, changed = f.Client, true
			}
			if r.Provider == "" && f.Provider != "" {
				r.Provider, changed = f.Provider, true
			}
			if r.ModelVendor == "" && f.ModelVendor != "" {
				r.ModelVendor, changed = f.ModelVendor, true
			}
			return changed
		}
		return fill(r, nil)
	}
	for _, name := range s.indexFiles() {
		n, err := s.backfillIndex(name, apply, dryRun)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		if n > 0 {
			rep.SummariesUpdated += n
			rep.IndexFiles = append(rep.IndexFiles, name)
		}
	}
	if !dryRun {
		s.mu.Lock()
		for i := range s.recent {
			apply(&s.recent[i])
		}
		s.mu.Unlock()
	}
	return rep, nil
}

func (s *Store) readForBackfill(ri recInfo) (*Detail, error) {
	if !ri.legacy {
		return s.decodeRecord(ri.path)
	}
	b, err := os.ReadFile(ri.path)
	if err != nil {
		return nil, err
	}
	var d Detail
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		d.ID = ri.id
	}
	if d.ID != ri.id {
		return nil, fmt.Errorf("holds request %q", d.ID)
	}
	return &d, nil
}

// backfillIndex rewrites one index file, returning how many lines changed.
func (s *Store) backfillIndex(name string, apply func(*Record) bool, dryRun bool) (int, error) {
	path := filepath.Join(s.dir, name)
	s.idx.Lock() // appends from this process wait
	defer s.idx.Unlock()
	for attempt := 0; ; attempt++ {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		lines := bytes.SplitAfter(b, []byte("\n"))
		changed := 0
		var out bytes.Buffer
		for _, ln := range lines {
			if len(bytes.TrimSpace(ln)) == 0 {
				out.Write(ln)
				continue
			}
			var r Record
			if json.Unmarshal(ln, &r) != nil || !apply(&r) {
				out.Write(ln)
				continue
			}
			nb, err := json.Marshal(r)
			if err != nil {
				out.Write(ln)
				continue
			}
			changed++
			out.Write(nb)
			if bytes.HasSuffix(ln, []byte("\n")) {
				out.WriteByte('\n')
			}
		}
		if changed == 0 || dryRun {
			return changed, nil
		}
		// Another process (a running gateway) may have appended since the
		// read: then read again rather than drop its lines.
		if info, err := os.Stat(path); err == nil && info.Size() != int64(len(b)) && attempt < 5 {
			continue
		}
		return changed, writeAtomic(path, out.Bytes())
	}
}
