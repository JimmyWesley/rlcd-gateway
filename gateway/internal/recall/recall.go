// Package recall lets the model fetch pruned content back (F4).
//
// When the pruner (F1) drops a block it leaves a marker made by
// pipeline.Marker, naming the request whose logged body still holds the
// original and the block's key. The gateway exposes an MCP server at /mcp
// with one tool, rlcd_recall, that takes those two values and returns the
// original content. Every call is recorded as an Event: a recall is the
// clearest sign the pruner dropped something the model needed.
//
// Endpoints:
//
//	POST|GET|DELETE /mcp          MCP Streamable HTTP (see mcp.go)
//	GET  /api/recall/events       recent events, newest first (?limit=N, default 100)
//	GET  /api/recall/stats        counts, top recalled keys and tools
//	GET  /api/recall/settings     {enabled, max_bytes, mcp_path, tool}
//	PUT  /api/recall/settings     {enabled?, max_bytes?}
package recall

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// ToolName is the tool's MCP name. Claude Code shows it to the model as
// mcp__<server name>__rlcd_recall.
const ToolName = "rlcd_recall"

// DefaultMaxBytes caps one recall (~25k tokens). Larger content is cut and
// the model is told so.
const DefaultMaxBytes = 100_000

// Settings is the "recall" config section.
type Settings struct {
	Enabled  bool `json:"enabled"`
	MaxBytes int  `json:"max_bytes"`
}

type Server struct {
	cfg    *config.Store
	st     *store.Store
	events *eventLog
	cache  *lru
	mcp    *mcpServer
	now    func() time.Time
}

// New wires the recall server. Events live in <config dir>/recall/; if that
// directory cannot be created, recalls still work and are only logged.
func New(cfg *config.Store, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, cache: newLRU(256), now: time.Now}
	ev, err := openEvents(filepath.Join(config.Dir(), "recall"))
	if err != nil {
		log.Printf("recall: events disabled: %v", err)
	}
	s.events = ev
	s.mcp = newMCPServer(s)
	return s
}

// Register mounts the recall endpoints (MCP at /mcp, dashboard API under /api/recall).
func (s *Server) Register(mux *http.ServeMux) {
	mux.Handle("POST /mcp", s.mcp)
	mux.Handle("GET /mcp", s.mcp)
	mux.Handle("DELETE /mcp", s.mcp)
	// Any other method on the MCP endpoint must not fall through to the proxy.
	mux.Handle("/mcp", s.mcp)
	mux.HandleFunc("GET /api/recall/events", s.listEvents)
	mux.HandleFunc("GET /api/recall/stats", s.getStats)
	mux.HandleFunc("GET /api/recall/settings", s.getSettings)
	mux.HandleFunc("PUT /api/recall/settings", s.putSettings)
}

