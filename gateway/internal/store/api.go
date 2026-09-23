package store

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"
)

// DiskUsage is what the store holds, for the dashboard.
type DiskUsage struct {
	Bytes  UsageBytes  `json:"bytes"`
	Counts UsageCounts `json:"counts"`
	// OldestRecord and NewestRecord are the oldest and newest details.
	OldestRecord *time.Time `json:"oldest_record"`
	NewestRecord *time.Time `json:"newest_record"`
	// LogicalBytes is what the details would take uncompressed and without
	// deduplication (the old format); DedupedBytes after deduplication,
	// still uncompressed; StoredBytes what records and blobs take on disk.
	LogicalBytes int64 `json:"logical_bytes"`
	DedupedBytes int64 `json:"deduped_bytes"`
	StoredBytes  int64 `json:"stored_bytes"`
	// DedupRatio = logical / deduped, CompressionRatio = deduped / stored,
	// TotalRatio = logical / stored. 0 when there is nothing to compare.
	DedupRatio       float64 `json:"dedup_ratio"`
	CompressionRatio float64 `json:"compression_ratio"`
	TotalRatio       float64 `json:"total_ratio"`
}

// UsageBytes is bytes on disk by category (file sizes, not blocks).
type UsageBytes struct {
	Blobs   int64 `json:"blobs"`
	Records int64 `json:"records"`
	Index   int64 `json:"index"`
	// PruneState is <home>/prune (conversation state and feedback),
	// RecallEvents <home>/recall, RouterState <home>/router.
	PruneState   int64 `json:"prune_state"`
	RecallEvents int64 `json:"recall_events"`
	RouterState  int64 `json:"router_state"`
	// Total is the sum of the above; ManagedTotal what max_total_bytes
	// counts (blobs, records and index).
	Total        int64 `json:"total"`
	ManagedTotal int64 `json:"managed_total"`
}

type UsageCounts struct {
	// Records are details on disk; LegacyRecords the ones still in the
	// uncompressed format (see `rlcd-gateway storage compact`).
	Records       int `json:"records"`
	LegacyRecords int `json:"legacy_records"`
	Blobs         int `json:"blobs"`
	IndexFiles    int `json:"index_files"`
	// Tombstones are details purged whose summaries are still listed.
	Tombstones int `json:"tombstones"`
}

// DiskUsage scans the store: the sizes of every file, and the heads of
// every record for the ratios. It reads no bodies.
func (s *Store) DiskUsage() (DiskUsage, error) {
	var u DiskUsage
	recs, err := s.scanRecords()
	if err != nil {
		return u, err
	}
	blobs := s.scanBlobs()
	unique := map[string]bool{}
	var blobRaw int64
	for _, r := range recs {
		u.Bytes.Records += r.size
		u.Counts.Records++
		if r.legacy {
			u.Counts.LegacyRecords++
			u.LogicalBytes += r.size
			u.DedupedBytes += r.size
			continue
		}
		u.LogicalBytes += int64(r.head.RawBytes)
		own := int64(r.head.RawBytes)
		for i, h := range r.head.Refs {
			if i < len(r.head.RefSizes) {
				own -= int64(r.head.RefSizes[i])
				if !unique[h] {
					unique[h] = true
					blobRaw += int64(r.head.RefSizes[i])
				}
			}
		}
		if own > 0 {
			u.DedupedBytes += own
		}
	}
	u.DedupedBytes += blobRaw
	if n := len(recs); n > 0 {
		o, w := recs[0].time, recs[n-1].time
		u.OldestRecord, u.NewestRecord = &o, &w
	}
	for _, b := range blobs {
		u.Bytes.Blobs += b.size
	}
	u.Counts.Blobs = len(blobs)
	for _, ii := range s.scanIndex() {
		u.Bytes.Index += ii.size
		u.Counts.IndexFiles++
	}
	u.Bytes.PruneState = dirBytes(filepath.Join(s.dir, "prune"))
	u.Bytes.RecallEvents = dirBytes(filepath.Join(s.dir, "recall"))
	u.Bytes.RouterState = dirBytes(filepath.Join(s.dir, "router"))
	u.Bytes.ManagedTotal = u.Bytes.Blobs + u.Bytes.Records + u.Bytes.Index
	u.Bytes.Total = u.Bytes.ManagedTotal + u.Bytes.PruneState + u.Bytes.RecallEvents + u.Bytes.RouterState
	u.StoredBytes = u.Bytes.Blobs + u.Bytes.Records
	ratio := func(a, b int64) float64 {
		if a <= 0 || b <= 0 {
			return 0
		}
		return float64(int64(float64(a)/float64(b)*100+0.5)) / 100
	}
	u.DedupRatio = ratio(u.LogicalBytes, u.DedupedBytes)
	u.CompressionRatio = ratio(u.DedupedBytes, u.StoredBytes)
	u.TotalRatio = ratio(u.LogicalBytes, u.StoredBytes)
	s.mu.RLock()
	u.Counts.Tombstones = len(s.purged)
	s.mu.RUnlock()
	return u, nil
}

