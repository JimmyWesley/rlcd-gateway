package recall

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

var bigRead = strings.Repeat("package main // line of a file\n", 200)

func fixtureBody(t *testing.T, firstResult any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4-5",
		"system":   []any{map[string]any{"type": "text", "text": "You are a coding agent."}},
		"tools":    []any{map[string]any{"name": "Bash", "description": "Run a command", "input_schema": map[string]any{"type": "object"}}},
		"metadata": map[string]any{"user_id": "user_x_account_y_session_1b2c3d4e-aaaa-bbbb-cccc-123456789abc"},
		"messages": []any{
			map[string]any{"role": "user", "content": "fix the failing test"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Running the tests."},
				map[string]any{"type": "tool_use", "id": "toolu_A", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_A", "content": firstResult},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_B", "name": "Read", "input": map[string]any{"path": "main.go"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_B", "content": bigRead},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var firstResultParts = []any{
	map[string]any{"type": "text", "text": "FAIL TestLogin"},
	map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}},
	map[string]any{"type": "text", "text": "login_test.go:12: want 200, got 500"},
}

type env struct {
	t   *testing.T
	srv *httptest.Server
	cfg *config.Store
	rs  *Server
}

// newEnv builds a temp config dir and store with three requests: req1 (full
// body), nobody (log_bodies off) and req2 (whose toolu_A result is already a
// marker pointing at req1), and serves the real Register wiring.
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RLCD_GATEWAY_HOME", dir)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	body := fixtureBody(t, firstResultParts)
	body2 := fixtureBody(t, pipeline.Marker("req1", "toolu_A", 20))
	for _, d := range []*store.Detail{
		{Record: store.Record{ID: "req1", ConversationID: pipeline.ConversationID(nil, []byte(body))}, RequestBody: body},
		{Record: store.Record{ID: "nobody", ConversationID: "cc-x"}},
		{Record: store.Record{ID: "req2", ConversationID: pipeline.ConversationID(nil, []byte(body2))}, RequestBody: body2},
	} {
		if err := st.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NewStore(config.Default())
	rs := New(cfg, st)
	mux := http.NewServeMux()
	rs.Register(mux)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proxy", http.StatusTeapot)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, cfg: cfg, rs: rs}
}

type resp struct {
	status int
	header http.Header
	raw    string
	msg    map[string]any
}

func (r resp) errCode() int {
	e, _ := r.msg["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return int(c)
}

func (r resp) result() map[string]any {
	res, _ := r.msg["result"].(map[string]any)
	return res
}

func (e *env) post(body string, hdr map[string]string) resp {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.do(req)
}

func (e *env) do(req *http.Request) resp {
	e.t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: string(b)}
	_ = json.Unmarshal(b, &out.msg)
	return out
}

// modern builds a 2026-07-28 request with matching headers.
func (e *env) modern(id int, method string, p map[string]any) resp {
	e.t.Helper()
	if p == nil {
		p = map[string]any{}
	}
	p["_meta"] = map[string]any{
		metaVersion:      modernVersion,
		metaClientInfo:   map[string]any{"name": "test-client", "version": "1.0"},
		metaCapabilities: map[string]any{},
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": p})
	h := map[string]string{hdrVersion: modernVersion, hdrMethod: method}
	if n, ok := p["name"].(string); ok {
		h[hdrName] = n
	}
	return e.post(string(b), h)
}

func callParams(args map[string]any) map[string]any {
	return map[string]any{"name": ToolName, "arguments": args}
}

func toolText(t *testing.T, r resp) (string, bool) {
	t.Helper()
	res := r.result()
	if res == nil {
		t.Fatalf("no result: %d %s", r.status, r.raw)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content: %s", r.raw)
	}
	c := content[0].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return c["text"].(string), isErr
}

func TestLegacyHandshake(t *testing.T) {
	e := newEnv(t)
	init := e.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.0"}}}`, nil)
	if init.status != 200 || init.result()["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %d %s", init.status, init.raw)
	}
	if ct := init.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	caps, _ := init.result()["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("no tools capability: %s", init.raw)
	}
	sid := init.header.Get(hdrSession)
	if sid == "" {
		t.Fatal("no session id")
	}
	h := map[string]string{hdrSession: sid, hdrVersion: "2025-06-18"}

	n := e.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, h)
	if n.status != http.StatusAccepted || n.raw != "" {
		t.Fatalf("initialized: %d %q", n.status, n.raw)
	}

	list := e.post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, h)
	tools, _ := list.result()["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != ToolName {
		t.Fatalf("tools/list: %s", list.raw)
	}
	schema := tools[0].(map[string]any)["inputSchema"].(map[string]any)
	if props := schema["properties"].(map[string]any); props["key"] == nil || props["req"] == nil || props["marker"] == nil {
		t.Errorf("schema: %v", schema)
	}
	if _, ok := list.result()["resultType"]; ok {
		t.Error("legacy results carry no resultType")
	}

	call := e.post(`{"jsonrpc":"2.0","id":"c3","method":"tools/call","params":{"name":"rlcd_recall","arguments":{"key":"toolu_A","req":"req1"}}}`, h)
	if call.msg["id"] != "c3" {
		t.Errorf("id not echoed: %s", call.raw)
	}
	text, isErr := toolText(t, call)
	if isErr {
		t.Fatalf("recall failed: %s", text)
	}
	want := "FAIL TestLogin\n[image: image/png, 4 bytes of base64 — not restorable as text]\nlogin_test.go:12: want 200, got 500"
	if !strings.HasSuffix(text, "\n"+want) {
		t.Errorf("content:\n%s", text)
	}
	if !strings.HasPrefix(text, "[rlcd recall · key toolu_A · req req1 · m2.b0 · toolu_A · Bash · ") {
		t.Errorf("header line: %s", strings.SplitN(text, "\n", 2)[0])
	}

	if p := e.post(`{"jsonrpc":"2.0","id":4,"method":"ping"}`, h); p.status != 200 || p.result() == nil {
		t.Errorf("ping: %d %s", p.status, p.raw)
	}

	del, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/mcp", nil)
	del.Header.Set(hdrSession, sid)
	if r := e.do(del); r.status != http.StatusNoContent {
		t.Errorf("delete: %d", r.status)
	}
	if r := e.post(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, h); r.status != http.StatusNotFound {
		t.Errorf("request on ended session: %d %s", r.status, r.raw)
	}
	if r := e.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, h); r.status != http.StatusNotFound {
		t.Errorf("notification on ended session: %d", r.status)
	}
	// Without a session id the server still answers (it needs no state).
	if r := e.post(`{"jsonrpc":"2.0","id":6,"method":"tools/list"}`, nil); r.status != 200 {
		t.Errorf("sessionless: %d", r.status)
	}
}