func (s *Server) settings() Settings {
	out := Settings{Enabled: true, MaxBytes: DefaultMaxBytes}
	raw := s.cfg.Get().Section("recall")
	if len(raw) == 0 {
		return out
	}
	var in struct {
		Enabled  *bool `json:"enabled"`
		MaxBytes *int  `json:"max_bytes"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return out
	}
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if in.MaxBytes != nil && *in.MaxBytes > 0 {
		out.MaxBytes = *in.MaxBytes
	}
	return out
}

// Args are rlcd_recall's arguments: key and req from the marker, or the
// whole marker.
type Args struct {
	Key    string `json:"key"`
	Req    string `json:"req"`
	Marker string `json:"marker"`
}

// normalize fills key and req from whatever the model sent. Models
// sometimes paste the marker into key or req, or quote the values.
func (a Args) normalize() (req, key string, ok bool) {
	for _, s := range []string{a.Marker, a.Key, a.Req} {
		if r, k, found := pipeline.ParseMarker(s); found {
			return r, k, true
		}
	}
	clean := func(s string) string { return strings.Trim(strings.TrimSpace(s), "`'\"[]") }
	req, key = clean(a.Req), clean(a.Key)
	return req, key, req != "" && key != ""
}

// Result is what one recall produced.
type Result struct {
	Text  string
	Error bool
	Event Event
}

// Recall runs the tool and records the event. client is the MCP client's
// self-reported identity, for the event only. keyID is the gateway key that
// called the tool: a key can only recall from requests it made itself.
func (s *Server) Recall(a Args, client, keyID string) Result {
	ev := Event{Time: s.now().UTC(), Client: client}
	fail := func(code, msg string) Result {
		ev.Error, ev.Message = code, msg
		s.record(ev)
		return Result{Text: msg, Error: true, Event: ev}
	}
	req, key, ok := a.normalize()
	ev.Req, ev.Key = req, key
	if !ok {
		return fail(ErrBadArgs, "rlcd_recall needs both key and req, copied from the marker "+
			"[rlcd: ~N tokens omitted · key <key> · req <req> · call rlcd_recall to restore], or the whole marker as marker.")
	}
	set := s.settings()
	if !set.Enabled {
		return fail(ErrDisabled, "Recall is turned off in the RLCD Gateway settings, so omitted content cannot be restored. Re-run the tool or re-read the file instead.")
	}

	f, err := s.lookup(req, key)
	if err == nil && keyID != "" && f.KeyID != keyID {
		// Same answer as a request that does not exist: another key's
		// traffic is not even acknowledged.
		err = unknownRequest(req)
	}
	if err != nil {
		var le *lookupError
		if errors.As(err, &le) {
			return fail(le.Code, le.Msg)
		}
		return fail(ErrStore, err.Error())
	}
	ev.ConversationID, ev.BlockKey, ev.ToolUseID, ev.Tool, ev.Kind = f.ConversationID, f.BlockKey, f.ToolUseID, f.Tool, f.Kind
	ev.Tokens = ir.EstimateTokens(len(f.Content))

	body, truncated := truncate(f.Content, set.MaxBytes)
	what := f.BlockKey
	if f.ToolUseID != "" && f.ToolUseID != f.BlockKey {
		what += " · " + f.ToolUseID
	}
	if f.Tool != "" {
		what += " · " + f.Tool
	}
	text := fmt.Sprintf("[rlcd recall · key %s · req %s · %s · %d bytes, ~%d tokens]\n%s", key, f.Req, what, len(f.Content), ev.Tokens, body)
	if truncated {
		text += fmt.Sprintf("\n[rlcd recall: truncated — showing the first %d of %d bytes (recall max_bytes). The rest is not shown; re-run the tool or read the file for a narrower slice.]", len(body), len(f.Content))
	}
	ev.OK, ev.Bytes, ev.Truncated = true, len(body), truncated
	s.record(ev)
	return Result{Text: text, Event: ev}
}

func (s *Server) record(ev Event) {
	if s.events == nil {
		return
	}
	if err := s.events.append(ev); err != nil {
		log.Printf("recall: write event: %v", err)
	}
}

// lookup resolves through the cache. Only successes are cached: a missing
// request may still be written by an in-flight turn.
func (s *Server) lookup(req, key string) (*found, error) {
	ck := req + "\x00" + key
	if f, ok := s.cache.get(ck); ok && !s.st.Purged(req) {
		return f, nil
	}
	f, err := resolve(s.st, req, key)
	if err != nil {
		return nil, err
	}
	s.cache.put(ck, f)
	return f, nil
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// lru is a small least-recently-used cache of resolved blocks. Logged
// bodies are immutable, so an entry only goes stale when retention purges
// its request, which lookup checks.
type lru struct {
	mu    sync.Mutex
	cap   int
	order []string
	items map[string]*found
}

func newLRU(n int) *lru { return &lru{cap: n, items: map[string]*found{}} }

func (c *lru) get(k string) (*found, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.items[k]
	if ok {
		c.touch(k)
	}
	return f, ok
}

func (c *lru) put(k string, f *found) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; !ok && len(c.items) >= c.cap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
	c.items[k] = f
	c.touch(k)
}

func (c *lru) touch(k string) {
	for i, x := range c.order {
		if x == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, k)
}

// --- dashboard API ---

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		n = v
	}
	out := []Event{}
	if s.events != nil {
		out = s.events.recent(n)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	st := Stats{ByError: map[string]int{}, TopKeys: []KeyCount{}, TopTools: []ToolCount{}}
	if s.events != nil {
		st = s.events.stats()
	}
	writeJSON(w, http.StatusOK, st)
}

type settingsView struct {
	Settings
	MCPPath string `json:"mcp_path"`
	Tool    string `json:"tool"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsView{s.settings(), "/mcp", ToolName})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled  *bool `json:"enabled"`
		MaxBytes *int  `json:"max_bytes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := s.settings()
	if in.Enabled != nil {
		cur.Enabled = *in.Enabled
	}
	if in.MaxBytes != nil {
		if *in.MaxBytes < 1000 {
			writeError(w, http.StatusBadRequest, "max_bytes must be at least 1000")
			return
		}
		cur.MaxBytes = *in.MaxBytes
	}
	raw, _ := json.Marshal(cur)
	if err := s.cfg.SetSection("recall", raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsView{cur, "/mcp", ToolName})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Recalls implements pipeline.RecallLog: the successful recalls in memory.
func (s *Server) Recalls() []pipeline.RecallEvent {
	if s.events == nil {
		return nil
	}
	return s.events.successes()
}

// --- storage ---

// StorageName names recall in the janitor's reports.
func (s *Server) StorageName() string { return "recall" }

// StorageConversations lists conversations by their newest recall.
func (s *Server) StorageConversations() []store.Conversation {
	if s.events == nil {
		return nil
	}
	var out []store.Conversation
	for id, t := range s.events.conversations() {
		out = append(out, store.Conversation{ID: id, LastActive: t})
	}
	return out
}

// ExpireConversations deletes the recall events of idle conversations.
func (s *Server) ExpireConversations(ids []string, before time.Time) (int, error) {
	if s.events == nil {
		return 0, nil
	}
	return s.events.expire(ids, before)
}
