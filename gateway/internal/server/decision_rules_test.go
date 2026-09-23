package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// A decision rule end to end: the router asks the jev backend with its
// token, routes by the answer, the resilience fallback still recovers the
// chosen route's failure, and the call is audited as an internal decision
// of backend jev.
func TestDecisionRuleThroughGateway(t *testing.T) {
	e := newResEnv(t)
	var auth []string
	jev := newFake(t, func(n int, body string, w http.ResponseWriter) {
		reply(w, 200, `{"model":"jev-1.13.0","answers":{"decision":{"type":"score","score":2.1,"confidence":0.81,`+
			`"probabilities":{"0":0.02,"1":0.1,"2":0.81,"3":0.07}}},"usage":{"input_tokens":120,"output_tokens":1}}`)
	})
	jevSrv := jev.srv
	strong := newFake(t, func(n int, body string, w http.ResponseWriter) {
		reply(w, 503, `{"error":{"message":"Service Unavailable","code":503}}`)
	})
	backup := newFake(t, func(n int, body string, w http.ResponseWriter) { reply(w, 200, okChat) })
	fast := newFake(t, func(n int, body string, w http.ResponseWriter) { reply(w, 200, okChat) })
	e.route("strong", strong, "", "strong-model")
	e.route("backup", backup, "", "backup-model")
	e.route("fast", fast, "", "fast-model")
	// Capture the Authorization the backend receives.
	jevSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		jev.handle(1, "", w)
	})
	t.Setenv("JEV_TEST_TOKEN", "tok-from-env")
	e.section("decisions", map[string]any{"log_internal": true, "backends": map[string]any{
		"jev": map[string]any{"base_url": jevSrv.URL, "auth": "key", "token_env": "JEV_TEST_TOKEN"}}})
	e.section("router", map[string]any{"sticky": true, "ttl_hours": 72, "rules": []map[string]any{{
		"name": "complexity", "enabled": true, "kind": "decision", "backend": "jev", "backend_model": "jev-latest",
		"question": map[string]any{"type": "score", "instructions": "How hard is the request?",
			"criteria": []string{"trivial", "simple", "moderate", "hard"}},
		"branches": []map[string]any{
			{"when": map[string]any{"op": ">=", "value": 2}, "then": map[string]any{"route": "strong"}},
			{"when": map[string]any{"op": "<=", "value": 1}, "then": map[string]any{"route": "fast"}}},
	}}})
	e.putResilience(map[string]any{"backoff": map[string]any{"retries": 0},
		"routes": map[string]any{"strong": map[string]any{"fallbacks": []string{"backup"}}}})

	body := `{"model":"whatever","max_tokens":20,"messages":[{"role":"user","content":"Design a lock-free queue in Rust."}]}`
	resp, out := e.do("POST", "/v1/chat/completions", body, map[string]string{"Conversation-Id": "conv-7"})
	if resp.StatusCode != 200 || out != okChat || resp.Header.Get("X-Rlcd-Route") != "backup" {
		t.Fatalf("%d %s route=%s", resp.StatusCode, out, resp.Header.Get("X-Rlcd-Route"))
	}
	if len(auth) != 1 || auth[0] != "Bearer tok-from-env" || len(fast.calls()) != 0 {
		t.Fatalf("backend auth %v, fast calls %d", auth, len(fast.calls()))
	}
	d := e.record(1)
	if d.PrimaryRoute != "strong" || d.Route != "backup" || !d.Recovered ||
		!strings.HasPrefix(d.RouteReason, "decision rule 'complexity': score 2/3 'moderate' (conf 0.81, jev ") ||
		!strings.HasSuffix(d.RouteReason, "→ strong; fallback from strong to backup") {
		t.Fatalf("record: %+v", d.Record)
	}
	waitRecords(t, e.st, 2)
	var internal *store.Record
	for _, r := range e.st.Recent() {
		if r.Decisions != nil && r.Decisions.Source == "router" {
			r := r
			internal = &r
		}
	}
	if internal == nil || internal.Route != "jev" || internal.Decisions.Backend != "jev" || internal.Provider != "open-rlcd" ||
		internal.Decisions.ParentID != d.ID || internal.ClientModel != "jev-latest" || internal.Decisions.Questions[0].Answer != "moderate" {
		t.Fatalf("internal decision: %+v %+v", internal, internal.Decisions)
	}
}
