package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// upstream is a fake provider. It records what it was sent and answers with
// resp (an SSE stream or a JSON body), split into uneven chunks.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []seen
	resp string
	sse  bool
}

type seen struct {
	path string
	h    http.Header
	body []byte
}

func newUpstream(t *testing.T, resp string, sse bool) *upstream {
	u := &upstream{resp: resp, sse: sse}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.hits = append(u.hits, seen{path: r.URL.RequestURI(), h: r.Header.Clone(), body: b})
		resp, sse := u.resp, u.sse
		u.mu.Unlock()
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		third := len(resp) / 3
		for _, part := range []string{resp[:third], resp[third : 2*third], resp[2*third:]} {
			_, _ = io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) last(t *testing.T) seen {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.hits) == 0 {
		t.Fatal("upstream was not called")
	}
	return u.hits[len(u.hits)-1]
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.hits)
}

type env struct {
	t     *testing.T
	home  string
	cs    *config.Store
	st    *store.Store
	gw    *Gateway
	srv   *httptest.Server
	anth  *upstream // Anthropic Messages
	oai   *upstream // OpenAI-compatible
	admin string
}

const (
	anthSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":12,"cache_read_input_tokens":900,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":57}}` + "\n\n"
	anthJSON = `{"id":"msg_1","type":"message","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":2}}`
	chatSSE  = `data: {"id":"c1","model":"llama-3.3-70b-versatile","choices":[{"delta":{"content":"hi"}}],"usage":null}` + "\n\n" +
		`data: {"id":"c1","model":"llama-3.3-70b-versatile","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":384}}}` + "\n\n" +
		"data: [DONE]\n\n"
	chatJSON = `{"id":"c2","object":"chat.completion","model":"gpt-4.1-2025","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`
	respSSE  = "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1000},"output_tokens":40}}}` + "\n\n"

	anthReq = `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"text","text":"yo"}]},{"role":"user","content":"again"}]}`
	chatReq = `{"model":"smart","stream":true,"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hello"}]}`

	adminToken = "admin-token-0123456789abcdef"
)

