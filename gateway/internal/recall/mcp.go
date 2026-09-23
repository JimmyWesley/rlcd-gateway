package recall

// A minimal MCP server over Streamable HTTP, with no dependencies.
//
// It targets MCP revision 2026-07-28
// (https://modelcontextprotocol.io/specification/2026-07-28) and is
// "dual-era" as that revision defines it, because clients in the field
// still speak the initialize-based revisions:
//
//   - Modern requests carry io.modelcontextprotocol/protocolVersion and
//     clientCapabilities in params._meta and are served statelessly. The
//     MCP-Protocol-Version, Mcp-Method and Mcp-Name headers must mirror the
//     body (else 400, HeaderMismatch -32020); an unknown version gets 400,
//     UnsupportedProtocolVersion -32022 listing the supported ones; missing
//     required _meta gets 400, -32602; an unknown method gets 404, -32601.
//     Methods: server/discover, tools/list, tools/call. Results carry
//     resultType and _meta serverInfo; tools/list carries ttlMs/cacheScope.
//   - Legacy clients (2025-11-25, 2025-06-18, 2025-03-26) open with
//     initialize; the version is negotiated there and a session id is
//     minted in Mcp-Session-Id. Requests with an unknown session id get 404
//     (so the client re-initializes, e.g. after a gateway restart); DELETE
//     ends a session. Requests without a session id are still served: the
//     server keeps no per-session state it needs. A missing
//     MCP-Protocol-Version header means 2025-03-26, the only revision that
//     allows JSON-RPC batches. Methods: initialize, ping, tools/list,
//     tools/call; notifications (notifications/initialized, ...) get 202.
//
// Transport rules common to both: one JSON-RPC message per POST (batches
// only for 2025-03-26); notifications and responses get 202 with no body;
// requests always get a single application/json response (never SSE: the
// tool is fast and sends no progress); GET gets 405 (no standalone stream);
// a non-loopback Origin gets 403 (DNS rebinding; the gateway also rejects
// non-loopback Host headers).

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	modernVersion = "2026-07-28"
	// legacyDefault is assumed when a legacy request has no version header.
	legacyDefault = "2025-03-26"
)

// legacyVersions are the initialize-based revisions served, newest first.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

func supportedVersions() []string { return append([]string{modernVersion}, legacyVersions...) }

// JSON-RPC 2.0 and MCP error codes.
const (
	codeParseError         = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
)

const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo   = "io.modelcontextprotocol/clientInfo"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"

	hdrVersion = "MCP-Protocol-Version"
	hdrSession = "Mcp-Session-Id"
	hdrMethod  = "Mcp-Method"
	hdrName    = "Mcp-Name"
)

const maxMessage = 4 << 20

const instructions = "RLCD Gateway sits between you and the model provider and may omit old context to save tokens. " +
	"Omitted content is replaced by a marker like [rlcd: ~N tokens omitted · key K · req R · call rlcd_recall to restore]. " +
	"If you need that content, call rlcd_recall with the key and req from the marker; do not guess what it said."

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

func serverInfo() implementation {
	v := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	return implementation{Name: "rlcd-gateway", Title: "RLCD Gateway", Version: v}
}

// toolDef is what tools/list returns. The model reads the description and
// the schema, so they explain where the values come from.
var toolDef = map[string]any{
	"name":  ToolName,
	"title": "Recall omitted context",
	"description": "Restore content that RLCD Gateway omitted from your context to save tokens. " +
		"Wherever a tool result or message was omitted you see a marker instead, for example:\n" +
		"[rlcd: ~1200 tokens omitted · key toolu_01A09q90qw90lq917835lq9 · req 20260923T010203-0a1b2c3d4e5f6a7b · call rlcd_recall to restore]\n" +
		"Call this tool with key and req copied exactly from that marker (or pass the whole marker as `marker`) " +
		"to get the original content back, verbatim. Only recall what the current step needs. " +
		"Very large content comes back truncated, with a notice saying so.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key": map[string]any{
				"type": "string",
				"description": "The value after `key` in the marker: a tool_use_id such as toolu_01A09q90qw90lq917835lq9, " +
					"or a block key such as m4.b0. Copy it exactly.",
			},
			"req": map[string]any{
				"type": "string",
				"description": "The value after `req` in the marker: the id of the logged request that still holds the " +
					"original, such as 20260923T010203-0a1b2c3d4e5f6a7b. Copy it exactly.",
			},
			"marker": map[string]any{
				"type":        "string",
				"description": "Instead of key and req: the whole marker text, from [rlcd: to the closing ].",
			},
		},
		"additionalProperties": false,
	},
	"annotations": map[string]any{
		"readOnlyHint":    true,
		"destructiveHint": false,
		"idempotentHint":  true,
		"openWorldHint":   false,
	},
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// reply is the outcome of one message: an HTTP status and, unless the
// message was a notification or response, a JSON-RPC response.
type reply struct {
	status  int
	resp    *rpcResponse
	session string // set on initialize
}

