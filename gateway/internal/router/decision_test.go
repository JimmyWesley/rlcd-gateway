package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// systemOne is a fake System One backend. answer builds the JSON answer to
// the "decision" question from the state it was shown.
type systemOne struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []s1Call
	answer func(state map[string]any) string
	delay  time.Duration
}

type s1Call struct {
	Auth     string
	Model    string
	State    map[string]any
	Question map[string]any
}

func newSystemOne(t *testing.T, answer func(state map[string]any) string) *systemOne {
	s := &systemOne{answer: answer}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model     string                    `json:"model"`
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		s.calls = append(s.calls, s1Call{Auth: r.Header.Get("Authorization"), Model: in.Model, State: in.State,
			Question: in.Questions[questionKey]})
		delay, answer := s.delay, s.answer
		s.mu.Unlock()
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rlcd-Forward-Ms", "12.5")
		_, _ = w.Write([]byte(`{"model":"fake","answers":{"decision":` + answer(in.State) + `}}`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *systemOne) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *systemOne) last() s1Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func (s *systemOne) set(answer string) {
	s.mu.Lock()
	s.answer = func(map[string]any) string { return answer }
	s.mu.Unlock()
}

func fixed(answer string) func(map[string]any) string {
	return func(map[string]any) string { return answer }
}

// useBackend configures a "jev" decisions backend (auth key, a token) at url.
func (e *env) useBackend(url string) {
	e.t.Helper()
	sec, _ := json.Marshal(map[string]any{"backends": map[string]any{
		"jev":       map[string]any{"base_url": url, "auth": "key", "token": "tok-jev-123", "provider": "typesafe"},
		"passthru":  map[string]any{"base_url": url, "auth": "passthrough"},
		"open-rlcd": map[string]any{"base_url": url, "auth": "key"},
	}})
	if err := e.cfg.SetSection("decisions", sec); err != nil {
		e.t.Fatal(err)
	}
}

// addRoutes adds OpenAI-format routes next to the default Anthropic ones.
func (e *env) addRoutes() {
	e.t.Helper()
	for _, n := range []string{"oa-fast", "oa-strong"} {
		if err := e.cfg.UpsertRoute(n, config.Route{Kind: config.KindOpenAI, BaseURL: "https://openrouter.ai/api/v1",
			Auth: config.AuthKey, APIKey: "k"}); err != nil {
			e.t.Fatal(err)
		}
	}
}

func choiceRule() Rule {
	return Rule{Name: "task", Enabled: true, Kind: KindDecision, Backend: "jev", BackendModel: "jev-latest", TimeoutMs: 1000,
		Question: &DecisionQuestion{Type: QuestionChoice, Instructions: "What kind of task is this?",
			Criteria: &Criteria{Choices: map[string]string{"code": "programming", "chat": "small talk", "other": "anything else",
				"legal": "law"}}},
		Branches: []Branch{
			{Label: "code", When: BranchWhen{Equals: "code"}, Then: Target{Route: "openrouter"}},
			{Label: "light", When: BranchWhen{In: []string{"chat", "other"}}, Then: Target{Route: "cheap", Model: "qwen/qwen3"}},
		}}
}

func scoreRule(ops ...Branch) Rule {
	return Rule{Name: "complexity", Enabled: true, Kind: KindDecision, Backend: "jev", TimeoutMs: 1000,
		Question: &DecisionQuestion{Type: QuestionScore, Instructions: "How hard?",
			Criteria: &Criteria{Legend: []string{"trivial", "simple", "moderate", "hard: deep reasoning"}}},
		Branches: ops}
}

func noulRule() Rule {
	return Rule{Name: "pii", Enabled: true, Kind: KindDecision, Backend: "jev", TimeoutMs: 1000,
		Question: &DecisionQuestion{Type: QuestionNoul, Instructions: "Sensitive personal data?"},
		Branches: []Branch{
			{When: BranchWhen{Op: ">=", Value: fp(0.7)}, Then: Target{Route: "claude-sub"}},
			{When: BranchWhen{Op: "<", Value: fp(0.7)}, Then: Target{Route: "cheap"}},
		}}
}

const catchAll = "fallback"

func withFallback(rules ...Rule) Settings {
	return Settings{Sticky: true, BackgroundBypass: true,
		Rules: append(rules, Rule{Name: catchAll, Enabled: true, Route: "claude-sub"})}
}

func chatBody(text string, extraTurns ...string) map[string]any {
	msgs := []any{map[string]any{"role": "user", "content": text}}
	for _, t := range extraTurns {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "ok"}, map[string]any{"role": "user", "content": t})
	}
	// Tools make it a main-loop turn, not a background request.
	return map[string]any{"model": "claude-opus-4-5", "max_tokens": 1000, "messages": msgs,
		"metadata": map[string]string{"user_id": session},
		"tools":    []any{map[string]any{"name": "Read", "description": "read", "input_schema": map[string]any{}}}}
}

