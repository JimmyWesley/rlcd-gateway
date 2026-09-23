package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/resilience"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/router"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// fake is a scripted provider: handle answers the n-th call (from 1).
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	handle func(n int, body string, w http.ResponseWriter)
}

func newFake(t *testing.T, handle func(n int, body string, w http.ResponseWriter)) *fake {
	f := &fake{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		n := len(f.bodies)
		f.mu.Unlock()
		f.handle(n, string(b), w)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func reply(w http.ResponseWriter, status int, body string, h ...string) {
	for i := 0; i+1 < len(h); i += 2 {
		w.Header().Set(h[i], h[i+1])
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// resEnv is a gateway with the resilience engine's clock and randomness
// pinned: backoffs are recorded, not slept.
type resEnv struct {
	*env
	mu    sync.Mutex
	slept []time.Duration
}

func newResEnv(t *testing.T) *resEnv {
	e := &resEnv{env: newEnv(t, false)}
	e.gw.Resilience.Rand = func() float64 { return 0.5 }
	e.gw.Resilience.Sleep = func(ctx context.Context, d time.Duration) error {
		e.mu.Lock()
		e.slept = append(e.slept, d)
		e.mu.Unlock()
		return ctx.Err()
	}
	return e
}

func (e *resEnv) route(name string, f *fake, provider, model string) {
	e.t.Helper()
	must(e.t, e.cs.UpsertRoute(name, config.Route{Kind: config.KindOpenAI, BaseURL: f.srv.URL + "/api/v1", Auth: config.AuthKey,
		APIKey: "sk-or-test-0000", Provider: provider, Model: model}))
}

func (e *resEnv) section(name string, v any) {
	e.t.Helper()
	b, _ := json.Marshal(v)
	must(e.t, e.cs.SetSection(name, b))
}

// putResilience goes through the settings API, validation included.
func (e *resEnv) putResilience(v any) {
	e.t.Helper()
	b, _ := json.Marshal(v)
	resp, out := e.do("PUT", "/api/resilience/settings", string(b), nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("PUT /api/resilience/settings: %d %s", resp.StatusCode, out)
	}
}

// record waits for the n-th model-call record (System One calls made by
// the pipeline are skipped) and loads it.
func (e *resEnv) record(n int) *store.Detail {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var recs []store.Record
		for _, r := range e.st.Recent() {
			if r.Protocol != "systemone" {
				recs = append(recs, r)
			}
		}
		if len(recs) >= n {
			// Recent is newest first.
			d, err := e.st.Get(recs[len(recs)-n].ID)
			must(e.t, err)
			return d
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("record %d never saved (%d so far)", n, len(recs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	okChat   = `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":20,"completion_tokens":2}}`
	smartReq = `{"model":"smart","messages":[{"role":"user","content":"Say hi in three words."}]}`
	qwen     = "qwen/qwen3-235b-a22b-2507"
)

func maxTokensOf(t *testing.T, body string) (any, any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m["max_tokens"], m["max_completion_tokens"]
}

// The incident, prevented: a chat app sends no max_tokens to an alias on
// OpenRouter, whose provider would default it to the whole window. The
// guard sets one from OpenRouter's model list before the call.
func TestGuardPreventsTheGMICloudIncident(t *testing.T) {
	e := newResEnv(t)
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		if !strings.Contains(body, `"max_tokens"`) {
			reply(w, 400, gmiOverflow(193))
			return
		}
		reply(w, 200, okChat)
	})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"`+qwen+`","context_length":262144,"top_provider":{"context_length":262144,"max_completion_tokens":235929}}]}`)
	}))
	defer catalog.Close()
	e.gw.Resilience.Registry.SetURL(catalog.URL)
	must(t, e.gw.Resilience.Registry.Refresh(context.Background()))
	e.route("openrouter-oa", up, "openrouter", "")
	e.section("router", map[string]any{"sticky": true, "rules": []any{},
		"aliases": []map[string]string{{"name": "smart", "route": "openrouter-oa", "model": qwen}}})

	resp, out := e.do("POST", "/v1/chat/completions", smartReq, map[string]string{"User-Agent": "OpenAI/Python 1.99.0"})
	if resp.StatusCode != 200 || out != okChat {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	calls := up.calls()
	if len(calls) != 1 {
		t.Fatalf("%d upstream calls", len(calls))
	}
	if mt, _ := maxTokensOf(t, calls[0]); mt != float64(4096) {
		t.Fatalf("max_tokens sent: %v", mt)
	}
	d := e.record(1)
	g := d.MaxTokensGuard
	if g == nil || g.Reason != resilience.ReasonFilled || g.From != nil || g.To != 4096 || g.LimitSource != resilience.SourceOpenRouter ||
		g.ContextWindow != 262144 {
		t.Fatalf("guard record: %+v", g)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Changes == nil || d.Attempts[0].Changes.MaxTokens.To != 4096 || d.Retried {
		t.Fatalf("attempts: %+v", d.Attempts)
	}
	if !strings.Contains(d.SentBody, `"max_tokens":4096`) {
		t.Fatalf("sent body not logged: %s", d.SentBody)
	}
}

func gmiOverflow(input int) string {
	inner := fmt.Sprintf(`{"error":{"message":"Requested token count exceeds the model's maximum context length of 131072 tokens. You requested a total of %d tokens: %d tokens from the input messages and 131072 tokens for the completion. Please reduce the number of tokens in the input messages or the completion to fit within the limit.","type":"BadRequest"},"request_id":"req-1"}`, input+131072, input)
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "Backend request failed with status 400",
		"type": "backend_error", "code": 400, "details": inner}})
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "Provider returned error", "code": 400,
		"metadata": map[string]any{"raw": string(raw), "provider_name": "GMICloud", "is_byok": false, "provider_error_code": "400"}},
		"user_id": "user_x"})
	return string(b)
}

// With the guard off, the incident happens, and recovery clamps max_tokens
// with the numbers from the provider's nested message, then retries.
func TestRecoveryClampsOutputLimit(t *testing.T) {
	e := newResEnv(t)
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		mt, _ := maxTokensOf(t, body)
		if v, ok := mt.(float64); !ok || v > 131072-193 {
			reply(w, 400, gmiOverflow(193))
			return
		}
		reply(w, 200, okChat)
	})
	e.route("openrouter-oa", up, "openrouter", qwen)
	e.section("router", map[string]any{"aliases": []map[string]string{{"name": "smart", "route": "openrouter-oa"}}})
	e.putResilience(map[string]any{"max_tokens_guard": map[string]any{"enabled": false}})

	resp, out := e.do("POST", "/v1/chat/completions", smartReq, nil)
	if resp.StatusCode != 200 || out != okChat || resp.Header.Get("X-Rlcd-Attempts") != "2" {
		t.Fatalf("%d %s attempts=%q", resp.StatusCode, out, resp.Header.Get("X-Rlcd-Attempts"))
	}
	calls := up.calls()
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	want := 131072 - 193 - 1 - 16
	if mt, _ := maxTokensOf(t, calls[1]); mt != float64(want) {
		t.Fatalf("retry max_tokens %v, want %d", mt, want)
	}
	d := e.record(1)
	if d.Status != 200 || !d.Retried || !d.Recovered || d.ErrorClass != "" || len(d.Attempts) != 2 {
		t.Fatalf("record: %+v", d.Record)
	}
	a1, a2 := d.Attempts[0], d.Attempts[1]
	if a1.N != 1 || a1.Status != 400 || a1.Class != resilience.ClassOutputTooLarge || a1.Action != resilience.ActionClamp ||
		a1.UpstreamProvider != "GMICloud" || !strings.HasPrefix(a1.Message, "Requested token count exceeds") || a1.Model != qwen ||
		a1.Route != "openrouter-oa" || a1.Provider != "openrouter" {
		t.Fatalf("attempt 1: %+v", a1)
	}
	if a2.N != 2 || a2.Status != 200 || a2.Class != "" || a2.Changes == nil || a2.Changes.MaxTokens == nil ||
		a2.Changes.MaxTokens.From != nil || a2.Changes.MaxTokens.To != want || a2.Changes.MaxTokens.Reason != resilience.ReasonProviderError {
		t.Fatalf("attempt 2: %+v %+v", a2, a2.Changes)
	}
	// The limits were learned: the next request is clamped before it is sent.
	e.putResilience(map[string]any{"max_tokens_guard": map[string]any{"enabled": true}})
	big := `{"model":"smart","max_tokens":200000,"messages":[{"role":"user","content":"hi"}]}`
	if resp, _ := e.do("POST", "/v1/chat/completions", big, nil); resp.StatusCode != 200 {
		t.Fatalf("second: %d", resp.StatusCode)
	}
	if calls := up.calls(); len(calls) != 3 {
		t.Fatalf("learned limits not used: %d calls", len(calls))
	}
	if g := e.record(2).MaxTokensGuard; g == nil || g.LimitSource != resilience.SourceLearned || g.To > 131072 {
		t.Fatalf("guard: %+v", g)
	}
}

// OpenRouter provider error: retried once without that provider, then a
// rate limit is waited out (Retry-After honoured), then success.
func TestRecoveryIgnoresProviderAndBacksOff(t *testing.T) {
	e := newResEnv(t)
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		switch n {
		case 1:
			reply(w, 502, `{"error":{"message":"Provider returned error","code":502,"metadata":{"raw":"upstream connect error","provider_name":"Flaky"}}}`)
		case 2:
			reply(w, 429, `{"error":{"message":"Rate limit exceeded: free-models-per-min","code":429}}`, "Retry-After", "2")
		default:
			reply(w, 200, okChat)
		}
	})
	e.route("or", up, "openrouter", "some/model")
	e.section("router", map[string]any{"aliases": []map[string]string{{"name": "smart", "route": "or"}}})

	req := `{"model":"smart","max_tokens":50,"provider":{"sort":"price"},"messages":[{"role":"user","content":"hi"}]}`
	resp, _ := e.do("POST", "/v1/chat/completions", req, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	calls := up.calls()
	if len(calls) != 3 {
		t.Fatalf("%d calls", len(calls))
	}
	for i, c := range calls[1:] {
		var m struct {
			Provider map[string]any `json:"provider"`
		}
		_ = json.Unmarshal([]byte(c), &m)
		ig, _ := m.Provider["ignore"].([]any)
		if len(ig) != 1 || ig[0] != "Flaky" || m.Provider["sort"] != "price" {
			t.Fatalf("call %d provider: %v", i+2, m.Provider)
		}
	}
	if strings.Contains(calls[0], "ignore") {
		t.Fatal("first call already ignored a provider")
	}
	e.mu.Lock()
	slept := append([]time.Duration(nil), e.slept...)
	e.mu.Unlock()
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("backoff: %v", slept)
	}
	d := e.record(1)
	acts := []string{}
	for _, a := range d.Attempts {
		acts = append(acts, a.Class+"/"+a.Action)
	}
	if strings.Join(acts, ",") != "provider_error/ignore_provider,rate_limited/backoff,/" || d.Attempts[1].WaitMs != 2000 ||
		d.Attempts[0].UpstreamProvider != "Flaky" || len(d.Attempts[1].Changes.IgnoredProviders) != 1 || !d.Recovered {
		t.Fatalf("attempts: %s %+v", acts, d.Attempts)
	}
}

// The route points at a model that does not exist, the first fallback is
// down too, the second serves. The conversation stays pinned to the
// primary, and the next turn tries it again.
func TestFallbackChainKeepsStickiness(t *testing.T) {
	e := newResEnv(t)
	var primaryUp sync.Mutex
	primaryOK := false
	a := newFake(t, func(n int, body string, w http.ResponseWriter) {
		primaryUp.Lock()
		ok := primaryOK
		primaryUp.Unlock()
		if ok {
			reply(w, 200, okChat)
			return
		}
		reply(w, 400, `{"error":{"message":"qwen/does-not-exist is not a valid model ID","code":400},"user_id":"u"}`)
	})
	b := newFake(t, func(n int, body string, w http.ResponseWriter) {
		reply(w, 500, `{"error":{"message":"Internal Server Error","code":500}}`)
	})
	c := newFake(t, func(n int, body string, w http.ResponseWriter) { reply(w, 200, okChat) })
	e.route("primary", a, "", "qwen/does-not-exist")
	e.route("second", b, "", "second-model")
	e.route("third", c, "", "")
	e.section("router", map[string]any{"sticky": true, "ttl_hours": 72,
		"rules": []map[string]any{{"name": "all", "enabled": true, "route": "primary", "when": map[string]any{"protocol": "openai"}}}})
	e.putResilience(map[string]any{"backoff": map[string]any{"retries": 0},
		"routes": map[string]any{"primary": map[string]any{"fallbacks": []string{"second", "third:third-model"}}}})
	// A fallback in another protocol is refused by validation.
	if resp, _ := e.do("PUT", "/api/resilience/settings", `{"routes":{"primary":{"fallbacks":["claude-sub"]}}}`, nil); resp.StatusCode != 400 {
		t.Fatalf("cross-protocol fallback accepted: %d", resp.StatusCode)
	}

	h := map[string]string{"Conversation-Id": "conv-1"}
	body := `{"model":"whatever","max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`
	resp, out := e.do("POST", "/v1/chat/completions", body, h)
	if resp.StatusCode != 200 || out != okChat || resp.Header.Get("X-Rlcd-Route") != "third" {
		t.Fatalf("%d %s route=%s", resp.StatusCode, out, resp.Header.Get("X-Rlcd-Route"))
	}
	if len(a.calls()) != 1 || len(b.calls()) != 1 || len(c.calls()) != 1 {
		t.Fatalf("calls: %d %d %d", len(a.calls()), len(b.calls()), len(c.calls()))
	}
	if !strings.Contains(c.calls()[0], `"model":"third-model"`) || !strings.Contains(b.calls()[0], `"model":"second-model"`) {
		t.Fatalf("fallback models: %s / %s", b.calls()[0], c.calls()[0])
	}
	d := e.record(1)
	if d.Route != "third" || d.PrimaryRoute != "primary" || d.FallbackRoute != "third" || !d.Recovered || d.Model != "third-model" {
		t.Fatalf("record: %+v", d.Record)
	}
	acts := []string{}
	for _, at := range d.Attempts {
		acts = append(acts, at.Route+":"+at.Class+"/"+at.Action)
	}
	if strings.Join(acts, ",") != "primary:model_unavailable/fallback,second:provider_error/fallback,third:/" {
		t.Fatalf("attempts: %v", acts)
	}

	// The pin is still the primary.
	_, list := e.do("GET", "/api/router/conversations", "", nil)
	var pins []router.Assignment
	_ = json.Unmarshal([]byte(list), &pins)
	if len(pins) != 1 || pins[0].Route != "primary" {
		t.Fatalf("pins: %s", list)
	}
	// Next turn: the primary is back and serves it.
	primaryUp.Lock()
	primaryOK = true
	primaryUp.Unlock()
	body2 := `{"model":"whatever","max_tokens":20,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`
	if resp, _ := e.do("POST", "/v1/chat/completions", body2, h); resp.StatusCode != 200 || resp.Header.Get("X-Rlcd-Route") != "primary" {
		t.Fatalf("second turn: %d %s", resp.StatusCode, resp.Header.Get("X-Rlcd-Route"))
	}
	if d := e.record(2); d.Route != "primary" || d.FallbackRoute != "" || d.Retried {
		t.Fatalf("second record: %+v", d.Record)
	}
}

const (
	anthStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n"
	anthDelta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	anthOver  = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
)

func sse(w http.ResponseWriter, parts ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	for _, p := range parts {
		_, _ = io.WriteString(w, p)
		w.(http.Flusher).Flush()
		time.Sleep(5 * time.Millisecond)
	}
}

// A stream that fails before any content is retried; one that fails after
// content reached the client never is.
func TestStreamErrorsBeforeAndAfterTheFirstByte(t *testing.T) {
	e := newResEnv(t)
	var mode sync.Mutex
	late := false
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		mode.Lock()
		l := late
		mode.Unlock()
		switch {
		case l:
			sse(w, anthStart, anthDelta, anthOver)
		case n == 1:
			sse(w, anthStart, anthOver)
		default:
			sse(w, anthStart, anthDelta)
		}
	})
	must(t, e.cs.UpsertRoute("claude-sub", config.Route{Kind: config.KindAnthropic, BaseURL: up.srv.URL, Auth: config.AuthPassthrough}))
	h := map[string]string{"X-Api-Key": "sk-ant-api03-client", "Anthropic-Version": "2023-06-01"}

	resp, out := e.do("POST", "/v1/messages", anthReq, h)
	if resp.StatusCode != 200 || out != anthStart+anthDelta {
		t.Fatalf("early error: the client must only see the retry: %q", out)
	}
	d := e.record(1)
	if len(up.calls()) != 2 || len(d.Attempts) != 2 || d.Attempts[0].Class != resilience.ClassOverloaded ||
		d.Attempts[0].Action != resilience.ActionBackoff || !d.Recovered {
		t.Fatalf("early: calls %d, attempts %+v", len(up.calls()), d.Attempts)
	}

	mode.Lock()
	late = true
	mode.Unlock()
	resp, out = e.do("POST", "/v1/messages", anthReq, h)
	if resp.StatusCode != 200 || out != anthStart+anthDelta+anthOver {
		t.Fatalf("late error must reach the client as it came: %q", out)
	}
	if n := len(up.calls()); n != 3 {
		t.Fatalf("a stream that already sent content was retried: %d calls", n)
	}
	if d := e.record(2); d.Retried || len(d.Attempts) > 1 {
		t.Fatalf("late: %+v", d.Attempts)
	}
}

// Recovery stops at the time budget: a Retry-After beyond it is not
// waited for, and the provider's own error reaches the client untouched.
func TestTimeBudget(t *testing.T) {
	e := newResEnv(t)
	rate := `{"error":{"message":"Rate limit exceeded","code":429}}`
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		if strings.Contains(body, "slow") {
			time.Sleep(250 * time.Millisecond)
			reply(w, 503, `{"error":{"message":"busy","code":503}}`)
			return
		}
		reply(w, 429, rate, "Retry-After", "30")
	})
	e.route("or", up, "", "m")
	e.section("router", map[string]any{"aliases": []map[string]string{{"name": "smart", "route": "or"}}})
	e.putResilience(map[string]any{"time_budget_ms": 200})

	start := time.Now()
	resp, out := e.do("POST", "/v1/chat/completions", smartReq, nil)
	if resp.StatusCode != 429 || out != rate || time.Since(start) > 2*time.Second {
		t.Fatalf("%d %s after %v", resp.StatusCode, out, time.Since(start))
	}
	d := e.record(1)
	if len(d.Attempts) != 1 || d.Attempts[0].Action != resilience.ActionGiveUp || !strings.Contains(d.Attempts[0].ActionDetail, "time budget") ||
		d.ErrorClass != resilience.ClassRateLimited || d.Status != 429 {
		t.Fatalf("attempts: %+v", d.Attempts)
	}
	e.mu.Lock()
	n := len(e.slept)
	e.mu.Unlock()
	if n != 0 {
		t.Fatal("waited beyond the budget")
	}
	// A slow failure spends the budget by itself: no retry.
	slow := `{"model":"smart","messages":[{"role":"user","content":"slow"}]}`
	if resp, _ := e.do("POST", "/v1/chat/completions", slow, nil); resp.StatusCode != 503 {
		t.Fatalf("slow: %d", resp.StatusCode)
	}
	if d := e.record(2); len(d.Attempts) != 1 || !strings.Contains(d.Attempts[0].ActionDetail, "time budget") {
		t.Fatalf("slow attempts: %+v", d.Attempts)
	}
}

