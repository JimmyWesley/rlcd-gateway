package recall

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

// Event is one call of rlcd_recall. Events are appended, one JSON object
// per line, to <config dir>/recall/events.jsonl.
//
// A successful recall means the pruner dropped something the model then
// needed: it is the strongest "should_keep" feedback F1 can get. To ingest
// it, take events with ok=true and treat (req, block_key) — or tool_use_id,
// which is stable across the turns of a conversation — as a block that must
// be kept in conversation_id from then on.
//
// The schema is additive: fields may be added, never renamed or removed.
type Event struct {
	Time time.Time `json:"time"`
	// ConversationID of the request the content came from (empty if the
	// request was not found).
	ConversationID string `json:"conversation_id,omitempty"`
	// Req and Key are what the model asked for (from the marker).
	Req string `json:"req"`
	Key string `json:"key"`
	// BlockKey is the ir key the key resolved to (e.g. "m4.b0"); ToolUseID,
	// Tool and Kind describe that block.
	BlockKey  string `json:"block_key,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Kind      string `json:"kind,omitempty"`
	// Tokens is the estimated size of the original block (chars/4); Bytes is
	// what was actually returned, after truncation.
	Tokens    int  `json:"tokens"`
	Bytes     int  `json:"bytes"`
	Truncated bool `json:"truncated,omitempty"`
	OK        bool `json:"ok"`
	// Error is one of the Err* codes when OK is false; Message is the text
	// the model was given.
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	// Client is the MCP client's self-reported name and version.
	Client string `json:"client,omitempty"`
}

// keepEvents bounds the events held in memory for the API and the stats.
const keepEvents = 5000

type eventLog struct {
	path   string
	mu     sync.Mutex
	events []Event
}

func openEvents(dir string) (*eventLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &eventLog{path: filepath.Join(dir, "events.jsonl")}
	f, err := os.Open(l.path)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.events = append(l.events, e)
			}
		}
		f.Close()
		if len(l.events) > keepEvents {
			l.events = l.events[len(l.events)-keepEvents:]
		}
	}
	return l, nil
}

func (l *eventLog) append(e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
	if len(l.events) > keepEvents {
		l.events = l.events[len(l.events)-keepEvents:]
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// recent returns up to n events, newest first.
func (l *eventLog) recent(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.events) {
		n = len(l.events)
	}
	out := make([]Event, 0, n)
	for i := len(l.events) - 1; i >= len(l.events)-n; i-- {
		out = append(out, l.events[i])
	}
	return out
}

// successes returns the ok events, oldest first.
func (l *eventLog) successes() []pipeline.RecallEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []pipeline.RecallEvent
	for _, e := range l.events {
		if e.OK {
			out = append(out, pipeline.RecallEvent{Time: e.Time, ConversationID: e.ConversationID, Req: e.Req, Key: e.Key,
				BlockKey: e.BlockKey, ToolUseID: e.ToolUseID, Tool: e.Tool, Kind: e.Kind, Tokens: e.Tokens})
		}
	}
	return out
}

// Stats summarises the events in memory.
type Stats struct {
	Total          int            `json:"total"`
	OK             int            `json:"ok"`
	Errors         int            `json:"errors"`
	ByError        map[string]int `json:"by_error"`
	TokensRestored int            `json:"tokens_restored"`
	Conversations  int            `json:"conversations"`
	Last           *time.Time     `json:"last,omitempty"`
	TopKeys        []KeyCount     `json:"top_keys"`
	TopTools       []ToolCount    `json:"top_tools"`
}

type KeyCount struct {
	Key   string `json:"key"`
	Tool  string `json:"tool,omitempty"`
	Count int    `json:"count"`
}

type ToolCount struct {
	Tool   string `json:"tool"`
	Count  int    `json:"count"`
	Tokens int    `json:"tokens"`
}

const topN = 10

func (l *eventLog) stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := Stats{ByError: map[string]int{}, TopKeys: []KeyCount{}, TopTools: []ToolCount{}}
	convs := map[string]bool{}
	keys := map[string]*KeyCount{}
	tools := map[string]*ToolCount{}
	for _, e := range l.events {
		s.Total++
		if e.ConversationID != "" {
			convs[e.ConversationID] = true
		}
		if !e.OK {
			s.Errors++
			s.ByError[e.Error]++
			continue
		}
		s.OK++
		s.TokensRestored += e.Tokens
		// A tool_use_id is stable across turns; an ir key is only unique
		// within its request.
		id := e.ToolUseID
		if id == "" {
			id = e.Req + "/" + e.BlockKey
		}
		k := keys[id]
		if k == nil {
			k = &KeyCount{Key: id, Tool: e.Tool}
			keys[id] = k
		}
		k.Count++
		name := e.Tool
		if name == "" {
			name = "(" + e.Kind + ")"
		}
		t := tools[name]
		if t == nil {
			t = &ToolCount{Tool: name}
			tools[name] = t
		}
		t.Count++
		t.Tokens += e.Tokens
	}
	s.Conversations = len(convs)
	if n := len(l.events); n > 0 {
		t := l.events[n-1].Time
		s.Last = &t
	}
	for _, k := range keys {
		s.TopKeys = append(s.TopKeys, *k)
	}
	sort.Slice(s.TopKeys, func(i, j int) bool {
		a, b := s.TopKeys[i], s.TopKeys[j]
		return a.Count > b.Count || (a.Count == b.Count && a.Key < b.Key)
	})
	if len(s.TopKeys) > topN {
		s.TopKeys = s.TopKeys[:topN]
	}
	for _, t := range tools {
		s.TopTools = append(s.TopTools, *t)
	}
	sort.Slice(s.TopTools, func(i, j int) bool {
		a, b := s.TopTools[i], s.TopTools[j]
		return a.Count > b.Count || (a.Count == b.Count && a.Tool < b.Tool)
	})
	if len(s.TopTools) > topN {
		s.TopTools = s.TopTools[:topN]
	}
	return s
}

// conversations reads every event on disk (not only those in memory) and
// returns each conversation's newest event.
func (l *eventLog) conversations() map[string]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]time.Time{}
	for _, e := range l.readAllLocked() {
		if e.ConversationID != "" && e.Time.After(out[e.ConversationID]) {
			out[e.ConversationID] = e.Time
		}
	}
	return out
}

func (l *eventLog) readAllLocked() []Event {
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// expire removes the events of the given conversations, and events tied to
// no conversation, when they are older than before. The file is rewritten
// atomically; events are never reordered.
func (l *eventLog) expire(ids []string, before time.Time) (int, error) {
	drop := map[string]bool{"": true}
	for _, id := range ids {
		drop[id] = true
	}
	gone := func(e Event) bool { return drop[e.ConversationID] && e.Time.Before(before) }
	l.mu.Lock()
	defer l.mu.Unlock()
	all := l.readAllLocked()
	var buf []byte
	n := 0
	for _, e := range all {
		if gone(e) {
			n++
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			return 0, err
		}
		buf = append(append(buf, line...), '\n')
	}
	if n == 0 {
		return 0, nil
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return 0, err
	}
	kept := l.events[:0]
	for _, e := range l.events {
		if !gone(e) {
			kept = append(kept, e)
		}
	}
	l.events = kept
	return n, nil
}
