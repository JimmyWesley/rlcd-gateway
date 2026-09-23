// Package proxy is the request path: agent -> gateway -> provider.
//
// The agent (Claude Code, OpenCode, ...) is pointed at the gateway through
// its base URL, so it keeps its own login. For every POST /v1/messages the
// gateway picks the active route and either forwards the client's own
// credentials (a subscription stays a subscription) or swaps them for a key
// it holds (OpenRouter). The agent never learns which one served the turn.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const (
	maxRequestBody = 64 << 20
	// Response bodies are streamed in full to the client; only this much is kept for the log.
	maxCapturedResponse = 4 << 20
)

type Proxy struct {
	Config *config.Store
	Store  *store.Store
	Client *http.Client
	Hooks  pipeline.Hooks
}

func New(cfg *config.Store, st *store.Store) *Proxy {
	return &Proxy{Config: cfg, Store: st, Client: &http.Client{
		// No overall timeout: a streamed turn can legitimately run for minutes.
		// Cancellation comes from the client's request context instead.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 10 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := p.Config.Get()
	if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
		p.messages(w, r, cfg)
		return
	}
	// Only the provider API is proxied. Anything else (a browser asking for
	// /favicon.ico) must not be forwarded upstream.
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	// The rest of /v1 (count_tokens, models, ...) belongs to the provider the
	// client is logged into, with the client's own credentials.
	p.forward(w, r, cfg, forwardPlan{
		route: "passthrough", upstream: cfg.PassthroughBaseURL, auth: config.AuthPassthrough,
	})
}

type forwardPlan struct {
	route    string
	kind     string
	upstream string
	auth     string
	key      string
	body     []byte // nil means stream r.Body through untouched
	detail   *store.Detail
}

func (p *Proxy) messages(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	id := newID()
	preq := &pipeline.Request{ID: id, Body: body, Headers: r.Header, Config: cfg,
		ConversationID: pipeline.ConversationID(body)}
	if x, err := ir.Parse(body); err == nil {
		preq.XRay = x
	}

	name, reason := cfg.ActiveRoute, ""
	if p.Hooks.Router != nil {
		dec, ok := p.Hooks.Router.Route(r.Context(), preq)
		switch _, exists := cfg.Routes[dec.Route]; {
		case !ok:
			reason = dec.Reason
		case exists:
			name, reason = dec.Route, dec.Reason
		default:
			reason = "router chose unknown route " + dec.Route + "; using active route"
		}
	}
	route := cfg.Routes[name]

	d := &store.Detail{Record: store.Record{
		ID: id, Time: time.Now(), Method: r.Method, Path: r.URL.Path,
		Route: name, Upstream: route.BaseURL, AuthMode: authMode(r.Header),
		ConversationID: preq.ConversationID, RouteReason: reason,
	}}
	d.RequestHeaders = redactHeaders(r.Header)
	if x := preq.XRay; x != nil {
		d.XRay = x
		d.ClientModel, d.Stream, d.EstTokens, d.ByKind = x.Model, x.Stream, x.Tokens, x.ByKind
	}

	// Transformers see the client's body and each other's output. A failing
	// stage is skipped, never fatal: the turn must go through.
	cur := body
	for _, t := range p.Hooks.Transformers {
		res, err := t.Transform(r.Context(), preq, cur)
		if err != nil {
			if d.StageErrors == nil {
				d.StageErrors = map[string]string{}
			}
			d.StageErrors[t.Name()] = err.Error()
			continue
		}
		if res == nil {
			continue
		}
		if res.Body != nil {
			cur = res.Body
		}
		if res.Summary != nil {
			if d.Stages == nil {
				d.Stages = map[string]json.RawMessage{}
			}
			d.Stages[t.Name()] = res.Summary
		}
		if res.Detail != nil {
			if d.StageDetails == nil {
				d.StageDetails = map[string]json.RawMessage{}
			}
			d.StageDetails[t.Name()] = res.Detail
		}
	}

	out, stripped, model, err := shapeBody(cur, route)
	if err != nil {
		// Not JSON we understand: forward as-is rather than break the agent.
		out, model = cur, d.ClientModel
	}
	d.Model, d.StrippedThinking = model, stripped
	if cfg.LogBodies {
		d.RequestBody = string(body)
		if !bytes.Equal(out, body) {
			d.SentBody = string(out)
		}
	}

	key := ""
	if route.Auth == config.AuthKey {
		if key = route.ResolvedKey(); key == "" {
			msg := "route " + name + " needs a key (api_key or env " + route.APIKeyEnv + ")"
			d.Status, d.Error = http.StatusBadGateway, msg
			p.save(d)
			writeAnthropicError(w, http.StatusBadGateway, msg)
			return
		}
	}
	p.forward(w, r, cfg, forwardPlan{
		route: name, kind: route.Kind, upstream: route.BaseURL, auth: route.Auth, key: key,
		body: out, detail: d,
	})
}

// shapeBody applies per-route rewrites. It only re-encodes when something
// actually changes, so a plain passthrough sends the client's exact bytes.
func shapeBody(body []byte, route config.Route) (out []byte, stripped int, model string, err error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, 0, "", err
	}
	model, _ = m["model"].(string)
	changed := false
	if route.Model != "" && route.Model != model {
		m["model"], model, changed = route.Model, route.Model, true
	}
	// Signed thinking blocks are only valid on the model that produced them.
	if route.Kind != config.KindAnthropic {
		if n := stripThinking(m); n > 0 {
			stripped, changed = n, true
		}
	}
	if !changed {
		return body, 0, model, nil
	}
	out, err = json.Marshal(m)
	return out, stripped, model, err
}