func accepted() reply { return reply{status: http.StatusAccepted} }

func errReply(status int, id json.RawMessage, code int, msg string, data any) reply {
	return reply{status: status, resp: &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg, data}}}
}

func okReply(id json.RawMessage, result any) reply {
	return reply{status: http.StatusOK, resp: &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}}
}

type session struct {
	version string
	client  string
	seen    time.Time
}

type mcpServer struct {
	rs       *Server
	mu       sync.Mutex
	sessions map[string]*session
}

const maxSessions = 1024

func newMCPServer(rs *Server) *mcpServer {
	return &mcpServer{rs: rs, sessions: map[string]*session{}}
}

func (m *mcpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r.Header.Get("Origin")) {
		writeRPC(w, errReply(http.StatusForbidden, nil, codeInvalidRequest, "Origin not allowed: the MCP endpoint only accepts local origins", nil))
		return
	}
	switch r.Method {
	case http.MethodPost:
		m.post(w, r)
	case http.MethodDelete:
		// Legacy session termination. Modern clients have no sessions.
		sid := r.Header.Get(hdrSession)
		if sid == "" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "no session to end", http.StatusMethodNotAllowed)
			return
		}
		m.mu.Lock()
		_, ok := m.sessions[sid]
		delete(m.sessions, sid)
		m.mu.Unlock()
		if !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		// GET: this server offers no standalone SSE stream (allowed by the
		// legacy revisions, required by 2026-07-28).
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "the MCP endpoint only accepts POST", http.StatusMethodNotAllowed)
	}
}

// originAllowed accepts no Origin (non-browser clients) or a loopback one.
func originAllowed(o string) bool {
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (m *mcpServer) post(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, _ := mime.ParseMediaType(ct); mt != "application/json" {
			writeRPC(w, errReply(http.StatusUnsupportedMediaType, nil, codeInvalidRequest, "Content-Type must be application/json", nil))
			return
		}
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		writeRPC(w, errReply(http.StatusNotAcceptable, nil, codeInvalidRequest, "Accept must include application/json", nil))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessage+1))
	if err != nil || len(body) > maxMessage {
		writeRPC(w, errReply(http.StatusRequestEntityTooLarge, nil, codeInvalidRequest, "message too large", nil))
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		m.batch(w, r, body)
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		writeRPC(w, errReply(http.StatusBadRequest, nil, codeParseError, "Parse error: "+err.Error(), nil))
		return
	}
	writeRPC(w, m.handle(r, msg, false))
}

// batch serves a JSON-RPC batch, which only revision 2025-03-26 allows.
func (m *mcpServer) batch(w http.ResponseWriter, r *http.Request, body []byte) {
	if v := r.Header.Get(hdrVersion); v != "" && v != legacyDefault {
		writeRPC(w, errReply(http.StatusBadRequest, nil, codeInvalidRequest, "JSON-RPC batches are not allowed in protocol version "+v, nil))
		return
	}
	var msgs []rpcMessage
	if err := json.Unmarshal(body, &msgs); err != nil {
		writeRPC(w, errReply(http.StatusBadRequest, nil, codeParseError, "Parse error: "+err.Error(), nil))
		return
	}
	if len(msgs) == 0 {
		writeRPC(w, errReply(http.StatusBadRequest, nil, codeInvalidRequest, "empty batch", nil))
		return
	}
	var out []*rpcResponse
	for _, msg := range msgs {
		if rep := m.handle(r, msg, true); rep.resp != nil {
			out = append(out, rep.resp)
		}
	}
	if len(out) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func acceptsJSON(values []string) bool {
	if len(values) == 0 {
		return true // lenient with clients that send none
	}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt, _, _ := mime.ParseMediaType(strings.TrimSpace(part))
			if mt == "application/json" || mt == "application/*" || mt == "*/*" {
				return true
			}
		}
	}
	return false
}

