package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// runAs is harness.run for another protocol.
func (h *harness) runAs(protocol string, body []byte) (string, *pipeline.Result, Summary, Detail) {
	h.t.Helper()
	h.n++
	id := fmt.Sprintf("req%03d", h.n)
	r := &pipeline.Request{ID: id, Protocol: protocol, Body: body, Config: h.cfg.Get(),
		ConversationID: pipeline.ConversationIDFor(protocol, nil, body)}
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

// chatConv builds an OpenAI Chat Completions agent loop.
type chatConv struct{ msgs []map[string]any }

func newChat(goal string) *chatConv {
	return &chatConv{msgs: []map[string]any{
		{"role": "system", "content": "You are a coding agent."},
		{"role": "user", "content": goal},
	}}
}

func (c *chatConv) call(id, name string, args map[string]any, result string) *chatConv {
	a, _ := json.Marshal(args)
	c.msgs = append(c.msgs,
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}},
		}},
		map[string]any{"role": "tool", "tool_call_id": id, "content": result})
	return c
}

func (c *chatConv) say(role, text string) *chatConv {
	c.msgs = append(c.msgs, map[string]any{"role": role, "content": text})
	return c
}

func (c *chatConv) body(t *testing.T) []byte {
	b, err := json.Marshal(map[string]any{
		"model": "gpt-4.1", "stream": true, "stream_options": map[string]any{"include_usage": true},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Read", "description": "Read a file", "parameters": map[string]any{}}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash", "description": "Run a command", "parameters": map[string]any{}}},
		},
		"messages": c.msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseChat() *chatConv {
	return newChat("Fix the failing login test").
		call("call_npm", "Bash", map[string]any{"command": "npm install"}, big("NOISE npm")).
		call("call_read", "Read", map[string]any{"file_path": "/src/login.go"}, big("package login v1")).
		call("call_web", "Bash", map[string]any{"command": "curl pasta"}, big("NOISE carbonara")).
		call("call_end", "Bash", map[string]any{"command": "true"}, big("NOISE last output"))
}

type chatBody struct {
	Messages []map[string]any `json:"messages"`
	Tools    []any            `json:"tools"`
}

func decodeChat(t *testing.T, b []byte) chatBody {
	t.Helper()
	var c chatBody
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChatPruningInvariantsAndStickyMarkers(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseChat()
	body := c.body(t)
	id1, res, s, d := h.runAs(ir.ProtocolOpenAIChat, body)
	if res == nil || res.Body == nil || !s.Applied {
		t.Fatalf("expected a pruned body: %+v", s)
	}
	if err := (&oaDoc{protocol: ir.ProtocolOpenAIChat}).verify(body, res.Body); err != nil {
		t.Fatalf("invariants: %v", err)
	}
	if d.Protocol != ir.ProtocolOpenAIChat || !d.Cost.Automatic {
		t.Errorf("detail protocol %q cost %+v", d.Protocol, d.Cost)
	}
	in, out := decodeChat(t, body), decodeChat(t, res.Body)
	want := pipeline.Marker(id1, "call_npm", ir.EstimateTokens(len(big("NOISE npm"))))
	for i, m := range out.Messages {
		orig := in.Messages[i]
		if m["role"] != orig["role"] || m["tool_call_id"] != orig["tool_call_id"] {
			t.Fatalf("message %d: role or tool_call_id changed", i)
		}
		if m["role"] == "assistant" && encJSON(m["tool_calls"]) != encJSON(orig["tool_calls"]) {
			t.Fatalf("message %d: tool_calls changed", i)
		}
		if m["tool_call_id"] == "call_npm" && m["content"] != want {
			t.Fatalf("noise output: %v, want %q", m["content"], want)
		}
		if m["tool_call_id"] == "call_read" && m["content"] != orig["content"] {
			t.Fatal("useful output dropped")
		}
	}
	last := out.Messages[len(out.Messages)-1]
	if last["content"] != big("NOISE last output") {
		t.Fatal("the latest turn must never be touched")
	}
	if encJSON(out.Tools) != encJSON(in.Tools) {
		t.Fatal("tool definitions changed")
	}
	if b := blockBy(d, "tool.0"); b.Protected != "tool definition" {
		t.Errorf("tool: %+v", b)
	}

	// Next turn, no epoch: identical marker bytes, no selector call.
	h.set(Settings{Mode: ModeEnforce, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(1 << 30)})
	h.sel.reset()
	c.call("call_ls", "Bash", map[string]any{"command": "ls"}, big("NOISE ls"))
	_, res2, s2, d2 := h.runAs(ir.ProtocolOpenAIChat, c.body(t))
	if _, calls := h.sel.reset(); calls != 0 || s2.EpochRan || s2.CacheInvalidating {
		t.Fatalf("steady turn: calls %d %+v", calls, s2)
	}
	out2 := decodeChat(t, res2.Body)
	for i := range out.Messages {
		if encJSON(out.Messages[i]) != encJSON(out2.Messages[i]) && i != len(out.Messages)-1 {
			t.Fatalf("message %d changed between turns:\n%s\n%s", i, encJSON(out.Messages[i]), encJSON(out2.Messages[i]))
		}
	}
	if b := blockBy(d2, "call_npm"); b.Reason != ReasonSticky || b.FirstReq != id1 {
		t.Fatalf("sticky drop: %+v", b)
	}
	// The previous "last output" is now old, but new since the epoch: kept as pending.
	if b := blockBy(d2, "call_end"); b.Decision != "keep" || b.Reason != ReasonPending {
		t.Fatalf("new block between epochs: %+v", b)
	}
}

func TestChatShadowByDefault(t *testing.T) {
	s := testSettings("")
	h := newHarness(t, s)
	_, res, sum, _ := h.runAs(ir.ProtocolOpenAIChat, baseChat().body(t))
	if res == nil || res.Body != nil || sum.Mode != ModeShadow || sum.Dropped == 0 {
		t.Fatalf("shadow must report without rewriting: %+v", sum)
	}
}

// respConv builds a Responses API agent loop the way Codex sends it.
type respConv struct{ items []map[string]any }

func newResp(goal string) *respConv {
	return &respConv{items: []map[string]any{
		{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "sandbox: workspace-write"}}},
		{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": goal}}},
	}}
}