// newEnv starts a gateway bound to a loopback port (exposed=false) or
// pretending to listen on all interfaces (exposed=true).
func newEnv(t *testing.T, exposed bool) *env {
	t.Helper()
	e := &env{t: t, home: t.TempDir()}
	t.Setenv("RLCD_GATEWAY_HOME", e.home)
	e.anth = newUpstream(t, anthSSE, true)
	e.oai = newUpstream(t, chatSSE, true)

	cfg := config.Default()
	cfg.Routes["claude-sub"] = config.Route{Kind: config.KindAnthropic, BaseURL: e.anth.srv.URL, Auth: config.AuthPassthrough}
	cfg.Routes["groq"] = config.Route{Kind: config.KindOpenAI, BaseURL: e.oai.srv.URL + "/openai/v1", Auth: config.AuthKey,
		APIKey: "gsk-provider-secret-0000", Headers: map[string]string{"X-Title": "rlcd test", "HTTP-Referer": "https://example.org"}}
	cfg.PassthroughBaseURL = e.anth.srv.URL
	e.cs = config.NewStore(cfg)
	raw, _ := json.Marshal(map[string]string{"openai_base_url": e.oai.srv.URL + "/v1", "chatgpt_base_url": e.oai.srv.URL + "/backend-api/codex"})
	must(t, e.cs.SetSection("adapters", raw))
	router, _ := json.Marshal(map[string]any{"sticky": true, "ttl_hours": 72, "background_bypass": true, "rules": []any{},
		"aliases": []map[string]string{
			{"name": "smart", "route": "groq", "model": "llama-3.3-70b-versatile"},
			{"name": "claude", "route": "claude-sub"},
			{"name": "fast", "route": "groq", "model": "llama-3.1-8b-instant"},
		}})
	must(t, e.cs.SetSection("router", router))

	st, err := store.Open(e.home)
	must(t, err)
	e.st = st

	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	listen := l.Addr().String()
	if exposed {
		_, port, _ := net.SplitHostPort(listen)
		listen = "0.0.0.0:" + port
	}
	e.admin = adminToken
	e.gw, err = New(e.cs, st, Options{Listen: listen, AdminToken: adminToken, Home: e.home})
	must(t, err)
	e.srv = &httptest.Server{Listener: l, Config: &http.Server{Handler: e.gw.Handler}}
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(method, path, body string, h map[string]string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range h {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) key(name string, lim keys.Limits) (string, keys.View) {
	e.t.Helper()
	k, v, err := e.gw.Keys.Create(name, lim)
	must(e.t, err)
	return k, v
}

func (e *env) lastRecord() *store.Detail {
	e.t.Helper()
	recs := e.st.Recent()
	if len(recs) == 0 {
		e.t.Fatal("no record")
	}
	d, err := e.st.Get(recs[0].ID)
	must(e.t, err)
	return d
}

// The Anthropic path without keys behaves exactly as before: the client's
// login and bytes go through untouched, both ways.
func TestAnthropicPathUnchanged(t *testing.T) {
	e := newEnv(t, false)
	hdr := map[string]string{"Authorization": "Bearer sk-ant-oat01-subscription", "Anthropic-Beta": "oauth-2025-04-20",
		"Anthropic-Version": "2023-06-01", "User-Agent": "claude-cli/2.1.280 (external, cli)"}
	resp, out := e.do("POST", "/v1/messages?beta=true", anthReq, hdr)
	if resp.StatusCode != 200 || out != anthSSE {
		t.Fatalf("SSE not byte-exact (%d):\n%q", resp.StatusCode, out)
	}
	got := e.anth.last(t)
	if string(got.body) != anthReq || got.path != "/v1/messages?beta=true" {
		t.Fatalf("request changed on the way: %s %s", got.path, got.body)
	}
	if got.h.Get("Authorization") != "Bearer sk-ant-oat01-subscription" || got.h.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("login not forwarded: %v", got.h)
	}
	d := e.lastRecord()
	if d.Protocol != ir.ProtocolAnthropic || d.Route != "claude-sub" || d.Usage == nil || d.Usage.OutputTokens != 57 ||
		d.Client == nil || d.Client.ID != "claude-code" || d.Provider != "custom" || d.ModelVendor != "anthropic" || d.CostUSD <= 0 {
		t.Fatalf("record: %+v client %+v", d.Record, d.Client)
	}

	e.anth.mu.Lock()
	e.anth.resp, e.anth.sse = anthJSON, false
	e.anth.mu.Unlock()
	nonStream := strings.Replace(anthReq, `"stream":true`, `"stream":false`, 1)
	if resp, out := e.do("POST", "/v1/messages", nonStream, hdr); resp.StatusCode != 200 || out != anthJSON {
		t.Fatalf("JSON not byte-exact: %q", out)
	}
	// /v1/models from an Anthropic SDK still reaches its provider.
	e.do("GET", "/v1/models", "", map[string]string{"Anthropic-Version": "2023-06-01", "X-Api-Key": "sk-ant-api03-x"})
	if p := e.anth.last(t).path; p != "/v1/models" {
		t.Fatalf("models passthrough: %s", p)
	}
}

// An alias routes an OpenAI SDK call to an OpenAI-compatible provider: the
// gateway key is stripped, the route's key and headers injected, the model
// swapped, and the stream returned byte for byte.
func TestAliasRoutingWithGatewayKey(t *testing.T) {
	e := newEnv(t, false)
	key, v := e.key("chatbot", keys.Limits{})
	hdr := map[string]string{"Authorization": "Bearer " + key, "User-Agent": "OpenAI/Python 1.109.1",
		"X-Stainless-Lang": "python", "X-Stainless-Package-Version": "1.109.1"}
	resp, out := e.do("POST", "/v1/chat/completions", chatReq, hdr)
	if resp.StatusCode != 200 || out != chatSSE {
		t.Fatalf("chat SSE not byte-exact (%d): %q", resp.StatusCode, out)
	}
	got := e.oai.last(t)
	if got.path != "/openai/v1/chat/completions" {
		t.Fatalf("upstream path %s", got.path)
	}
	if got.h.Get("Authorization") != "Bearer gsk-provider-secret-0000" || got.h.Get("X-Title") != "rlcd test" ||
		got.h.Get("Http-Referer") != "https://example.org" || got.h.Get("X-Rlcd-Key") != "" {
		t.Fatalf("upstream headers: %v", got.h)
	}
	var sent map[string]any
	_ = json.Unmarshal(got.body, &sent)
	if sent["model"] != "llama-3.3-70b-versatile" {
		t.Fatalf("model not swapped: %v", sent["model"])
	}
	if resp.Header.Get("X-Rlcd-Route") != "groq" {
		t.Errorf("route header %q", resp.Header.Get("X-Rlcd-Route"))
	}
	d := e.lastRecord()
	if d.Alias != "smart" || d.Route != "groq" || d.KeyID != v.ID || d.KeyName != "chatbot" || d.Protocol != ir.ProtocolOpenAIChat ||
		d.ClientModel != "smart" || d.Model != "llama-3.3-70b-versatile" || d.Provider != "custom" || d.ModelVendor != "meta" ||
		d.Client.ID != "openai-python" || d.Client.KeyName != "chatbot" || d.AuthMode != "gateway-key" || !strings.HasPrefix(d.ConversationID, v.ID+":") {
		t.Fatalf("record %+v client %+v", d.Record, d.Client)
	}
	if d.Usage == nil || d.Usage.CacheReadTokens != 384 || d.CostUSD <= 0 {
		t.Fatalf("usage %+v cost %v", d.Usage, d.CostUSD)
	}
	if !strings.Contains(d.RouteReason, "alias 'smart'") {
		t.Errorf("reason %q", d.RouteReason)
	}
	// Usage and cost reach the key.
	for _, k := range e.gw.Keys.List() {
		if k.ID == v.ID && (k.Usage.Requests != 1 || k.Usage.InputTokens != 116 || k.Usage.CostUSD <= 0) {
			t.Fatalf("key usage %+v", k.Usage)
		}
	}
	// Per-key stats.
	_, stats := e.do("GET", "/api/stats", "", nil)
	if !strings.Contains(stats, `"by_key":{"chatbot"`) {
		t.Errorf("stats by key: %s", stats)
	}
	// The key never lands on disk in plaintext.
	assertNotOnDisk(t, e.home, key)
}

func assertNotOnDisk(t *testing.T, dir, secret string) {
	t.Helper()
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), secret) {
			t.Errorf("%s contains the secret", p)
		}
		return nil
	})
}

