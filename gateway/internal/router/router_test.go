package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const session = "user_abc_account_def_session_11111111-2222-3333-4444-555555555555"

type env struct {
	t   *testing.T
	cfg *config.Store
	st  *store.Store
	r   *Router
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RLCD_GATEWAY_HOME", dir)
	c := config.Default()
	c.Routes["cheap"] = config.Route{Kind: config.KindOpenRouter, BaseURL: "https://openrouter.ai/api", Auth: config.AuthKey,
		APIKey: "sk-or-secret-123", Model: "anthropic/claude-haiku-4.5"}
	cs := config.NewStore(c)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, cfg: cs, st: st, r: New(cs, st), dir: dir}
}

func (e *env) setRules(s Settings) {
	e.t.Helper()
	if s.TTLHours == 0 {
		s.TTLHours = defaultTTLHours
	}
	if err := s.validate(e.cfg.Get()); err != nil {
		e.t.Fatal(err)
	}
	raw, _ := marshalSettings(s)
	if err := e.cfg.SetSection("router", raw); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) request(body map[string]any, headers ...string) *pipeline.Request {
	b, _ := json.Marshal(body)
	h := http.Header{}
	for i := 0; i+1 < len(headers); i += 2 {
		h.Set(headers[i], headers[i+1])
	}
	req := &pipeline.Request{ID: "t", Body: b, Headers: h, Config: e.cfg.Get(), ConversationID: pipeline.ConversationID(b)}
	req.XRay, _ = ir.Parse(b)
	return req
}

func (e *env) route(body map[string]any, headers ...string) (pipeline.RouteDecision, bool) {
	return e.r.Route(context.Background(), e.request(body, headers...))
}

// mainTurn looks like a Claude Code main-loop request: tools, cache_control,
// thinking, session metadata.
func mainTurn(turns int, extra ...string) map[string]any {
	msgs := []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "<system-reminder>CLAUDE.md says hi</system-reminder>"},
		map[string]any{"type": "text", "text": "refactor the auth module", "cache_control": map[string]string{"type": "ephemeral"}},
	}}}
	for i := 1; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "hmm", "signature": "sig"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]string{"file": "a.go"}}}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": strings.Repeat("x", 4000)}}})
	}
	b := map[string]any{
		"model": "claude-opus-4-5", "max_tokens": 32000, "stream": true,
		"metadata": map[string]string{"user_id": session},
		"system":   []any{map[string]any{"type": "text", "text": "You are Claude Code", "cache_control": map[string]string{"type": "ephemeral"}}},
		"tools":    []any{map[string]any{"name": "Read", "description": "read", "input_schema": map[string]any{}}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 4000},
		"messages": msgs,
	}
	for i := 0; i+1 < len(extra); i += 2 {
		b[extra[i]] = extra[i+1]
	}
	return b
}

// titleRequest looks like Claude Code's session-title side query: no tools,
// no thinking, one user message, no cache_control, small model, and the
// model's default max_tokens.
func titleRequest() map[string]any {
	return map[string]any{
		"model": "claude-haiku-4-5", "max_tokens": 32000, "stream": true,
		"metadata": map[string]string{"user_id": session},
		"system":   []any{map[string]any{"type": "text", "text": "Generate a concise title. Return JSON with a single \"title\" field."}},
		"messages": []any{map[string]any{"role": "user", "content": "<session>refactor the auth module</session>"}},
	}
}

func quotaRequest() map[string]any {
	return map[string]any{"model": "claude-haiku-4-5", "max_tokens": 1,
		"metadata": map[string]string{"user_id": session},
		"messages": []any{map[string]any{"role": "user", "content": "quota"}}}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestBackgroundDetection(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body map[string]any
		want bool
	}{
		{"main turn", mainTurn(1), false},
		{"main turn later", mainTurn(3), false},
		{"title", titleRequest(), true},
		{"quota probe", quotaRequest(), true},
		{"subagent first turn keeps tools", func() map[string]any {
			b := mainTurn(1)
			delete(b, "thinking")
			return b
		}(), false},
	}
	for _, c := range cases {
		f := extractFacts(e.request(c.body))
		if f.Background != c.want {
			t.Errorf("%s: background=%v want %v (signals %v)", c.name, f.Background, c.want, f.BackgroundSignals)
		}
	}
}