// Auth failures pass through untouched, never retried.
func TestAuthErrorsPassThrough(t *testing.T) {
	e := newResEnv(t)
	denied := `{"error":{"message":"No auth credentials found","code":401}}`
	up := newFake(t, func(n int, body string, w http.ResponseWriter) { reply(w, 401, denied, "X-Custom", "1") })
	e.route("or", up, "openrouter", "m")
	e.section("router", map[string]any{"aliases": []map[string]string{{"name": "smart", "route": "or"}}})
	e.putResilience(map[string]any{"routes": map[string]any{"or": map[string]any{"fallbacks": []string{"groq"}}}})
	resp, out := e.do("POST", "/v1/chat/completions", smartReq, nil)
	if resp.StatusCode != 401 || out != denied || resp.Header.Get("X-Custom") != "1" || len(up.calls()) != 1 {
		t.Fatalf("%d %s calls=%d", resp.StatusCode, out, len(up.calls()))
	}
	if d := e.record(1); len(d.Attempts) != 1 || d.Attempts[0].Action != resilience.ActionNone || d.ErrorClass != resilience.ClassAuth {
		t.Fatalf("%+v", d.Attempts)
	}
	if n := e.anth.count() + e.oai.count(); n != 0 {
		t.Fatalf("the fallback was called: %d", n)
	}
}

