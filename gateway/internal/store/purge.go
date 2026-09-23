package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// recInfo is one detail file on disk.
type recInfo struct {
	id     string
	path   string
	legacy bool
	size   int64
	time   time.Time
	head   head
	// headErr is set when the head could not be read: the refs of that
	// record are unknown, so no blob may be swept in this run.
	headErr error
}

type blobInfo struct {
	path  string
	size  int64
	mtime time.Time
}

// idTime reads the time our ids start with (relay.NewID, UTC).
func idTime(id string) (time.Time, bool) {
	if len(id) < 15 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405", id[:15])
	return t, err == nil
}

// scanRecords lists the detail files, oldest first. A record's time is the
// one in its id, or the file's modification time for ids we did not make.
func (s *Store) scanRecords() ([]recInfo, error) {
	dir := filepath.Join(s.dir, "requests")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	byID := map[string]int{}
	var out []recInfo
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		var r recInfo
		switch {
		case strings.HasSuffix(name, recordExt):
			r.id = strings.TrimSuffix(name, recordExt)
		case strings.HasSuffix(name, legacyExt):
			r.id, r.legacy = strings.TrimSuffix(name, legacyExt), true
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		r.path, r.size = filepath.Join(dir, name), info.Size()
		if t, ok := idTime(r.id); ok {
			r.time = t
		} else {
			r.time = info.ModTime().UTC()
		}
		if !r.legacy {
			r.head, r.headErr = readHead(r.path)
		}
		if i, dup := byID[r.id]; dup {
			// Both formats for one id (compact interrupted): Get reads the
			// new one; both go together.
			out[i].size += r.size
			if !r.legacy {
				out[i].head, out[i].headErr = r.head, r.headErr
			}
			out[i].legacy = false
			continue
		}
		byID[r.id] = len(out)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].time.Before(out[j].time) || out[i].time.Equal(out[j].time) && out[i].id < out[j].id
	})
	return out, nil
}

// scanBlobs lists blobs/<xx>/<sha256>.gz by hash.
func (s *Store) scanBlobs() map[string]blobInfo {
	out := map[string]blobInfo{}
	root := filepath.Join(s.dir, "blobs")
	subs, _ := os.ReadDir(root)
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		ents, _ := os.ReadDir(filepath.Join(root, sub.Name()))
		for _, e := range ents {
			h := strings.TrimSuffix(e.Name(), ".gz")
			if e.IsDir() || !validHash(h) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out[h] = blobInfo{path: filepath.Join(root, sub.Name(), e.Name()), size: info.Size(), mtime: info.ModTime()}
		}
	}
	return out
}

// tempFiles are leftovers of interrupted atomic writes.
func (s *Store) tempFiles() []blobInfo {
	var out []blobInfo
	for _, dir := range []string{filepath.Join(s.dir, "requests"), filepath.Join(s.dir, "blobs")} {
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.HasPrefix(e.Name(), ".tmp-") {
				if info, err := e.Info(); err == nil {
					out = append(out, blobInfo{path: p, size: info.Size(), mtime: info.ModTime()})
				}
			}
			return nil
		})
	}
	return out
}

type indexInfo struct {
	name string
	size int64
	// end is when the newest summary it can hold was written.
	end time.Time
}

func (s *Store) scanIndex() []indexInfo {
	var out []indexInfo
	for _, name := range s.indexFiles() {
		info, err := os.Stat(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		ii := indexInfo{name: name, size: info.Size(), end: info.ModTime()}
		if m, ok := indexMonth(name); ok {
			ii.end = m.AddDate(0, 1, 0)
		}
		out = append(out, ii)
	}
	return out
}

// PurgeOptions drive one purge.
type PurgeOptions struct {
	Now      time.Time
	Settings Settings
	// Pinned request ids are never deleted: an active conversation's
	// markers point at them, and recall reads their bodies.
	Pinned map[string]bool
	// Grace protects blobs (and temp files) modified this recently.
	Grace  time.Duration
	DryRun bool
}

// DeletedDetail is one detail a purge removed (or would remove).
type DeletedDetail struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"` // age | size
	Bytes  int64     `json:"bytes"`
}