func TestConditions(t *testing.T) {
	e := newEnv(t)
	main := extractFacts(e.request(mainTurn(3), "X-Team", "platform-infra"))
	title := extractFacts(e.request(titleRequest()))
	img := mainTurn(1)
	img["messages"] = []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]string{"type": "base64", "data": "AAAA"}}}}}
	imgF := extractFacts(e.request(img))

	cases := []struct {
		name string
		m    Match
		f    *Facts
		want bool
	}{
		{"empty matches all", Match{}, main, true},
		{"model regex hit", Match{Model: "opus"}, main, true},
		{"model regex miss", Match{Model: "^claude-haiku"}, main, false},
		{"min context hit", Match{MinContextTokens: 1000}, main, true},
		{"min context miss", Match{MinContextTokens: 1_000_000}, main, false},
		{"max context hit", Match{MaxContextTokens: 1_000_000}, main, true},
		{"max context miss", Match{MaxContextTokens: 10}, main, false},
		{"has tools", Match{HasTools: yes()}, main, true},
		{"has tools miss", Match{HasTools: yes()}, title, false},
		{"no tools", Match{HasTools: no()}, title, true},
		{"has images", Match{HasImages: yes()}, imgF, true},
		{"has images miss", Match{HasImages: yes()}, main, false},
		{"has thinking", Match{HasThinking: yes()}, main, true},
		{"no thinking", Match{HasThinking: no()}, title, true},
		{"background", Match{Background: yes()}, title, true},
		{"background miss", Match{Background: yes()}, main, false},
		{"not background", Match{Background: no()}, main, true},
		{"max_tokens lte hit", Match{MaxTokensLTE: 32000}, main, true},
		{"max_tokens lte miss", Match{MaxTokensLTE: 1024}, main, false},
		{"header equals", Match{Headers: []HeaderCond{{Name: "x-team", Equals: "platform-infra"}}}, main, true},
		{"header equals miss", Match{Headers: []HeaderCond{{Name: "x-team", Equals: "platform"}}}, main, false},
		{"header contains", Match{Headers: []HeaderCond{{Name: "X-Team", Contains: "infra"}}}, main, true},
		{"header present", Match{Headers: []HeaderCond{{Name: "X-Team"}}}, main, true},
		{"header absent", Match{Headers: []HeaderCond{{Name: "X-Other"}}}, main, false},
		{"conversation", Match{Conversation: main.ConversationID}, main, true},
		{"conversation miss", Match{Conversation: "cc-other"}, main, false},
		{"all must hold", Match{Model: "opus", HasTools: no()}, main, false},
	}
	for _, c := range cases {
		checks, ok := matchRule(c.m, compileOrNil(c.m.Model), c.f)
		if ok != c.want {
			t.Errorf("%s: got %v want %v (%+v)", c.name, ok, c.want, checks)
		}
	}
}

func compileOrNil(s string) *regexp.Regexp {
	if s == "" {
		return nil
	}
	return regexp.MustCompile(s)
}

func TestFirstMatchWinsAndReasons(t *testing.T) {
	e := newEnv(t)
	e.setRules(Settings{Sticky: false, Rules: []Rule{
		{Name: "off", Enabled: false, Route: "cheap"},
		{Name: "bg", Enabled: true, Route: "cheap", When: Match{Background: yes()}},
		{Name: "opus", Enabled: true, Route: "openrouter", When: Match{Model: "opus"}},
		{Name: "all", Enabled: true, Route: "claude-sub"},
	}})
	d, ok := e.route(titleRequest())
	if !ok || d.Route != "cheap" || !strings.Contains(d.Reason, "rule 'bg' matched: background request (no tools") {
		t.Fatalf("title: %+v %v", d, ok)
	}
	d, ok = e.route(mainTurn(1))
	if !ok || d.Route != "openrouter" || d.Reason != "rule 'opus' matched: model claude-opus-4-5" {
		t.Fatalf("main: %+v %v", d, ok)
	}
	ev := e.r.evaluate(context.Background(), e.request(mainTurn(1)), evalOpts{})
	want := []string{ResultDisabled, ResultNoMatch, ResultMatched, ResultNotReached}
	for i, tr := range ev.Trace {
		if tr.Result != want[i] {
			t.Errorf("trace[%d] %s = %s want %s", i, tr.Name, tr.Result, want[i])
		}
	}

	e.setRules(Settings{Sticky: false, Rules: []Rule{{Name: "haiku", Enabled: true, Route: "cheap", When: Match{Model: "haiku"}}}})
	if d, ok := e.route(mainTurn(1)); ok {
		t.Fatalf("no rule should match: %+v", d)
	}
}

