package resilience

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

func guardOn() Guard { return DefaultPolicy().MaxTokensGuard }

func runGuard(t *testing.T, body string, in GuardInput) (map[string]any, *Body, bool) {
	t.Helper()
	b, err := DecodeBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ch := ApplyGuard(b, in)
	var m map[string]any
	if err := json.Unmarshal(b.Encode(), &m); err != nil {
		t.Fatal(err)
	}
	return m, b, ch != nil
}

func TestGuardFillsMissingPerProtocol(t *testing.T) {
	qwen := Limits{ContextWindow: 262144, MaxOutputTokens: 235929, Source: SourceOpenRouter}
	// The incident: an OpenAI SDK chat app, no max_tokens, an aggregator.
	m, _, changed := runGuard(t, `{"model":"qwen/qwen3-235b-a22b-2507","messages":[{"role":"user","content":"hi"}]}`,
		GuardInput{Protocol: ir.ProtocolOpenAIChat, Guard: guardOn(), Limits: qwen, EstInputTokens: 193})
	if !changed || m["max_tokens"] != float64(4096) || m["max_completion_tokens"] != nil {
		t.Fatalf("chat fill: %v", m)
	}
	// OpenAI itself wants max_completion_tokens (with fill_missing=always).
	g := guardOn()
	g.FillMissing = FillAlways
	m, _, _ = runGuard(t, `{"model":"gpt-4.1","messages":[]}`, GuardInput{Protocol: ir.ProtocolOpenAIChat, Guard: g,
		Limits: Limits{ContextWindow: 1047576, MaxOutputTokens: 32768}, FirstParty: true, OpenAIAPI: true})
	if m["max_completion_tokens"] != float64(4096) {
		t.Fatalf("openai fill: %v", m)
	}
	// auto leaves first-party APIs alone.
	if _, _, changed := runGuard(t, `{"model":"gpt-4.1","messages":[]}`, GuardInput{Protocol: ir.ProtocolOpenAIChat,
		Guard: guardOn(), Limits: Limits{ContextWindow: 1047576, MaxOutputTokens: 32768}, FirstParty: true}); changed {
		t.Fatal("auto filled a first-party request")
	}
	// Responses: max_output_tokens; an agent gets the largest value that fits.
	m, _, _ = runGuard(t, `{"model":"x","input":"hi"}`, GuardInput{Protocol: ir.ProtocolOpenAIResponses, Guard: guardOn(),
		Limits: Limits{ContextWindow: 100000, MaxOutputTokens: 64000}, Agent: true, EstInputTokens: 10000})
	if m["max_output_tokens"] != float64(64000) {
		t.Fatalf("agent fill: %v", m)
	}
	m, _, _ = runGuard(t, `{"model":"x","input":"hi"}`, GuardInput{Protocol: ir.ProtocolOpenAIResponses, Guard: guardOn(),
		Limits: Limits{ContextWindow: 100000, MaxOutputTokens: 64000}, Agent: true, EstInputTokens: 60000})
	if want := 100000 - 60000 - 6000 - 256; m["max_output_tokens"] != float64(want) {
		t.Fatalf("agent fill limited by window: %v, want %d", m, want)
	}
	// Anthropic requires max_tokens: never filled.
	if _, _, changed := runGuard(t, `{"model":"claude-sonnet-4-5","messages":[]}`, GuardInput{Protocol: ir.ProtocolAnthropic,
		Guard: g, Limits: Limits{ContextWindow: 200000, MaxOutputTokens: 64000}}); changed {
		t.Fatal("anthropic filled")
	}
	// Unknown model: behave as today.
	if _, _, changed := runGuard(t, `{"model":"x","messages":[]}`, GuardInput{Protocol: ir.ProtocolOpenAIChat, Guard: guardOn()}); changed {
		t.Fatal("unknown limits changed the body")
	}
	// Guard off, or fill never.
	off := guardOn()
	off.Enabled = false
	never := guardOn()
	never.FillMissing = FillNever
	for _, gg := range []Guard{off, never} {
		if _, _, changed := runGuard(t, `{"model":"x","messages":[]}`, GuardInput{Protocol: ir.ProtocolOpenAIChat, Guard: gg, Limits: qwen}); changed {
			t.Fatalf("guard %+v changed the body", gg)
		}
	}
}

