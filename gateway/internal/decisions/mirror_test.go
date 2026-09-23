package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// The mirror runs after the client has its answer: a mirror backend that
// hangs does not delay the response, and once it answers, agreement and
// latency are recorded side by side.
func TestMirrorNeverDelaysTheResponse(t *testing.T) {
	e := newEnv(t, false)
	e.twoBackends()
	s, _ := e.d.Settings()
	// Mirror open-rlcd traffic to Jev, whose answer to "urgency" differs.
	s.Mirror = &Mirror{Backend: "jev", SampleRate: 1, Model: "jev-latest"}
	e.configure(s)
	e.jev.mu.Lock()
	e.jev.resp = strings.Replace(strings.Replace(respBody, `"score":1.522924`, `"score":0.9`, 1), "Open-RLCD-text", "jev-latest", 1)
	e.jev.hold = make(chan struct{})
	e.jev.mu.Unlock()

	start := time.Now()
	resp, out := e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer ts-client-key"})
	if elapsed := time.Since(start); resp.StatusCode != 200 || out != respBody || elapsed > 2*time.Second {
		t.Fatalf("client answer: %d in %s", resp.StatusCode, elapsed)
	}
	id := resp.Header.Get("X-Rlcd-Request-Id")
	eventually(t, "the mirror call to start", func() bool { return e.jev.count() == 1 })
	if _, ok := e.d.mirrors.Get(id); ok {
		t.Fatal("mirror result recorded before the mirror answered")
	}
	// The mirror got the same request with its own model and credentials.
	got := e.jev.last(t)
	var sent map[string]json.RawMessage
	must(t, json.Unmarshal(got.body, &sent))
	if string(sent["model"]) != `"jev-latest"` || got.h.Get("Authorization") != "Bearer ts-client-key" {
		t.Errorf("mirror request: %s %v", sent["model"], got.h)
	}
	close(e.jev.hold)
	eventually(t, "the mirror result", func() bool { _, ok := e.d.mirrors.Get(id); return ok })

	m, _ := e.d.mirrors.Get(id)
	if m.Backend != "jev" || m.PrimaryBackend != "rlcd" || m.Compared != 3 || m.Agreed != 2 || m.Model != "jev-latest" ||
		m.ForwardMs == nil || m.PrimaryForwardMs == nil || m.Status != 200 {
		t.Fatalf("mirror result: %+v", m)
	}
	for _, q := range m.Questions {
		if q.ID == "urgency" && (q.Agree || q.Answer != "medium" || q.PrimaryAnswer != "high") {
			t.Errorf("urgency: %+v", q)
		}
	}
	st := e.stats("")
	if st.Mirror.Calls != 1 || st.Mirror.Compared != 3 || st.Mirror.Rate == nil || *st.Mirror.Rate != 0.6667 ||
		st.Questions["urgency"].Mirror.Compared != 1 || st.Questions["urgency"].Mirror.Agreed != 0 ||
		st.Mirror.ByBackend["jev"] == nil || st.Mirror.PrimaryP50Ms == nil {
		t.Errorf("mirror stats: %+v", st.Mirror)
	}
	if l := e.list(""); l.Items[0].Mirror == nil || l.Items[0].Mirror.Agreed != 2 {
		t.Errorf("list mirror: %+v", l.Items[0].Mirror)
	}
}

// Sampling: at rate 0 or when the sampler says no, nothing is mirrored;
// the mirror is never the backend that served the call.
func TestMirrorSampling(t *testing.T) {
	e := newEnv(t, false)
	e.twoBackends()
	s, _ := e.d.Settings()
	s.Mirror = &Mirror{Backend: "jev", SampleRate: 0.25}
	e.configure(s)
	n := 0
	e.d.sample = func(rate float64) bool {
		if rate != 0.25 {
			t.Errorf("rate %v", rate)
		}
		n++
		return n%4 == 0
	}
	for i := 0; i < 8; i++ {
		e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer k"})
	}
	eventually(t, "two mirrors", func() bool { return e.jev.count() == 2 })
	// A Jev call is not mirrored to Jev itself.
	e.do("POST", "/v1/systemone", bodyFor("jev-latest"), map[string]string{"Authorization": "Bearer k"})
	e.do("POST", "/v1/systemone", bodyFor("jev-latest"), map[string]string{"Authorization": "Bearer k"})
	time.Sleep(100 * time.Millisecond)
	if c := e.jev.count(); c != 4 { // 2 mirrors + 2 direct calls
		t.Errorf("jev calls: %d", c)
	}
	// Off by default.
	e.configure(Settings{Backends: s.Backends, Models: s.Models, DefaultBackend: s.DefaultBackend})
	before := e.jev.count()
	e.d.sample = func(float64) bool { return true }
	e.do("POST", "/v1/systemone", reqBody, nil)
	time.Sleep(100 * time.Millisecond)
	if e.jev.count() != before {
		t.Error("mirrored without a mirror setting")
	}
}

