package resilience

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

func TestDecide(t *testing.T) {
	p := DefaultPolicy()
	p.Backoff = Backoff{BaseMs: 100, MaxMs: 1000, Jitter: 0, Retries: 2}
	p.TimeBudgetMs = 5000
	f := func(class string) Failure { return Failure{Class: class} }
	cases := []struct {
		name string
		f    Failure
		s    State
		want string
	}{
		{"auth passes through", f(ClassAuth), State{Attempt: 1}, ActionNone},
		{"bad request passes through", f(ClassBadRequest), State{Attempt: 1, FallbacksLeft: 2}, ActionNone},
		{"clamp", f(ClassOutputTooLarge), State{Attempt: 1, CanClamp: true}, ActionClamp},
		{"no clamp left", f(ClassOutputTooLarge), State{Attempt: 2}, ActionGiveUp},
		{"emergency", f(ClassContextOverflow), State{Attempt: 1}, ActionEmergencyPrune},
		{"emergency once", f(ClassContextOverflow), State{Attempt: 2, EmergencyTried: true}, ActionGiveUp},
		{"ignore provider first", Failure{Class: ClassProviderError, UpstreamProvider: "X"}, State{Attempt: 1, OpenRouter: true}, ActionIgnoreProvider},
		{"ignore once", Failure{Class: ClassOverloaded, UpstreamProvider: "X"}, State{Attempt: 2, OpenRouter: true, Ignored: []string{"X"}}, ActionBackoff},
		{"not openrouter", Failure{Class: ClassProviderError, UpstreamProvider: "X"}, State{Attempt: 1}, ActionBackoff},
		{"backoffs used, fallback", f(ClassRateLimited), State{Attempt: 3, BackoffsUsed: 2, FallbacksLeft: 1}, ActionFallback},
		{"backoffs used, nothing left", f(ClassRateLimited), State{Attempt: 3, BackoffsUsed: 2}, ActionGiveUp},
		{"retry-after beyond budget", Failure{Class: ClassRateLimited, RetryAfter: 10 * time.Second}, State{Attempt: 1, FallbacksLeft: 1}, ActionFallback},
		{"model unavailable", f(ClassModelUnavailable), State{Attempt: 1, FallbacksLeft: 1}, ActionFallback},
		{"max attempts", f(ClassOverloaded), State{Attempt: 4, FallbacksLeft: 1}, ActionGiveUp},
		{"budget spent", f(ClassOverloaded), State{Attempt: 1, Elapsed: 6 * time.Second, FallbacksLeft: 1}, ActionGiveUp},
	}
	for _, c := range cases {
		d := Decide(p, c.f, c.s, 0.5)
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, d.Action, d.Detail, c.want)
		}
	}
	off := p
	off.Enabled = false
	if d := Decide(off, f(ClassOverloaded), State{Attempt: 1}, 0.5); d.Action != ActionGiveUp {
		t.Errorf("disabled: %+v", d)
	}
}

func TestBackoffWait(t *testing.T) {
	b := Backoff{BaseMs: 500, MaxMs: 3000, Jitter: 0.5, Retries: 3}
	if w := BackoffWait(b, 0, 0, 0.5); w != 500*time.Millisecond {
		t.Errorf("no jitter at 0.5: %v", w)
	}
	if w := BackoffWait(b, 1, 0, 1); w != 1500*time.Millisecond {
		t.Errorf("max jitter: %v", w)
	}
	if w := BackoffWait(b, 5, 0, 0.5); w != 3000*time.Millisecond {
		t.Errorf("capped: %v", w)
	}
	if w := BackoffWait(b, 0, 4*time.Second, 0); w != 4*time.Second {
		t.Errorf("retry-after wins: %v", w)
	}
}