func TestGuardClampsPerProtocol(t *testing.T) {
	l := Limits{ContextWindow: 200000, MaxOutputTokens: 64000, Source: SourceBuiltin}
	// Above the model's max output.
	m, _, changed := runGuard(t, `{"model":"claude-sonnet-4-5","max_tokens":100000,"messages":[]}`,
		GuardInput{Protocol: ir.ProtocolAnthropic, Guard: guardOn(), Limits: l, EstInputTokens: 1000})
	if !changed || m["max_tokens"] != float64(64000) {
		t.Fatalf("anthropic clamp: %v", m)
	}
	// Input + max_tokens over the window: fit it, and keep the thinking
	// budget below max_tokens.
	b, _ := DecodeBody([]byte(`{"model":"claude-sonnet-4-5","max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":50000},"messages":[]}`))
	ch := ApplyGuard(b, GuardInput{Protocol: ir.ProtocolAnthropic, Guard: guardOn(), Limits: l, EstInputTokens: 150000})
	want := 200000 - 150000 - 15000 - 256
	if ch == nil || ch.To != want || ch.Reason != ReasonExceedsWindow || *ch.From != 64000 || ch.ThinkingBudgetTo != want-1 {
		t.Fatalf("window clamp: %+v", ch)
	}
	// Both chat fields are clamped; values within limits are untouched.
	m, _, _ = runGuard(t, `{"model":"x","max_tokens":999999,"max_completion_tokens":1000,"messages":[]}`,
		GuardInput{Protocol: ir.ProtocolOpenAIChat, Guard: guardOn(), Limits: l})
	if m["max_tokens"] != float64(64000) || m["max_completion_tokens"] != float64(1000) {
		t.Fatalf("chat clamp: %v", m)
	}
	m, _, _ = runGuard(t, `{"model":"x","max_output_tokens":70000,"input":"x"}`,
		GuardInput{Protocol: ir.ProtocolOpenAIResponses, Guard: guardOn(), Limits: l})
	if m["max_output_tokens"] != float64(64000) {
		t.Fatalf("responses clamp: %v", m)
	}
	// Clamp off.
	g := guardOn()
	g.Clamp = false
	if _, _, changed := runGuard(t, `{"model":"x","max_tokens":999999,"messages":[]}`,
		GuardInput{Protocol: ir.ProtocolAnthropic, Guard: g, Limits: l}); changed {
		t.Fatal("clamp off still clamped")
	}
	// Numbers other than the limit survive the rewrite exactly.
	_, b2, _ := runGuard(t, `{"model":"x","max_tokens":999999,"temperature":0.30000000000000004,"messages":[{"role":"user","content":"<b>&</b>"}]}`,
		GuardInput{Protocol: ir.ProtocolAnthropic, Guard: guardOn(), Limits: l})
	if s := string(b2.Encode()); !strings.Contains(s, `0.30000000000000004`) || !strings.Contains(s, `<b>&</b>`) {
		t.Fatalf("re-encoding changed the body: %s", s)
	}
}

func TestProviderIgnoreKeepsPreferences(t *testing.T) {
	b, _ := DecodeBody([]byte(`{"model":"x","provider":{"order":["A"],"ignore":["B"]}}`))
	IgnoreProviders(b, []string{"C", "B"})
	var m struct {
		Provider struct {
			Order  []string `json:"order"`
			Ignore []string `json:"ignore"`
		} `json:"provider"`
	}
	_ = json.Unmarshal(b.Encode(), &m)
	if strings.Join(m.Provider.Order, ",") != "A" || strings.Join(m.Provider.Ignore, ",") != "B,C" {
		t.Fatalf("%+v", m)
	}
}

func TestBuiltinLimits(t *testing.T) {
	for model, want := range map[string]int{
		"claude-sonnet-4-5-20250929":  64000,
		"claude-opus-4-1-20250805":    32000,
		"claude-opus-4-20250514":      32000,
		"anthropic/claude-sonnet-4.5": 64000,
		"gpt-4.1-2025-04-14":          32768,
		"gpt-4o-mini":                 16384,
		"openai/gpt-5":                128000,
	} {
		l, ok := builtinFor(model, false)
		if !ok || l.MaxOutputTokens != want {
			t.Errorf("%s: %+v %v", model, l, ok)
		}
	}
	// A newer family is unknown, not guessed from an older one's prefix.
	for _, model := range []string{"claude-opus-4-7", "claude-sonnet-5", "gpt-4.1-turbo", "llama-3.3-70b", "qwen/qwen3"} {
		if l, ok := builtinFor(model, false); ok {
			t.Errorf("%s matched %+v", model, l)
		}
	}
	if l, _ := builtinFor("claude-sonnet-4-5", true); l.ContextWindow != 1000000 {
		t.Errorf("1M beta: %+v", l)
	}
}

