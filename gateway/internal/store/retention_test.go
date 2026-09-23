package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

var now0 = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// idAt makes an id the way relay.NewID does, at t.
func idAt(t time.Time, tag string) string {
	return t.UTC().Format("20060102T150405") + "-" + tag
}

func randomText(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// turnBody is a request whose blocks are shared (a big system prompt) and
// its own (a big tool result).
func turnBody(shared, own string) string {
	return fmt.Sprintf(`{"model":"m","system":[{"type":"text","text":%q}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":%q}]}]}`, shared, own)
}

func saveAt(t *testing.T, s *Store, at time.Time, tag, body string) string {
	t.Helper()
	id := idAt(at, tag)
	if err := s.Save(&Detail{Record: Record{ID: id, Time: at, ConversationID: "conv-" + tag}, RequestBody: body}); err != nil {
		t.Fatal(err)
	}
	return id
}

// ageAllBlobs moves every blob out of the grace period.
func ageAllBlobs(t *testing.T, s *Store) {
	t.Helper()
	old := now0.Add(-48 * time.Hour)
	for _, b := range s.scanBlobs() {
		if err := os.Chtimes(b.path, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func settings(mut func(*Settings)) Settings {
	s := DefaultSettings()
	mut(&s)
	return s
}

func TestRetentionByAge(t *testing.T) {
	s := openTemp(t)
	shared := randomText(4000)
	oldOwn := randomText(3000)
	oldID := saveAt(t, s, now0.Add(-20*Day), "old", turnBody(shared, oldOwn))
	newID := saveAt(t, s, now0.Add(-2*Day), "new", turnBody(shared, randomText(3000)))
	ageAllBlobs(t, s)
	before := len(s.scanBlobs())

	rep, err := s.Purge(PurgeOptions{Now: now0, Settings: DefaultSettings(), Grace: Grace})
	if err != nil {
		t.Fatal(err)
	}
	if rep.DetailsDeleted != 1 || rep.DetailsByAge != 1 || rep.Details[0].ID != oldID || rep.Details[0].Reason != "age" {
		t.Fatalf("report: %+v", rep)
	}
	// The old turn's own blob goes; the shared prompt stays for the new one.
	if rep.BlobsDeleted != 1 || len(s.scanBlobs()) != before-1 {
		t.Fatalf("blobs deleted %d, left %d of %d", rep.BlobsDeleted, len(s.scanBlobs()), before)
	}
	if _, err := s.Get(newID); err != nil {
		t.Fatalf("new record: %v", err)
	}
	_, err = s.Get(oldID)
	var pe *PurgedError
	if !errors.As(err, &pe) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old record: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "expired: the original was purged by retention after 14d (detail_max_age)") {
		t.Errorf("message: %s", err)
	}
	// The tombstone survives a restart.
	re, _ := Open(s.dir)
	if _, err := re.Get(oldID); !errors.As(err, &pe) {
		t.Errorf("after reopen: %v", err)
	}
	// A second run has nothing left to do.
	if rep, _ := s.Purge(PurgeOptions{Now: now0, Settings: DefaultSettings(), Grace: Grace}); rep.DetailsDeleted != 0 || rep.BlobsDeleted != 0 {
		t.Errorf("second run: %+v", rep)
	}
}

func TestRetentionBySize(t *testing.T) {
	s := openTemp(t)
	var ids []string
	for i := 0; i < 6; i++ {
		// Random text barely compresses: each turn is ~20 KB on disk.
		ids = append(ids, saveAt(t, s, now0.Add(time.Duration(i-10)*time.Hour), fmt.Sprintf("s%d", i), turnBody("p", randomText(40000))))
	}
	ageAllBlobs(t, s)
	u, _ := s.DiskUsage()
	limit := u.Bytes.ManagedTotal / 2
	pinned := map[string]bool{ids[0]: true}
	rep, err := s.Purge(PurgeOptions{Now: now0, Settings: settings(func(st *Settings) { st.MaxTotalBytes = limit }), Pinned: pinned, Grace: Grace})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalBytesAfter > limit || rep.OverLimit {
		t.Fatalf("still over the limit: %+v", rep)
	}
	after, _ := s.DiskUsage()
	if after.Bytes.ManagedTotal > limit {
		t.Fatalf("on disk %d > limit %d", after.Bytes.ManagedTotal, limit)
	}
	// Oldest first, the pinned one skipped, the newest kept.
	if rep.DetailsBySize == 0 || rep.Details[0].ID != ids[1] {
		t.Fatalf("deleted %+v", rep.Details)
	}
	for _, id := range []string{ids[0], ids[5]} {
		if _, err := s.Get(id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := s.Get(ids[1]); !strings.Contains(fmt.Sprint(err), "max_total_bytes") {
		t.Errorf("size purge error: %v", err)
	}

	// Pinned bodies alone over the limit: kept, and reported.
	all := map[string]bool{}
	for _, id := range ids {
		all[id] = true
	}
	rep, _ = s.Purge(PurgeOptions{Now: now0, Settings: settings(func(st *Settings) { st.MaxTotalBytes = 1 }), Pinned: all, Grace: Grace})
	if !rep.OverLimit || rep.DetailsDeleted != 0 {
		t.Errorf("pinned over limit: %+v", rep)
	}
}

func TestBodiesPolicy(t *testing.T) {
	mk := func(status int, keep bool) *Detail {
		return &Detail{Record: Record{Status: status}, RequestBody: "req", SentBody: "sent", ResponseBody: "resp", KeepBody: keep}
	}
	cases := []struct {
		policy          string
		status          int
		keep            bool
		req, sent, resp string
	}{
		{BodiesFull, 200, false, "req", "sent", "resp"},
		{BodiesErrorsOnly, 200, false, "", "", ""},
		{BodiesErrorsOnly, 500, false, "req", "sent", "resp"},
		{BodiesErrorsOnly, 200, true, "req", "", ""}, // recall needs it
		{BodiesNone, 500, true, "", "", ""},
	}
	for _, c := range cases {
		d := mk(c.status, c.keep)
		ApplyBodies(c.policy, d)
		if d.RequestBody != c.req || d.SentBody != c.sent || d.ResponseBody != c.resp {
			t.Errorf("%s status %d keep %v: %q %q %q", c.policy, c.status, c.keep, d.RequestBody, d.SentBody, d.ResponseBody)
		}
	}
	d := mk(200, false)
	d.Error = "client cancelled"
	ApplyBodies(BodiesErrorsOnly, d)
	if d.RequestBody == "" {
		t.Error("a call with an error is a failed call")
	}

	c := config.Default()
	if EffectiveBodies(*c) != BodiesFull {
		t.Error("default is full")
	}
	c.Sections = map[string]json.RawMessage{"storage": json.RawMessage(`{"bodies":"errors_only"}`)}
	if EffectiveBodies(*c) != BodiesErrorsOnly {
		t.Error("section not read")
	}
	c.LogBodies = false
	if EffectiveBodies(*c) != BodiesNone {
		t.Error("log_bodies=false must mean none")
	}
}

func TestSettingsParsing(t *testing.T) {
	var s settingsPatch
	if err := json.Unmarshal([]byte(`{"detail_max_age":"36h","summary_max_age":"1d12h","conversation_ttl":86400,"max_total_bytes":1073741824}`), &s); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultSettings().apply(s)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(got.DetailMaxAge) != 36*time.Hour || time.Duration(got.SummaryMaxAge) != 36*time.Hour ||
		time.Duration(got.ConversationTTL) != Day || got.MaxTotalBytes != 1<<30 {
		t.Fatalf("%+v", got)
	}
	b, _ := json.Marshal(DefaultSettings())
	if string(b) != `{"detail_max_age":"14d","summary_max_age":"90d","max_total_bytes":2147483648,"bodies":"full","conversation_ttl":"7d"}` {
		t.Errorf("defaults JSON: %s", b)
	}
	for _, bad := range []string{`{"bodies":"some"}`, `{"detail_max_age":"5m"}`, `{"conversation_ttl":"0"}`, `{"max_total_bytes":1000}`, `{"detail_max_age":"-1d"}`} {
		var p settingsPatch
		err := json.Unmarshal([]byte(bad), &p)
		if err == nil {
			_, err = DefaultSettings().apply(p)
		}
		if err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	// 0 disables an age limit or the size cap.
	var p settingsPatch
	_ = json.Unmarshal([]byte(`{"detail_max_age":"0","max_total_bytes":0}`), &p)
	if got, err := DefaultSettings().apply(p); err != nil || got.DetailMaxAge != 0 || got.MaxTotalBytes != 0 {
		t.Errorf("zero: %+v %v", got, err)
	}
}

// Orphan blobs go only once their grace period is over.
func TestJanitorOrphanGrace(t *testing.T) {
	s := openTemp(t)
	id := saveAt(t, s, now0.Add(-time.Hour), "live", turnBody(randomText(3000), randomText(3000)))
	ageAllBlobs(t, s)
	orphanOld := blob{hash: hashOf([]byte("old orphan")), data: []byte("old orphan")}
	orphanNew := blob{hash: hashOf([]byte("fresh orphan")), data: []byte("fresh orphan")}
	for _, b := range []blob{orphanOld, orphanNew} {
		if _, err := s.putBlob(b); err != nil {
			t.Fatal(err)
		}
	}
	old := now0.Add(-time.Hour)
	_ = os.Chtimes(s.blobPath(orphanOld.hash), old, old)
	_ = os.Chtimes(s.blobPath(orphanNew.hash), now0.Add(-time.Minute), now0.Add(-time.Minute))
	// A leftover temp file of an interrupted write.
	tmp := filepath.Join(s.dir, "blobs", ".tmp-123")
	_ = os.WriteFile(tmp, []byte("x"), 0o600)
	_ = os.Chtimes(tmp, old, old)

	dry, _ := s.Purge(PurgeOptions{Now: now0, Settings: DefaultSettings(), Grace: Grace, DryRun: true})
	if dry.BlobsDeleted != 1 || dry.BlobsPending != 1 || dry.TempFiles != 1 {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := os.Stat(s.blobPath(orphanOld.hash)); err != nil {
		t.Fatal("dry run deleted a blob")
	}
	rep, err := s.Purge(PurgeOptions{Now: now0, Settings: DefaultSettings(), Grace: Grace})
	if err != nil {
		t.Fatal(err)
	}
	if rep.BlobsDeleted != 1 || rep.BlobsPending != 1 || rep.TempFiles != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if _, err := os.Stat(s.blobPath(orphanOld.hash)); !os.IsNotExist(err) {
		t.Error("old orphan kept")
	}
	if _, err := os.Stat(s.blobPath(orphanNew.hash)); err != nil {
		t.Error("orphan inside the grace period deleted")
	}
	if _, err := s.Get(id); err != nil {
		t.Errorf("referenced blobs deleted: %v", err)
	}
	// Past the grace period, it goes too.
	rep, _ = s.Purge(PurgeOptions{Now: now0.Add(time.Hour), Settings: DefaultSettings(), Grace: Grace})
	if rep.BlobsDeleted != 1 {
		t.Errorf("later run: %+v", rep)
	}
}

// Summaries past summary_max_age go whole-file; the current month stays.
func TestIndexRetention(t *testing.T) {
	s := openTemp(t)
	for _, at := range []time.Time{now0.AddDate(0, -5, 0), now0.AddDate(0, -4, 0), now0.AddDate(0, -1, 0), now0} {
		saveAt(t, s, at, at.Format("0102"), "")
	}
	legacy := filepath.Join(s.dir, legacyIndex)
	_ = os.WriteFile(legacy, []byte("{}\n"), 0o600)
	old := now0.AddDate(0, -6, 0)
	_ = os.Chtimes(legacy, old, old)
	rep, err := s.Purge(PurgeOptions{Now: now0, Settings: DefaultSettings(), Grace: Grace})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rep.IndexFiles, ",") != "index.jsonl,index-2026-04.jsonl,index-2026-05.jsonl" {
		t.Fatalf("index files deleted: %v", rep.IndexFiles)
	}
	for _, r := range s.Recent() {
		if r.Time.Before(now0.AddDate(0, -3, 0)) {
			t.Errorf("summary %s still in memory", r.ID)
		}
	}
	re, _ := Open(s.dir)
	if n := len(re.Recent()); n != 2 {
		t.Errorf("after reopen %d summaries, want 2", n)
	}
}

// fakeSource is a feature with conversation state.
type fakeSource struct {
	mu      sync.Mutex
	convs   map[string]Conversation
	expired []string
}

func (f *fakeSource) StorageName() string { return "fake" }
func (f *fakeSource) StorageConversations() []Conversation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Conversation
	for _, c := range f.convs {
		out = append(out, c)
	}
	return out
}
func (f *fakeSource) ExpireConversations(ids []string, before time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, id := range ids {
		if c, ok := f.convs[id]; ok && c.LastActive.Before(before) {
			delete(f.convs, id)
			f.expired = append(f.expired, id)
			n++
		}
	}
	return n, nil
}

// A body an active conversation's markers point at survives retention by
// age and by size; once the conversation expires, it goes.
func TestPinnedBodiesSurviveWhileActive(t *testing.T) {
	s := openTemp(t)
	pinnedID := saveAt(t, s, now0.Add(-30*Day), "pin", turnBody("p", randomText(4000)))
	otherID := saveAt(t, s, now0.Add(-30*Day), "other", turnBody("p", randomText(4000)))
	src := &fakeSource{convs: map[string]Conversation{
		"active": {ID: "active", LastActive: now0.Add(-2 * Day), Pins: []string{pinnedID}},
		"idle":   {ID: "idle", LastActive: now0.Add(-9 * Day), Pins: []string{otherID}},
	}}
	cs := config.NewStore(config.Default())
	j := NewJanitor(cs, s, src)
	clock := now0
	j.now = func() time.Time { return clock }

	dry := j.Run(true, "test")
	if dry.PinnedRequests != 1 || dry.ConversationsExpired < 1 || len(src.expired) != 0 {
		t.Fatalf("dry run: %+v, expired %v", dry, src.expired)
	}
	if c, r := j.Pinned(); c != 1 || r != 1 {
		t.Errorf("pinned = %d conversations, %d requests", c, r)
	}
	rep := j.Run(false, "test")
	if rep.Purge.PinnedKept != 1 || rep.Purge.DetailsDeleted != 1 || rep.Purge.Details[0].ID != otherID {
		t.Fatalf("run: %+v", rep.Purge)
	}
	if strings.Join(src.expired, ",") != "idle" || rep.ExpiredState["fake"] != 1 {
		t.Fatalf("expired %v / %v", src.expired, rep.ExpiredState)
	}
	if _, err := s.Get(pinnedID); err != nil {
		t.Fatalf("pinned body purged: %v", err)
	}
	if j.Last() != rep {
		t.Error("last run not kept")
	}

	// Over the size cap, the pin still holds.
	rep2, _ := s.Purge(PurgeOptions{Now: clock, Settings: settings(func(st *Settings) { st.MaxTotalBytes = 1 }),
		Pinned: map[string]bool{pinnedID: true}, Grace: Grace})
	if rep2.DetailsDeleted != 0 {
		t.Fatalf("size purge removed a pinned body: %+v", rep2)
	}

	// A week later the conversation is idle: its state expires, the pin is
	// released, and the body goes with it.
	clock = now0.Add(6 * Day)
	rep = j.Run(false, "test")
	if strings.Join(src.expired, ",") != "idle,active" || rep.Purge.DetailsDeleted != 1 {
		t.Fatalf("after expiry: expired %v, %+v", src.expired, rep.Purge)
	}
	if _, err := s.Get(pinnedID); !errors.As(err, new(*PurgedError)) {
		t.Fatalf("expired body: %v", err)
	}
}

// Saves racing purges never lose a blob. Old records share their blobs with
// the records being written, and those blobs start outside the grace
// period, so only Save's refresh under the lock keeps them. Run with -race.
func TestConcurrentSaveDuringPurge(t *testing.T) {
	s := openTemp(t)
	shared := []string{randomText(3000), randomText(3000), randomText(3000)}
	for i := 0; i < 30; i++ {
		saveAt(t, s, now0.Add(-30*Day), fmt.Sprintf("old%d", i), turnBody(shared[i%3], randomText(2000)))
	}
	ageAllBlobs(t, s)
	var wg sync.WaitGroup
	ids := make(chan string, 200)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				at := time.Now().UTC()
				id := idAt(at, fmt.Sprintf("w%d-%d", w, i))
				body := turnBody(shared[(w+i)%3], randomText(1200))
				if err := s.Save(&Detail{Record: Record{ID: id, Time: at}, RequestBody: body}); err != nil {
					t.Error(err)
					return
				}
				ids <- id
			}
		}(w)
	}
	stop := make(chan struct{})
	var pw sync.WaitGroup
	pw.Add(1)
	go func() {
		defer pw.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Grace far shorter than the blobs' age, far longer than a Save.
			if _, err := s.Purge(PurgeOptions{Now: time.Now(), Settings: DefaultSettings(), Grace: time.Minute}); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	close(stop)
	pw.Wait()
	close(ids)
	n := 0
	for id := range ids {
		n++
		d, err := s.Get(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.Contains(d.RequestBody, `"system"`) {
			t.Fatalf("%s: body lost", id)
		}
	}
	if n != 100 {
		t.Fatalf("saved %d", n)
	}
	// And the old records are gone.
	u, _ := s.DiskUsage()
	if u.Counts.Records != 100 {
		t.Errorf("records left: %d", u.Counts.Records)
	}
}

// The exact interleaving the grace period exists for: a purge has scanned,
// found a blob unreferenced (its only record is being deleted), and before
// it deletes, a Save references the blob again.
func TestSaveBetweenScanAndSweep(t *testing.T) {
	s := openTemp(t)
	shared := randomText(5000)
	saveAt(t, s, now0.Add(-30*Day), "old", turnBody(shared, "a"))
	ageAllBlobs(t, s)
	var newID string
	s.afterScan = func() {
		newID = saveAt(t, s, time.Now(), "new", turnBody(shared, "b"))
	}
	rep, err := s.Purge(PurgeOptions{Now: time.Now(), Settings: DefaultSettings(), Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if rep.DetailsDeleted != 1 {
		t.Fatalf("%+v", rep)
	}
	d, err := s.Get(newID)
	if err != nil || !strings.Contains(d.RequestBody, shared) {
		t.Fatalf("the racing Save lost its blob: %v", err)
	}
}

func TestStorageAPI(t *testing.T) {
	s := openTemp(t)
	t.Setenv("RLCD_GATEWAY_HOME", s.dir)
	saveAt(t, s, now0.Add(-30*Day), "old", turnBody(randomText(3000), "x"))
	saveAt(t, s, time.Now(), "new", turnBody(randomText(3000), "x"))
	cs := config.NewStore(config.Default())
	j := NewJanitor(cs, s)
	mux := http.NewServeMux()
	j.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	var view map[string]any
	if call("GET", "/api/storage", "", &view) != 200 {
		t.Fatal("GET /api/storage")
	}
	for _, k := range []string{"bytes", "counts", "oldest_record", "newest_record", "dedup_ratio", "compression_ratio", "pinned", "settings", "last_janitor_run"} {
		if _, ok := view[k]; !ok {
			t.Errorf("GET /api/storage has no %q", k)
		}
	}
	if view["counts"].(map[string]any)["records"].(float64) != 2 {
		t.Errorf("counts: %v", view["counts"])
	}

	var set map[string]any
	if call("PUT", "/api/storage/settings", `{"detail_max_age":"7d","bodies":"errors_only"}`, &set) != 200 ||
		set["detail_max_age"] != "7d" || set["bodies"] != "errors_only" || set["summary_max_age"] != "90d" || set["effective_bodies"] != "errors_only" {
		t.Fatalf("PUT settings: %v", set)
	}
	if SettingsFrom(cs.Get()).Bodies != BodiesErrorsOnly {
		t.Error("not saved")
	}
	for _, bad := range []string{`{"bodies":"x"}`, `{"nope":1}`, `not json`} {
		if code := call("PUT", "/api/storage/settings", bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	var rep JanitorReport
	if call("POST", "/api/storage/purge", `{"dry_run":true}`, &rep) != 200 || !rep.DryRun || rep.Purge.DetailsDeleted != 1 {
		t.Fatalf("dry purge: %+v", rep)
	}
	if u, _ := s.DiskUsage(); u.Counts.Records != 2 {
		t.Fatal("dry run deleted")
	}
	if call("POST", "/api/storage/purge", ``, &rep) != 200 || rep.DryRun || rep.Purge.DetailsDeleted != 1 {
		t.Fatalf("purge: %+v", rep)
	}
	call("GET", "/api/storage", "", &view)
	if view["last_janitor_run"] == nil || view["counts"].(map[string]any)["tombstones"].(float64) != 1 {
		t.Errorf("after purge: %v %v", view["last_janitor_run"], view["counts"])
	}
}