func TestPeekStream(t *testing.T) {
	// OpenRouter keep-alives, then an error: caught before the first byte.
	pk, stop := PeekStream(strings.NewReader(": OPENROUTER PROCESSING\n\n: OPENROUTER PROCESSING\n\n"+
		`data: {"error":{"message":"Provider returned error","code":502,"metadata":{"raw":"boom","provider_name":"Flaky"}}}`+"\n\n"), 1<<20, time.Second)
	stop()
	if pk.Failure == nil || pk.Failure.Class != ClassProviderError || pk.Failure.UpstreamProvider != "Flaky" {
		t.Fatalf("error not caught: %+v", pk)
	}
	// Anthropic: message_start is preamble, the overload right after it counts.
	pk, stop = PeekStream(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n"+
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"), 1<<20, time.Second)
	stop()
	if pk.Failure == nil || pk.Failure.Class != ClassOverloaded {
		t.Fatalf("overload after message_start: %+v", pk)
	}
	// Content first: committed, and every byte comes back in order.
	src := "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"late\"}}\n\n"
	pk, stop = PeekStream(strings.NewReader(src), 1<<20, time.Second)
	if pk.Failure != nil || !pk.Committed {
		t.Fatalf("content must commit: %+v", pk)
	}
	rest, _ := io.ReadAll(pk.Rest)
	stop()
	if string(pk.Buffered)+string(rest) != src {
		t.Fatalf("bytes lost: %q", string(pk.Buffered)+string(rest))
	}
	// A slow stream is committed at the time limit, and nothing is lost.
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, ": keep-alive\n\n")
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(pw, "data: {\"x\":1}\n\n")
		pw.Close()
	}()
	pk, stop = PeekStream(pr, 1<<20, 50*time.Millisecond)
	if !pk.Committed || pk.Why != "peek time limit" {
		t.Fatalf("time limit: %+v", pk)
	}
	rest, _ = io.ReadAll(pk.Rest)
	stop()
	if string(pk.Buffered)+string(rest) != ": keep-alive\n\ndata: {\"x\":1}\n\n" {
		t.Fatalf("bytes lost: %q", string(pk.Buffered)+string(rest))
	}
}

func TestComputeStats(t *testing.T) {
	now := time.Now()
	from := func(n int) *int { return &n }
	recs := []store.Record{
		{Time: now, Model: "m", Status: 200},
		{Time: now, Model: "qwen", Status: 200, MaxTokensGuard: &store.MaxTokensChange{Reason: ReasonFilled, To: 4096}},
		{Time: now, Model: "qwen", Status: 200, Retried: true, Recovered: true, Attempts: []store.Attempt{
			{N: 1, Provider: "openrouter", UpstreamProvider: "GMICloud", Model: "qwen", Status: 400, Class: ClassOutputTooLarge, Action: ActionClamp},
			{N: 2, Provider: "openrouter", Model: "qwen", Status: 200, Changes: &store.AttemptChanges{MaxTokens: &store.MaxTokensChange{From: from(131072), To: 130000}}},
		}},
		{Time: now, Model: "b", Status: 200, Retried: true, Recovered: true, PrimaryRoute: "a", FallbackRoute: "b", Route: "b", Attempts: []store.Attempt{
			{N: 1, Provider: "custom", Model: "a", Status: 503, Class: ClassOverloaded, Action: ActionFallback},
			{N: 2, Provider: "custom", Model: "b", Status: 200},
		}},
		{Time: now, Model: "c", Status: 401, ErrorClass: ClassAuth, Attempts: []store.Attempt{
			{N: 1, Provider: "custom", Model: "c", Status: 401, Class: ClassAuth, Action: ActionNone},
		}},
		{Time: now.Add(-40 * 24 * time.Hour), Model: "old", Status: 500, Attempts: []store.Attempt{{N: 1, Class: ClassProviderError}}},
	}
	scan := func(fn func(store.Record) bool) error {
		for _, r := range recs {
			if !fn(r) {
				break
			}
		}
		return nil
	}
	s, _ := ComputeStats(scan, 30, now)
	if s.Requests != 5 || s.Engaged != 3 || s.Retried != 2 || s.Recovered != 2 || s.RecoveryRate != 1 || s.Fallbacks != 1 ||
		s.Guard.Filled != 1 || s.FirstAttemptFailed != 3 {
		t.Fatalf("totals: %+v", s)
	}
	if c := s.ByClass[ClassOutputTooLarge]; c.Failures != 1 || c.Recovered != 1 {
		t.Fatalf("by class: %+v", c)
	}
	if a := s.ByAction[ActionClamp]; a.Count != 1 || a.Succeeded != 1 {
		t.Fatalf("by action: %+v", a)
	}
	if len(s.TopProviders) != 2 || s.TopProviders[0].Name != "custom" || s.TopProviders[1].Name != "openrouter/GMICloud" {
		t.Fatalf("providers: %+v", s.TopProviders)
	}
	if len(s.FallbackRoutes) != 1 || s.FallbackRoutes[0] != (FallbackPair{From: "a", To: "b", Count: 1}) {
		t.Fatalf("fallbacks: %+v", s.FallbackRoutes)
	}
	all, _ := ComputeStats(scan, 0, now)
	if all.Requests != 6 {
		t.Fatalf("days=0 counts everything: %d", all.Requests)
	}
}