func TestRegistryCatalogCacheAndLearning(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"data":[{"id":"qwen/qwen3-235b-a22b-2507","context_length":262144,"top_provider":{"context_length":262144,"max_completion_tokens":235929}},{"id":"x/nolimits","context_length":0,"top_provider":{}}]}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	r := NewRegistry(dir)
	r.SetURL(srv.URL)
	if l := r.Lookup("qwen/qwen3-235b-a22b-2507", true, false); l.Known() {
		t.Fatalf("known before any fetch: %+v", l)
	}
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l := r.Lookup("qwen/qwen3-235b-a22b-2507:free", true, false); l.ContextWindow != 262144 || l.MaxOutputTokens != 235929 || l.Source != SourceOpenRouter {
		t.Fatalf("catalog: %+v", l)
	}
	// Only OpenRouter routes read the catalog.
	if l := r.Lookup("qwen/qwen3-235b-a22b-2507", false, false); l.Known() {
		t.Fatalf("catalog used for a non-OpenRouter route: %+v", l)
	}
	// The disk cache survives a restart, and is fresh: no refetch needed.
	r2 := NewRegistry(dir)
	if l := r2.Lookup("qwen/qwen3-235b-a22b-2507", true, false); l.ContextWindow != 262144 || r2.stale() {
		t.Fatalf("cache: %+v stale=%v", l, r2.stale())
	}
	// A provider's error teaches a lower limit.
	r2.Learn("qwen/qwen3-235b-a22b-2507", Limits{ContextWindow: 131072, MaxOutputTokens: 131072})
	if l := r2.Lookup("qwen/qwen3-235b-a22b-2507", true, false); l.ContextWindow != 131072 || l.MaxOutputTokens != 131072 || l.Source != SourceLearned {
		t.Fatalf("learned: %+v", l)
	}
	r2.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if l := r2.Lookup("qwen/qwen3-235b-a22b-2507", true, false); l.ContextWindow != 262144 || !r2.stale() {
		t.Fatalf("learned limits must expire: %+v", l)
	}
	if hits != 1 {
		t.Fatalf("fetched %d times", hits)
	}
}

func TestSettingsResolveAndValidate(t *testing.T) {
	c := config.Default()
	c.Routes["or"] = config.Route{Kind: config.KindOpenAI, BaseURL: "https://openrouter.ai/api/v1", Auth: config.AuthKey}
	c.Routes["or2"] = config.Route{Kind: config.KindOpenAI, BaseURL: "https://openrouter.ai/api/v1", Auth: config.AuthKey}
	c.Sections = map[string]json.RawMessage{"router": json.RawMessage(`{"aliases":[{"name":"smart","route":"or","model":"qwen/x"}]}`)}
	s, err := ApplyPatch(DefaultSettings(), []byte(`{"max_attempts":3,"backoff":{"retries":1},
		"routes":{"or":{"backoff":{"max_ms":1000},"fallbacks":["or2"],"context_window":131072}},
		"aliases":{"smart":{"max_tokens_guard":{"default_max_tokens":300},"fallbacks":["or2:google/gemini-2.5-flash-lite"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(*c); err != nil {
		t.Fatal(err)
	}
	r := s.Resolve("or", "smart")
	if r.MaxAttempts != 3 || r.Backoff.Retries != 1 || r.Backoff.MaxMs != 1000 || r.Backoff.BaseMs != 500 ||
		r.MaxTokensGuard.DefaultMaxTokens != 300 || !r.MaxTokensGuard.Enabled || r.WindowOverride != 131072 ||
		len(r.Fallbacks) != 1 || r.Fallbacks[0] != "or2:google/gemini-2.5-flash-lite" {
		t.Fatalf("resolved: %+v", r)
	}
	if r := s.Resolve("or", ""); len(r.Fallbacks) != 1 || r.Fallbacks[0] != "or2" || r.MaxTokensGuard.DefaultMaxTokens != 4096 {
		t.Fatalf("route only: %+v", r)
	}
	// null removes an override.
	s2, err := ApplyPatch(s, []byte(`{"routes":{"or":null}}`))
	if err != nil || len(s2.Routes) != 0 || len(s.Routes) != 1 {
		t.Fatalf("remove: %v %+v", err, s2.Routes)
	}
	for _, bad := range []string{
		`{"max_attempts":0}`,
		`{"backoff":{"jitter":2}}`,
		`{"max_tokens_guard":{"fill_missing":"sometimes"}}`,
		`{"emergency_prune":{"keep_threshold":1.5}}`,
		`{"routes":{"nope":{}}}`,
		`{"routes":{"or":{"fallbacks":["claude-sub"]}}}`,
		`{"routes":{"or":{"fallbacks":["or"]}}}`,
		`{"routes":{"or":{"fallbacks":["ghost"]}}}`,
		`{"routes":{"or":{"surprise":1}}}`,
		`{"aliases":{"ghost":{}}}`,
		`{"models":{"m":{"context_window":100,"max_output_tokens":200}}}`,
	} {
		s3, err := ApplyPatch(DefaultSettings(), []byte(bad))
		if err == nil {
			err = s3.Validate(*c)
		}
		if err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := ApplyPatch(DefaultSettings(), []byte(`{"unknown_field":1}`)); err == nil {
		t.Error("unknown top-level field accepted")
	}
}