func TestStickyAcrossTurnsAndRestart(t *testing.T) {
	e := newEnv(t)
	e.setRules(Settings{Sticky: true, Rules: []Rule{
		{Name: "small", Enabled: true, Route: "cheap", When: Match{MaxContextTokens: 2000}},
	}})
	d, ok := e.route(mainTurn(1))
	if !ok || d.Route != "cheap" {
		t.Fatalf("turn 1: %+v %v", d, ok)
	}
	// Turn 3 is past the rule's limit, but the conversation stays put.
	d, ok = e.route(mainTurn(3))
	if !ok || d.Route != "cheap" || d.Reason != "sticky: conversation started on 'cheap' (rule 'small')" {
		t.Fatalf("turn 3: %+v %v", d, ok)
	}

	// Restart: a new router reads the pin from disk.
	r2 := New(e.cfg, e.st)
	d, ok = r2.Route(context.Background(), e.request(mainTurn(4)))
	if !ok || d.Route != "cheap" || !strings.HasPrefix(d.Reason, "sticky:") {
		t.Fatalf("after restart: %+v %v", d, ok)
	}
	list := r2.sticky.list(time.Hour)
	if len(list) != 1 || list[0].Turns < 2 {
		t.Fatalf("assignments: %+v", list)
	}

	// Expiry: a pin not seen for longer than the TTL is forgotten.
	r2.sticky.now = func() time.Time { return time.Now().Add(100 * time.Hour) }
	if d, ok := r2.Route(context.Background(), e.request(mainTurn(5))); ok {
		t.Fatalf("expired pin must not apply: %+v", d)
	}
}

func TestNoMatchPinsActiveRoute(t *testing.T) {
	e := newEnv(t)
	e.setRules(Settings{Sticky: true, Rules: []Rule{
		{Name: "big", Enabled: true, Route: "openrouter", When: Match{MinContextTokens: 2000}},
	}})
	if d, ok := e.route(mainTurn(1)); ok {
		t.Fatalf("turn 1 should use the active route: %+v", d)
	}
	// The context grows past the threshold, but the conversation started on
	// the active route and stays there.
	if d, ok := e.route(mainTurn(3)); ok {
		t.Fatalf("turn 3 must not switch mid-conversation: %+v", d)
	}
	ev := e.r.evaluate(context.Background(), e.request(mainTurn(3)), evalOpts{})
	if ev.Source != SourceSticky || !strings.Contains(ev.Decision.Reason, "active route") {
		t.Fatalf("got %+v", ev)
	}
}

func TestOverrideSticky(t *testing.T) {
	e := newEnv(t)
	e.setRules(Settings{Sticky: true, Rules: []Rule{
		{Name: "huge", Enabled: true, Route: "openrouter", OverrideSticky: true, When: Match{MinContextTokens: 2000}},
		{Name: "all", Enabled: true, Route: "cheap"},
	}})
	if d, _ := e.route(mainTurn(1)); d.Route != "cheap" {
		t.Fatalf("turn 1: %+v", d)
	}
	d, _ := e.route(mainTurn(3))
	if d.Route != "openrouter" || !strings.Contains(d.Reason, "overrode sticky") {
		t.Fatalf("override: %+v", d)
	}
	if a := e.r.sticky.get(e.request(mainTurn(1)).ConversationID, time.Hour); a == nil || a.Route != "openrouter" {
		t.Fatalf("pin should move: %+v", a)
	}
}