func TestDecisionChoiceBranches(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, func(state map[string]any) string {
		switch {
		case strings.Contains(state["user_message"].(string), "python"):
			return `{"type":"choice","choice":"code","confidence":0.97}`
		case strings.Contains(state["user_message"].(string), "weather"):
			return `{"type":"choice","choice":"other","confidence":0.8}`
		}
		return `{"type":"choice","choice":"legal","confidence":0.9}`
	})
	e.useBackend(s1.srv.URL)
	e.setRules(Settings{Sticky: false, Rules: []Rule{choiceRule(), {Name: catchAll, Enabled: true, Route: "claude-sub"}}})

	d, ok := e.route(chatBody("write a python script"))
	if !ok || d.Route != "openrouter" || d.Model != "" ||
		!strings.HasPrefix(d.Reason, "decision rule 'task': choice 'code' (conf 0.97, jev ") || !strings.HasSuffix(d.Reason, "→ openrouter") {
		t.Fatalf("equals: %+v", d)
	}
	c := s1.last()
	if c.Auth != "Bearer tok-jev-123" || c.Model != "jev-latest" || c.Question["type"] != "choice" {
		t.Fatalf("call: %+v", c)
	}
	// Default inputs: the user's text, the context size and the client.
	if _, ok := c.State["context_tokens"]; !ok || c.State["client"] == nil || c.State["user_message"] != "write a python script" {
		t.Fatalf("state: %+v", c.State)
	}
	d, _ = e.route(chatBody("what's the weather"))
	if d.Route != "cheap" || d.Model != "qwen/qwen3" || !strings.HasSuffix(d.Reason, "→ cheap:qwen/qwen3") {
		t.Fatalf("in: %+v", d)
	}
	// No branch for "legal": else is the next rule.
	ev := e.r.evaluate(context.Background(), e.request(chatBody("can I sue my landlord")), evalOpts{})
	if ev.Decision.Route != "claude-sub" || ev.Trace[0].Result != ResultFallback ||
		!strings.Contains(ev.Trace[0].Reason, "no branch matched → next rule") || ev.Trace[0].Decision.Branch != -1 {
		t.Fatalf("no branch: %+v / %+v", ev.Decision, ev.Trace[0])
	}
}