// PurgeReport says what a purge did, or with DryRun would do.
type PurgeReport struct {
	DryRun bool `json:"dry_run"`
	// Details lists the deleted details, oldest first (at most
	// maxReportedDetails; DetailsDeleted is the full count).
	Details        []DeletedDetail `json:"details"`
	DetailsDeleted int             `json:"details_deleted"`
	DetailsByAge   int             `json:"details_by_age"`
	DetailsBySize  int             `json:"details_by_size"`
	DetailBytes    int64           `json:"detail_bytes"`
	// PinnedKept counts details past detail_max_age kept because an active
	// conversation's markers point at them.
	PinnedKept   int   `json:"pinned_kept"`
	BlobsDeleted int   `json:"blobs_deleted"`
	BlobBytes    int64 `json:"blob_bytes"`
	// BlobsPending are unreferenced blobs still inside the grace period.
	BlobsPending int      `json:"blobs_pending"`
	IndexFiles   []string `json:"index_files_deleted"`
	IndexBytes   int64    `json:"index_bytes"`
	TempFiles    int      `json:"temp_files_deleted"`
	// TotalBytes is what the store holds before and after (records, blobs
	// and index files).
	TotalBytesBefore int64 `json:"total_bytes_before"`
	TotalBytesAfter  int64 `json:"total_bytes_after"`
	// OverLimit is set when pinned details alone exceed max_total_bytes.
	OverLimit bool     `json:"over_limit"`
	Warnings  []string `json:"warnings,omitempty"`
}

const maxReportedDetails = 200

