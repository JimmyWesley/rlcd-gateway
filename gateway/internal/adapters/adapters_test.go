package adapters

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// A Codex-shaped Responses request: developer instructions, a user turn, a
// tool call with its output, encrypted reasoning and an image.
const responsesReq = `{"model":"gpt-5-codex","stream":true,"instructions":"You are Codex.","prompt_cache_key":"sess-123",
 "tools":[{"type":"function","name":"shell","description":"Run a command","parameters":{}},{"type":"web_search"}],
 "input":[
  {"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},
  {"type":"reasoning","summary":[],"encrypted_content":"gAAAAB..."},
  {"type":"function_call","name":"shell","arguments":"{\"cmd\":[\"ls\"]}","call_id":"call_1"},
  {"type":"function_call_output","call_id":"call_1","output":"README.md\nmain.go"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Found it."}]}
 ]}`

const responsesSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5-codex","usage":null}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5-codex-2025","usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1000},"output_tokens":40,"output_tokens_details":{"reasoning_tokens":12},"total_tokens":1240}}}` + "\n\n"

const chatReq = `{"model":"gpt-4.1","stream":true,"stream_options":{"include_usage":true},
 "tools":[{"type":"function","function":{"name":"read","description":"Read a file"}}],
 "messages":[
  {"role":"system","content":"Be brief."},
  {"role":"user","content":[{"type":"text","text":"hello"}]},
  {"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},
  {"role":"tool","tool_call_id":"t1","content":"file body"}
 ]}`

const chatSSE = `data: {"id":"c1","model":"gpt-4.1-2025","choices":[{"delta":{"content":"hi"}}],"usage":null}` + "\n\n" +
	`data: {"id":"c1","model":"gpt-4.1-2025","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":384}}}` + "\n\n" +
	"data: [DONE]\n\n"

const jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJlc2VjcmV0"

type seen struct {
	path, auth, account, beta string
	body                      []byte
}

type fixture struct {
	gw   *httptest.Server
	st   *store.Store
	got  *seen
	cs   *config.Store
	resp func(w http.ResponseWriter, r *http.Request)
}

func newFixture(t *testing.T, logBodies bool) *fixture {
	t.Helper()
	f := &fixture{got: &seen{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.got.path = r.URL.RequestURI()
		f.got.auth, f.got.account, f.got.beta = r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"), r.Header.Get("OpenAI-Beta")
		f.got.body, _ = io.ReadAll(r.Body)
		f.resp(w, r)
	}))
	t.Cleanup(up.Close)

	t.Setenv("RLCD_GATEWAY_HOME", t.TempDir())
	cfg := config.Default()
	cfg.LogBodies = logBodies
	f.cs = config.NewStore(cfg)
	raw, _ := json.Marshal(Settings{OpenAIBaseURL: up.URL + "/v1", ChatGPTBaseURL: up.URL + "/backend-api/codex"})
	if err := f.cs.SetSection("adapters", raw); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(config.Dir())
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	mux := http.NewServeMux()
	New(f.cs, st).Register(mux)
	f.gw = httptest.NewServer(mux)
	t.Cleanup(f.gw.Close)
	return f
}

// streamParts writes body in uneven chunks, splitting lines, to exercise the
// incremental SSE parser.
func streamParts(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, cut := range [][2]int{{0, 37}, {37, 150}, {150, len(body)}} {
			_, _ = io.WriteString(w, body[cut[0]:cut[1]])
			w.(http.Flusher).Flush()
		}
	}
}