func TestBackgroundBypassesStickiness(t *testing.T) {
	e := newEnv(t)
	rules := []Rule{
		{Name: "bg", Enabled: true, Route: "cheap", When: Match{Background: yes()}},
		{Name: "main", Enabled: true, Route: "openrouter"},
	}
	e.setRules(Settings{Sticky: true, BackgroundBypass: true, Rules: rules})

	// A background request arrives first (title generation races the first
	// turn): it must not pin the conversation to the background route.
	if d, _ := e.route(titleRequest()); d.Route != "cheap" {
		t.Fatalf("title: %+v", d)
	}
	if d, _ := e.route(mainTurn(1)); d.Route != "openrouter" || strings.HasPrefix(d.Reason, "sticky") {
		t.Fatalf("main turn 1: %+v", d)
	}
	// Pinned now, but background requests still go their own way.
	if d, _ := e.route(quotaRequest()); d.Route != "cheap" {
		t.Fatalf("quota after pin: %+v", d)
	}
	if d, _ := e.route(mainTurn(2)); d.Route != "openrouter" || !strings.HasPrefix(d.Reason, "sticky") {
		t.Fatalf("main turn 2: %+v", d)
	}

	// With bypass off, background requests follow the pin.
	e.setRules(Settings{Sticky: true, BackgroundBypass: false, Rules: rules})
	if d, _ := e.route(titleRequest()); d.Route != "openrouter" || !strings.HasPrefix(d.Reason, "sticky") {
		t.Fatalf("title without bypass: %+v", d)
	}
}