func TestResponsesAndJSONByteExact(t *testing.T) {
	e := newEnv(t, false)
	// No alias, no default route: the F3 passthrough to OpenAI by login.
	e.oai.mu.Lock()
	e.oai.resp = respSSE
	e.oai.mu.Unlock()
	body := `{"model":"gpt-5","stream":true,"input":"hello"}`
	resp, out := e.do("POST", "/openai/v1/responses", body, map[string]string{"Authorization": "Bearer sk-proj-client-key"})
	if resp.StatusCode != 200 || out != respSSE {
		t.Fatalf("responses SSE (%d): %q", resp.StatusCode, out)
	}
	got := e.oai.last(t)
	if got.path != "/v1/responses" || string(got.body) != body || got.h.Get("Authorization") != "Bearer sk-proj-client-key" {
		t.Fatalf("passthrough changed the call: %s %s %v", got.path, got.body, got.h.Get("Authorization"))
	}
	if d := e.lastRecord(); d.Route != "openai" || d.Usage == nil || d.Usage.CacheReadTokens != 1000 {
		t.Fatalf("record %+v", d.Record)
	}

	e.oai.mu.Lock()
	e.oai.resp, e.oai.sse = chatJSON, false
	e.oai.mu.Unlock()
	cb := `{"model":"gpt-4.1","messages":[{"role":"user","content":"x"}]}`
	if resp, out := e.do("POST", "/v1/chat/completions", cb, map[string]string{"Authorization": "Bearer sk-proj-client-key"}); resp.StatusCode != 200 || out != chatJSON {
		t.Fatalf("chat JSON not byte-exact: %q", out)
	}
	if d := e.lastRecord(); d.Usage == nil || d.Usage.InputTokens != 50 || d.Model != "gpt-4.1" {
		t.Fatalf("record %+v %+v", d.Record, d.Usage)
	}
	// The pipeline ran: the pruner reported on the OpenAI request (shadow).
	if d := e.lastRecord(); d.Stages["prune"] == nil {
		t.Fatalf("pruning stage did not run on the OpenAI path: %+v", d.StageErrors)
	}
}