func (f *fixture) post(t *testing.T, path, body string, h map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.gw.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (f *fixture) last(t *testing.T) *store.Detail {
	t.Helper()
	recs := f.st.Recent()
	if len(recs) == 0 {
		t.Fatal("no record stored")
	}
	d, err := f.st.Get(recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestResponsesSubscriptionStream(t *testing.T) {
	f := newFixture(t, true)
	f.resp = streamParts(responsesSSE)
	resp, got := f.post(t, "/openai/v1/responses", responsesReq, map[string]string{
		"Authorization": "Bearer " + jwt, "ChatGPT-Account-ID": "acct-42", "OpenAI-Beta": "responses=experimental",
	})
	if got != responsesSSE {
		t.Fatalf("stream not passed through byte for byte:\n%q\nwant\n%q", got, responsesSSE)
	}
	if resp.Header.Get("X-Rlcd-Route") != RouteChatGPT {
		t.Errorf("route header = %q", resp.Header.Get("X-Rlcd-Route"))
	}
	// Subscription login goes to the ChatGPT backend, credentials untouched.
	if f.got.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path = %q", f.got.path)
	}
	if f.got.auth != "Bearer "+jwt || f.got.account != "acct-42" || f.got.beta != "responses=experimental" {
		t.Errorf("headers not forwarded: %+v", f.got)
	}
	if string(f.got.body) != responsesReq {
		t.Error("request body changed on the way")
	}

	d := f.last(t)
	if d.Path != "/openai/v1/responses" || d.Route != RouteChatGPT || d.AuthMode != "oauth" || !d.Stream {
		t.Errorf("record = %+v", d.Record)
	}
	want := store.Usage{InputTokens: 200, CacheReadTokens: 1000, OutputTokens: 40}
	if d.Usage == nil || *d.Usage != want {
		t.Errorf("usage = %+v, want %+v", d.Usage, want)
	}
	if d.Model != "gpt-5-codex" || d.ConversationID != "cx-sess-123" {
		t.Errorf("model %q conversation %q", d.Model, d.ConversationID)
	}
	if d.XRay == nil || len(d.XRay.Blocks) == 0 {
		t.Fatal("no X-ray")
	}
	if a := d.RequestHeaders["Authorization"]; strings.Contains(a, "c2lnbmF0dXJl") || !strings.Contains(a, "chars") {
		t.Errorf("Authorization not masked: %q", a)
	}
	if a := d.RequestHeaders["Chatgpt-Account-Id"]; a == "acct-42" {
		t.Error("account id not masked")
	}
	// Nothing on disk may carry the token.
	files, _ := filepath.Glob(filepath.Join(config.Dir(), "requests", "*.json"))
	for _, p := range append(files, filepath.Join(config.Dir(), "index.jsonl")) {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), jwt) || strings.Contains(string(b), "c2lnbmF0dXJlc2VjcmV0") {
			t.Errorf("%s contains the token", p)
		}
	}
}

func TestResponsesAPIKeyNonStreamed(t *testing.T) {
	f := newFixture(t, false)
	f.resp = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_2","model":"gpt-5","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":3}}`)
	}
	body := `{"model":"gpt-5","input":"hello"}`
	resp, _ := f.post(t, "/v1/responses", body, map[string]string{"Authorization": "Bearer sk-proj-abcdefghijklmnopqrstuvwxyz"})
	if resp.StatusCode != 200 || f.got.path != "/v1/responses" || resp.Header.Get("X-Rlcd-Route") != RouteOpenAI {
		t.Fatalf("status %d path %q route %q", resp.StatusCode, f.got.path, resp.Header.Get("X-Rlcd-Route"))
	}
	d := f.last(t)
	if d.Usage == nil || *d.Usage != (store.Usage{InputTokens: 10, OutputTokens: 3}) || d.AuthMode != "api-key" {
		t.Errorf("record %+v usage %+v", d.Record, d.Usage)
	}
	if d.RequestBody != "" || d.ResponseBody != "" {
		t.Error("bodies logged with log_bodies=false")
	}
	if len(d.XRay.Blocks) != 1 || d.XRay.Blocks[0].Key != "m0.b0" || d.XRay.Blocks[0].Role != "user" {
		t.Errorf("string input X-ray: %+v", d.XRay.Blocks)
	}
}