func TestDecisionScoreOps(t *testing.T) {
	cases := []struct {
		score float64
		when  BranchWhen
		match bool
	}{
		{2.4, BranchWhen{Op: ">=", Value: fp(2)}, true},
		{1.4, BranchWhen{Op: ">=", Value: fp(2)}, false},
		{1, BranchWhen{Op: "<=", Value: fp(1)}, true},
		{3, BranchWhen{Op: "==", Value: fp(3)}, true},
		{2.6, BranchWhen{Op: "==", Value: fp(2)}, false},
		{1.6, BranchWhen{Op: "between", Min: fp(1), Max: fp(2)}, true},
		{0.2, BranchWhen{Op: "between", Min: fp(1), Max: fp(2)}, false},
		{7, BranchWhen{Op: "==", Value: fp(3)}, true}, // clamped to the legend
	}
	for _, c := range cases {
		dt := &DecisionTrace{}
		i := int(c.score + 0.5)
		i = min(i, 3)
		dt.Index = &i
		if got := branchMatches(QuestionScore, c.when, dt); got != c.match {
			t.Errorf("score %.1f %+v: got %v", c.score, c.when, got)
		}
	}

	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"score","score":2.2,"confidence":0.81,"probabilities":{"0":0.01,"1":0.1,"2":0.81,"3":0.08}}`))
	e.useBackend(s1.srv.URL)
	e.setRules(withFallback(scoreRule(
		Branch{When: BranchWhen{Op: ">=", Value: fp(2)}, Then: Target{Route: "openrouter"}},
		Branch{When: BranchWhen{Op: "<=", Value: fp(1)}, Then: Target{Route: "cheap"}})))
	ev := e.r.evaluate(context.Background(), e.request(chatBody("design a distributed lock")), evalOpts{})
	if ev.Decision.Route != "openrouter" || ev.Source != SourceDecision ||
		!strings.HasPrefix(ev.Decision.Reason, "decision rule 'complexity': score 2/3 'moderate' (conf 0.81, jev ") ||
		!strings.HasSuffix(ev.Decision.Reason, " ms) → openrouter") {
		t.Fatalf("score: %+v", ev.Decision)
	}
	dt := ev.Trace[0].Decision
	if dt == nil || *dt.Index != 2 || dt.Label != "moderate" || dt.Branch != 0 || dt.Outcome != OutcomeBranch ||
		dt.Backend != "jev" || dt.Provider != "typesafe" || dt.ForwardMs == nil || *dt.ForwardMs != 12.5 || dt.Route != "openrouter" {
		t.Fatalf("trace: %+v", dt)
	}
	// The whole trace serializes for the dashboard.
	b, _ := json.Marshal(ev)
	for _, want := range []string{`"kind":"decision"`, `"question_type":"score"`, `"max_index":3`, `"outcome":"branch"`, `"result":"matched"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("trace JSON lacks %s: %s", want, b)
		}
	}
}