func TestCrossProtocolAliasRefused(t *testing.T) {
	e := newEnv(t, false)
	body := strings.Replace(chatReq, `"smart"`, `"claude"`, 1)
	resp, out := e.do("POST", "/v1/chat/completions", body, map[string]string{"Authorization": "Bearer sk-x"})
	if resp.StatusCode != 400 || !strings.Contains(out, "does not translate") || !strings.Contains(out, "openrouter.ai/api/v1") {
		t.Fatalf("expected a clear refusal: %d %s", resp.StatusCode, out)
	}
	if e.oai.count()+e.anth.count() != 0 {
		t.Fatal("a refused call reached a provider")
	}
	// The other way round: an OpenAI route for an Anthropic request.
	resp, out = e.do("POST", "/v1/messages", strings.Replace(anthReq, "claude-sonnet-4-5", "smart", 1), nil)
	if resp.StatusCode != 400 || !strings.Contains(out, `"type":"error"`) {
		t.Fatalf("anthropic side: %d %s", resp.StatusCode, out)
	}
}

func TestModelsEndpoint(t *testing.T) {
	e := newEnv(t, false)
	key, _ := e.key("limited", keys.Limits{Aliases: []string{"smart", "claude"}})
	for _, path := range []string{"/v1/models", "/openai/v1/models"} {
		resp, out := e.do("GET", path, "", map[string]string{"Authorization": "Bearer " + key})
		var raw struct {
			Object string           `json:"object"`
			Data   []map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(out), &raw)
		if resp.StatusCode != 200 || raw.Object != "list" || len(raw.Data) != 2 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, out)
		}
		if raw.Data[0]["id"] != "smart" || raw.Data[0]["object"] != "model" || raw.Data[0]["type"] != "model" {
			t.Fatalf("%s entry: %v", path, raw.Data[0])
		}
	}
	// Without a key, aliases exist: an OpenAI SDK gets the list too.
	if _, out := e.do("GET", "/v1/models", "", map[string]string{"Authorization": "Bearer sk-x"}); !strings.Contains(out, `"fast"`) {
		t.Fatalf("keyless OpenAI client: %s", out)
	}
	if e.anth.count() != 0 {
		t.Fatal("alias list went upstream")
	}
}