func writeRPC(w http.ResponseWriter, rep reply) {
	if rep.session != "" {
		w.Header().Set(hdrSession, rep.session)
	}
	if rep.resp == nil {
		w.WriteHeader(rep.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_ = json.NewEncoder(w).Encode(rep.resp)
}

type params struct {
	Meta      map[string]json.RawMessage `json:"_meta"`
	Name      string                     `json:"name"`
	URI       string                     `json:"uri"`
	Arguments json.RawMessage            `json:"arguments"`
	// initialize (legacy)
	ProtocolVersion string          `json:"protocolVersion"`
	ClientInfo      *implementation `json:"clientInfo"`
}

func (p params) metaString(k string) string {
	var s string
	_ = json.Unmarshal(p.Meta[k], &s)
	return s
}

// handle serves one JSON-RPC message.
func (m *mcpServer) handle(r *http.Request, msg rpcMessage, inBatch bool) reply {
	hasID := len(msg.ID) > 0
	if msg.JSONRPC != "2.0" {
		return errReply(http.StatusBadRequest, validID(msg.ID), codeInvalidRequest, `Invalid Request: jsonrpc must be "2.0"`, nil)
	}
	if msg.Method == "" {
		if hasID && (msg.Result != nil || msg.Error != nil) {
			return accepted() // a response to a server request; this server sends none
		}
		return errReply(http.StatusBadRequest, validID(msg.ID), codeInvalidRequest, "Invalid Request: no method", nil)
	}
	if hasID && validID(msg.ID) == nil {
		return errReply(http.StatusBadRequest, nil, codeInvalidRequest, "Invalid Request: id must be a string or an integer", nil)
	}

	var p params
	if len(msg.Params) > 0 && string(msg.Params) != "null" {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			if !hasID {
				return errReply(http.StatusBadRequest, nil, codeInvalidParams, "Invalid params: "+err.Error(), nil)
			}
			return errReply(http.StatusBadRequest, msg.ID, codeInvalidParams, "Invalid params: params must be an object", nil)
		}
	}

	if !hasID {
		// Notifications (notifications/initialized, notifications/cancelled,
		// ...): nothing to do, but a legacy one on a dead session must still
		// learn that the session is gone.
		if sid := r.Header.Get(hdrSession); sid != "" && p.Meta[metaVersion] == nil && m.touch(sid) == nil {
			return reply{status: http.StatusNotFound}
		}
		return accepted()
	}

	if msg.Method == "initialize" {
		if inBatch {
			return errReply(http.StatusBadRequest, msg.ID, codeInvalidRequest, "initialize must not be part of a batch", nil)
		}
		return m.initialize(r, msg.ID, p)
	}
	if v := p.metaString(metaVersion); v != "" && !slices.Contains(legacyVersions, v) {
		return m.modern(r, msg, p, v)
	}
	return m.legacy(r, msg, p)
}

// validID returns id if it is a JSON string or integer, else nil.
func validID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		return id
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return id
		}
	}
	return nil
}

// modern serves a 2026-07-28 request: all context is in the request.
func (m *mcpServer) modern(r *http.Request, msg rpcMessage, p params, v string) reply {
	id := msg.ID
	h := r.Header.Get(hdrVersion)
	switch {
	case h == "":
		return errReply(http.StatusBadRequest, id, codeHeaderMismatch, "Header mismatch: missing MCP-Protocol-Version header", nil)
	case h != v:
		return errReply(http.StatusBadRequest, id, codeHeaderMismatch,
			"Header mismatch: MCP-Protocol-Version header value '"+h+"' does not match body value '"+v+"'", nil)
	}
	if v != modernVersion {
		return errReply(http.StatusBadRequest, id, codeUnsupportedVersion, "Unsupported protocol version",
			map[string]any{"supported": supportedVersions(), "requested": v})
	}
	if p.Meta[metaCapabilities] == nil {
		return errReply(http.StatusBadRequest, id, codeInvalidParams, "Invalid params: _meta is missing "+metaCapabilities, nil)
	}
	if got := r.Header.Get(hdrMethod); got != msg.Method {
		if got == "" {
			return errReply(http.StatusBadRequest, id, codeHeaderMismatch, "Header mismatch: missing Mcp-Method header", nil)
		}
		return errReply(http.StatusBadRequest, id, codeHeaderMismatch,
			"Header mismatch: Mcp-Method header value '"+got+"' does not match body value '"+msg.Method+"'", nil)
	}
	switch msg.Method {
	case "tools/call", "prompts/get", "resources/read":
		want := p.Name
		if msg.Method == "resources/read" {
			want = p.URI
		}
		raw := r.Header.Get(hdrName)
		got, ok := decodeHeader(raw)
		if raw == "" || !ok {
			return errReply(http.StatusBadRequest, id, codeHeaderMismatch, "Header mismatch: missing or malformed Mcp-Name header", nil)
		}
		if got != want {
			return errReply(http.StatusBadRequest, id, codeHeaderMismatch,
				"Header mismatch: Mcp-Name header value '"+got+"' does not match body value '"+want+"'", nil)
		}
	}

	var ci implementation
	_ = json.Unmarshal(p.Meta[metaClientInfo], &ci)
	client := clientName(ci, r)
	meta := map[string]any{metaServerInfo: serverInfo()}
	switch msg.Method {
	case "server/discover":
		return okReply(id, map[string]any{
			"resultType":        "complete",
			"supportedVersions": supportedVersions(),
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      instructions,
			"ttlMs":             300000,
			"cacheScope":        "public",
			"_meta":             meta,
		})
	case "tools/list":
		return okReply(id, map[string]any{
			"resultType": "complete",
			"tools":      []any{toolDef},
			"ttlMs":      300000,
			"cacheScope": "public",
			"_meta":      meta,
		})
	case "tools/call":
		res, rep := m.call(id, p, client)
		if res == nil {
			return rep
		}
		res["resultType"] = "complete"
		res["_meta"] = meta
		return okReply(id, res)
	}
	return errReply(http.StatusNotFound, id, codeMethodNotFound, "Method not found: "+msg.Method, nil)
}