// fakeSelector answers the route question with choice, after delay.
func fakeSelector(t *testing.T, choice string, delay time.Duration, calls *int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var in struct {
			Questions map[string]struct {
				Type     string            `json:"type"`
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
			State map[string]any `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		q := in.Questions["route"]
		if q.Type != "choice" || len(q.Criteria) < 2 {
			t.Errorf("bad question: %+v", in)
		}
		if in.State["first_message"] != "refactor the auth module" {
			t.Errorf("first_message should skip system reminders: %v", in.State["first_message"])
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"route":{"type":"choice","choice":"` + choice +
			`","confidence":0.9,"probabilities":{"cheap":0.95,"openrouter":0.05}}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (e *env) useSelector(url string) {
	sel := e.cfg.Get().Selector
	sel.BaseURL = url
	if err := e.cfg.SetSelector(sel); err != nil {
		e.t.Fatal(err)
	}
}

func autoSettings(timeout int) Settings {
	return Settings{Sticky: true, BackgroundBypass: true,
		Routes: map[string]RouteMeta{
			"cheap":      {Description: "fast cheap model for simple questions"},
			"openrouter": {Description: "strongest model for complex refactors"},
		},
		Rules: []Rule{
			{Name: "auto", Enabled: true, Kind: KindAuto, TimeoutMs: timeout},
			{Name: "fallback", Enabled: true, Route: "claude-sub"},
		}}
}

func TestAutoRule(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.useSelector(fakeSelector(t, "cheap", 0, &calls).URL)
	e.setRules(autoSettings(2000))

	d, ok := e.route(mainTurn(1))
	if !ok || d.Route != "cheap" || !strings.Contains(d.Reason, "auto rule 'auto' chose 'cheap' (confidence 0.90") {
		t.Fatalf("auto: %+v %v", d, ok)
	}
	// Later turns reuse the pin; the selector is not asked again.
	d, _ = e.route(mainTurn(3))
	if d.Route != "cheap" || calls != 1 {
		t.Fatalf("turn 3: %+v calls=%d", d, calls)
	}
	// Background requests never ask the selector.
	if d, _ := e.route(titleRequest()); d.Route != "claude-sub" || calls != 1 {
		t.Fatalf("background asked selector (calls=%d): %+v", calls, d)
	}
}

func TestAutoRuleFallbacks(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		e := newEnv(t)
		calls := 0
		e.useSelector(fakeSelector(t, "cheap", 2*time.Second, &calls).URL)
		e.setRules(autoSettings(100))
		start := time.Now()
		d, ok := e.route(mainTurn(1))
		if !ok || d.Route != "claude-sub" || d.Reason != "rule 'fallback' matched: no conditions (catch-all)" {
			t.Fatalf("timeout fallback: %+v %v", d, ok)
		}
		if el := time.Since(start); el > time.Second {
			t.Fatalf("timeout not honoured: %s", el)
		}
		ev := e.r.evaluate(context.Background(), e.request(mainTurn(1)), evalOpts{IgnoreSticky: true})
		if ev.Trace[0].Result != ResultFallback || !strings.Contains(ev.Trace[0].Reason, "timed out") {
			t.Fatalf("trace: %+v", ev.Trace[0])
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		e := newEnv(t)
		calls := 0
		e.useSelector(fakeSelector(t, "gpt-9", 0, &calls).URL)
		e.setRules(autoSettings(1000))
		if d, _ := e.route(mainTurn(1)); d.Route != "claude-sub" {
			t.Fatalf("unknown key must fall back: %+v", d)
		}
	})
	t.Run("too few descriptions", func(t *testing.T) {
		e := newEnv(t)
		s := autoSettings(1000)
		delete(s.Routes, "cheap")
		e.setRules(s)
		ev := e.r.evaluate(context.Background(), e.request(mainTurn(1)), evalOpts{})
		if ev.Trace[0].Result != ResultFallback || ev.Decision.Route != "claude-sub" {
			t.Fatalf("got %+v", ev)
		}
	})
}

func TestDryRunEqualsRoute(t *testing.T) {
	e := newEnv(t)
	e.setRules(Settings{Sticky: true, Rules: []Rule{
		{Name: "bg", Enabled: true, Route: "cheap", When: Match{Background: yes()}},
		{Name: "tools", Enabled: true, Route: "openrouter", When: Match{HasTools: yes(), Model: "opus"}},
	}})
	for i, body := range []map[string]any{mainTurn(1), titleRequest(), quotaRequest()} {
		b, _ := json.Marshal(body)
		id := "20260923T000000-00000" + string(rune('a'+i))
		if err := e.st.Save(&store.Detail{Record: store.Record{ID: id, ConversationID: pipeline.ConversationID(b)},
			RequestHeaders: map[string]string{"Content-Type": "application/json"}, RequestBody: string(b)}); err != nil {
			t.Fatal(err)
		}
		dry, err := e.r.DryRun(context.Background(), id, false)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := e.route(body)
		if dry.Decision != d || dry.OK != ok {
			t.Errorf("request %d: dry run %+v/%v, route %+v/%v", i, dry.Decision, dry.OK, d, ok)
		}
	}
	// The main turn is now pinned: the dry run says so, and ignore_sticky
	// shows what a fresh conversation would get.
	dry, _ := e.r.DryRun(context.Background(), "20260923T000000-00000a", false)
	if dry.Source != SourceSticky {
		t.Errorf("expected sticky, got %+v", dry.Evaluation)
	}
	dry, _ = e.r.DryRun(context.Background(), "20260923T000000-00000a", true)
	if dry.Source != SourceRule || dry.EffectiveRoute != "openrouter" {
		t.Errorf("ignore_sticky: %+v", dry.Evaluation)
	}
	// Dry runs never write pins.
	if n := len(e.r.sticky.list(time.Hour)); n != 1 {
		t.Errorf("pins after dry runs: %d", n)
	}
}

func TestMergeRouteKeySemantics(t *testing.T) {
	cur := config.Route{Kind: config.KindOpenRouter, BaseURL: "https://openrouter.ai/api", Auth: config.AuthKey, APIKey: "old"}
	in := routeInput{Kind: config.KindOpenRouter, BaseURL: "https://openrouter.ai/api/", Auth: config.AuthKey, Model: "x"}
	if rt, _ := mergeRoute(in, cur, true); rt.APIKey != "old" {
		t.Errorf("same host, empty key must keep it: %q", rt.APIKey)
	}
	in.BaseURL = "https://evil.example/api"
	if rt, _ := mergeRoute(in, cur, true); rt.APIKey != "" {
		t.Errorf("key must not follow the route to another host: %q", rt.APIKey)
	}
	in.BaseURL = cur.BaseURL
	in.ClearKey = true
	if rt, _ := mergeRoute(in, cur, true); rt.APIKey != "" {
		t.Errorf("clear_key: %q", rt.APIKey)
	}
	in.ClearKey, in.APIKey = false, "new"
	if rt, _ := mergeRoute(in, cur, true); rt.APIKey != "new" {
		t.Errorf("new key: %q", rt.APIKey)
	}
	if _, err := mergeRoute(routeInput{Kind: "gpt", BaseURL: "https://x", Auth: "key"}, cur, false); err == nil {
		t.Error("bad kind accepted")
	}
	if _, err := mergeRoute(routeInput{Kind: "anthropic", BaseURL: "ftp://x", Auth: "key"}, cur, false); err == nil {
		t.Error("bad base_url accepted")
	}
}