func TestGatewayKeyAuth(t *testing.T) {
	e := newEnv(t, true)
	loop := func(h map[string]string) map[string]string {
		if h == nil {
			h = map[string]string{}
		}
		return h
	}
	// Missing.
	resp, out := e.do("POST", "/v1/chat/completions", chatReq, loop(nil))
	if resp.StatusCode != 401 || !strings.Contains(out, "gateway key is required") || !strings.Contains(out, "invalid_api_key") {
		t.Fatalf("missing: %d %s", resp.StatusCode, out)
	}
	// Missing on the Anthropic path: Anthropic error shape.
	resp, out = e.do("POST", "/v1/messages", anthReq, map[string]string{"Authorization": "Bearer sk-ant-oat01-x"})
	if resp.StatusCode != 401 || !strings.Contains(out, `"authentication_error"`) {
		t.Fatalf("anthropic missing: %d %s", resp.StatusCode, out)
	}
	// Wrong.
	resp, _ = e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer rlcd-0000"})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	// Valid, then revoked.
	key, v := e.key("app", keys.Limits{})
	if resp, out := e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer " + key}); resp.StatusCode != 200 {
		t.Fatalf("valid key: %d %s", resp.StatusCode, out)
	}
	if got := e.oai.last(t).h.Get("Authorization"); got != "Bearer gsk-provider-secret-0000" {
		t.Fatalf("provider key not injected: %q", got)
	}
	_, err := e.gw.Keys.Revoke(v.ID)
	must(t, err)
	resp, out = e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer " + key})
	if resp.StatusCode != 401 || !strings.Contains(out, "revoked") {
		t.Fatalf("revoked: %d %s", resp.StatusCode, out)
	}
	// Rate limited.
	rk, _ := e.key("slow", keys.Limits{RPM: 2})
	for i := 0; i < 2; i++ {
		if resp, _ := e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"X-Api-Key": rk}); resp.StatusCode != 200 {
			t.Fatalf("call %d under the limit: %d", i, resp.StatusCode)
		}
	}
	resp, out = e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"X-Api-Key": rk})
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || !strings.Contains(out, "rate_limit_exceeded") {
		t.Fatalf("rate limit: %d %s %v", resp.StatusCode, out, resp.Header)
	}
	// Restricted to some aliases.
	ak, _ := e.key("only-fast", keys.Limits{Aliases: []string{"fast"}})
	resp, out = e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer " + ak})
	if resp.StatusCode != 403 || !strings.Contains(out, "fast") {
		t.Fatalf("alias restriction: %d %s", resp.StatusCode, out)
	}
	// Daily token limit: the first call completes, the next is refused.
	qk, _ := e.key("quota", keys.Limits{TokensPerDay: 100})
	if resp, _ := e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer " + qk}); resp.StatusCode != 200 {
		t.Fatalf("first quota call: %d", resp.StatusCode)
	}
	resp, out = e.do("POST", "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer " + qk})
	if resp.StatusCode != 429 || !strings.Contains(out, "daily token limit") {
		t.Fatalf("quota: %d %s", resp.StatusCode, out)
	}
	// A passthrough route cannot serve a request that only carries a key.
	ck, _ := e.key("claude", keys.Limits{})
	resp, out = e.do("POST", "/v1/messages", anthReq, map[string]string{"X-Api-Key": ck})
	if resp.StatusCode != 401 || !strings.Contains(out, "X-Rlcd-Key") {
		t.Fatalf("passthrough with only a key: %d %s", resp.StatusCode, out)
	}
	// With X-Rlcd-Key next to its own login, a subscription still works.
	resp, out = e.do("POST", "/v1/messages", anthReq, map[string]string{"X-Rlcd-Key": ck, "Authorization": "Bearer sk-ant-oat01-own"})
	if resp.StatusCode != 200 || out != anthSSE {
		t.Fatalf("key plus own login: %d %s", resp.StatusCode, out)
	}
	if got := e.anth.last(t).h; got.Get("Authorization") != "Bearer sk-ant-oat01-own" || got.Get("X-Rlcd-Key") != "" {
		t.Fatalf("forwarded headers: %v", got)
	}
	// No key ever reaches a log or a provider.
	for _, k := range []string{key, rk, ak, qk, ck} {
		assertNotOnDisk(t, e.home, k)
		for _, u := range []*upstream{e.oai, e.anth} {
			u.mu.Lock()
			for _, h := range u.hits {
				for _, vs := range h.h {
					if strings.Contains(strings.Join(vs, " "), k) {
						t.Errorf("a gateway key reached a provider")
					}
				}
			}
			u.mu.Unlock()
		}
	}
}

// guardDo calls the handler directly, so the peer address can be remote.
func (e *env) guardDo(method, path, host, remote string, h map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host, req.RemoteAddr = host, remote
	for k, v := range h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.gw.Handler.ServeHTTP(rec, req)
	return rec
}

