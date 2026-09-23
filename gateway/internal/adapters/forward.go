package adapters

// The forwarding helpers below mirror internal/proxy (header copy, masking,
// capped capture, ids). They are duplicated on purpose so this package does
// not reach into the proxy's internals; keep the two in step.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const (
	maxRequestBody      = 64 << 20
	maxCapturedResponse = 4 << 20
)

// Request kinds, by endpoint.
const (
	kindResponses = "responses"
	kindChat      = "chat"
	kindOther     = "other"
)

// Upstream labels, stored as the record's route.
const (
	RouteOpenAI  = "openai"
	RouteChatGPT = "chatgpt"
)

// endpointOf maps a request path to the part appended to the upstream base
// URL: "/openai/v1/responses" and "/v1/responses" both give "/responses".
func endpointOf(path string) string {
	rest := strings.TrimPrefix(path, Prefix)
	if rest == "/v1" {
		return ""
	}
	return strings.TrimPrefix(rest, "/v1")
}

func kindOf(method, endpoint string) string {
	if method != http.MethodPost {
		return kindOther
	}
	switch {
	case endpoint == "/responses" || strings.HasPrefix(endpoint, "/responses/"):
		// "/responses/compact" (Codex remote compaction) carries the same input shape.
		return kindResponses
	case endpoint == "/chat/completions":
		return kindChat
	}
	return kindOther
}

// isChatGPTLogin tells a ChatGPT subscription login from an OpenAI API key.
// Codex sends the login as a JWT access token plus ChatGPT-Account-ID;
// API keys start with "sk-".
func isChatGPTLogin(h http.Header) bool {
	if h.Get("Chatgpt-Account-Id") != "" {
		return true
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
	return strings.HasPrefix(tok, "eyJ")
}

func (a *Adapters) serveOpenAI(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	s := a.Settings()
	endpoint := endpointOf(r.URL.Path)
	kind := kindOf(r.Method, endpoint)

	route, base := RouteOpenAI, s.OpenAIBaseURL
	if isChatGPTLogin(r.Header) {
		route, base = RouteChatGPT, s.ChatGPTBaseURL
	}
	cfg := a.cfg.Get()
	d := &store.Detail{Record: store.Record{
		ID: newID(), Time: start, Method: r.Method, Path: r.URL.Path,
		Route: route, Upstream: base, AuthMode: authMode(r.Header),
	}, RequestHeaders: redactHeaders(r.Header)}

	var body []byte
	if kind != kindOther {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
		if err != nil || len(b) > maxRequestBody {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable")
			return
		}
		body = b
		a.describe(d, r.Header, kind, body)
		if cfg.LogBodies {
			d.RequestBody = string(body)
		}
	}

	target := strings.TrimRight(base, "/") + endpoint
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var reqBody io.Reader = r.Body
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, reqBody)
	if err != nil {
		d.Status, d.Error = http.StatusBadGateway, err.Error()
		a.save(d)
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	} else {
		req.ContentLength = r.ContentLength
	}
	copyHeaders(req.Header, r.Header)

	resp, err := a.Client.Do(req)
	if err != nil {
		d.DurationMs = time.Since(start).Milliseconds()
		if errors.Is(err, context.Canceled) {
			d.Error = "client cancelled"
		} else {
			d.Status, d.Error = http.StatusBadGateway, err.Error()
			writeOpenAIError(w, http.StatusBadGateway, "upstream: "+err.Error())
		}
		a.save(d)
		return
	}
	defer resp.Body.Close()
	d.Status = resp.StatusCode
	d.TTFBMs = time.Since(start).Milliseconds()

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Rlcd-Request-Id", d.ID)
	w.Header().Set("X-Rlcd-Route", route)
	w.WriteHeader(resp.StatusCode)

	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	capture := &capped{limit: maxCapturedResponse}
	events := &sseUsage{kind: kind}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := w.Write(chunk); werr != nil {
				d.Error = "client went away: " + werr.Error()
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			capture.Write(chunk)
			if isSSE {
				events.feed(chunk)
			}
		}
		if rerr != nil {
			if rerr != io.EOF && d.Error == "" && !errors.Is(rerr, context.Canceled) {
				d.Error = "upstream read: " + rerr.Error()
			}
			break
		}
	}
	d.DurationMs = time.Since(start).Milliseconds()

	var model string
	if isSSE {
		events.flush()
		d.Usage, model = events.result(), events.model
		if d.Error == "" && events.failure != "" {
			d.Error = events.failure
		}
	} else if kind != kindOther {
		d.Usage, model = jsonUsage(capture.Bytes())
	}
	if d.Model == "" {
		d.Model = model
	}
	if resp.StatusCode >= 400 && d.Error == "" {
		d.Error = errorMessage(capture.Bytes())
	}
	if cfg.LogBodies {
		d.ResponseBody = capture.String()
	}
	a.save(d)
}