func TestDecisionNoulAndConfidenceGate(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"noul","noul":0.83}`))
	e.useBackend(s1.srv.URL)
	rule := noulRule()
	e.setRules(withFallback(rule))
	if d, _ := e.route(chatBody("my SSN is 123-45-6789")); d.Route != "claude-sub" || !strings.Contains(d.Reason, "noul 0.83 (conf 0.83") {
		t.Fatalf("noul >=: %+v", d)
	}
	s1.set(`{"type":"noul","noul":0.1}`)
	ev := e.r.evaluate(context.Background(), e.request(chatBody("hello there")), evalOpts{IgnoreSticky: true})
	if ev.Decision.Route != "cheap" || *ev.Trace[0].Decision.Confidence != 0.9 {
		t.Fatalf("noul <: %+v %+v", ev.Decision, ev.Trace[0].Decision)
	}

	// noul confidence is max(p, 1-p): 0.55 is unsure and fails a 0.7 gate,
	// so the next rule decides.
	s1.set(`{"type":"noul","noul":0.55}`)
	rule.MinConfidence = 0.7
	e.setRules(withFallback(rule))
	ev = e.r.evaluate(context.Background(), e.request(chatBody("hello")), evalOpts{IgnoreSticky: true})
	if ev.Decision.Route != "claude-sub" || ev.Trace[0].Result != ResultFallback ||
		!strings.Contains(ev.Trace[0].Reason, "confidence 0.55 < min_confidence 0.70") {
		t.Fatalf("gate: %+v %+v", ev.Decision, ev.Trace[0])
	}

	// A choice below the gate with an explicit else target.
	s1.set(`{"type":"choice","choice":"code","confidence":0.4}`)
	cr := choiceRule()
	cr.MinConfidence = 0.6
	cr.Else = &Target{Route: "cheap"}
	e.setRules(withFallback(cr))
	ev = e.r.evaluate(context.Background(), e.request(chatBody("hello")), evalOpts{IgnoreSticky: true})
	if ev.Decision.Route != "cheap" || ev.Source != SourceDecision || ev.Trace[0].Result != ResultElse ||
		!strings.Contains(ev.Decision.Reason, "confidence 0.40 < min_confidence 0.60") || !strings.HasSuffix(ev.Decision.Reason, "→ else cheap") {
		t.Fatalf("else target: %+v %+v", ev.Decision, ev.Trace[0])
	}
	// A score without a confidence uses the probability of its index.
	s1.set(`{"type":"score","score":3,"probabilities":{"3":0.66}}`)
	sr := scoreRule(Branch{When: BranchWhen{Op: ">=", Value: fp(2)}, Then: Target{Route: "openrouter"}})
	sr.MinConfidence = 0.7
	e.setRules(withFallback(sr))
	ev = e.r.evaluate(context.Background(), e.request(chatBody("hello")), evalOpts{IgnoreSticky: true})
	if ev.Decision.Route != "claude-sub" || !strings.Contains(ev.Trace[0].Reason, "confidence 0.66 < min_confidence 0.70") {
		t.Fatalf("score gate: %+v", ev.Trace[0])
	}
}

func TestDecisionFailuresTakeElse(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	s1.delay = 2 * time.Second
	e.useBackend(s1.srv.URL)
	r := choiceRule()
	r.TimeoutMs = 100
	e.setRules(withFallback(r))
	start := time.Now()
	ev := e.r.evaluate(context.Background(), e.request(chatBody("write a python script")), evalOpts{})
	if el := time.Since(start); el > time.Second {
		t.Fatalf("timeout not honoured: %s", el)
	}
	if ev.Decision.Route != "claude-sub" || ev.Trace[0].Result != ResultFallback ||
		!strings.Contains(ev.Trace[0].Reason, "timed out after 100ms (jev ") || !strings.HasSuffix(ev.Trace[0].Reason, "→ next rule") {
		t.Fatalf("timeout: %+v %+v", ev.Decision, ev.Trace[0])
	}

	// An unknown label, an unreachable backend and a missing backend.
	s1.mu.Lock()
	s1.delay = 0
	s1.mu.Unlock()
	s1.set(`{"type":"choice","choice":"poetry","confidence":0.9}`)
	r.TimeoutMs, r.Else = 1000, &Target{Route: "cheap"}
	e.setRules(withFallback(r))
	ev = e.r.evaluate(context.Background(), e.request(chatBody("x")), evalOpts{})
	if ev.Decision.Route != "cheap" || !strings.Contains(ev.Decision.Reason, `unknown label "poetry"`) {
		t.Fatalf("unknown label: %+v", ev.Decision)
	}
	s1.srv.Close()
	ev = e.r.evaluate(context.Background(), e.request(chatBody("x")), evalOpts{})
	if ev.Decision.Route != "cheap" || ev.Trace[0].Result != ResultElse {
		t.Fatalf("backend down: %+v", ev.Trace[0])
	}
	if err := e.cfg.SetSection("decisions", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	ev = e.r.evaluate(context.Background(), e.request(chatBody("x")), evalOpts{})
	if ev.Decision.Route != "cheap" || !strings.Contains(ev.Decision.Reason, `decisions backend "jev" does not exist`) {
		t.Fatalf("no backend: %+v", ev.Decision)
	}
}

func TestDecisionEvaluateModes(t *testing.T) {
	answer := `{"type":"choice","choice":"code","confidence":0.9}`
	setup := func(t *testing.T, mode string, over int) (*env, *systemOne) {
		e := newEnv(t)
		s1 := newSystemOne(t, func(map[string]any) string { return answer })
		e.useBackend(s1.srv.URL)
		r := choiceRule()
		r.Evaluate, r.ContextOverTokens = mode, over
		e.setRules(withFallback(r))
		return e, s1
	}
	t.Run("conversation_start pins", func(t *testing.T) {
		e, s1 := setup(t, "", 0)
		if d, _ := e.route(chatBody("write a python script")); d.Route != "openrouter" {
			t.Fatal(d)
		}
		s1.set(`{"type":"choice","choice":"chat","confidence":0.9}`)
		d, _ := e.route(chatBody("write a python script", "now just chat"))
		if d.Route != "openrouter" || s1.n() != 1 || !strings.HasPrefix(d.Reason, "sticky: conversation started on 'openrouter' (rule 'task')") {
			t.Fatalf("turn 2: %+v (calls %d)", d, s1.n())
		}
		ev := e.r.evaluate(context.Background(), e.request(chatBody("write a python script", "x")), evalOpts{})
		if ev.Trace[0].Result != ResultSkipped || !strings.Contains(ev.Trace[0].Reason, "runs at conversation start") {
			t.Fatalf("trace: %+v", ev.Trace[0])
		}
	})
	t.Run("pinned model is reused", func(t *testing.T) {
		e, s1 := setup(t, "", 0)
		s1.set(`{"type":"choice","choice":"chat","confidence":0.9}`)
		if d, _ := e.route(chatBody("hi")); d.Route != "cheap" || d.Model != "qwen/qwen3" {
			t.Fatal(d)
		}
		d, _ := e.route(chatBody("hi", "again"))
		if d.Route != "cheap" || d.Model != "qwen/qwen3" || s1.n() != 1 {
			t.Fatalf("sticky model: %+v", d)
		}
	})
	t.Run("every_request", func(t *testing.T) {
		e, s1 := setup(t, EvalEveryRequest, 0)
		if d, _ := e.route(chatBody("write a python script")); d.Route != "openrouter" {
			t.Fatal(d)
		}
		s1.set(`{"type":"choice","choice":"chat","confidence":0.9}`)
		d, _ := e.route(chatBody("write a python script", "thanks!"))
		if d.Route != "cheap" || s1.n() != 2 || !strings.HasPrefix(d.Reason, "decision rule 'task' re-decided:") {
			t.Fatalf("turn 2: %+v", d)
		}
		pins := e.r.sticky.list(time.Hour)
		if len(pins) != 1 || pins[0].Route != "cheap" || pins[0].Model != "qwen/qwen3" {
			t.Fatalf("pin: %+v", pins)
		}
	})
	t.Run("when_context_over", func(t *testing.T) {
		e, s1 := setup(t, EvalWhenContextOver, 1000)
		if d, _ := e.route(chatBody("write a python script")); d.Route != "openrouter" {
			t.Fatal(d)
		}
		s1.set(`{"type":"choice","choice":"chat","confidence":0.9}`)
		// Still small: the pin serves, no call.
		if d, _ := e.route(chatBody("write a python script", "more")); d.Route != "openrouter" || s1.n() != 1 {
			t.Fatalf("small: %+v", d)
		}
		big := strings.Repeat("lorem ipsum dolor sit amet ", 600)
		d, _ := e.route(chatBody("write a python script", big))
		if d.Route != "cheap" || s1.n() != 2 || !strings.Contains(d.Reason, "re-decided") {
			t.Fatalf("past the threshold: %+v (calls %d)", d, s1.n())
		}
		// Once only: bigger still, the new pin serves.
		s1.set(`{"type":"choice","choice":"code","confidence":0.9}`)
		d, _ = e.route(chatBody("write a python script", big, big))
		if d.Route != "cheap" || s1.n() != 2 || !strings.HasPrefix(d.Reason, "sticky:") {
			t.Fatalf("after: %+v (calls %d)", d, s1.n())
		}
		ev := e.r.evaluate(context.Background(), e.request(chatBody("write a python script", big, big)), evalOpts{})
		if !strings.Contains(ev.Trace[0].Reason, "already re-decided once past 1000 tokens") {
			t.Fatalf("trace: %+v", ev.Trace[0])
		}
	})
	t.Run("when_context_over failure keeps the pin and does not retry", func(t *testing.T) {
		e, s1 := setup(t, EvalWhenContextOver, 1000)
		e.route(chatBody("write a python script"))
		s1.set(`{"type":"choice","choice":"nonsense","confidence":0.9}`)
		big := strings.Repeat("lorem ipsum dolor sit amet ", 600)
		if d, _ := e.route(chatBody("write a python script", big)); d.Route != "openrouter" || s1.n() != 2 {
			t.Fatalf("failed re-decision: %+v", d)
		}
		if d, _ := e.route(chatBody("write a python script", big, big)); d.Route != "openrouter" || s1.n() != 2 {
			t.Fatalf("retried: %+v (calls %d)", d, s1.n())
		}
	})
}

func TestDecisionBackgroundBypass(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	e.useBackend(s1.srv.URL)
	e.setRules(withFallback(choiceRule()))
	e.route(chatBody("write a python script"))
	ev := e.r.evaluate(context.Background(), e.request(titleRequest()), evalOpts{})
	if s1.n() != 1 || ev.Trace[0].Result != ResultSkipped || ev.Trace[0].Reason != "decision rules do not run for background requests" ||
		ev.Decision.Route != "claude-sub" {
		t.Fatalf("background: %+v (calls %d)", ev.Trace[0], s1.n())
	}
}

func TestDecisionDryRunDoesNotPin(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	e.useBackend(s1.srv.URL)
	e.setRules(withFallback(choiceRule()))
	b, _ := json.Marshal(chatBody("write a python script"))
	id := "20260923T000000-00000d"
	if err := e.st.Save(&store.Detail{Record: store.Record{ID: id, ConversationID: pipeline.ConversationID(nil, b)},
		RequestHeaders: map[string]string{"Content-Type": "application/json"}, RequestBody: string(b)}); err != nil {
		t.Fatal(err)
	}
	var sources []string
	ctx := selector.WithTrace(context.Background(), selector.Trace{Observe: func(c selector.Call) {
		sources = append(sources, c.Source+"/"+c.BackendName)
	}})
	dry, err := e.r.DryRun(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.EffectiveRoute != "openrouter" || s1.n() != 1 || !dry.Trace[0].Decision.DryRun ||
		!strings.HasPrefix(dry.Decision.Reason, "decision rule 'task': [dry run] choice 'code'") {
		t.Fatalf("dry run: %+v %+v", dry.Evaluation, dry.Trace[0].Decision)
	}
	if n := len(e.r.sticky.list(time.Hour)); n != 0 {
		t.Fatalf("a dry run pinned: %d", n)
	}
	if strings.Join(sources, ",") != "router-dryrun/jev" {
		t.Fatalf("observed: %v", sources)
	}
}

func TestDecisionProtocols(t *testing.T) {
	e := newEnv(t)
	e.addRoutes()
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	e.useBackend(s1.srv.URL)
	mixed := choiceRule()
	mixed.Branches[1].Then = Target{Route: "oa-fast"}
	s := withFallback(mixed)
	s.TTLHours = 1
	err := s.validate(e.cfg.Get())
	if err == nil || !strings.Contains(err.Error(), "An Anthropic-format request can't go to an OpenAI-format route") ||
		!strings.Contains(err.Error(), `route "oa-fast"`) {
		t.Fatalf("mixed targets: %v", err)
	}
	onlyOA := choiceRule()
	onlyOA.When.Protocol = ir.ProtocolAnthropic
	onlyOA.Branches[0].Then, onlyOA.Branches[1].Then = Target{Route: "oa-strong"}, Target{Route: "oa-fast"}
	if err := withFallback(onlyOA).validate(e.cfg.Get()); err == nil || !strings.Contains(err.Error(), "only matches anthropic-messages requests") {
		t.Fatalf("protocol condition: %v", err)
	}
	// OpenAI targets: an Anthropic request skips the rule without a call,
	// an OpenAI request is decided.
	onlyOA.When.Protocol = ""
	e.setRules(Settings{Sticky: true, Rules: []Rule{onlyOA}})
	ev := e.r.evaluate(context.Background(), e.request(chatBody("write a python script")), evalOpts{})
	if s1.n() != 0 || ev.Trace[0].Result != ResultSkipped || !strings.Contains(ev.Trace[0].Reason, "none of this rule's targets speaks anthropic-messages") {
		t.Fatalf("anthropic request: %+v", ev.Trace[0])
	}
	oa := e.requestAs(ir.ProtocolOpenAIChat, map[string]any{"model": "gpt", "messages": []any{
		map[string]any{"role": "user", "content": "write a python script"}}})
	oa.ConversationID = "cx-1"
	d, ok := e.r.Route(context.Background(), oa)
	if !ok || d.Route != "oa-strong" || s1.last().State["user_message"] != "write a python script" {
		t.Fatalf("openai request: %+v", d)
	}
}

func TestDecisionValidation(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{}`))
	e.useBackend(s1.srv.URL)
	bad := func(mut func(r *Rule), want string) {
		t.Helper()
		r := choiceRule()
		mut(&r)
		err := withFallback(r).validate(e.cfg.Get())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	bad(func(r *Rule) { r.Question = nil }, "needs a question")
	bad(func(r *Rule) { r.Question.Type = "vote" }, "question type must be")
	bad(func(r *Rule) { r.Question.Criteria = &Criteria{Legend: []string{"a", "b"}} }, "a choice question needs criteria")
	bad(func(r *Rule) { r.Branches[0].When = BranchWhen{Equals: "poetry"} }, `label "poetry" is not one of`)
	bad(func(r *Rule) { r.Branches[0].When = BranchWhen{Op: ">=", Value: fp(1)} }, "a choice branch uses equals or in")
	bad(func(r *Rule) { r.Branches = nil }, "at least one branch")
	bad(func(r *Rule) { r.Branches[0].Then = Target{Route: "nope"} }, `unknown route "nope"`)
	bad(func(r *Rule) { r.Branches[0].Then = Target{Alias: "nope"} }, `unknown alias "nope"`)
	bad(func(r *Rule) { r.Branches[0].Then = Target{NextRule: true} }, "next_rule is only valid as the else")
	bad(func(r *Rule) { r.Else = &Target{NextRule: true, Route: "cheap"} }, "either next_rule or a target")
	bad(func(r *Rule) { r.Backend = "passthru" }, "forwards the client's own credentials")
	bad(func(r *Rule) { r.Backend = "ghost" }, `decisions backend "ghost" does not exist`)
	bad(func(r *Rule) { r.Inputs = &DecisionInputs{Facts: []string{"password"}} }, `unknown input "password"`)
	bad(func(r *Rule) { r.Inputs = &DecisionInputs{Headers: []string{"Authorization"}} }, "never sent to a decision model")
	bad(func(r *Rule) { r.Evaluate = EvalWhenContextOver }, "needs context_over_tokens > 0")
	bad(func(r *Rule) { r.ContextOverTokens = 5 }, "only applies to evaluate")
	bad(func(r *Rule) { r.Evaluate = "sometimes" }, "evaluate must be")
	bad(func(r *Rule) { r.OverrideSticky = true }, "not override_sticky")
	bad(func(r *Rule) { r.MinConfidence = 2 }, "min_confidence")
	bad(func(r *Rule) { r.Kind = KindMatch; r.Route = "cheap" }, "only apply to decision rules")
	badScore := func(w BranchWhen, want string) {
		t.Helper()
		err := withFallback(scoreRule(Branch{When: w, Then: Target{Route: "cheap"}})).validate(e.cfg.Get())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	badScore(BranchWhen{Op: ">=", Value: fp(4)}, "legend index from 0 to 3")
	badScore(BranchWhen{Op: ">=", Value: fp(1.5)}, "legend index from 0 to 3")
	badScore(BranchWhen{Op: "between", Min: fp(2), Max: fp(1)}, "between needs min ≤ max")
	badScore(BranchWhen{Op: ">"}, "op must be")
	nr := noulRule()
	nr.Branches[0].When.Value = fp(1.2)
	if err := withFallback(nr).validate(e.cfg.Get()); err == nil || !strings.Contains(err.Error(), "probability between 0 and 1") {
		t.Errorf("noul value: %v", err)
	}
	nr = noulRule()
	nr.Branches[0].When.Op = "<="
	if err := withFallback(nr).validate(e.cfg.Get()); err == nil || !strings.Contains(err.Error(), `op must be ">=" or "<"`) {
		t.Errorf("noul op: %v", err)
	}
	// Aliases are valid targets, and a route used by a decision rule cannot
	// be deleted.
	ok := choiceRule()
	ok.Branches[1].Then = Target{Alias: "fast"}
	s := withFallback(ok)
	s.Aliases = []Alias{{Name: "fast", Route: "cheap", Model: "qwen/qwen3"}}
	e.setRules(s)
	if used := rulesUsing(e.r.current(), "openrouter"); !contains(used, "task") {
		t.Errorf("used by: %v", used)
	}
}

func TestDecisionAliasTargetAndInputs(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"chat","confidence":0.9}`))
	e.useBackend(s1.srv.URL)
	r := choiceRule()
	r.Branches[1].Then = Target{Alias: "fast"}
	r.Inputs = &DecisionInputs{Facts: []string{InputGoal, InputRecentToolCalls, InputHasTools, InputProtocol, InputModelRequested},
		GoalTurns: 2, Headers: []string{"X-Team"}}
	s := withFallback(r)
	s.Aliases = []Alias{{Name: "fast", Route: "cheap", Model: "qwen/qwen3"}}
	e.setRules(s)
	body := mainTurn(3)
	req := e.request(body, "X-Team", "payments", "Authorization", "Bearer secret")
	d, _ := e.r.Route(context.Background(), req)
	if d.Route != "cheap" || d.Model != "qwen/qwen3" || !strings.HasSuffix(d.Reason, "→ alias 'fast'") {
		t.Fatalf("alias: %+v", d)
	}
	st := s1.last().State
	if st["conversation_goal"] != "refactor the auth module" || !strings.Contains(st["recent_tool_calls"].(string), "Read") ||
		st["has_tools"] != true || st["protocol"] != ir.ProtocolAnthropic || st["model_requested"] != "claude-opus-4-5" {
		t.Fatalf("state: %+v", st)
	}
	hs, _ := st["headers"].(map[string]any)
	if hs["x-team"] != "payments" || len(hs) != 1 {
		t.Fatalf("headers: %+v", st["headers"])
	}
	if _, ok := st["user_message"]; ok {
		t.Fatalf("latest_user_text was not asked for: %+v", st)
	}
}

func TestDecisionEconomyBackend(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	e.useSelector(s1.srv.URL)
	r := choiceRule()
	r.Backend, r.BackendModel = "", ""
	e.setRules(withFallback(r))
	d, _ := e.route(chatBody("write a python script"))
	if d.Route != "openrouter" || !strings.Contains(d.Reason, "economy") || s1.last().Model != "Open-RLCD-text" || s1.last().Auth != "" {
		t.Fatalf("economy: %+v %+v", d, s1.last())
	}
}

func TestCriteriaJSON(t *testing.T) {
	var q DecisionQuestion
	if err := json.Unmarshal([]byte(`{"type":"score","instructions":"x","criteria":["a","b"]}`), &q); err != nil || len(q.Criteria.Legend) != 2 {
		t.Fatalf("legend: %v %+v", err, q)
	}
	b, _ := json.Marshal(q)
	if !strings.Contains(string(b), `"criteria":["a","b"]`) {
		t.Fatal(string(b))
	}
	q = DecisionQuestion{}
	if err := json.Unmarshal([]byte(`{"type":"choice","instructions":"x","criteria":{"a":"A"}}`), &q); err != nil || q.Criteria.Choices["a"] != "A" {
		t.Fatalf("choices: %v", err)
	}
	b, _ = json.Marshal(DecisionQuestion{Type: "noul", Instructions: "x"})
	if strings.Contains(string(b), "criteria") {
		t.Fatal(string(b))
	}
	if err := json.Unmarshal([]byte(`{"criteria":"x"}`), &q); err == nil {
		t.Fatal("a string criteria must be refused")
	}
}
