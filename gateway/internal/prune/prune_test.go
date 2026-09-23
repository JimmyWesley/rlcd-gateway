package prune

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// fakeSelector is an httptest System One: blocks whose preview contains
// "NOISE" score low, everything else high. It records every question id.
type fakeSelector struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls int
	asked []string
	fail  bool
	delay time.Duration
}

func newFakeSelector(t *testing.T) *fakeSelector {
	f := &fakeSelector{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			State     map[string]any `json:"state"`
			Questions map[string]struct {
				Type         string `json:"type"`
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls++
		fail, delay := f.fail, f.delay
		for k := range req.Questions {
			f.asked = append(f.asked, k)
		}
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		ans := map[string]any{}
		for k, q := range req.Questions {
			if q.Type != "noul" {
				t.Errorf("question %s: type %q", k, q.Type)
			}
			v := 0.9
			if strings.Contains(q.Instructions, "NOISE") {
				v = 0.01
			} else if strings.Contains(q.Instructions, "MEDIUM") {
				v = 0.3
			}
			ans[k] = map[string]any{"type": "noul", "noul": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// set changes the failure mode; the handler of an earlier, timed-out call
// may still be reading it.
func (f *fakeSelector) set(fail bool, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail, f.delay = fail, delay
}

func (f *fakeSelector) reset() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, c := f.asked, f.calls
	f.asked, f.calls = nil, 0
	return a, c
}

// conv builds a Claude Code-like request, one tool call at a time.
type conv struct {
	msgs  []map[string]any
	tools []map[string]any
}

func newConv(goal string) *conv {
	c := &conv{}
	for _, n := range []string{"Read", "Bash", "Edit", "Write", "WebFetch", "Unused", "mcp__rlcd-gateway__rlcd_recall"} {
		c.tools = append(c.tools, map[string]any{"name": n, "description": "tool " + n + strings.Repeat(" doc", 200),
			"input_schema": map[string]any{"type": "object"}})
	}
	c.msgs = append(c.msgs, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": goal}}})
	return c
}

func (c *conv) call(id, name string, input map[string]any, result string, isErr bool) *conv {
	c.msgs = append(c.msgs,
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "hmm " + id, "signature": "sig-" + id},
			map[string]any{"type": "tool_use", "id": id, "name": name, "input": input},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isErr, "content": result},
		}})
	return c
}

func (c *conv) body(t *testing.T) []byte {
	msgs := make([]any, len(c.msgs))
	for i, m := range c.msgs {
		msgs[i] = m
	}
	// Claude Code puts a breakpoint on the last message.
	last := c.msgs[len(c.msgs)-1]["content"].([]any)
	lastBlock := map[string]any{}
	for k, v := range last[len(last)-1].(map[string]any) {
		lastBlock[k] = v
	}
	lastBlock["cache_control"] = map[string]any{"type": "ephemeral"}
	lastMsg := map[string]any{"role": "user", "content": append(append([]any{}, last[:len(last)-1]...), lastBlock)}
	msgs[len(msgs)-1] = lastMsg
	tools := make([]any, len(c.tools))
	for i, tl := range c.tools {
		tools[i] = tl
	}
	b, err := json.Marshal(map[string]any{
		"model": "claude-opus-5", "max_tokens": 1000, "stream": true,
		"metadata": map[string]any{"user_id": "user_x_session_11111111-2222-3333-4444-555555555555"},
		"system": []any{
			map[string]any{"type": "text", "text": "You are a coding agent.", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "NOISE project notes" + strings.Repeat(" note", 400)},
		},
		"tools": tools, "messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func big(tag string) string { return tag + " " + strings.Repeat("line of output\n", 200) }

// baseConv is a small session: noise, a file read, an error, an edit and a recall.
func baseConv() *conv {
	return newConv("Fix the failing login test").
		call("toolu_npm", "Bash", map[string]any{"command": "npm install"}, big("NOISE npm"), false).
		call("toolu_read1", "Read", map[string]any{"file_path": "/src/login.go"}, big("package login v1"), false).
		call("toolu_err", "Bash", map[string]any{"command": "go test"}, big("NOISE FAIL TestLogin"), true).
		call("toolu_edit", "Edit", map[string]any{"file_path": "/src/login.go"}, big("NOISE edit applied"), false).
		call("toolu_recall", "mcp__rlcd-gateway__rlcd_recall", map[string]any{"key": "x"}, big("NOISE recalled"), false).
		call("toolu_web", "WebFetch", map[string]any{"url": "https://pasta.example"}, big("NOISE carbonara"), false).
		call("toolu_end", "Bash", map[string]any{"command": "true"}, "ok", false)
}

type harness struct {
	t    *testing.T
	sel  *fakeSelector
	home string
	cfg  *config.Store
	p    *Pruner
	n    int
}

func newHarness(t *testing.T, s Settings) *harness {
	t.Helper()
	h := &harness{t: t, sel: newFakeSelector(t), home: t.TempDir()}
	c := config.Default()
	c.Selector.BaseURL = h.sel.srv.URL
	raw, _ := json.Marshal(s)
	c.Sections = map[string]json.RawMessage{"prune": raw}
	h.cfg = config.NewStore(c)
	st, err := store.Open(h.home)
	if err != nil {
		t.Fatal(err)
	}
	h.p = newPruner(h.cfg, st, h.home)
	return h
}

func (h *harness) set(s Settings) {
	c := h.cfg.Get()
	raw, _ := json.Marshal(s)
	c.Sections["prune"] = raw
	h.cfg = config.NewStore(&c)
	h.p.cfg = h.cfg
}

func (h *harness) run(body []byte) (string, *pipeline.Result, Summary, Detail) {
	h.t.Helper()
	h.n++
	id := fmt.Sprintf("req%03d", h.n)
	r := &pipeline.Request{ID: id, Body: body, Config: h.cfg.Get(), ConversationID: pipeline.ConversationID(nil, body)}
	res, err := h.p.Transform(context.Background(), r, body)
	if err != nil {
		h.t.Fatalf("transform: %v", err)
	}
	var s Summary
	var d Detail
	if res != nil {
		_ = json.Unmarshal(res.Summary, &s)
		_ = json.Unmarshal(res.Detail, &d)
	}
	return id, res, s, d
}

func ptr[T any](v T) *T { return &v }

// testSettings: every block is big enough, and epochs run whenever there is
// anything new, unless a test says otherwise.
func testSettings(mode string) Settings {
	return Settings{Mode: mode, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(0)}
}

func blockBy(d Detail, key string) BlockReport {
	for _, b := range d.Blocks {
		if b.Key == key || b.IRKey == key {
			return b
		}
	}
	return BlockReport{}
}

// toolResults maps tool_use_id to the JSON of its tool_result block.
func toolResults(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var m struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, msg := range m.Messages {
		for _, b := range msg.Content {
			var h struct {
				Type string `json:"type"`
				ID   string `json:"tool_use_id"`
			}
			_ = json.Unmarshal(b, &h)
			if h.Type == "tool_result" {
				out[h.ID] = string(b)
			}
		}
	}
	return out
}

func TestShadowNeverChangesBody(t *testing.T) {
	h := newHarness(t, testSettings(ModeShadow))
	body := baseConv().body(t)
	_, res, s, d := h.run(body)
	if res == nil || res.Body != nil {
		t.Fatalf("shadow must not return a body")
	}
	if s.Mode != ModeShadow || !s.EpochRan || s.Dropped == 0 || s.SavedTokens <= 0 {
		t.Fatalf("shadow should still report what it would drop: %+v", s)
	}
	if s.Applied {
		t.Fatal("shadow reported applied")
	}
	if b := blockBy(d, "toolu_web"); b.Decision != "drop" || b.Reason != ReasonSelector {
		t.Fatalf("noise web fetch: %+v", b)
	}
	// Enforce without body logs cannot be recalled: it falls back to shadow.
	h.set(testSettings(ModeEnforce))
	c := h.cfg.Get()
	c.LogBodies = false
	h.cfg = config.NewStore(&c)
	h.p.cfg = h.cfg
	_, res, s, _ = h.run(body)
	if res.Body != nil || s.Warning == "" {
		t.Fatalf("enforce without log_bodies must not rewrite: %+v", s)
	}
}

var markerRe = regexp.MustCompile(`\[rlcd: [^\]]*\]`)

func TestStickyDecisionsIdenticalMarkerBytes(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseConv()
	id1, res1, s1, _ := h.run(c.body(t))
	if res1.Body == nil || !s1.Applied || !s1.CacheInvalidating {
		t.Fatalf("first epoch should prune: %+v", s1)
	}
	asked1, calls := h.sel.reset()
	if calls != 1 {
		t.Fatalf("one batched selector call per epoch, got %d", calls)
	}
	tr1 := toolResults(t, res1.Body)
	if !strings.Contains(tr1["toolu_web"], pipeline.Marker(id1, "toolu_web", s1Tokens(t, c, "toolu_web"))) {
		t.Fatalf("marker missing or wrong: %s", tr1["toolu_web"])
	}

	// Next turn, with no new epoch: the same drops with byte-identical markers.
	h.set(Settings{Mode: ModeEnforce, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(1 << 30)})
	c.call("toolu_ls", "Bash", map[string]any{"command": "ls"}, big("NOISE ls"), false)
	_, res2, s2, d2 := h.run(c.body(t))
	if s2.EpochRan {
		t.Fatal("no epoch expected")
	}
	if _, calls := h.sel.reset(); calls != 0 {
		t.Fatal("selector must not be called between epochs")
	}
	if s2.CacheInvalidating {
		t.Fatal("re-applying stored decisions must not invalidate the cache")
	}
	tr2 := toolResults(t, res2.Body)
	for id, v := range tr1 {
		if strings.Contains(v, "[rlcd:") && tr2[id] != v {
			t.Fatalf("%s changed between turns:\n%s\n%s", id, v, tr2[id])
		}
	}
	if b := blockBy(d2, "toolu_web"); b.Reason != ReasonSticky || b.FirstReq != id1 {
		t.Fatalf("sticky drop must keep its first request id: %+v", b)
	}
	if b := blockBy(d2, "toolu_read1"); b.Decision != "keep" || b.Reason != ReasonSticky {
		t.Fatalf("kept block should be sticky keep: %+v", b)
	}

	// A new epoch asks only about blocks that are new since the last one.
	h.set(testSettings(ModeEnforce))
	c.call("toolu_cat", "Bash", map[string]any{"command": "cat x"}, big("useful"), false)
	_, res3, s3, _ := h.run(c.body(t))
	asked3, _ := h.sel.reset()
	if !s3.EpochRan {
		t.Fatal("epoch expected")
	}
	for _, k := range asked3 {
		for _, old := range asked1 {
			if k == old {
				t.Fatalf("%s was re-litigated", k)
			}
		}
	}
	if strings.Join(asked3, ",") != "toolu_ls" {
		t.Fatalf("only the block new since the last epoch should be asked, got %v", asked3)
	}
	tr3 := toolResults(t, res3.Body)
	if tr3["toolu_web"] != tr2["toolu_web"] {
		t.Fatal("marker bytes changed at a later epoch")
	}
	// Restart: a new Pruner on the same directory reads the decisions back.
	st, _ := store.Open(h.home)
	h.p = newPruner(h.cfg, st, h.home)
	h.set(Settings{Mode: ModeEnforce, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(1 << 30)})
	_, res4, _, _ := h.run(c.body(t))
	if got := toolResults(t, res4.Body)["toolu_web"]; got != tr3["toolu_web"] {
		t.Fatalf("decisions did not survive a restart:\n%s\n%s", tr3["toolu_web"], got)
	}
	if markers := markerRe.FindAllString(string(res4.Body), -1); len(markers) == 0 {
		t.Fatal("no markers after restart")
	}
}

// s1Tokens is the estimated size of a tool result, as the marker reports it.
func s1Tokens(t *testing.T, c *conv, id string) int {
	for _, m := range c.msgs {
		for _, b := range m["content"].([]any) {
			bm := b.(map[string]any)
			if bm["tool_use_id"] == id {
				return (len(bm["content"].(string)) + 3) / 4
			}
		}
	}
	t.Fatalf("no %s", id)
	return 0
}

func TestPairInvariantAndProtectedBlocks(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseConv().call("toolu_last", "Bash", map[string]any{"command": "echo"}, big("NOISE last"), false)
	body := c.body(t)
	_, res, _, d := h.run(body)
	if res.Body == nil {
		t.Fatal("expected a pruned body")
	}
	if err := verify(body, res.Body); err != nil {
		t.Fatalf("invariants: %v", err)
	}
	// Every tool_use still has its tool_result with the same id and is_error.
	var m struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(res.Body, &m)
	uses, results := map[string]bool{}, map[string]map[string]any{}
	for _, msg := range m.Messages {
		if len(msg.Content) == 0 {
			t.Fatal("empty content")
		}
		for _, b := range msg.Content {
			switch b["type"] {
			case "tool_use":
				uses[b["id"].(string)] = true
			case "tool_result":
				results[b["tool_use_id"].(string)] = b
			case "thinking":
				if !strings.HasPrefix(b["signature"].(string), "sig-") || !strings.HasPrefix(b["thinking"].(string), "hmm ") {
					t.Fatal("thinking block touched")
				}
			}
		}
	}
	for id := range uses {
		if results[id] == nil {
			t.Fatalf("tool_use %s lost its result", id)
		}
	}
	if results["toolu_err"]["is_error"] != true {
		t.Fatal("is_error lost")
	}
	if tr := results["toolu_npm"]; tr != nil {
		parts, _ := tr["content"].([]any)
		if len(parts) != 1 || !strings.Contains(parts[0].(map[string]any)["text"].(string), "[rlcd:") {
			t.Fatalf("dropped result should be one marker text block: %v", tr["content"])
		}
	}

	// All of these score as noise, and none may be dropped.
	for key, reason := range map[string]string{
		"toolu_err":    "keep_errors",
		"toolu_edit":   "keep_edits",
		"toolu_recall": ReasonProtected,
		"toolu_last":   ReasonProtected,
		"sys.1":        ReasonProtected, // prune_system is off
		"tool.5":       ReasonProtected, // prune_tools is off
	} {
		if b := blockBy(d, key); b.Decision != "keep" || b.Reason != reason {
			t.Errorf("%s: want keep/%s, got %s/%s (%s)", key, reason, b.Decision, b.Reason, b.Protected)
		}
	}
	for _, b := range d.Blocks {
		if (b.Kind == "thinking" || b.Kind == "tool_use") && b.Decision != "keep" {
			t.Errorf("%s %s dropped", b.Kind, b.IRKey)
		}
	}

	// verify rejects the classic 400s.
	var broken map[string]any
	_ = json.Unmarshal(res.Body, &broken)
	msgs := broken["messages"].([]any)
	msgs[2].(map[string]any)["content"] = []any{}
	bb, _ := json.Marshal(broken)
	if verify(body, bb) == nil {
		t.Fatal("verify accepted empty content")
	}
	_ = json.Unmarshal(res.Body, &broken)
	tr := broken["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	tr["tool_use_id"] = "other"
	bb, _ = json.Marshal(broken)
	if verify(body, bb) == nil {
		t.Fatal("verify accepted a broken pair")
	}
}

func TestFailOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fail  bool
		delay time.Duration
	}{{"error", true, 0}, {"timeout", false, 400 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings(ModeEnforce)
			s.SelectorTimeoutMs = ptr(100)
			h := newHarness(t, s)
			h.sel.set(tc.fail, tc.delay)
			c := baseConv().call("toolu_read2", "Read", map[string]any{"file_path": "/src/login.go"}, big("package login v2"), false)
			_, res, sum, d := h.run(c.body(t))
			if res.Body != nil || sum.Dropped != 0 {
				t.Fatalf("selector failure must keep everything: %+v", sum)
			}
			if sum.SelectorError == "" || !sum.EpochRan {
				t.Fatalf("error must be reported: %+v", sum)
			}
			if b := blockBy(d, "toolu_read1"); b.Reason != ReasonFailOpen {
				t.Fatalf("even deterministic drops are kept on failure: %+v", b)
			}
			st, _ := h.p.states.load(pipeline.ConversationID(nil, c.body(t)))
			if len(st.Decisions) != 0 {
				t.Fatal("nothing may be recorded on failure")
			}
			// Recovered selector: decided at the next epoch.
			h.sel.set(false, 0)
			_, res, sum, _ = h.run(c.body(t))
			if res.Body == nil || sum.Dropped == 0 {
				t.Fatalf("next epoch should prune: %+v", sum)
			}
		})
	}
}

func TestEpochGating(t *testing.T) {
	c := baseConv()
	body := c.body(t)
	total := 0
	{
		h := newHarness(t, testSettings(ModeShadow))
		_, _, s, _ := h.run(body)
		total = s.EstTokensBefore
	}
	h := newHarness(t, Settings{Mode: ModeShadow, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50),
		FloorTokens: ptr(total + 1), EpochTokens: ptr(1000)})
	_, _, s, d := h.run(body)
	if s.EpochRan || s.Dropped != 0 || s.NextEpochAt != total+1 {
		t.Fatalf("below the floor: %+v", s)
	}
	if b := blockBy(d, "toolu_web"); b.Reason != ReasonPending {
		t.Fatalf("undecided block should be pending: %+v", b)
	}
	c.call("toolu_a", "Bash", map[string]any{"command": "a"}, big("NOISE a"), false)
	_, _, s, _ = h.run(c.body(t))
	if !s.EpochRan {
		t.Fatalf("above the floor: %+v", s)
	}
	first := s.EstTokensBefore
	c.call("toolu_b", "Bash", map[string]any{"command": "b"}, "NOISE small", false)
	_, _, s, _ = h.run(c.body(t))
	if s.EpochRan || s.NextEpochAt != first+1000 {
		t.Fatalf("grew less than epoch_tokens: %+v", s)
	}
	for i := 0; s.EstTokensBefore < first+1000; i++ {
		c.call(fmt.Sprintf("toolu_g%d", i), "Bash", map[string]any{"command": "g"}, big("NOISE g"), false)
		_, _, s, _ = h.run(c.body(t))
		if s.EstTokensBefore < first+1000 && s.EpochRan {
			t.Fatal("epoch ran early")
		}
	}
	if !s.EpochRan {
		t.Fatalf("grew by epoch_tokens: %+v", s)
	}
}

func TestSupersededReads(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseConv().
		call("toolu_part", "Read", map[string]any{"file_path": "/src/other.go", "offset": 10, "limit": 20}, big("partial"), false).
		call("toolu_read2", "Read", map[string]any{"file_path": "/src/login.go"}, big("package login v2"), false).
		call("toolu_part2", "Read", map[string]any{"file_path": "/src/other.go", "offset": 40, "limit": 20}, big("partial 2"), false).
		call("toolu_end", "Bash", map[string]any{"command": "true"}, "ok", false)
	_, _, _, d := h.run(c.body(t))
	if b := blockBy(d, "toolu_read1"); b.Decision != "drop" || b.Reason != ReasonSuperseded {
		t.Fatalf("first read should be superseded: %+v", b)
	}
	if b := blockBy(d, "toolu_read2"); b.Reason == ReasonSuperseded {
		t.Fatal("the latest read is not superseded")
	}
	if b := blockBy(d, "toolu_part"); b.Reason == ReasonSuperseded {
		t.Fatal("a read of a different range is not superseded")
	}
	asked, _ := h.sel.reset()
	for _, k := range asked {
		if k == "toolu_read1" {
			t.Fatal("deterministic drops must not go to the selector")
		}
	}
}

func TestPruneToolsAndSystem(t *testing.T) {
	s := testSettings(ModeEnforce)
	s.PruneTools, s.PruneSystem = ptr(true), ptr(true)
	h := newHarness(t, s)
	c := baseConv()
	c.tools[5]["description"] = "NOISE never used" + strings.Repeat(" doc", 200)
	body := c.body(t)
	_, res, sum, d := h.run(body)
	if !sum.CacheInvalidating {
		t.Fatal("dropping tools changes the prefix")
	}
	if b := blockBy(d, "tool.5"); b.Decision != "drop" {
		t.Fatalf("unused tool: %+v", b)
	}
	if b := blockBy(d, "tool.0"); b.Decision != "keep" || b.Protected != "called in history" {
		t.Fatalf("a called tool must stay: %+v", b)
	}
	if b := blockBy(d, "tool.6"); b.Decision != "keep" {
		t.Fatalf("recall tool must stay: %+v", b)
	}
	if b := blockBy(d, "sys.0"); b.Decision != "keep" {
		t.Fatal("first system block must stay")
	}
	if b := blockBy(d, "sys.1"); b.Decision != "drop" {
		t.Fatalf("later system block: %+v", b)
	}
	if bytes.Contains(res.Body, []byte(`"name":"Unused"`)) {
		t.Fatal("unused tool still sent")
	}
	if err := verify(body, res.Body); err != nil {
		t.Fatal(err)
	}
}

func TestCostEstimate(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseConv()
	_, _, s1, d1 := h.run(c.body(t))
	if !d1.Cost.Cached || d1.Cost.Priced != "claude-opus-5" || d1.Cost.InvalidFrom <= 0 {
		t.Fatalf("epoch cost: %+v", d1.Cost)
	}
	h.set(Settings{Mode: ModeEnforce, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(1 << 30)})
	c.call("toolu_x", "Bash", map[string]any{"command": "x"}, "ok", false)
	_, _, s2, d2 := h.run(c.body(t))
	if d2.Cost.InvalidFrom != -1 || !(s2.EstCostAfter < s2.EstCostBefore) {
		t.Fatalf("steady state should be cheaper: %+v %+v", s2, d2.Cost)
	}
	if s1.EstCostBefore <= 0 {
		t.Fatal("no cost")
	}
	if p, k := priceFor(mergePrices(nil), "anthropic/claude-sonnet-4.5"); k != "claude-sonnet-4" || p.Input != 3 {
		t.Fatalf("routed model price: %s %+v", k, p)
	}
}

func TestReplay(t *testing.T) {
	cases := []Feedback{
		{ID: "a", RequestID: "r1", Key: "k1", Verdict: VerdictShouldKeep, Kind: "tool_result", Tokens: 500,
			What: "tool_result of Read x", Preview: "NOISE but needed", Decision: "drop", Reason: ReasonSelector},
		{ID: "b", RequestID: "r1", Key: "k2", Verdict: VerdictShouldDrop, Kind: "tool_result", Tokens: 500,
			What: "tool_result of Bash ls", Preview: "NOISE listing", Decision: "drop", Reason: ReasonSelector},
		{ID: "c", RequestID: "r2", Key: "k3", Verdict: VerdictShouldKeep, Kind: "tool_result", Tokens: 500,
			IsError: true, Preview: "NOISE error", Decision: "keep", Reason: "keep_errors"},
		{ID: "d", RequestID: "r2", Key: "k4", Verdict: VerdictShouldDrop, Kind: "tool_result", Tokens: 500,
			Preview: "important", Decision: "keep", Reason: ReasonSelector},
	}
	var calls int
	ask := func(ctx context.Context, state map[string]any, qs map[string]selector.Question) (map[string]float64, error) {
		calls++
		out := map[string]float64{}
		for k, q := range qs {
			v := 0.9
			if strings.Contains(q.Instructions, "NOISE") {
				v = 0.2
			}
			out[k] = v
		}
		return out, nil
	}
	eff := Settings{}.resolve() // balanced: threshold 0.25
	rep := replayCases(context.Background(), eff, ask, cases)
	if calls != 2 {
		t.Fatalf("one batched call per request, got %d", calls)
	}
	if rep.Agree != 2 || rep.Disagree != 2 {
		t.Fatalf("balanced: %+v", rep)
	}
	// A lower threshold keeps the first case: fixed, and breaks the second.
	eff.KeepThreshold = 0.1
	rep = replayCases(context.Background(), eff, ask, cases)
	byID := map[string]ReplayCase{}
	for _, c := range rep.Cases {
		byID[c.ID] = c
	}
	if !byID["a"].Agrees || byID["b"].Agrees || byID["c"].Reason != "keep_errors" || byID["d"].Agrees {
		t.Fatalf("threshold 0.1: %+v", rep.Cases)
	}
	if rep.Fixed != 1 || rep.Broken != 1 {
		t.Fatalf("fixed/broken: %+v", rep)
	}
	// Criteria text reaches the question.
	eff.Criteria = "NOISE everywhere"
	rep = replayCases(context.Background(), eff, ask, cases)
	if byID := rep.Cases[3]; byID.Score == nil || *byID.Score != 0.2 {
		t.Fatalf("criteria not used: %+v", byID)
	}
	// A failing selector is an error, not a verdict.
	failing := func(ctx context.Context, state map[string]any, qs map[string]selector.Question) (map[string]float64, error) {
		return nil, fmt.Errorf("down")
	}
	rep = replayCases(context.Background(), eff, failing, cases)
	if rep.Errors != 3 || rep.Agree != 1 {
		t.Fatalf("errors: %+v", rep)
	}
}