// Purge applies the retention settings: details past detail_max_age, then
// the oldest details while the store is over max_total_bytes, never a
// pinned one; index files past summary_max_age, whole files; and blobs no
// record references any more, once they are older than the grace period.
//
// Blobs are found by mark and sweep: every record's head lists the blobs it
// references. The scan runs without blocking Save; the deletions run under
// the store's exclusive lock, and a blob is only deleted when its
// modification time, checked again under the lock, is older than the grace
// period. Save refreshes that time on every blob it references (under the
// shared lock) before writing its record, so a Save racing the purge keeps
// its blobs.
func (s *Store) Purge(o PurgeOptions) (PurgeReport, error) {
	rep := PurgeReport{DryRun: o.DryRun, Details: []DeletedDetail{}, IndexFiles: []string{}}
	recs, err := s.scanRecords()
	if err != nil {
		return rep, err
	}
	blobs := s.scanBlobs()
	index := s.scanIndex()

	refcount := map[string]int{}
	sweepable := true
	var total int64
	for _, r := range recs {
		total += r.size
		if r.headErr != nil {
			sweepable = false
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("record %s: %v; no blob is deleted this run", r.id, r.headErr))
		}
		for _, h := range r.head.Refs {
			refcount[h]++
		}
	}
	for _, b := range blobs {
		total += b.size
	}
	for _, ii := range index {
		total += ii.size
	}
	rep.TotalBytesBefore = total

	st := o.Settings
	deleted := map[string]string{}
	del := func(r recInfo, reason string) {
		deleted[r.id] = reason
		total -= r.size
		rep.DetailsDeleted++
		if reason == "age" {
			rep.DetailsByAge++
		} else {
			rep.DetailsBySize++
		}
		rep.DetailBytes += r.size
		if len(rep.Details) < maxReportedDetails {
			rep.Details = append(rep.Details, DeletedDetail{ID: r.id, Time: r.time, Reason: reason, Bytes: r.size})
		}
		for _, h := range r.head.Refs {
			if refcount[h]--; refcount[h] == 0 {
				total -= blobs[h].size
			}
		}
	}
	if age := time.Duration(st.DetailMaxAge); age > 0 {
		cutoff := o.Now.Add(-age)
		for _, r := range recs {
			if !r.time.Before(cutoff) {
				break
			}
			if o.Pinned[r.id] {
				rep.PinnedKept++
				continue
			}
			del(r, "age")
		}
	}

	// Summaries: whole files, never the one being written this month.
	var dropIndex []indexInfo
	if age := time.Duration(st.SummaryMaxAge); age > 0 {
		cutoff := o.Now.Add(-age)
		current := indexName(o.Now)
		for _, ii := range index {
			if ii.name != current && ii.end.Before(cutoff) {
				dropIndex = append(dropIndex, ii)
				total -= ii.size
				rep.IndexFiles = append(rep.IndexFiles, ii.name)
				rep.IndexBytes += ii.size
			}
		}
	}

	if st.MaxTotalBytes > 0 && total > st.MaxTotalBytes {
		for _, r := range recs {
			if total <= st.MaxTotalBytes {
				break
			}
			if deleted[r.id] != "" || o.Pinned[r.id] {
				continue
			}
			del(r, "size")
		}
		rep.OverLimit = total > st.MaxTotalBytes
		if rep.OverLimit {
			rep.Warnings = append(rep.Warnings, "still over max_total_bytes: what is left is pinned by active conversations (or is index)")
		}
	}

	cutoff := o.Now.Add(-o.Grace)
	var orphans []string
	if sweepable {
		for h, b := range blobs {
			if refcount[h] > 0 {
				continue
			}
			if b.mtime.Before(cutoff) {
				orphans = append(orphans, h)
			} else {
				rep.BlobsPending++
			}
		}
		sort.Strings(orphans)
	}
	var temps []blobInfo
	for _, t := range s.tempFiles() {
		if t.mtime.Before(cutoff) {
			temps = append(temps, t)
		}
	}

	if o.DryRun {
		for _, h := range orphans {
			rep.BlobsDeleted++
			rep.BlobBytes += blobs[h].size
		}
		rep.TempFiles = len(temps)
		rep.TotalBytesAfter = total
		return rep, nil
	}

	if s.afterScan != nil {
		s.afterScan()
	}
	s.gc.Lock()
	defer s.gc.Unlock()
	var firstErr error
	keep := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	at := time.Now().UTC()
	s.mu.Lock()
	for _, r := range recs {
		reason := deleted[r.id]
		if reason == "" {
			continue
		}
		t := Tombstone{ID: r.id, Time: r.time, At: at, Reason: "the log reached max_total_bytes (" + FormatBytes(st.MaxTotalBytes) + ")"}
		if reason == "age" {
			t.Reason = st.DetailMaxAge.String() + " (detail_max_age)"
		}
		s.purged[r.id] = t
	}
	s.mu.Unlock()
	for _, r := range recs {
		if deleted[r.id] == "" {
			continue
		}
		keep(os.Remove(s.recordPath(r.id)))
		keep(os.Remove(s.legacyPath(r.id)))
	}
	for _, h := range orphans {
		b := blobs[h]
		// Checked again under the lock: a Save may have just refreshed it.
		info, err := os.Stat(b.path)
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(b.path); err == nil {
			rep.BlobsDeleted++
			rep.BlobBytes += b.size
		} else {
			keep(err)
		}
	}
	for _, t := range temps {
		if os.Remove(t.path) == nil {
			rep.TempFiles++
		}
	}
	for _, ii := range dropIndex {
		keep(os.Remove(filepath.Join(s.dir, ii.name)))
	}
	forgotten := 0
	if age := time.Duration(st.SummaryMaxAge); age > 0 {
		forgotten = s.forgetSummaries(o.Now.Add(-age))
	}
	if len(deleted) > 0 || forgotten > 0 {
		keep(s.writeTombstones())
	}
	rep.TotalBytesAfter = total
	return rep, firstErr
}

// forgetSummaries drops in-memory summaries and tombstones older than
// cutoff, as their index files go. It returns the tombstones dropped.
func (s *Store) forgetSummaries(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.recent[:0]
	for _, r := range s.recent {
		if r.Time.IsZero() || !r.Time.Before(cutoff) {
			kept = append(kept, r)
		}
	}
	s.recent = kept
	n := 0
	for id, t := range s.purged {
		if t.Time.Before(cutoff) {
			delete(s.purged, id)
			n++
		}
	}
	return n
}

// FormatBytes writes a size for people: 2 GB, 1.5 MB, 800 KB.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, u := float64(n), 0
	for v >= unit && u < 4 {
		v /= unit
		u++
	}
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + []string{"B", "KB", "MB", "GB", "TB"}[u]
}
