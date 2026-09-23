// Package proxy is the request path: client -> gateway -> provider.
//
// One engine serves every protocol the gateway speaks: Anthropic Messages
// (POST /v1/messages), OpenAI Chat Completions and OpenAI Responses (mounted
// by the adapters package). Each request is parsed in its own protocol, run
// through the pipeline hooks (router, then transformers such as pruning),
// shaped for its route, and forwarded. The provider's response goes back to
// the client byte for byte.
//
// A client is pointed at the gateway through its base URL, so it keeps its
// own login. A route either forwards that login untouched (a subscription
// stays a subscription) or swaps it for a key the gateway holds. With a
// gateway key ("rlcd-…", see internal/keys) the client holds no provider
// key at all: the guard strips the gateway key and the route injects its own.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/clients"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/resilience"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Upstream is where an OpenAI-format request goes when no route claims it.
type Upstream struct {
	Name    string // recorded as the route
	BaseURL string
}

type Proxy struct {
	Config *config.Store
	Store  *store.Store
	Client *http.Client
	Hooks  pipeline.Hooks
	// Keys records per-key usage and enforces daily limits; nil without keys.
	Keys *keys.Store
	// SelectorObserver, when set, sees every economy-model call the
	// pipeline makes while handling a request (pruning, the auto rule), so
	// they can be audited as decisions. It must not block.
	SelectorObserver func(selector.Call)
	// OpenAIDefault picks the upstream for an OpenAI-format request that no
	// alias, rule or default_openai_route claims. The adapters package sets
	// it (OpenAI for API keys, the ChatGPT backend for a ChatGPT login).
	OpenAIDefault func(h http.Header) Upstream
	// Resilience guards output limits and recovers from upstream failures;
	// nil forwards every model call once, as it was built.
	Resilience *resilience.Engine
}

func New(cfg *config.Store, st *store.Store) *Proxy {
	return &Proxy{Config: cfg, Store: st, Client: relay.NewClient()}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := p.Config.Get()
	if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
		p.Serve(w, r, ir.ProtocolAnthropic)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" && p.ServesModels(r) {
		p.Models(w, r)
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
	plan := Plan{Protocol: ir.ProtocolAnthropic, Route: "passthrough", Upstream: cfg.PassthroughBaseURL,
		Target: strings.TrimRight(cfg.PassthroughBaseURL, "/") + r.URL.Path, Auth: config.AuthPassthrough}
	if id := keys.FromContext(r.Context()); id != nil && !relay.HasCredentials(r.Header) {
		// A gateway-key client has no login of its own: the active route's
		// key serves these calls, or nothing does.
		active := cfg.Routes[cfg.ActiveRoute]
		key := active.ResolvedKey()
		if active.Auth != config.AuthKey || key == "" || !id.AllowsRoute(cfg.ActiveRoute) {
			relay.WriteAnthropicError(w, http.StatusUnauthorized, relay.ErrAuthentication,
				"this request carried only a gateway key, and the active route "+cfg.ActiveRoute+" holds no provider key for it")
			return
		}
		plan.Route, plan.Kind, plan.Upstream, plan.Auth, plan.Key, plan.Headers =
			cfg.ActiveRoute, active.Kind, active.BaseURL, config.AuthKey, key, active.Headers
		plan.Target = strings.TrimRight(active.BaseURL, "/") + r.URL.Path
	}
	p.Forward(w, r, plan)
}

// Plan is one upstream call.
type Plan struct {
	Protocol string
	Route    string
	Kind     string
	Upstream string // base URL, for the record
	Target   string // full URL without the query
	Auth     string
	Key      string
	Headers  map[string]string
	// Body is what to send; nil streams r.Body through untouched.
	Body []byte
	// DropEncoding is set when Body was decompressed and rewritten.
	DropEncoding bool
	// ReadUsage parses usage out of the response (model calls only).
	ReadUsage bool
	Detail    *store.Detail
}

// endpointFor is the path appended to an OpenAI-style base URL.
func endpointFor(protocol string) string {
	if protocol == ir.ProtocolOpenAIChat {
		return "/chat/completions"
	}
	return "/responses"
}

