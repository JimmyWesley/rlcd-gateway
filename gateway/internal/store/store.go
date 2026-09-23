// Package store keeps request records: a summary list in memory (and in
// append-only monthly index files), and one detail file per request.
//
// Bodies are split into content blocks stored once each (body.go), records
// are gzipped (record.go), and a janitor applies the retention settings
// (janitor.go). Plain files instead of a database keep the binary
// dependency-free, and a request log is append-only anyway.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// Client is who made a call, detected from its headers (internal/clients).
type Client struct {
	// ID is a stable slug for the UI's icon: claude-code, codex, opencode,
	// openai-python, openai-node, anthropic-python, anthropic-node, curl, ...
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Kind is agent | sdk | cli | browser | internal | unknown.
	Kind string `json:"kind"`
	// KeyName is the gateway key's name when the call used one.
	KeyName string `json:"key_name,omitempty"`
}

// Record is the summary of one proxied call.
type Record struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Route    string    `json:"route"`
	Upstream string    `json:"upstream"`
	// Protocol is the request's format (one of ir.Protocol*). Records
	// written before it existed are all Anthropic Messages.
	Protocol string `json:"protocol,omitempty"`
	// Alias is the model alias the client asked for, when one matched.
	Alias string `json:"alias,omitempty"`
	// KeyID and KeyName name the gateway key that made the call.
	KeyID   string `json:"key_id,omitempty"`
	KeyName string `json:"key_name,omitempty"`
	// Client is who made the call; Provider is who served it (anthropic,
	// openrouter, openai, groq, ..., custom) and ModelVendor who made the
	// model that was sent (anthropic, openai, qwen, meta, ...).
	Client      *Client `json:"client,omitempty"`
	Provider    string  `json:"provider,omitempty"`
	ModelVendor string  `json:"model_vendor,omitempty"`
	// ClientModel is what the agent asked for; Model is what was actually sent.
	ClientModel string `json:"client_model,omitempty"`
	Model       string `json:"model,omitempty"`
	AuthMode    string `json:"auth_mode"` // oauth | api-key | none, as seen from the client
	Stream      bool   `json:"stream"`
	Status      int    `json:"status"`
	TTFBMs      int64  `json:"ttfb_ms"`
	DurationMs  int64  `json:"duration_ms"`
	// EstTokens is our estimate of the request as the client sent it.
	EstTokens int            `json:"est_tokens"`
	ByKind    map[string]int `json:"by_kind,omitempty"`
	Usage     *Usage         `json:"usage,omitempty"`
	// CostUSD prices Usage with the price table: an estimate.
	CostUSD float64 `json:"est_cost_usd,omitempty"`
	// StrippedThinking counts thinking blocks removed when crossing providers.
	StrippedThinking int    `json:"stripped_thinking,omitempty"`
	Error            string `json:"error,omitempty"`
	// ConversationID groups the turns of one agent session.
	ConversationID string `json:"conversation_id,omitempty"`
	// RouteReason explains the route choice when a router made it.
	RouteReason string `json:"route_reason,omitempty"`
	// Stages holds each pipeline stage's small summary, keyed by stage name.
	Stages map[string]json.RawMessage `json:"stages,omitempty"`
	// StageErrors records stages that failed and were skipped.
	StageErrors map[string]string `json:"stage_errors,omitempty"`
	// Decisions summarizes a System One decision call (protocol systemone).
	Decisions *Decisions `json:"decisions,omitempty"`
}