func stripThinking(m map[string]any) int {
	msgs, _ := m["messages"].([]any)
	n := 0
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		kept := parts[:0:0]
		for _, pt := range parts {
			b, _ := pt.(map[string]any)
			if t, _ := b["type"].(string); t == "thinking" || t == "redacted_thinking" {
				n++
				continue
			}
			kept = append(kept, pt)
		}
		if len(kept) == 0 {
			// The API rejects empty content; keep the turn's shape.
			kept = append(kept, map[string]any{"type": "text", "text": "(reasoning omitted)"})
		}
		msg["content"] = kept
	}
	return n
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, cfg config.Config, plan forwardPlan) {
	start := time.Now()
	d := plan.detail
	if d == nil {
		d = &store.Detail{Record: store.Record{
			ID: newID(), Time: start, Method: r.Method, Path: r.URL.Path,
			Route: plan.route, Upstream: plan.upstream, AuthMode: authMode(r.Header),
		}, RequestHeaders: redactHeaders(r.Header)}
	}

	var body io.Reader = r.Body
	if plan.body != nil {
		body = bytes.NewReader(plan.body)
	}
	target := strings.TrimRight(plan.upstream, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		d.Status, d.Error = http.StatusBadGateway, err.Error()
		p.save(d)
		writeAnthropicError(w, http.StatusBadGateway, err.Error())
		return
	}
	if plan.body != nil {
		req.ContentLength = int64(len(plan.body))
	} else {
		req.ContentLength = r.ContentLength
	}
	copyHeaders(req.Header, r.Header)
	if plan.auth == config.AuthKey {
		applyKey(req.Header, plan.kind, plan.key)
	}

	resp, err := p.Client.Do(req)
	if err != nil {
		d.DurationMs = time.Since(start).Milliseconds()
		if errors.Is(err, context.Canceled) {
			d.Error = "client cancelled"
		} else {
			d.Status, d.Error = http.StatusBadGateway, err.Error()
			writeAnthropicError(w, http.StatusBadGateway, "upstream: "+err.Error())
		}
		p.save(d)
		return
	}
	defer resp.Body.Close()
	d.Status = resp.StatusCode
	d.TTFBMs = time.Since(start).Milliseconds()

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Rlcd-Request-Id", d.ID)
	w.Header().Set("X-Rlcd-Route", plan.route)
	w.WriteHeader(resp.StatusCode)

	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	capture := &capped{limit: maxCapturedResponse}
	usage := &sseUsage{}
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
				usage.feed(chunk)
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

	if isSSE {
		d.Usage = usage.result()
		if usage.model != "" && d.Model == "" {
			d.Model = usage.model
		}
	} else {
		d.Usage = jsonUsage(capture.Bytes())
	}
	if resp.StatusCode >= 400 && d.Error == "" {
		d.Error = errorMessage(capture.Bytes())
	}
	if cfg.LogBodies {
		d.ResponseBody = capture.String()
	}
	p.save(d)
}

func (p *Proxy) save(d *store.Detail) {
	if err := p.Store.Save(d); err != nil {
		log.Printf("store: %v", err)
	}
}

// Hop-by-hop headers, plus those Go's transport must own.
var skipHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Host": true, "Content-Length": true,
	// Dropping Accept-Encoding lets the transport negotiate gzip and hand us
	// plain bytes, which we need to read usage out of the stream.
	"Accept-Encoding": true,
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

// applyKey replaces client credentials with the route's key.
func applyKey(h http.Header, kind, key string) {
	h.Del("Authorization")
	h.Del("X-Api-Key")
	// Subscription logins announce themselves with an oauth beta flag; it
	// means nothing to another provider and can get the call rejected.
	if betas := h.Values("Anthropic-Beta"); len(betas) > 0 {
		h.Del("Anthropic-Beta")
		var keep []string
		for _, v := range betas {
			for _, b := range strings.Split(v, ",") {
				if b = strings.TrimSpace(b); b != "" && !strings.HasPrefix(b, "oauth-") {
					keep = append(keep, b)
				}
			}
		}
		if len(keep) > 0 {
			h.Set("Anthropic-Beta", strings.Join(keep, ","))
		}
	}
	if kind == config.KindAnthropic {
		h.Set("X-Api-Key", key)
	} else {
		h.Set("Authorization", "Bearer "+key)
	}
}

func authMode(h http.Header) string {
	if a := h.Get("Authorization"); a != "" {
		if strings.Contains(a, "sk-ant-oat") {
			return "oauth"
		}
		return "bearer"
	}
	if h.Get("X-Api-Key") != "" {
		return "api-key"
	}
	return "none"
}

var secretHeaders = map[string]bool{"Authorization": true, "X-Api-Key": true, "Cookie": true}

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

// mask keeps a short prefix, enough to tell an OAuth token from an API key.
func mask(v string) string {
	const keep = 18
	if len(v) <= keep {
		return strings.Repeat("•", len(v))
	}
	return v[:keep] + "…(" + itoa(len(v)) + " chars)"
}

func writeAnthropicError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "api_error", "message": "rlcd-gateway: " + msg},
	})
}

func errorMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
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

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
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