// The gateway's own selector calls are logged as decisions, asynchronously;
// when storing fails or the queue is full the call still succeeds and only
// a counter moves.
func TestInternalDecisionsLogging(t *testing.T) {
	e := newEnv(t, false)
	sel := config.Selector{Backend: config.SelectorOpenRLCDLocal, BaseURL: e.economy.srv.URL, Model: "Open-RLCD-text"}
	ctx := selector.WithTrace(context.Background(), selector.Trace{Observe: e.d.ObserveInternal,
		ParentID: "20260901T120000-00000000000000aa", ConversationID: "conv-1"})
	ask := func(ctx context.Context) error {
		_, err := selector.New(sel).Ask(selector.WithSource(ctx, "prune"), map[string]any{"goal": "fix the bug"},
			map[string]selector.Question{"m3.b0": {Type: "noul", Instructions: "Needed?"}})
		return err
	}
	must(t, ask(ctx))
	eventually(t, "the internal record", func() bool { return e.d.internalLogged.Load() == 1 })
	d := e.last()
	if d.Client == nil || d.Client.ID != "rlcd-gateway" || d.Client.Kind != "internal" || d.Decisions.Source != "prune" ||
		d.Decisions.ParentID != "20260901T120000-00000000000000aa" || d.ConversationID != "conv-1" || d.Route != Economy ||
		d.Usage == nil || d.KeyID != "" || !near(d.Decisions.ForwardMs, 82.6) {
		t.Fatalf("internal record: %+v %+v", d.Record, d.Decisions)
	}
	if l := e.list("source=prune"); l.Total != 1 || l.Items[0].Client.ID != "rlcd-gateway" {
		t.Errorf("list by source: %+v", l)
	}

	// Storage fails: the turn is unaffected, the failure is counted.
	e.d.save = func(*store.Detail) error { return errors.New("disk full") }
	must(t, ask(ctx))
	eventually(t, "the failure counter", func() bool { return e.d.internalFailed.Load() == 1 })

	// A selector error is logged too, and the caller still gets its error.
	e.d.save = e.st.Save
	bad := sel
	bad.BaseURL = "http://127.0.0.1:1"
	if _, err := selector.New(bad).Ask(selector.WithSource(ctx, "router"), "s",
		map[string]selector.Question{"route": {Type: "choice", Instructions: "?", Criteria: map[string]string{"a": "x", "b": "y"}}}); err == nil {
		t.Fatal("expected an error")
	}
	eventually(t, "the error record", func() bool { return e.d.internalLogged.Load() == 2 })
	if l := e.list("source=router&status=error"); l.Total != 1 {
		t.Errorf("router error record: %d", l.Total)
	}

	// Queue full: dropped, never blocking.
	blocked := New(e.cs, e.st)
	blocked.internalQ = make(chan selector.Call) // nobody reads it
	blocked.internalOnce.Do(func() {})
	done := make(chan struct{})
	go func() {
		blocked.ObserveInternal(selector.Call{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ObserveInternal blocked")
	}
	if blocked.internalDropped.Load() != 1 {
		t.Errorf("dropped: %d", blocked.internalDropped.Load())
	}

	// Switched off: nothing is logged.
	off := false
	e.configure(Settings{LogInternal: &off})
	before := e.d.internalLogged.Load()
	must(t, ask(ctx))
	time.Sleep(100 * time.Millisecond)
	if e.d.internalLogged.Load() != before {
		t.Error("logged with log_internal off")
	}
	st := e.stats("")
	if st.Internal.Logged != 2 || st.Internal.Failed != 1 {
		t.Errorf("internal counters: %+v", st.Internal)
	}
}