// Detail is everything kept about one call, loaded on demand.
type Detail struct {
	Record
	RequestHeaders map[string]string `json:"request_headers"`
	XRay           *ir.Request       `json:"xray,omitempty"`
	// StageDetails holds each stage's large report (e.g. the pruning diff).
	StageDetails map[string]json.RawMessage `json:"stage_details,omitempty"`
	// SentBody is what was actually forwarded, when a stage changed it.
	SentBody     string `json:"sent_body,omitempty"`
	RequestBody  string `json:"request_body,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	// KeepBody is set when a pipeline stage made this request the source of
	// a marker (pruning's first drop): recall will read its request body,
	// so the bodies policy must not drop it. Never serialized.
	KeepBody bool `json:"-"`
}

const keepInMemory = 500

type Store struct {
	dir    string
	mu     sync.RWMutex
	recent []Record
	subs   map[chan Record]struct{}
	// lastSeen is the newest record time per conversation, over every index
	// file still on disk: the janitor's measure of an active conversation.
	lastSeen map[string]time.Time
	// purged are tombstones of details retention deleted, so a lookup can
	// say "expired" instead of "not found".
	purged map[string]Tombstone

	// gc orders writers against the janitor: Save holds it shared while it
	// writes blobs and the record, the janitor exclusively while it deletes.
	gc sync.RWMutex
	// idx serializes appends to the index files.
	idx sync.Mutex
	// now is the clock (tests).
	now func() time.Time
	// afterScan runs between a purge's scan and its deletions (tests).
	afterScan func()
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, subs: map[chan Record]struct{}{}, lastSeen: map[string]time.Time{},
		purged: map[string]Tombstone{}, now: time.Now}
	s.loadIndex()
	s.loadTombstones()
	return s, nil
}

// Dir is the directory the store lives in.
func (s *Store) Dir() string { return s.dir }

// --- index files ---

const legacyIndex = "index.jsonl"

// indexName is the monthly index file for t (UTC): index-2026-09.jsonl.
func indexName(t time.Time) string {
	return "index-" + t.UTC().Format("2006-01") + ".jsonl"
}

// indexMonth parses a monthly index file name.
func indexMonth(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "index-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01", strings.TrimSuffix(strings.TrimPrefix(name, "index-"), ".jsonl"))
	return t, err == nil
}

// indexFiles lists the index files oldest first: the legacy index.jsonl
// (written before rotation), then the monthly files.
func (s *Store) indexFiles() []string {
	ents, _ := os.ReadDir(s.dir)
	var monthly []string
	legacy := false
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if e.Name() == legacyIndex {
			legacy = true
		} else if _, ok := indexMonth(e.Name()); ok {
			monthly = append(monthly, e.Name())
		}
	}
	sort.Strings(monthly)
	if legacy {
		monthly = append([]string{legacyIndex}, monthly...)
	}
	return monthly
}

func (s *Store) loadIndex() {
	for _, name := range s.indexFiles() {
		f, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), maxIndexLine)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			s.recent = append(s.recent, r)
			if len(s.recent) > 2*keepInMemory {
				s.recent = append(s.recent[:0], s.recent[len(s.recent)-keepInMemory:]...)
			}
			s.seen(r)
		}
		f.Close()
	}
	if len(s.recent) > keepInMemory {
		s.recent = s.recent[len(s.recent)-keepInMemory:]
	}
}

// maxIndexLine bounds one summary line when scanning (a decision call with
// many questions writes a long one).
const maxIndexLine = 16 << 20

// Scan calls fn with every summary in the index files, oldest first, until
// fn returns false. It reads the files, not the in-memory list, so it sees
// every summary retention has not dropped. Unreadable lines are skipped.
func (s *Store) Scan(fn func(Record) bool) error {
	for _, name := range s.indexFiles() {
		f, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			continue // dropped by retention meanwhile
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), maxIndexLine)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if !fn(r) {
				f.Close()
				return nil
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// seen records a conversation's activity. Callers hold mu (or own s).
func (s *Store) seen(r Record) {
	if r.ConversationID != "" && r.Time.After(s.lastSeen[r.ConversationID]) {
		s.lastSeen[r.ConversationID] = r.Time
	}
}

func (s *Store) appendIndex(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	t := r.Time
	if t.IsZero() {
		t = s.now()
	}
	s.idx.Lock()
	defer s.idx.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, indexName(t)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// --- details ---

func (s *Store) recordPath(id string) string {
	return filepath.Join(s.dir, "requests", id+recordExt)
}

func (s *Store) legacyPath(id string) string {
	return filepath.Join(s.dir, "requests", id+legacyExt)
}

func validID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

// Save persists a finished call and notifies live subscribers. The bodies
// are split into content blocks, each stored once; the rest of the record
// is gzipped. It runs after the response has been streamed to the client.
func (s *Store) Save(d *Detail) error {
	if !validID(d.ID) {
		return fmt.Errorf("store: invalid request id %q", d.ID)
	}
	if err := s.writeDetail(d); err != nil {
		return err
	}
	err := s.appendIndex(d.Record)

	s.mu.Lock()
	s.recent = append(s.recent, d.Record)
	if len(s.recent) > keepInMemory {
		s.recent = s.recent[len(s.recent)-keepInMemory:]
	}
	s.seen(d.Record)
	for ch := range s.subs {
		select {
		case ch <- d.Record:
		default: // a slow dashboard tab must never block the proxy
		}
	}
	s.mu.Unlock()
	return err
}

// writeDetail writes the blobs, then the record that references them.
func (s *Store) writeDetail(d *Detail) error {
	rec, blobs, err := encodeRecord(d)
	if err != nil {
		return err
	}
	s.gc.RLock()
	defer s.gc.RUnlock()
	for _, b := range blobs {
		if _, err := s.putBlob(b); err != nil {
			return err
		}
	}
	return writeAtomic(s.recordPath(d.ID), rec)
}

// Recent returns summaries, newest first.
func (s *Store) Recent() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, len(s.recent))
	for i, r := range s.recent {
		out[len(s.recent)-1-i] = r
	}
	return out
}

// Get loads a detail with its bodies reassembled. A detail retention
// deleted returns a *PurgedError, which also matches os.ErrNotExist.
func (s *Store) Get(id string) (*Detail, error) {
	// ids are generated by us (hex), but never trust a path segment.
	if !validID(id) {
		return nil, os.ErrNotExist
	}
	d, err := s.decodeRecord(s.recordPath(id))
	if !errors.Is(err, os.ErrNotExist) {
		return d, err
	}
	b, err := os.ReadFile(s.legacyPath(id))
	if errors.Is(err, os.ErrNotExist) {
		s.mu.RLock()
		t, ok := s.purged[id]
		s.mu.RUnlock()
		if ok {
			return nil, &PurgedError{t}
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	var ld Detail
	return &ld, json.Unmarshal(b, &ld)
}

func (s *Store) Subscribe() (chan Record, func()) {
	ch := make(chan Record, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// --- tombstones ---

const tombstoneFile = "purged.jsonl"

// Tombstone says a detail was deleted by retention, when and why.
type Tombstone struct {
	ID string `json:"id"`
	// Time is the request's own time; At when it was purged.
	Time time.Time `json:"time"`
	At   time.Time `json:"at"`
	// Reason completes "purged by retention after ...".
	Reason string `json:"reason"`
}

// PurgedError is returned by Get for a detail retention deleted.
type PurgedError struct{ Tombstone }

func (e *PurgedError) Error() string {
	return fmt.Sprintf("expired: the original was purged by retention after %s, on %s",
		e.Reason, e.At.UTC().Format("2006-01-02 15:04 UTC"))
}

func (e *PurgedError) Is(target error) bool { return target == os.ErrNotExist }

func (s *Store) loadTombstones() {
	f, err := os.Open(filepath.Join(s.dir, tombstoneFile))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var t Tombstone
		if json.Unmarshal(sc.Bytes(), &t) == nil && t.ID != "" {
			s.purged[t.ID] = t
		}
	}
}

// writeTombstones rewrites the tombstone file from memory. Callers hold gc.
func (s *Store) writeTombstones() error {
	s.mu.RLock()
	list := make([]Tombstone, 0, len(s.purged))
	for _, t := range s.purged {
		list = append(list, t)
	}
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		return list[i].At.Before(list[j].At) || list[i].At.Equal(list[j].At) && list[i].ID < list[j].ID
	})
	var buf strings.Builder
	for _, t := range list {
		b, _ := json.Marshal(t)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return writeAtomic(filepath.Join(s.dir, tombstoneFile), []byte(buf.String()))
}

// Purged reports whether retention deleted the detail of request id.
func (s *Store) Purged(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.purged[id]
	return ok
}