// Serve runs a model call in protocol through the whole pipeline.
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request, protocol string) {
	cfg := p.Config.Get()
	openAI := ir.IsOpenAI(protocol)
	fail := func(status int, typ, msg string) {
		if openAI {
			relay.WriteOpenAIError(w, status, typ, msg)
		} else {
			relay.WriteAnthropicError(w, status, typ, msg)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, relay.MaxRequestBody+1))
	if err != nil || len(raw) > relay.MaxRequestBody {
		if openAI {
			fail(http.StatusRequestEntityTooLarge, relay.ErrInvalidRequest, "request body too large or unreadable")
		} else {
			http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		}
		return
	}
	body, decodeErr := raw, error(nil)
	if openAI {
		body, decodeErr = relay.DecodeBody(r.Header.Get("Content-Encoding"), raw)
		if decodeErr != nil {
			body = raw
		}
	}

	ident := keys.FromContext(r.Context())
	id := relay.NewID()
	preq := &pipeline.Request{ID: id, Protocol: protocol, Body: body, Headers: r.Header, Config: cfg,
		ConversationID: pipeline.ConversationIDFor(protocol, r.Header, body),
		ClientKind:     clients.Detect(r.Header).Kind}
	if ident != nil {
		// Scope everything sticky to the key: two apps (or users) sending
		// the same prompt must never share pruning decisions or pins.
		preq.KeyID = ident.ID
		preq.ConversationID = ident.ID + ":" + preq.ConversationID
	}
	d := &store.Detail{Record: store.Record{
		ID: id, Time: time.Now(), Method: r.Method, Path: r.URL.Path, Protocol: protocol,
		AuthMode: relay.AuthMode(openAI, r.Header), ConversationID: preq.ConversationID,
	}}
	d.Client = detectClient(r.Header, ident)
	if ident != nil {
		d.KeyID, d.KeyName = ident.ID, ident.Name
		if !relay.HasCredentials(r.Header) {
			d.AuthMode = "gateway-key"
		}
	}
	d.RequestHeaders = relay.RedactHeaders(r.Header)
	if decodeErr != nil {
		d.StageErrors = map[string]string{"xray": decodeErr.Error()}
	} else if x, err := ir.ParseFor(protocol, body); err == nil {
		preq.XRay = x
	} else if openAI {
		d.StageErrors = map[string]string{"xray": err.Error()}
	}
	if x := preq.XRay; x != nil {
		d.XRay = x
		d.ClientModel, d.Stream, d.EstTokens, d.ByKind = x.Model, x.Stream, x.Tokens, x.ByKind
	}
	if cfg.LogBodies {
		d.RequestBody = string(body)
	}
	ctx := r.Context()
	if p.SelectorObserver != nil {
		ctx = selector.WithTrace(ctx, selector.Trace{Observe: p.SelectorObserver, ParentID: id,
			ConversationID: preq.ConversationID})
	}
	refuse := func(status int, typ, msg string) {
		d.Status, d.Error = status, msg
		p.save(d, ident)
		fail(status, typ, msg)
	}

	// Routing: the router (aliases, then rules), else the protocol's default.
	var dec pipeline.RouteDecision
	decided := false
	if p.Hooks.Router != nil {
		dec, decided = p.Hooks.Router.Route(ctx, preq)
	}
	d.Alias = dec.Alias
	if dec.Error != "" {
		d.RouteReason = dec.Reason
		refuse(http.StatusBadRequest, relay.ErrInvalidRequest, dec.Error)
		return
	}
	name, reason := "", dec.Reason
	var route config.Route
	if decided {
		if rt, ok := cfg.Routes[dec.Route]; ok {
			name, route = dec.Route, rt
		} else {
			decided = false
			reason = "router chose unknown route " + dec.Route + "; using active route"
			if openAI {
				reason = "router chose unknown route " + dec.Route + "; using the default OpenAI route"
			}
		}
	}
	legacy := false
	if !decided {
		switch {
		case !openAI:
			name = cfg.ActiveRoute
			route = cfg.Routes[name]
		case cfg.DefaultOpenAIRoute != "":
			name = cfg.DefaultOpenAIRoute
			route = cfg.Routes[name]
		default:
			up := p.openAIDefault(r.Header)
			name, legacy = up.Name, true
			route = config.Route{Kind: config.KindOpenAI, BaseURL: up.BaseURL, Auth: config.AuthPassthrough}
		}
	}
	d.Route, d.Upstream, d.RouteReason, d.Provider = name, route.BaseURL, reason, route.ProviderName()
	if !route.Speaks(protocol) {
		refuse(http.StatusBadRequest, relay.ErrInvalidRequest, pipeline.CrossProtocolError(name, route, protocol))
		return
	}
	if ident != nil {
		switch {
		case !ident.AllowsAlias(dec.Alias):
			refuse(http.StatusForbidden, relay.ErrPermission, fmt.Sprintf(
				"this gateway key may only use the models %s (asked for %q)", strings.Join(ident.Aliases, ", "), d.ClientModel))
			return
		case len(ident.Routes) > 0 && (legacy || !ident.AllowsRoute(name)):
			refuse(http.StatusForbidden, relay.ErrPermission, "this gateway key may not use route "+name)
			return
		}
		if p.Keys != nil {
			if err := p.Keys.CheckQuota(ident.ID); err != nil {
				refuse(http.StatusTooManyRequests, relay.ErrRateLimit, err.Error())
				return
			}
		}
	}

	// Transformers see the client's body and each other's output. A failing
	// stage is skipped, never fatal: the turn must go through. A body that
	// could not be decompressed is forwarded as-is.
	model := route.Model
	if dec.Model != "" {
		model = dec.Model
	}
	preq.Route, preq.UpstreamModel = name, model
	if model == "" {
		preq.UpstreamModel = d.ClientModel
	}

	// Only a model turn is pruned: a compaction request (Responses
	// /responses/compact) summarizes the history and must see all of it.
	endpoint := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/openai"), "/v1")
	cur := body
	if decodeErr == nil && (!openAI || endpoint == endpointFor(protocol)) {
		cur = p.transform(ctx, preq, d, body)
	}

	c := &call{p: p, w: w, r: r, cfg: cfg, d: d, ident: ident, preq: preq, protocol: protocol, openAI: openAI,
		endpoint: endpoint, raw: raw, body: body, decoded: decodeErr == nil, alias: dec.Alias, legacy: legacy,
		primaryModel: model, fail: fail}
	c.run(hop{name: name, route: route, model: model}, cur)
}

