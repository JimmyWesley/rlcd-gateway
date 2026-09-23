package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const sseBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":12,"cache_read_input_tokens":900,"cache_creation_input_tokens":40,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":57}}` + "\n\n"

const reqBody = `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,
 "messages":[{"role":"user","content":"hi"},
  {"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"text","text":"yo"}]},
  {"role":"user","content":"again"}]}`

type seen struct {
	auth, apiKey, beta string
	body               map[string]any
}

func setup(t *testing.T, route config.Route) (*httptest.Server, *seen, *store.Store) {
	t.Helper()
	got := &seen{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.apiKey, got.beta = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("Anthropic-Beta")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.Header().Set("Content-Type", "text/event-stream")
		// Split the stream mid-line to exercise the incremental parser.
		for _, part := range []string{sseBody[:50], sseBody[50:200], sseBody[200:]} {
			_, _ = io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(upstream.Close)

	t.Setenv("RLCD_GATEWAY_HOME", t.TempDir())
	route.BaseURL = upstream.URL
	cs, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := cs.Get()
	cfg.Routes["test"] = route
	cfg.ActiveRoute = "test"
	st, err := store.Open(config.Dir())
	if err != nil {
		t.Fatal(err)
	}
	cs2 := config.NewStore(&cfg)
	gw := httptest.NewServer(New(cs2, st))
	t.Cleanup(gw.Close)
	return gw, got, st
}

func post(t *testing.T, url string, h map[string]string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/messages?beta=true", strings.NewReader(reqBody))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestPassthroughKeepsSubscriptionAuth(t *testing.T) {
	gw, got, st := setup(t, config.Route{Kind: config.KindAnthropic, Auth: config.AuthPassthrough})
	out := post(t, gw.URL, map[string]string{
		"Authorization": "Bearer sk-ant-oat01-secret", "Anthropic-Beta": "oauth-2025-04-20,fine-grained",
	})
	if out != sseBody {
		t.Fatalf("stream altered:\n%q", out)
	}
	if got.auth != "Bearer sk-ant-oat01-secret" || got.beta != "oauth-2025-04-20,fine-grained" {
		t.Errorf("auth not passed through: %q / %q", got.auth, got.beta)
	}
	if n := len(got.body["messages"].([]any)[1].(map[string]any)["content"].([]any)); n != 2 {
		t.Errorf("thinking must survive on anthropic routes, got %d blocks", n)
	}
	recs := st.Recent()
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	r := recs[0]
	if r.AuthMode != "oauth" || r.Usage == nil || r.Usage.CacheReadTokens != 900 || r.Usage.OutputTokens != 57 || r.Usage.InputTokens != 12 {
		t.Errorf("bad record: %+v usage=%+v", r, r.Usage)
	}
	d, err := st.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.RequestHeaders["Authorization"], "secret") {
		t.Error("token leaked into the log")
	}
}

func TestKeyRouteSwapsAuthModelAndStripsThinking(t *testing.T) {
	gw, got, st := setup(t, config.Route{Kind: config.KindOpenRouter, Auth: config.AuthKey,
		APIKey: "or-key", Model: "qwen/qwen3-coder"})
	post(t, gw.URL, map[string]string{
		"Authorization": "Bearer sk-ant-oat01-secret", "Anthropic-Beta": "oauth-2025-04-20,fine-grained",
	})
	if got.auth != "Bearer or-key" || got.apiKey != "" {
		t.Errorf("auth = %q api-key = %q", got.auth, got.apiKey)
	}
	if got.beta != "fine-grained" {
		t.Errorf("oauth beta should be dropped, got %q", got.beta)
	}
	if got.body["model"] != "qwen/qwen3-coder" {
		t.Errorf("model = %v", got.body["model"])
	}
	content := got.body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "text" {
		t.Errorf("thinking not stripped: %v", content)
	}
	r := st.Recent()[0]
	if r.ClientModel != "claude-sonnet-4-5" || r.Model != "qwen/qwen3-coder" || r.StrippedThinking != 1 {
		t.Errorf("record = %+v", r)
	}
}

func TestKeyRouteWithoutKeyFailsLoudly(t *testing.T) {
	gw, _, _ := setup(t, config.Route{Kind: config.KindOpenRouter, Auth: config.AuthKey, APIKeyEnv: "NOPE_UNSET"})
	out := post(t, gw.URL, nil)
	if !strings.Contains(out, "needs a key") {
		t.Errorf("expected a clear error, got %s", out)
	}
}
