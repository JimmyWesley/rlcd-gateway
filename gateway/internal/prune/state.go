package prune

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Decision is what was decided about one block, keyed by its stable id.
// Drops are forever: the marker text is stored so every later turn sends
// the identical bytes and the provider's prompt cache keeps matching.
type Decision struct {
	Decision string   `json:"decision"` // keep | drop
	Reason   string   `json:"reason"`
	Score    *float64 `json:"score,omitempty"`
	// Key is what the marker names (tool_use_id, or the ir key in FirstReq).
	Key string `json:"key"`
	// FirstReq is the request where the block was first dropped. Its logged
	// body holds the original content, which is what recall reads back.
	FirstReq string    `json:"first_req,omitempty"`
	Tokens   int       `json:"tokens"`
	Marker   string    `json:"marker,omitempty"`
	Epoch    int       `json:"epoch"`
	At       time.Time `json:"at"`
}

// Thread is the epoch bookkeeping of one message history. A Claude Code
// session sends its subagents' requests under the same session id, so one
// conversation can hold several threads; they share decisions (ids are
// unique) but each grows at its own pace.
type Thread struct {
	LastEpochTokens int       `json:"last_epoch_tokens"`
	Epochs          int       `json:"epochs"`
	LastEpochAt     time.Time `json:"last_epoch_at,omitempty"`
	// Applied lists the ids dropped in the body last forwarded (enforce
	// only). A turn whose drops differ changes the prefix: a cache write.
	Applied []string `json:"applied,omitempty"`
}

type convState struct {
	ConversationID string               `json:"conversation_id"`
	Decisions      map[string]*Decision `json:"decisions"`
	Threads        map[string]*Thread   `json:"threads"`
	Updated        time.Time            `json:"updated"`
}

// states persists one JSON file per conversation under <dir>/conversations.
// Each conversation has its own lock, held for a whole Transform, so two
// turns of the same session never race on an epoch.
type states struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newStates(dir string) *states {
	return &states{dir: filepath.Join(dir, "conversations"), locks: map[string]*sync.Mutex{}}
}

func (s *states) lock(conv string) func() {
	s.mu.Lock()
	l := s.locks[conv]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[conv] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (s *states) path(conv string) string {
	name := unsafeChars.ReplaceAllString(conv, "_")
	if name == "" || name == "." || name == ".." {
		name = "_"
	}
	return filepath.Join(s.dir, name+".json")
}

func (s *states) load(conv string) (*convState, error) {
	st := &convState{ConversationID: conv, Decisions: map[string]*Decision{}, Threads: map[string]*Thread{}}
	b, err := os.ReadFile(s.path(conv))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	if st.Decisions == nil {
		st.Decisions = map[string]*Decision{}
	}
	if st.Threads == nil {
		st.Threads = map[string]*Thread{}
	}
	return st, nil
}

func (s *states) save(st *convState) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	st.Updated = time.Now()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	p := s.path(st.ConversationID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// --- storage: conversations and the requests their markers point at ---

// StorageName names the pruner in the janitor's reports.
func (p *Pruner) StorageName() string { return "prune" }

// StorageConversations lists every conversation with pruning state. Each
// drop pins the request its marker names (FirstReq): recall reads that
// request's body, so retention must keep it while the conversation lives.
func (p *Pruner) StorageConversations() []store.Conversation {
	ents, err := os.ReadDir(p.states.dir)
	if err != nil {
		return nil
	}
	var out []store.Conversation
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.states.dir, name))
		if err != nil {
			continue
		}
		var st convState
		if json.Unmarshal(b, &st) != nil || st.ConversationID == "" {
			continue
		}
		c := store.Conversation{ID: st.ConversationID, LastActive: st.Updated}
		if info, err := e.Info(); err == nil && info.ModTime().After(c.LastActive) {
			c.LastActive = info.ModTime()
		}
		seen := map[string]bool{}
		for _, d := range st.Decisions {
			if d != nil && d.Decision == "drop" && d.FirstReq != "" && !seen[d.FirstReq] {
				seen[d.FirstReq] = true
				c.Pins = append(c.Pins, d.FirstReq)
			}
		}
		out = append(out, c)
	}
	return out
}

// ExpireConversations deletes the pruning state of idle conversations.
// Each is checked again under its lock: a turn that just arrived keeps it.
func (p *Pruner) ExpireConversations(ids []string, before time.Time) (int, error) {
	n := 0
	var firstErr error
	for _, id := range ids {
		unlock := p.states.lock(id)
		path := p.states.path(id)
		info, err := os.Stat(path)
		if err == nil && info.ModTime().Before(before) {
			st, lerr := p.states.load(id)
			if lerr == nil && st.ConversationID == id && st.Updated.Before(before) {
				if err := os.Remove(path); err == nil {
					n++
				} else if firstErr == nil {
					firstErr = err
				}
			}
		}
		unlock()
	}
	return n, firstErr
}