// describe fills the record from the request body: model, stream flag,
// X-ray and conversation id. It never fails the request.
func (a *Adapters) describe(d *store.Detail, h http.Header, kind string, body []byte) {
	plain, err := decodeBody(h.Get("Content-Encoding"), body)
	if err != nil {
		d.StageErrors = map[string]string{"xray": err.Error()}
		return
	}
	var parse = ParseResponses
	if kind == kindChat {
		parse = ParseChat
	}
	x, err := parse(plain)
	if err != nil {
		d.StageErrors = map[string]string{"xray": err.Error()}
		return
	}
	d.XRay = x
	d.ClientModel, d.Model, d.Stream, d.EstTokens, d.ByKind = x.Model, x.Model, x.Stream, x.Tokens, x.ByKind
	d.ConversationID = conversationID(h, kind, plain)
}

// decodeBody undoes request compression for parsing only; the upstream
// always gets the client's bytes. Codex can compress requests with zstd,
// which the standard library cannot read.
func decodeBody(encoding string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(io.LimitReader(zr, maxRequestBody))
	}
	return nil, errors.New("request body is " + encoding + "-compressed; X-ray unavailable")
}

// conversationID is stable across the turns of one agent session. Codex
// sends its session id as a header and as prompt_cache_key; otherwise the
// id hashes the instructions and first user input, like the Anthropic path.
func conversationID(h http.Header, kind string, body []byte) string {
	for _, k := range []string{"Session_id", "Session-Id", "Conversation_id", "Conversation-Id"} {
		if v := h.Values(k); len(v) > 0 && v[0] != "" {
			return "cx-" + v[0]
		}
	}
	var head struct {
		PromptCacheKey string            `json:"prompt_cache_key"`
		Instructions   json.RawMessage   `json:"instructions"`
		Input          json.RawMessage   `json:"input"`
		Messages       []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &head)
	if head.PromptCacheKey != "" {
		return "cx-" + head.PromptCacheKey
	}
	sum := sha256.New()
	if kind == kindChat {
		// The system message and the first user message.
		for _, raw := range head.Messages {
			var m struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(raw, &m)
			sum.Write(raw)
			if m.Role == "user" {
				break
			}
		}
	} else {
		sum.Write(head.Instructions)
		var items []json.RawMessage
		if json.Unmarshal(head.Input, &items) != nil {
			sum.Write(head.Input)
		}
		for _, raw := range items {
			var m struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(raw, &m)
			if m.Role == "user" {
				sum.Write(raw)
				break
			}
		}
	}
	return "h-" + hex.EncodeToString(sum.Sum(nil))[:16]
}

func (a *Adapters) save(d *store.Detail) {
	if err := a.st.Save(d); err != nil {
		log.Printf("store: %v", err)
	}
}

// Hop-by-hop headers, plus those Go's transport must own. Dropping
// Accept-Encoding lets the transport negotiate gzip and hand us plain bytes,
// which we need to read usage out of the stream.
var skipHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Host": true, "Content-Length": true, "Accept-Encoding": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if skipHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func authMode(h http.Header) string {
	if isChatGPTLogin(h) {
		return "oauth"
	}
	if a := h.Get("Authorization"); a != "" {
		if strings.Contains(a, "sk-") {
			return "api-key"
		}
		return "bearer"
	}
	if h.Get("Api-Key") != "" || h.Get("X-Api-Key") != "" {
		return "api-key"
	}
	return "none"
}

// secretHeaders are masked before a record is stored. The account id is not
// a credential, but it identifies the user's ChatGPT account.
var secretHeaders = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "Api-Key": true, "Cookie": true,
	"Chatgpt-Account-Id": true, "Openai-Organization": true, "Openai-Project": true,
}

func redactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		v := strings.Join(vs, ", ")
		if secretHeaders[http.CanonicalHeaderKey(k)] {
			v = mask(v)
		}
		out[k] = v
	}
	return out
}

// mask keeps a short prefix, enough to tell a JWT from an API key.
func mask(v string) string {
	const keep = 12
	if len(v) <= keep {
		return strings.Repeat("•", len([]rune(v)))
	}
	return v[:keep] + "…(" + strconv.Itoa(len(v)) + " chars)"
}

func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"type": "api_error", "message": "rlcd-gateway: " + msg},
	})
}

func errorMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Detail != "" {
			return e.Detail
		}
	}
	s := string(b)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// capped is a buffer that silently stops growing at limit.
type capped struct {
	bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) {
	if room := c.limit - c.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.Buffer.Write(p)
	}
}