func TestVersionNegotiation(t *testing.T) {
	e := newEnv(t)
	for req, want := range map[string]string{"2025-03-26": "2025-03-26", "2025-11-25": "2025-11-25", "2099-01-01": "2025-11-25", "2026-07-28": "2025-11-25"} {
		r := e.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+req+`","capabilities":{}}}`, nil)
		if got := r.result()["protocolVersion"]; got != want {
			t.Errorf("initialize %s: got %v, want %s", req, got, want)
		}
	}

	d := e.modern(1, "server/discover", nil)
	if d.status != 200 || d.result()["resultType"] != "complete" {
		t.Fatalf("discover: %d %s", d.status, d.raw)
	}
	vs, _ := d.result()["supportedVersions"].([]any)
	if len(vs) == 0 || vs[0] != modernVersion {
		t.Errorf("supportedVersions: %v", vs)
	}

	// Unsupported modern version: 400 with the supported list.
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"` + metaVersion + `":"2099-01-01","` + metaCapabilities + `":{}}}}`
	r := e.post(body, map[string]string{hdrVersion: "2099-01-01", hdrMethod: "tools/list"})
	if r.status != 400 || r.errCode() != codeUnsupportedVersion {
		t.Fatalf("unsupported: %d %s", r.status, r.raw)
	}
	data := r.msg["error"].(map[string]any)["data"].(map[string]any)
	if data["requested"] != "2099-01-01" || len(data["supported"].([]any)) != 4 {
		t.Errorf("data: %v", data)
	}

	// Legacy header with an unknown version: 400.
	if r := e.post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, map[string]string{hdrVersion: "1999-01-01"}); r.status != 400 || r.errCode() != codeUnsupportedVersion {
		t.Errorf("legacy unknown header: %d %s", r.status, r.raw)
	}
	// Modern header without the required _meta: 400, -32602.
	if r := e.post(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/list"}); r.status != 400 || r.errCode() != codeInvalidParams {
		t.Errorf("modern without meta: %d %s", r.status, r.raw)
	}
}

func TestModernHeaders(t *testing.T) {
	e := newEnv(t)
	mk := func(method, name string, meta map[string]any) string {
		p := map[string]any{"_meta": meta}
		if name != "" {
			p["name"] = name
			p["arguments"] = map[string]any{"key": "toolu_A", "req": "req1"}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": method, "params": p})
		return string(b)
	}
	full := map[string]any{metaVersion: modernVersion, metaCapabilities: map[string]any{}}
	call := mk("tools/call", ToolName, full)
	cases := []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
		code   int
	}{
		{"no version header", call, map[string]string{hdrMethod: "tools/call", hdrName: ToolName}, 400, codeHeaderMismatch},
		{"version mismatch", call, map[string]string{hdrVersion: "2025-11-25", hdrMethod: "tools/call", hdrName: ToolName}, 400, codeHeaderMismatch},
		{"no method header", call, map[string]string{hdrVersion: modernVersion, hdrName: ToolName}, 400, codeHeaderMismatch},
		{"method mismatch", call, map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/list", hdrName: ToolName}, 400, codeHeaderMismatch},
		{"no name header", call, map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/call"}, 400, codeHeaderMismatch},
		{"name mismatch", call, map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/call", hdrName: "other"}, 400, codeHeaderMismatch},
		{"base64 name", call, map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/call",
			hdrName: "=?base64?" + base64.StdEncoding.EncodeToString([]byte(ToolName)) + "?="}, 200, 0},
		{"no capabilities", mk("tools/list", "", map[string]any{metaVersion: modernVersion}),
			map[string]string{hdrVersion: modernVersion, hdrMethod: "tools/list"}, 400, codeInvalidParams},
		{"unknown method", mk("prompts/list", "", full), map[string]string{hdrVersion: modernVersion, hdrMethod: "prompts/list"}, 404, codeMethodNotFound},
		{"ping is gone", mk("ping", "", full), map[string]string{hdrVersion: modernVersion, hdrMethod: "ping"}, 404, codeMethodNotFound},
	}
	for _, c := range cases {
		r := e.post(c.body, c.hdr)
		if r.status != c.status || r.errCode() != c.code {
			t.Errorf("%s: got %d/%d, want %d/%d: %s", c.name, r.status, r.errCode(), c.status, c.code, r.raw)
		}
	}
}

func TestModernFlow(t *testing.T) {
	e := newEnv(t)
	list := e.modern(1, "tools/list", nil)
	res := list.result()
	if res["resultType"] != "complete" || res["ttlMs"] == nil || res["cacheScope"] == nil {
		t.Fatalf("tools/list: %s", list.raw)
	}
	if meta, _ := res["_meta"].(map[string]any); meta[metaServerInfo] == nil {
		t.Errorf("no serverInfo: %s", list.raw)
	}
	if list.header.Get(hdrSession) != "" {
		t.Error("modern responses must not mint sessions")
	}

	// By ir key.
	call := e.modern(2, "tools/call", callParams(map[string]any{"key": "m4.b0", "req": "req1"}))
	if call.result()["resultType"] != "complete" {
		t.Errorf("resultType: %s", call.raw)
	}
	text, isErr := toolText(t, call)
	if isErr || !strings.HasSuffix(text, "\n"+bigRead) || !strings.Contains(text, "· m4.b0 · toolu_B · Read ·") {
		t.Errorf("by ir key: %v %.200s", isErr, text)
	}
	// System prompt and tool definition by key.
	if text, _ := toolText(t, e.modern(3, "tools/call", callParams(map[string]any{"key": "sys.0", "req": "req1"}))); !strings.HasSuffix(text, "\nYou are a coding agent.") {
		t.Errorf("sys.0: %s", text)
	}
	if text, _ := toolText(t, e.modern(4, "tools/call", callParams(map[string]any{"key": "tool.0", "req": "req1"}))); !strings.Contains(text, `"name": "Bash"`) {
		t.Errorf("tool.0: %s", text)
	}
	// A tool_use block by key renders name, id and input.
	if text, _ := toolText(t, e.modern(5, "tools/call", callParams(map[string]any{"key": "m1.b1", "req": "req1"}))); !strings.Contains(text, "tool_use Bash (id toolu_A) input:") {
		t.Errorf("m1.b1: %s", text)
	}
}

func TestJSONRPCErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		body   string
		status int
		code   int
	}{
		{"parse error", `{"jsonrpc":`, 400, codeParseError},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, 400, codeInvalidRequest},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, 400, codeInvalidRequest},
		{"fractional id", `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, 400, codeInvalidRequest},
		{"no method", `{"jsonrpc":"2.0","id":1}`, 400, codeInvalidRequest},
		{"params not object", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[1]}`, 400, codeInvalidParams},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, 200, codeMethodNotFound},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`, 200, codeInvalidParams},
		{"arguments not object", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rlcd_recall","arguments":"x"}}`, 200, codeInvalidParams},
	}
	for _, c := range cases {
		r := e.post(c.body, nil)
		if r.status != c.status || r.errCode() != c.code {
			t.Errorf("%s: got %d/%d, want %d/%d: %s", c.name, r.status, r.errCode(), c.status, c.code, r.raw)
		}
	}

	// Batches: only 2025-03-26 (the default when no header is sent).
	b := e.post(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"}]`, nil)
	var arr []map[string]any
	if b.status != 200 || json.Unmarshal([]byte(b.raw), &arr) != nil || len(arr) != 1 {
		t.Errorf("batch: %d %s", b.status, b.raw)
	}
	if r := e.post(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`, nil); r.status != 202 || r.raw != "" {
		t.Errorf("notification-only batch: %d %q", r.status, r.raw)
	}
	if r := e.post(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, map[string]string{hdrVersion: "2025-06-18"}); r.status != 400 {
		t.Errorf("batch on 2025-06-18: %d", r.status)
	}

	// Transport-level rejections.
	get, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/mcp", nil)
	get.Header.Set("Accept", "text/event-stream")
	if r := e.do(get); r.status != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", r.status)
	}
	put, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/mcp", nil)
	if r := e.do(put); r.status != http.StatusMethodNotAllowed {
		t.Errorf("PUT must not reach the proxy: %d", r.status)
	}
	if r := e.post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://evil.example"}); r.status != http.StatusForbidden {
		t.Errorf("foreign origin: %d", r.status)
	}
	if r := e.post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "http://localhost:5177"}); r.status != 200 {
		t.Errorf("local origin: %d", r.status)
	}
	if r := e.post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Accept": "text/html"}); r.status != http.StatusNotAcceptable {
		t.Errorf("accept: %d", r.status)
	}
	if r := e.post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Content-Type": "text/plain"}); r.status != http.StatusUnsupportedMediaType {
		t.Errorf("content-type: %d", r.status)
	}
}

func TestNotificationsAndResponses(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"legacy notification":   `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		"modern notification":   `{"jsonrpc":"2.0","method":"notifications/whatever","params":{"_meta":{"` + metaVersion + `":"2026-07-28"}}}`,
		"client response":       `{"jsonrpc":"2.0","id":7,"result":{}}`,
		"client error response": `{"jsonrpc":"2.0","id":7,"error":{"code":-1,"message":"no"}}`,
	} {
		r := e.post(body, nil)
		if r.status != http.StatusAccepted || r.raw != "" {
			t.Errorf("%s: %d %q", name, r.status, r.raw)
		}
	}
}