func TestChatCompletionsStreamUsage(t *testing.T) {
	f := newFixture(t, true)
	f.resp = streamParts(chatSSE)
	_, got := f.post(t, "/v1/chat/completions", chatReq, map[string]string{"Authorization": "Bearer sk-test-000000000000000000"})
	if got != chatSSE {
		t.Fatalf("chat stream changed: %q", got)
	}
	if f.got.path != "/v1/chat/completions" {
		t.Errorf("upstream path %q", f.got.path)
	}
	d := f.last(t)
	want := store.Usage{InputTokens: 116, CacheReadTokens: 384, OutputTokens: 7}
	if d.Usage == nil || *d.Usage != want {
		t.Errorf("usage = %+v, want %+v", d.Usage, want)
	}
	if d.Path != "/v1/chat/completions" || d.Model != "gpt-4.1" {
		t.Errorf("record %+v", d.Record)
	}
}

func TestChatNonStreamedUsage(t *testing.T) {
	f := newFixture(t, true)
	f.resp = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4.1","usage":{"prompt_tokens":50,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":20}}}`)
	}
	f.post(t, "/openai/v1/chat/completions", `{"model":"gpt-4.1","messages":[{"role":"user","content":"x"}]}`, nil)
	d := f.last(t)
	if d.Usage == nil || *d.Usage != (store.Usage{InputTokens: 30, CacheReadTokens: 20, OutputTokens: 5}) {
		t.Errorf("usage %+v", d.Usage)
	}
}

func TestOtherPathsPassThroughAndErrors(t *testing.T) {
	f := newFixture(t, true)
	f.resp = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad token"}}`)
	}
	req, _ := http.NewRequest(http.MethodGet, f.gw.URL+"/openai/v1/models?client_version=1.0", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body) // EOF arrives once the record is saved
	resp.Body.Close()
	if resp.StatusCode != 401 || f.got.path != "/backend-api/codex/models?client_version=1.0" {
		t.Fatalf("status %d path %q", resp.StatusCode, f.got.path)
	}
	if d := f.last(t); d.Error != "bad token" || d.Method != "GET" {
		t.Errorf("record %+v", d.Record)
	}
}

func TestAgentsAPIRequiresConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", t.TempDir())
	f := newFixture(t, true)

	// A form post (what a cross-site page could send) is refused.
	resp, err := http.Post(f.gw.URL+"/api/agents/codex/setup", "application/x-www-form-urlencoded", strings.NewReader("confirm=true"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", resp.StatusCode)
	}
	if r, _ := f.post(t, "/api/agents/codex/setup", `{}`, nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("no confirm: %d", r.StatusCode)
	}
	if r, _ := f.post(t, "/api/agents/codex/setup", `{"confirm":true}`, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Errorf("cross origin: %d", r.StatusCode)
	}
	if r, _ := f.post(t, "/api/agents/nope/setup", `{"confirm":true}`, nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d", r.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err == nil {
		t.Fatal("a refused request wrote the config")
	}

	r, body := f.post(t, "/api/agents/codex/setup", `{"confirm":true}`, nil)
	if r.StatusCode != 200 {
		t.Fatalf("setup: %d %s", r.StatusCode, body)
	}
	var res agentResult
	_ = json.Unmarshal([]byte(body), &res)
	if len(res.Agents) != 3 || res.Agents[0].Name != "claude" || !res.Agents[1].Configured {
		t.Errorf("status after setup: %+v", res.Agents)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(b), strings.TrimPrefix(f.gw.URL, "http://")+"/openai/v1") {
		t.Errorf("config does not point at the gateway:\n%s", b)
	}
	if r, _ := f.post(t, "/api/agents/codex/undo", `{"confirm":true}`, nil); r.StatusCode != 200 {
		t.Errorf("undo: %d", r.StatusCode)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); len(b) != 0 {
		t.Errorf("undo left %q", b)
	}

	resp, err = http.Get(f.gw.URL + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 3 || list[0]["try_command"] == "" {
		t.Errorf("GET /api/agents: %+v", list)
	}
}