func (c *respConv) call(id, name string, args map[string]any, output any) *respConv {
	a, _ := json.Marshal(args)
	c.items = append(c.items,
		map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "gAAAA-" + id},
		map[string]any{"type": "function_call", "name": name, "arguments": string(a), "call_id": id},
		map[string]any{"type": "function_call_output", "call_id": id, "output": output})
	return c
}

func (c *respConv) body(t *testing.T) []byte {
	b, err := json.Marshal(map[string]any{
		"model": "gpt-5-codex", "stream": true, "instructions": "You are Codex.", "store": false,
		"tools": []any{map[string]any{"type": "function", "name": "shell", "parameters": map[string]any{}}},
		"input": c.items,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestResponsesPruningInvariants(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := newResp("Fix the login bug").
		call("call_1", "shell", map[string]any{"command": "npm install"}, big("NOISE npm")).
		call("call_2", "shell", map[string]any{"command": "cat login.go"}, []any{map[string]any{"type": "input_text", "text": big("NOISE listing as parts")}}).
		call("call_3", "shell", map[string]any{"command": "cat auth.go"}, big("package auth")).
		call("call_4", "shell", map[string]any{"command": "true"}, big("NOISE latest"))
	body := c.body(t)
	id, res, s, d := h.runAs(ir.ProtocolOpenAIResponses, body)
	if res == nil || res.Body == nil || !s.Applied || s.Dropped != 2 {
		t.Fatalf("expected two drops: %+v", s)
	}
	if err := (&oaDoc{protocol: ir.ProtocolOpenAIResponses}).verify(body, res.Body); err != nil {
		t.Fatal(err)
	}
	var in, out struct {
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
	}
	_ = json.Unmarshal(body, &in)
	_ = json.Unmarshal(res.Body, &out)
	if out.Instructions != in.Instructions || len(out.Input) != len(in.Input) {
		t.Fatal("instructions or item count changed")
	}
	for i, it := range out.Input {
		orig := in.Input[i]
		switch it["type"] {
		case "reasoning", "function_call", "message":
			if encJSON(it) != encJSON(orig) {
				t.Fatalf("item %d (%v) changed", i, it["type"])
			}
		case "function_call_output":
			if it["call_id"] != orig["call_id"] {
				t.Fatal("call_id changed")
			}
		}
	}
	// A string output stays a string, a list of parts stays a list.
	if got := out.Input[4]["output"]; got != pipeline.Marker(id, "call_1", ir.EstimateTokens(len(big("NOISE npm")))) {
		t.Fatalf("string output: %v", got)
	}
	parts, ok := out.Input[7]["output"].([]any)
	if !ok || len(parts) != 1 || parts[0].(map[string]any)["type"] != "input_text" ||
		!strings.HasPrefix(parts[0].(map[string]any)["text"].(string), "[rlcd: ") {
		t.Fatalf("list output: %v", out.Input[7]["output"])
	}
	if encJSON(out.Input[10]["output"]) != encJSON(in.Input[10]["output"]) {
		t.Fatal("useful output dropped")
	}
	if encJSON(out.Input[len(out.Input)-1]) != encJSON(in.Input[len(in.Input)-1]) {
		t.Fatal("latest output touched")
	}
	if b := blockBy(d, "sys.0"); b.Protected != "instructions" {
		t.Errorf("instructions: %+v", b)
	}
	if b := blockBy(d, "m2.b0"); b.Kind != ir.KindThinking || b.Decision != "keep" {
		t.Errorf("reasoning: %+v", b)
	}
}

func TestConversationTextNeedsFlag(t *testing.T) {
	chat := newChat("Tell me about pasta").
		say("assistant", "NOISE "+strings.Repeat("a long answer about carbonara. ", 300)).
		say("user", "and pizza? "+strings.Repeat("with details ", 100)).
		say("assistant", "NOISE "+strings.Repeat("a long answer about pizza. ", 300)).
		say("user", "thanks, now risotto")
	body := func() []byte {
		b, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "messages": chat.msgs})
		return b
	}

	h := newHarness(t, testSettings(ModeEnforce))
	_, res, s, d := h.runAs(ir.ProtocolOpenAIChat, body())
	if (res != nil && res.Body != nil) || s.Dropped != 0 {
		t.Fatalf("text must not be pruned without the flag: %+v", s)
	}
	if b := blockBy(d, "m2.b0"); !strings.Contains(b.Protected, "prune_conversation_text") {
		t.Fatalf("reason should name the flag: %+v", b)
	}

	st := testSettings(ModeEnforce)
	st.PruneConversationText = ptr(true)
	h = newHarness(t, st)
	_, res, s, d = h.runAs(ir.ProtocolOpenAIChat, body())
	if res == nil || res.Body == nil || s.Dropped == 0 {
		t.Fatalf("old assistant answer should drop with the flag: %+v", s)
	}
	if b := blockBy(d, "m2.b0"); b.Decision != "drop" {
		t.Fatalf("old assistant text: %+v", b)
	}
	if b := blockBy(d, "m3.b0"); b.Decision != "keep" || b.Reason != "always_keep_user_text" {
		t.Fatalf("user text stays: %+v", b)
	}
	if b := blockBy(d, "m5.b0"); b.Protected != "recent turn" {
		t.Fatalf("latest user turn: %+v", b)
	}
	if err := (&oaDoc{protocol: ir.ProtocolOpenAIChat}).verify(body(), res.Body); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateCallIDsGetDistinctIDs(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := newChat("list twice").
		call("call_0", "Bash", map[string]any{"command": "ls a"}, big("NOISE first")).
		call("call_0", "Bash", map[string]any{"command": "ls b"}, big("NOISE second")).
		call("call_1", "Bash", map[string]any{"command": "true"}, "ok")
	_, res, s, d := h.runAs(ir.ProtocolOpenAIChat, c.body(t))
	if res == nil || s.Dropped != 2 {
		t.Fatalf("both outputs should drop: %+v", s)
	}
	a, b := blockBy(d, "m3.b0"), blockBy(d, "m5.b0")
	if a.ID == b.ID || a.Key != "m3.b0" || b.Key != "m5.b0" {
		t.Fatalf("ambiguous call ids must be keyed apart and named by block key: %+v / %+v", a, b)
	}
}

func TestOAVerifyRejects(t *testing.T) {
	orig := baseChat().body(t)
	d := &oaDoc{protocol: ir.ProtocolOpenAIChat}
	mutate := func(f func(c *chatBody, m map[string]any)) []byte {
		var m map[string]any
		_ = json.Unmarshal(orig, &m)
		c := decodeChat(t, orig)
		f(&c, m)
		m["messages"] = c.Messages
		b, _ := json.Marshal(m)
		return b
	}
	cases := map[string][]byte{
		"tool_call_id": mutate(func(c *chatBody, _ map[string]any) { c.Messages[3]["tool_call_id"] = "other" }),
		"emptied":      mutate(func(c *chatBody, _ map[string]any) { c.Messages[3]["content"] = "" }),
		"outside":      mutate(func(_ *chatBody, m map[string]any) { m["temperature"] = 2 }),
		"tool_calls":   mutate(func(c *chatBody, _ map[string]any) { c.Messages[2]["tool_calls"] = []any{} }),
		"last":         mutate(func(c *chatBody, _ map[string]any) { c.Messages[len(c.Messages)-1]["content"] = "x" }),
		"dropped":      mutate(func(c *chatBody, _ map[string]any) { c.Messages = c.Messages[:len(c.Messages)-1] }),
	}
	for name, b := range cases {
		if err := d.verify(orig, b); err == nil {
			t.Errorf("%s: change not caught", name)
		}
	}
	if err := d.verify(orig, orig); err != nil {
		t.Errorf("identical body rejected: %v", err)
	}
}

func TestOACostHasNoWritePremium(t *testing.T) {
	eff := Settings{}.resolve()
	items := []*item{
		{Block: ir.Block{Kind: ir.KindToolResult, Msg: 1, Tokens: 10000}, ID: "a"},
		{Block: ir.Block{Kind: ir.KindText, Msg: 2, Tokens: 1000}, ID: "b"},
	}
	for _, it := range items {
		it.After = it.Tokens
	}
	items[0].After = 20
	changed := map[string]bool{"a": true}
	oa := estimateCost(eff, "gpt-4.1", ir.ProtocolOpenAIChat, items, true, changed, 2)
	// Everything after the change is plain input ($2/M), nothing at a premium.
	if want := float64(20+1000) * 2 / 1e6; round6(oa.After) != round6(want) || !oa.Automatic {
		t.Fatalf("openai after = %v, want %v (%+v)", oa.After, want, oa)
	}
	an := estimateCost(eff, "claude-sonnet-4-5", ir.ProtocolAnthropic, items, true, changed, 2)
	if want := float64(20+1000) * 3.75 / 1e6; round6(an.After) != round6(want) {
		t.Fatalf("anthropic after = %v, want %v", an.After, want)
	}
}

func TestThreadFingerprintSkipsBillingLine(t *testing.T) {
	mk := func(suffix string) *doc {
		b := fmt.Sprintf(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280.%s; cc_entrypoint=cli"},{"type":"text","text":"You are Claude Code."}],"messages":[{"role":"user","content":"hi"}]}`, suffix)
		d, err := parseDoc([]byte(b))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if mk("afb").threadFingerprint() != mk("0e4").threadFingerprint() {
		t.Fatal("the billing line split one thread in two")
	}
}

type fakeRecalls []pipeline.RecallEvent

func (f fakeRecalls) Recalls() []pipeline.RecallEvent { return f }

func TestRecallIsShouldKeepFeedback(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	body := baseConv().body(t)
	conv := pipeline.ConversationID(nil, body)
	id1, res, _, d := h.run(body)
	if b := blockBy(d, "toolu_web"); b.Decision != "drop" {
		t.Fatalf("setup: %+v", b)
	}
	// The request log holds the pruning report the feedback snapshots.
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(h.p.st.Save(&store.Detail{Record: store.Record{ID: id1, ConversationID: conv},
		StageDetails: map[string]json.RawMessage{"prune": res.Detail}}))
	ev := pipeline.RecallEvent{ConversationID: conv, Req: id1, Key: "toolu_web", ToolUseID: "toolu_web", Tool: "WebFetch", Kind: ir.KindToolResult}
	h.p.Recalls = fakeRecalls{ev, ev} // recalled twice: one case

	fs, err := h.p.allFeedback()
	must(err)
	var rc []Feedback
	for _, f := range fs {
		if f.Source == SourceRecall {
			rc = append(rc, f)
		}
	}
	if len(rc) != 1 || rc[0].Verdict != VerdictShouldKeep || rc[0].Decision != "drop" || rc[0].Preview == "" || rc[0].Goal == "" {
		t.Fatalf("recall feedback: %+v", rc)
	}
	rep := replayCases(context.Background(), Settings{}.resolve(), h.p.asker(h.cfg.Get().Selector), fs)
	if len(rep.Cases) != len(fs) || rep.Cases[len(rep.Cases)-1].Verdict != VerdictShouldKeep {
		t.Fatalf("replay must include recall cases: %+v", rep)
	}

	// The same conversation, its pruning state lost (a new home): the block
	// the model recalled is never dropped again.
	h2 := newHarness(t, testSettings(ModeEnforce))
	h2.p.Recalls = fakeRecalls{ev}
	_, _, _, d2 := h2.run(body)
	if b := blockBy(d2, "toolu_web"); b.Decision != "keep" || b.Reason != ReasonRecalled || !b.Recalled {
		t.Fatalf("recalled block re-dropped: %+v", b)
	}
	if b := blockBy(d2, "toolu_npm"); b.Decision != "drop" {
		t.Fatalf("other noise should still drop: %+v", b)
	}
}