func recallText(t *testing.T, e *env, args map[string]any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": callParams(args)})
	return toolText(t, e.post(string(b), nil))
}

func TestRecallCases(t *testing.T) {
	e := newEnv(t)
	marker := pipeline.Marker("req1", "toolu_B", 1500)

	for name, c := range map[string]struct {
		args    map[string]any
		isErr   bool
		contain string
	}{
		"marker argument":      {map[string]any{"marker": marker}, false, bigRead},
		"marker pasted in key": {map[string]any{"key": "see " + marker}, false, bigRead},
		"quoted values":        {map[string]any{"key": " `toolu_B` ", "req": "\"req1\""}, false, bigRead},
		"follows a marker":     {map[string]any{"key": "toolu_A", "req": "req2"}, false, "FAIL TestLogin"},
		"unknown request":      {map[string]any{"key": "toolu_A", "req": "nope"}, true, `no request with id "nope"`},
		"body not logged":      {map[string]any{"key": "toolu_A", "req": "nobody"}, true, "log_bodies is off"},
		"key not found":        {map[string]any{"key": "toolu_Z", "req": "req1"}, true, `no block with key "toolu_Z"`},
		"missing args":         {map[string]any{"key": "toolu_A"}, true, "needs both key and req"},
	} {
		text, isErr := recallText(t, e, c.args)
		if isErr != c.isErr || !strings.Contains(text, c.contain) {
			t.Errorf("%s: isErr=%v\n%.300s", name, isErr, text)
		}
	}
	// The followed marker reports where the content really came from.
	if text, _ := recallText(t, e, map[string]any{"key": "toolu_A", "req": "req2"}); !strings.HasPrefix(text, "[rlcd recall · key toolu_A · req req1 ·") {
		t.Errorf("followed header: %.120s", text)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	if err := e.cfg.SetSection("recall", json.RawMessage(`{"max_bytes":1000}`)); err != nil {
		t.Fatal(err)
	}
	text, isErr := recallText(t, e, map[string]any{"key": "toolu_B", "req": "req1"})
	body := strings.SplitN(text, "\n", 2)[1]
	if isErr || !strings.HasPrefix(body, bigRead[:1000]) || strings.Contains(body, bigRead[:1001]) {
		t.Errorf("not truncated to 1000 bytes:\n%.300s", text)
	}
	if !strings.Contains(text, "truncated — showing the first 1000 of 6200 bytes") {
		t.Errorf("no truncation notice:\n%s", text[len(text)-300:])
	}
	if !e.rs.events.recent(1)[0].Truncated {
		t.Error("event not marked truncated")
	}

	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/api/recall/settings", strings.NewReader(`{"enabled":false}`))
	if r := e.do(req); r.status != 200 || r.msg["enabled"] != false || r.msg["max_bytes"] != float64(1000) {
		t.Fatalf("put settings: %d %s", r.status, r.raw)
	}
	if text, isErr := recallText(t, e, map[string]any{"key": "toolu_B", "req": "req1"}); !isErr || !strings.Contains(text, "turned off") {
		t.Errorf("disabled: %v %s", isErr, text)
	}
	get, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/recall/settings", nil)
	if r := e.do(get); r.msg["enabled"] != false || r.msg["mcp_path"] != "/mcp" || r.msg["tool"] != ToolName {
		t.Errorf("get settings: %s", r.raw)
	}
	bad, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/api/recall/settings", strings.NewReader(`{"max_bytes":5}`))
	if r := e.do(bad); r.status != 400 {
		t.Errorf("max_bytes 5: %d", r.status)
	}
}

func TestDefaultSettings(t *testing.T) {
	e := newEnv(t)
	if s := e.rs.settings(); !s.Enabled || s.MaxBytes != DefaultMaxBytes {
		t.Errorf("defaults: %+v", s)
	}
}

func TestEvents(t *testing.T) {
	e := newEnv(t)
	recallText(t, e, map[string]any{"key": "toolu_B", "req": "req1"})
	recallText(t, e, map[string]any{"key": "toolu_B", "req": "req1"}) // cached, still recorded
	recallText(t, e, map[string]any{"key": "m2.b0", "req": "req1"})
	recallText(t, e, map[string]any{"key": "toolu_A", "req": "nobody"})
	e.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"claude-code","version":"2.1.0"}}}`, nil)

	get := func(path string) resp {
		r, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
		return e.do(r)
	}
	var evs []Event
	if r := get("/api/recall/events"); json.Unmarshal([]byte(r.raw), &evs) != nil || len(evs) != 4 {
		t.Fatalf("events: %s", r.raw)
	}
	last, first := evs[0], evs[3]
	if last.OK || last.Error != ErrBodyNotLogged || last.ConversationID != "" {
		t.Errorf("newest first, error event: %+v", last)
	}
	conv := pipeline.ConversationID(nil, []byte(fixtureBody(t, firstResultParts)))
	if !first.OK || first.Tool != "Read" || first.ToolUseID != "toolu_B" || first.BlockKey != "m4.b0" ||
		first.Kind != "tool_result" || first.ConversationID != conv || first.Tokens != 1550 || first.Bytes != len(bigRead) {
		t.Errorf("ok event: %+v", first)
	}
	if !strings.HasPrefix(first.Client, "Go-http-client") {
		t.Errorf("client: %q", first.Client)
	}
	if r := get("/api/recall/events?limit=1"); strings.Count(r.raw, `"req"`) != 1 {
		t.Errorf("limit: %s", r.raw)
	}

	var st Stats
	if r := get("/api/recall/stats"); json.Unmarshal([]byte(r.raw), &st) != nil {
		t.Fatal(r.raw)
	}
	if st.Total != 4 || st.OK != 3 || st.Errors != 1 || st.ByError[ErrBodyNotLogged] != 1 || st.Conversations != 1 {
		t.Errorf("stats: %+v", st)
	}
	if len(st.TopKeys) != 2 || st.TopKeys[0].Key != "toolu_B" || st.TopKeys[0].Count != 2 {
		t.Errorf("top keys: %+v", st.TopKeys)
	}
	if len(st.TopTools) != 2 || st.TopTools[0].Tool != "Read" || st.TopTools[0].Count != 2 {
		t.Errorf("top tools: %+v", st.TopTools)
	}

	// Append-only JSONL on disk, reloaded by a new server.
	f, err := os.Open(filepath.Join(config.Dir(), "recall", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for sc := bufio.NewScanner(f); sc.Scan(); lines++ {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Errorf("line %d: %v", lines, err)
		}
	}
	f.Close()
	if lines != 4 {
		t.Errorf("%d lines on disk", lines)
	}
	if again := New(e.cfg, e.rs.st); len(again.events.recent(0)) != 4 {
		t.Error("events not reloaded")
	}
}

func TestEventClientFromSession(t *testing.T) {
	e := newEnv(t)
	init := e.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"claude-code","version":"2.1.0"}}}`, nil)
	h := map[string]string{hdrSession: init.header.Get(hdrSession), hdrVersion: "2025-11-25"}
	e.post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rlcd_recall","arguments":{"key":"toolu_A","req":"req1"}}}`, h)
	e.modern(3, "tools/call", callParams(map[string]any{"key": "toolu_A", "req": "req1"}))
	evs := e.rs.events.recent(2)
	if evs[1].Client != "claude-code 2.1.0" || evs[0].Client != "test-client 1.0" {
		t.Errorf("clients: %q %q", evs[1].Client, evs[0].Client)
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	s, cut := truncate("aé", 2)
	if !cut || s != "a" {
		t.Errorf("%q %v", s, cut)
	}
}