// legacy serves a request from an initialize-based client.
func (m *mcpServer) legacy(r *http.Request, msg rpcMessage, p params) reply {
	id := msg.ID
	var sess *session
	if sid := r.Header.Get(hdrSession); sid != "" {
		if sess = m.touch(sid); sess == nil {
			return errReply(http.StatusNotFound, id, codeInvalidRequest, "Unknown or expired session: send initialize again", nil)
		}
	}
	if h := r.Header.Get(hdrVersion); h != "" && !slices.Contains(legacyVersions, h) {
		if h == modernVersion {
			return errReply(http.StatusBadRequest, id, codeInvalidParams,
				"Invalid params: protocol version "+h+" requires _meta "+metaVersion+" and "+metaCapabilities, nil)
		}
		return errReply(http.StatusBadRequest, id, codeUnsupportedVersion, "Unsupported protocol version",
			map[string]any{"supported": supportedVersions(), "requested": h})
	}
	client := r.UserAgent()
	if sess != nil {
		client = sess.client
	}
	switch msg.Method {
	case "ping":
		return okReply(id, map[string]any{})
	case "tools/list":
		return okReply(id, map[string]any{"tools": []any{toolDef}})
	case "tools/call":
		res, rep := m.call(id, p, client)
		if res == nil {
			return rep
		}
		return okReply(id, res)
	}
	return errReply(http.StatusOK, id, codeMethodNotFound, "Method not found: "+msg.Method, nil)
}

func (m *mcpServer) initialize(r *http.Request, id json.RawMessage, p params) reply {
	v := p.ProtocolVersion
	if !slices.Contains(legacyVersions, v) {
		// Per the lifecycle rules: answer with the latest version we speak;
		// the client disconnects if it cannot use it.
		v = legacyVersions[0]
	}
	var ci implementation
	if p.ClientInfo != nil {
		ci = *p.ClientInfo
	}
	sid := newSessionID()
	m.mu.Lock()
	if len(m.sessions) >= maxSessions {
		var oldest string
		for k, s := range m.sessions {
			if oldest == "" || s.seen.Before(m.sessions[oldest].seen) {
				oldest = k
			}
		}
		delete(m.sessions, oldest)
	}
	m.sessions[sid] = &session{version: v, client: clientName(ci, r), seen: time.Now()}
	m.mu.Unlock()
	rep := okReply(id, map[string]any{
		"protocolVersion": v,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      serverInfo(),
		"instructions":    instructions,
	})
	rep.session = sid
	return rep
}

// call runs tools/call. It returns the result object, or a protocol error
// reply when the request itself is malformed. Recall failures are tool
// results with isError, so the model can read them and correct itself.
func (m *mcpServer) call(id json.RawMessage, p params, client string) (map[string]any, reply) {
	if p.Name != ToolName {
		return nil, errReply(http.StatusOK, id, codeInvalidParams, "Unknown tool: "+p.Name, nil)
	}
	var a Args
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &a); err != nil {
			return nil, errReply(http.StatusOK, id, codeInvalidParams, "Invalid params: arguments must be an object with string fields key, req or marker", nil)
		}
	}
	res := m.rs.Recall(a, client)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": res.Text}},
		"isError": res.Error,
	}, reply{}
}

func (m *mcpServer) touch(sid string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sid]
	if s != nil {
		s.seen = time.Now()
	}
	return s
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clientName(ci implementation, r *http.Request) string {
	if ci.Name != "" {
		return strings.TrimSpace(ci.Name + " " + ci.Version)
	}
	return r.UserAgent()
}

// decodeHeader undoes the =?base64?…?= sentinel encoding of Mcp-Name.
func decodeHeader(v string) (string, bool) {
	if !strings.HasPrefix(v, "=?base64?") || !strings.HasSuffix(v, "?=") || len(v) < len("=?base64??=") {
		return v, true
	}
	b, err := base64.StdEncoding.DecodeString(v[len("=?base64?") : len(v)-2])
	if err != nil {
		return "", false
	}
	return string(b), true
}