func TestNonLoopbackBoundary(t *testing.T) {
	e := newEnv(t, true)
	lan, remote := "192.168.1.20:4777", "192.168.1.77:53211"

	// The dashboard API refuses a remote browser without the admin token...
	if rec := e.guardDo("GET", "/api/config", lan, remote, nil); rec.Code != 401 || !strings.Contains(rec.Body.String(), "admin_login") {
		t.Fatalf("remote /api without token: %d %s", rec.Code, rec.Body)
	}
	if rec := e.guardDo("GET", "/api/requests", lan, remote, map[string]string{"Authorization": "Bearer wrong-token-0000000000000"}); rec.Code != 401 {
		t.Fatalf("wrong admin token: %d", rec.Code)
	}
	// ...a gateway key is not an admin token...
	key, _ := e.key("app", keys.Limits{})
	if rec := e.guardDo("GET", "/api/keys", lan, remote, map[string]string{"Authorization": "Bearer " + key}); rec.Code != 401 {
		t.Fatalf("gateway key opened the dashboard: %d", rec.Code)
	}
	// ...and accepts it with the admin token, as a header or as the cookie.
	if rec := e.guardDo("GET", "/api/config", lan, remote, map[string]string{"Authorization": "Bearer " + adminToken}); rec.Code != 200 {
		t.Fatalf("admin token: %d", rec.Code)
	}
	login := httptest.NewRequest("POST", "http://"+lan+"/auth/admin", strings.NewReader(`{"token":"`+adminToken+`"}`))
	login.Host, login.RemoteAddr = lan, remote
	rec := httptest.NewRecorder()
	e.gw.Handler.ServeHTTP(rec, login)
	cookie := rec.Result().Cookies()
	if rec.Code != 200 || len(cookie) != 1 || !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteStrictMode || strings.Contains(cookie[0].Value, adminToken) {
		t.Fatalf("login: %d %+v", rec.Code, cookie)
	}
	if rec := e.guardDo("GET", "/api/config", lan, remote, map[string]string{"Cookie": cookie[0].Name + "=" + cookie[0].Value}); rec.Code != 200 {
		t.Fatalf("cookie: %d", rec.Code)
	}
	// A cross-site page cannot ride the session to change anything.
	if rec := e.guardDo("PUT", "/api/route", lan, remote, map[string]string{"Authorization": "Bearer " + adminToken, "Origin": "https://evil.example"}); rec.Code != 403 {
		t.Fatalf("cross-origin write: %d", rec.Code)
	}
	// This machine's own browser needs no token.
	if rec := e.guardDo("GET", "/api/config", "127.0.0.1:4777", "127.0.0.1:50000", nil); rec.Code != 200 {
		t.Fatalf("local dashboard: %d", rec.Code)
	}
	// A remote peer claiming a loopback Host is still remote.
	if rec := e.guardDo("GET", "/api/config", "127.0.0.1:4777", remote, nil); rec.Code != 401 {
		t.Fatalf("spoofed loopback host: %d", rec.Code)
	}
	// DNS rebinding: a name that is not ours is refused on every path.
	for _, p := range []string{"/api/config", "/v1/chat/completions", "/ui/"} {
		if rec := e.guardDo("GET", p, "attacker.example:4777", "127.0.0.1:50000", nil); rec.Code != 403 {
			t.Fatalf("rebinding host on %s: %d", p, rec.Code)
		}
	}
	// An allowed host name works.
	c := e.cs.Get()
	c.AllowedHosts = []string{"gateway.lan"}
	e.gw.Guard.Config = config.NewStore(&c)
	if rec := e.guardDo("GET", "/api/config", "gateway.lan:4777", remote, map[string]string{"Authorization": "Bearer " + adminToken}); rec.Code != 200 {
		t.Fatalf("allowed host: %d", rec.Code)
	}
	// MCP recall needs a key too.
	if rec := e.guardDo("POST", "/mcp", lan, remote, nil); rec.Code != 401 {
		t.Fatalf("mcp without key: %d", rec.Code)
	}
}

func TestNonLoopbackWithoutAdminToken(t *testing.T) {
	e := newEnv(t, true)
	e.gw.Guard.AdminToken = ""
	h := e.gw.Guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, p := range []string{"/ui/", "/", "/api/config"} {
		req := httptest.NewRequest("GET", "http://192.168.1.20:4777"+p, nil)
		req.RemoteAddr = "192.168.1.77:5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("%s served to a remote browser without an admin token", p)
		}
	}
}

func TestLoopbackHostCheck(t *testing.T) {
	e := newEnv(t, false)
	_, port, _ := net.SplitHostPort(e.srv.Listener.Addr().String())
	for host, want := range map[string]int{
		"127.0.0.1:" + port:    200,
		"localhost:" + port:    200,
		"127.0.0.1:1":          403, // another port
		"evil.example:" + port: 403, // rebinding
		"127.0.0.1":            403, // no port
	} {
		rec := e.guardDo("GET", "/api/config", host, "127.0.0.1:5000", nil)
		if rec.Code != want {
			t.Errorf("Host %q: %d, want %d", host, rec.Code, want)
		}
	}
	// Loopback without keys: no key needed, as before.
	if resp, _ := e.do("POST", "/v1/messages", anthReq, map[string]string{"Authorization": "Bearer sk-ant-oat01-x"}); resp.StatusCode != 200 {
		t.Fatalf("loopback without key: %d", resp.StatusCode)
	}
	// require_keys turns them on anyway.
	must(t, e.cs.SetRequireKeys(true))
	if resp, _ := e.do("POST", "/v1/messages", anthReq, map[string]string{"Authorization": "Bearer sk-ant-oat01-x"}); resp.StatusCode != 401 {
		t.Fatalf("require_keys: %d", resp.StatusCode)
	}
}