// SettingsView is GET/PUT /api/storage/settings.
type SettingsView struct {
	Settings
	// EffectiveBodies is the policy in force: none when log_bodies is off.
	EffectiveBodies string `json:"effective_bodies"`
	LogBodies       bool   `json:"log_bodies"`
	// Defaults are the values a missing setting takes.
	Defaults Settings `json:"defaults"`
}

func (j *Janitor) settingsView() SettingsView {
	c := j.cfg.Get()
	return SettingsView{Settings: SettingsFrom(c), EffectiveBodies: EffectiveBodies(c), LogBodies: c.LogBodies,
		Defaults: DefaultSettings()}
}

// StorageView is GET /api/storage.
type StorageView struct {
	DiskUsage
	Pinned struct {
		Conversations int `json:"conversations"`
		Requests      int `json:"requests"`
	} `json:"pinned"`
	Settings SettingsView `json:"settings"`
	// LastJanitorRun is the last run that deleted (null before the first).
	LastJanitorRun *JanitorReport `json:"last_janitor_run"`
	// JanitorIntervalSeconds is how often the janitor runs on its own.
	JanitorIntervalSeconds int `json:"janitor_interval_seconds"`
}

// Register mounts the storage API:
//
//	GET  /api/storage            usage, pins, settings and the last janitor run
//	GET  /api/storage/settings   the "storage" config section
//	PUT  /api/storage/settings   partial update; unknown fields are refused
//	POST /api/storage/purge      run the janitor now ({"dry_run": true} to preview)
func (j *Janitor) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/storage", j.getStorage)
	mux.HandleFunc("GET /api/storage/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, j.settingsView())
	})
	mux.HandleFunc("PUT /api/storage/settings", j.putSettings)
	mux.HandleFunc("POST /api/storage/purge", j.postPurge)
}

func (j *Janitor) getStorage(w http.ResponseWriter, r *http.Request) {
	u, err := j.st.DiskUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v := StorageView{DiskUsage: u, Settings: j.settingsView(), LastJanitorRun: j.Last(),
		JanitorIntervalSeconds: int(JanitorInterval / time.Second)}
	v.Pinned.Conversations, v.Pinned.Requests = j.Pinned()
	writeJSON(w, http.StatusOK, v)
}

func (j *Janitor) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsPatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next, err := SettingsFrom(j.cfg.Get()).apply(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(next)
	if err := j.cfg.SetSection("storage", raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, j.settingsView())
}

func (j *Janitor) postPurge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun bool `json:"dry_run"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	rep := j.Run(in.DryRun, "api")
	if !in.DryRun {
		j.logRun(rep)
	}
	writeJSON(w, http.StatusOK, rep)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
