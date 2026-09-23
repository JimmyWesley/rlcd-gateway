package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

func (e *env) server() *httptest.Server {
	mux := http.NewServeMux()
	e.r.Register(mux)
	srv := httptest.NewServer(mux)
	e.t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRoutesAPIKeysAreWriteOnly(t *testing.T) {
	e := newEnv(t)
	srv := e.server()

	code, body := do(t, "PUT", srv.URL+"/api/router/routes/fast",
		`{"kind":"openrouter","base_url":"https://openrouter.ai/api","auth":"key","model":"z-ai/glm-4.6","api_key":"sk-or-v1-supersecret","description":"fast cheap model"}`)
	if code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	_, list := do(t, "GET", srv.URL+"/api/router/routes", "")
	for _, secret := range []string{"sk-or-v1-supersecret", "sk-or-secret-123"} {
		if strings.Contains(body+list, secret) {
			t.Fatalf("a key leaked through the API: %s", list)
		}
	}
	var views []RouteView
	_ = json.Unmarshal([]byte(list), &views)
	var fast *RouteView
	for i := range views {
		if views[i].Name == "fast" {
			fast = &views[i]
		}
	}
	if fast == nil || !fast.HasKey || fast.Description != "fast cheap model" || fast.Model != "z-ai/glm-4.6" {
		t.Fatalf("fast route: %+v", fast)
	}

	// Empty key, same host: keeps the key.
	do(t, "PUT", srv.URL+"/api/router/routes/fast",
		`{"kind":"openrouter","base_url":"https://openrouter.ai/api","auth":"key","model":"z-ai/glm-4.7","description":"fast cheap model"}`)
	if k := e.cfg.Get().Routes["fast"].APIKey; k != "sk-or-v1-supersecret" {
		t.Fatalf("key should be kept on same host, got %q", k)
	}
	// Empty key, different host: the key must not follow.
	do(t, "PUT", srv.URL+"/api/router/routes/fast",
		`{"kind":"openrouter","base_url":"https://other.example/api","auth":"key","model":"z-ai/glm-4.7"}`)
	if k := e.cfg.Get().Routes["fast"].APIKey; k != "" {
		t.Fatalf("key followed the route to another host: %q", k)
	}
	if d := e.r.current().Routes["fast"].Description; d != "" {
		t.Fatalf("empty description should clear it: %q", d)
	}

	if code, _ := do(t, "PUT", srv.URL+"/api/router/routes/bad%20name", `{"kind":"anthropic","base_url":"https://x","auth":"passthrough"}`); code != 400 {
		t.Errorf("bad name accepted: %d", code)
	}
}

func TestRoutesAPIDelete(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	e.setRules(Settings{Sticky: true, Rules: []Rule{{Name: "r", Enabled: true, Route: "cheap"}}})
	if code, _ := do(t, "DELETE", srv.URL+"/api/router/routes/cheap", ""); code != http.StatusConflict {
		t.Fatalf("deleting a route used by a rule: %d", code)
	}
	if code, _ := do(t, "DELETE", srv.URL+"/api/router/routes/claude-sub", ""); code != 400 {
		t.Fatalf("deleting the active route: %d", code)
	}
	e.setRules(Settings{Sticky: true})
	if code, body := do(t, "DELETE", srv.URL+"/api/router/routes/cheap", ""); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, ok := e.cfg.Get().Routes["cheap"]; ok {
		t.Fatal("route still there")
	}
}

func TestRulesAPI(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	_, body := do(t, "GET", srv.URL+"/api/router/rules", "")
	var doc rulesDoc
	_ = json.Unmarshal([]byte(body), &doc)
	if !doc.Sticky || !doc.BackgroundBypass || doc.TTLHours != defaultTTLHours || doc.Rules == nil {
		t.Fatalf("defaults: %s", body)
	}
	bad := []string{
		`{"sticky":true,"rules":[{"name":"x","enabled":true,"route":"nope","when":{}}]}`,
		`{"sticky":true,"rules":[{"name":"x","enabled":true,"route":"cheap","when":{"model":"("}}]}`,
		`{"sticky":true,"rules":[{"name":"x","enabled":true,"route":"cheap","when":{}},{"name":"x","enabled":true,"route":"cheap","when":{}}]}`,
		`{"sticky":true,"rules":[{"name":"a","enabled":true,"kind":"auto","override_sticky":true,"when":{}}]}`,
		`{"sticky":true,"surprise":1}`,
	}
	for _, b := range bad {
		if code, _ := do(t, "PUT", srv.URL+"/api/router/rules", b); code != 400 {
			t.Errorf("accepted %s", b)
		}
	}
	// Route descriptions survive a rules update.
	do(t, "PUT", srv.URL+"/api/router/routes/cheap", `{"kind":"openrouter","base_url":"https://openrouter.ai/api","auth":"key","description":"cheap one"}`)
	code, body := do(t, "PUT", srv.URL+"/api/router/rules",
		`{"sticky":false,"ttl_hours":12,"background_bypass":true,"rules":[{"name":"bg","enabled":true,"route":"cheap","when":{"background":true}}]}`)
	if code != 200 {
		t.Fatalf("put rules: %d %s", code, body)
	}
	s := e.r.current()
	if s.Sticky || s.TTLHours != 12 || len(s.Rules) != 1 || s.Routes["cheap"].Description != "cheap one" {
		t.Fatalf("stored: %+v", s)
	}
}

func TestDryRunAndConversationsAPI(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	e.setRules(Settings{Sticky: true, Rules: []Rule{{Name: "opus", Enabled: true, Route: "openrouter", When: Match{Model: "opus"}}}})

	b, _ := json.Marshal(mainTurn(1))
	conv := pipeline.ConversationID(nil, b)
	_ = e.st.Save(&store.Detail{Record: store.Record{ID: "20260923T000000-aaaa", ConversationID: conv, Route: "claude-sub"},
		RequestBody: string(b)})
	_ = e.st.Save(&store.Detail{Record: store.Record{ID: "20260923T000000-bbbb"}})

	code, body := do(t, "POST", srv.URL+"/api/router/dryrun", `{"request_id":"20260923T000000-aaaa"}`)
	var res struct {
		Decision       pipeline.RouteDecision `json:"decision"`
		OK             bool                   `json:"ok"`
		EffectiveRoute string                 `json:"effective_route"`
		Trace          []RuleTrace            `json:"trace"`
		Facts          Facts                  `json:"facts"`
		Logged         struct{ Route string } `json:"logged"`
	}
	_ = json.Unmarshal([]byte(body), &res)
	if code != 200 || !res.OK || res.EffectiveRoute != "openrouter" || len(res.Trace) != 1 ||
		res.Trace[0].Result != ResultMatched || res.Logged.Route != "claude-sub" || res.Facts.Tools != 1 {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if code, _ := do(t, "POST", srv.URL+"/api/router/dryrun", `{"request_id":"20260923T000000-bbbb"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("no body: %d", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/api/router/dryrun", `{"request_id":"missing"}`); code != 404 {
		t.Errorf("missing: %d", code)
	}

	// A real turn pins the conversation; the API lists and resets it.
	e.route(mainTurn(1))
	_, body = do(t, "GET", srv.URL+"/api/router/conversations", "")
	var list []Assignment
	_ = json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0].ConversationID != conv || list[0].Route != "openrouter" || list[0].Rule != "opus" {
		t.Fatalf("conversations: %s", body)
	}
	if code, _ := do(t, "DELETE", srv.URL+"/api/router/conversations/"+conv, ""); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if code, _ := do(t, "DELETE", srv.URL+"/api/router/conversations/"+conv, ""); code != 404 {
		t.Fatalf("second reset: %d", code)
	}
}