func (p *Proxy) openAIDefault(h http.Header) Upstream {
	if p.OpenAIDefault != nil {
		return p.OpenAIDefault(h)
	}
	return Upstream{Name: "openai", BaseURL: "https://api.openai.com/v1"}
}

func (p *Proxy) transform(ctx context.Context, preq *pipeline.Request, d *store.Detail, body []byte) []byte {
	cur := body
	for _, t := range p.Hooks.Transformers {
		res, err := t.Transform(ctx, preq, cur)
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
		if res.KeepBody {
			d.KeepBody = true
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
	return cur
}

// shapeBody applies per-route rewrites. It only re-encodes when something
// actually changes, so a plain passthrough sends the client's exact bytes.
func shapeBody(body []byte, protocol, kind, wantModel string) (out []byte, stripped int, model string, err error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, 0, "", err
	}
	model, _ = m["model"].(string)
	changed := false
	if wantModel != "" && wantModel != model {
		m["model"], model, changed = wantModel, wantModel, true
	}
	// Signed thinking blocks are only valid on the model that produced them.
	if protocol == ir.ProtocolAnthropic && kind != config.KindAnthropic {
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

// Forward makes one upstream call and streams the response back untouched.
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request, plan Plan) {
	start := time.Now()
	openAI := ir.IsOpenAI(plan.Protocol)
	fail := relay.WriteAnthropicError
	if openAI {
		fail = relay.WriteOpenAIError
	}
	ident := keys.FromContext(r.Context())
	d := plan.Detail
	if d == nil {
		d = &store.Detail{Record: store.Record{
			ID: relay.NewID(), Time: start, Method: r.Method, Path: r.URL.Path, Protocol: plan.Protocol,
			Route: plan.Route, Upstream: plan.Upstream, AuthMode: relay.AuthMode(openAI, r.Header),
		}, RequestHeaders: relay.RedactHeaders(r.Header)}
		d.Client, d.Provider = detectClient(r.Header, ident), config.ProviderFromURL(plan.Upstream)
		if ident != nil {
			d.KeyID, d.KeyName = ident.ID, ident.Name
		}
	}

	resp, err := p.send(r, plan)
	if err != nil {
		d.DurationMs = time.Since(start).Milliseconds()
		if errors.Is(err, context.Canceled) {
			d.Error = "client cancelled"
		} else {
			d.Status, d.Error = http.StatusBadGateway, err.Error()
			fail(w, http.StatusBadGateway, relay.ErrAPI, "upstream: "+err.Error())
		}
		p.save(d, ident)
		return
	}
	defer resp.Body.Close()
	p.deliver(w, plan, d, resp, resp.Body, start, nil)
	p.save(d, ident)
}

// send makes the upstream request.
func (p *Proxy) send(r *http.Request, plan Plan) (*http.Response, error) {
	var body io.Reader = r.Body
	if plan.Body != nil {
		body = bytes.NewReader(plan.Body)
	}
	target := plan.Target
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		return nil, err
	}
	if plan.Body != nil {
		req.ContentLength = int64(len(plan.Body))
	} else {
		req.ContentLength = r.ContentLength
	}
	relay.CopyHeaders(req.Header, r.Header)
	if plan.DropEncoding {
		req.Header.Del("Content-Encoding")
	}
	if plan.Auth == config.AuthKey {
		applyKey(req.Header, plan.Kind, plan.Key)
	}
	for k, v := range plan.Headers {
		req.Header.Set(k, v)
	}
	return p.Client.Do(req)
}

// deliver streams resp to the client (body is resp's body, possibly after
// bytes already read from it) and fills the record from what went by.
// extra, when set, adds response headers.
func (p *Proxy) deliver(w http.ResponseWriter, plan Plan, d *store.Detail, resp *http.Response, body io.Reader,
	start time.Time, extra func(http.Header)) {
	openAI := ir.IsOpenAI(plan.Protocol)
	d.Status = resp.StatusCode
	d.TTFBMs = time.Since(start).Milliseconds()

	relay.CopyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Rlcd-Request-Id", d.ID)
	w.Header().Set("X-Rlcd-Route", plan.Route)
	if extra != nil {
		extra(w.Header())
	}
	w.WriteHeader(resp.StatusCode)

	isSSE := relay.IsSSE(resp.Header)
	capture := &relay.Capped{Limit: relay.MaxCapturedResponse}
	usage := relay.NewUsageReader(plan.Protocol)
	if msg := relay.Pump(w, body, func(chunk []byte) {
		capture.Write(chunk)
		if isSSE {
			usage.Feed(chunk)
		}
	}); msg != "" && d.Error == "" {
		d.Error = msg
	}
	d.DurationMs = time.Since(start).Milliseconds()

	var model, failure string
	switch {
	case isSSE:
		d.Usage, model, failure = usage.Result()
	case plan.ReadUsage || !openAI:
		d.Usage, model = relay.JSONUsage(plan.Protocol, capture.Bytes())
	}
	if d.Error == "" && failure != "" {
		d.Error = failure
	}
	if d.Model == "" && model != "" {
		d.Model, d.ModelVendor = model, config.ModelVendor(model)
	}
	if resp.StatusCode >= 400 && d.Error == "" {
		d.Error = relay.ErrorMessage(capture.Bytes())
	}
	if p.Config.Get().LogBodies {
		d.ResponseBody = capture.String()
	}
}

func detectClient(h http.Header, ident *keys.Identity) *store.Client {
	c := clients.Detect(h)
	if ident != nil {
		c.KeyName = ident.Name
	}
	return &c
}

// save prices the call, charges it to its gateway key and stores it.
func (p *Proxy) save(d *store.Detail, ident *keys.Identity) {
	if d.Usage != nil {
		d.CostUSD = pricing.Cost(pricing.Table(p.Config.Get()), d.Model, d.Protocol, d.Usage)
	}
	if ident != nil && p.Keys != nil {
		p.Keys.Record(ident.ID, d.Usage, d.CostUSD, d.Status >= 400 || d.Error != "")
	}
	store.ApplyBodies(store.EffectiveBodies(p.Config.Get()), d)
	if err := p.Store.Save(d); err != nil {
		log.Printf("store: %v", err)
	}
}

// applyKey replaces client credentials with the route's key.
func applyKey(h http.Header, kind, key string) {
	h.Del("Authorization")
	h.Del("X-Api-Key")
	h.Del("Api-Key")
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
	// A ChatGPT account id belongs to the client's own login, not to the key.
	h.Del("Chatgpt-Account-Id")
}

// ServesModels decides who answers GET /v1/models (and /openai/v1/models).
// The gateway lists its aliases to gateway-key clients, and to other
// clients once aliases exist; an Anthropic SDK (anthropic-version header)
// or a ChatGPT login keeps getting its own provider's list, as before.
func (p *Proxy) ServesModels(r *http.Request) bool {
	if keys.FromContext(r.Context()) != nil {
		return true
	}
	if r.Header.Get("Anthropic-Version") != "" || relay.IsChatGPTLogin(r.Header) {
		return false
	}
	return p.Hooks.Models != nil && len(p.Hooks.Models.Models(p.Config.Get())) > 0
}

// Models answers GET /v1/models with the model aliases, in a shape both
// SDK families read: OpenAI's list ({object, data[{id, object, created,
// owned_by}]}) with Anthropic's fields (type, display_name, created_at,
// has_more, first_id, last_id) alongside.
func (p *Proxy) Models(w http.ResponseWriter, r *http.Request) {
	var models []pipeline.Model
	if p.Hooks.Models != nil {
		models = p.Hooks.Models.Models(p.Config.Get())
	}
	ident := keys.FromContext(r.Context())
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	data := []map[string]any{}
	for _, m := range models {
		if ident != nil && !ident.AllowsAlias(m.ID) {
			continue
		}
		if ident != nil && len(ident.Routes) > 0 && !ident.AllowsRoute(m.Route) {
			continue
		}
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "created": created.Unix(), "owned_by": "rlcd-gateway",
			"type": "model", "display_name": m.ID, "created_at": created.Format(time.RFC3339),
		})
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