// selectorFake is System One: blocks mentioning NOISE score low.
func selectorFake(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		ans := map[string]any{}
		for k, q := range req.Questions {
			v := 0.9
			if strings.Contains(q.Instructions, "NOISE") {
				v = 0.01
			}
			ans[k] = map[string]any{"type": "noul", "noul": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// agentBody is a Claude Code-like turn with bulky tool output.
func agentBody(extra ...string) string {
	type blk = map[string]any
	msgs := []any{blk{"role": "user", "content": "Fix the login test"}}
	call := func(id, cmd, out string) {
		msgs = append(msgs,
			blk{"role": "assistant", "content": []any{blk{"type": "tool_use", "id": id, "name": "Bash", "input": blk{"command": cmd}}}},
			blk{"role": "user", "content": []any{blk{"type": "tool_result", "tool_use_id": id, "content": out}}})
	}
	call("toolu_npm", "npm install", "NOISE npm "+strings.Repeat("added package\n", 400))
	call("toolu_test", "go test", "FAIL TestLogin "+strings.Repeat("expected 200 got 500\n", 100))
	for i, x := range extra {
		call(fmt.Sprintf("toolu_x%d", i), "echo", x)
	}
	call("toolu_end", "true", "ok")
	b, _ := json.Marshal(blk{"model": "claude-sonnet-4-5", "max_tokens": 1000,
		"metadata": blk{"user_id": "user_x_session_11111111-2222-3333-4444-555555555555"}, "messages": msgs})
	return string(b)
}

// A context overflow is recovered by an emergency prune, which sticks:
// the next turn is sent pruned from the start. When pruning cannot make
// it fit, the client gets a clear error that leads with the provider's.
func TestEmergencyPruneRecovery(t *testing.T) {
	e := newResEnv(t)
	sel := selectorFake(t)
	must(t, e.cs.SetSelector(config.Selector{Backend: config.SelectorOpenRLCDLocal, BaseURL: sel.URL, Model: "Open-RLCD-text"}))
	// Shadow mode, and no epoch would be due: only the emergency prunes.
	e.section("prune", map[string]any{"mode": "shadow", "keep_last_n_turns": 1, "min_block_tokens": 50,
		"floor_tokens": 1 << 30, "epoch_tokens": 1 << 30})
	tooLong := `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`
	var always sync.Mutex
	alwaysFail := false
	up := newFake(t, func(n int, body string, w http.ResponseWriter) {
		always.Lock()
		af := alwaysFail
		always.Unlock()
		if af || strings.Contains(body, "NOISE npm") {
			reply(w, 400, tooLong)
			return
		}
		reply(w, 200, anthJSON)
	})
	must(t, e.cs.UpsertRoute("claude-sub", config.Route{Kind: config.KindAnthropic, BaseURL: up.srv.URL, Auth: config.AuthPassthrough}))
	h := map[string]string{"X-Api-Key": "sk-ant-api03-client", "Anthropic-Version": "2023-06-01"}

	resp, out := e.do("POST", "/v1/messages", agentBody(), h)
	if resp.StatusCode != 200 || out != anthJSON {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	calls := up.calls()
	if len(calls) != 2 || !strings.Contains(calls[1], "[rlcd:") || strings.Contains(calls[1], "NOISE npm") ||
		!strings.Contains(calls[1], "FAIL TestLogin") {
		t.Fatalf("retry body: %d calls", len(calls))
	}
	d := e.record(1)
	if len(d.Attempts) != 2 || d.Attempts[0].Class != resilience.ClassContextOverflow || d.Attempts[0].Action != resilience.ActionEmergencyPrune {
		t.Fatalf("attempts: %+v", d.Attempts)
	}
	ch := d.Attempts[1].Changes
	if ch == nil || ch.EmergencyPrune == nil || ch.EmergencyPrune.Dropped < 1 || ch.EmergencyPrune.SavedTokens <= 0 || !ch.CacheInvalidated {
		t.Fatalf("changes: %+v", ch)
	}
	if d.Stages["prune_emergency"] == nil || !d.Recovered {
		t.Fatalf("record: %+v", d.Record)
	}

	// The next turn goes out pruned on its first attempt.
	resp, _ = e.do("POST", "/v1/messages", agentBody("more output"), h)
	if resp.StatusCode != 200 || len(up.calls()) != 3 || strings.Contains(up.calls()[2], "NOISE npm") {
		t.Fatalf("next turn: %d, %d calls", resp.StatusCode, len(up.calls()))
	}
	if d := e.record(2); d.Retried {
		t.Fatalf("next turn retried: %+v", d.Attempts)
	}

	// Nothing makes it fit: a clear error, provider's words first.
	always.Lock()
	alwaysFail = true
	always.Unlock()
	resp, out = e.do("POST", "/v1/messages", agentBody("more output", "even more"), h)
	var eb struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(out), &eb)
	if resp.StatusCode != 400 || eb.Type != "error" || eb.Error.Type != "invalid_request_error" ||
		!strings.HasPrefix(eb.Error.Message, "prompt is too long: 250000 tokens > 200000 maximum [rlcd-gateway:") ||
		!strings.Contains(eb.Error.Message, "context window is 200000 tokens") || !strings.Contains(eb.Error.Message, "emergency prune") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if d := e.record(3); d.ErrorClass != resilience.ClassContextOverflow || d.Status != 400 {
		t.Fatalf("%+v", d.Record)
	}
}

func TestResilienceAPI(t *testing.T) {
	e := newResEnv(t)
	resp, out := e.do("GET", "/api/resilience/settings", "", nil)
	var v resilience.SettingsView
	if err := json.Unmarshal([]byte(out), &v); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if !v.Settings.Enabled || v.Settings.MaxAttempts != 4 || !v.Settings.MaxTokensGuard.Enabled || v.Effective.Routes["groq"].MaxAttempts != 4 ||
		len(v.Classes) == 0 || v.Effective.Aliases["smart"].Fallbacks == nil {
		t.Fatalf("view: %s", out)
	}
	for _, bad := range []string{`{"max_attempts":99}`, `{"routes":{"ghost":{}}}`, `{"what":1}`, `[]`} {
		if resp, _ := e.do("PUT", "/api/resilience/settings", bad, nil); resp.StatusCode != 400 {
			t.Errorf("accepted %s", bad)
		}
	}
	e.putResilience(map[string]any{"aliases": map[string]any{"smart": map[string]any{"max_attempts": 2, "fallbacks": []string{"groq:llama-3.1-8b-instant"}}}})
	_, out = e.do("GET", "/api/resilience/settings", "", nil)
	_ = json.Unmarshal([]byte(out), &v)
	if a := v.Effective.Aliases["smart"]; a.MaxAttempts != 2 || len(a.Fallbacks) != 1 {
		t.Fatalf("alias override: %+v", a)
	}
	resp, out = e.do("GET", "/api/resilience/limits?model=claude-sonnet-4-5-20250929&route=claude-sub", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(out, `"max_output_tokens":64000`) {
		t.Fatalf("limits: %s", out)
	}
	resp, out = e.do("GET", "/api/resilience/stats?days=7", "", nil)
	var s resilience.Stats
	if err := json.Unmarshal([]byte(out), &s); err != nil || resp.StatusCode != 200 || s.Days != 7 || len(s.ByClass) != len(resilience.Classes) {
		t.Fatalf("stats: %s", out)
	}
	if resp, _ := e.do("GET", "/api/resilience/stats?days=x", "", nil); resp.StatusCode != 400 {
		t.Fatal("bad days accepted")
	}
}
