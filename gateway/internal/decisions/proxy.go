package decisions

import (
	"bytes"
	"context"
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
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Endpoint is the System One path on every backend.
const Endpoint = "/v1/systemone"

// Source of a client's own call.
const SourceClient = "client"

// conversationHeaders may name the conversation a decision belongs to.
var conversationHeaders = []string{"X-Rlcd-Conversation-Id", "Conversation-Id", "Session-Id", "X-Claude-Code-Session-Id"}

func conversationID(h http.Header) string {
	for _, k := range conversationHeaders {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return "cx-" + v
		}
	}
	return ""
}

func authMode(h http.Header) string {
	switch {
	case h.Get("Authorization") != "":
		return "bearer"
	case h.Get("X-Api-Key") != "" || h.Get("Api-Key") != "":
		return "api-key"
	}
	return "none"
}

// fail answers with an error the System One clients read: {"error":
// {"type", "message"}} (rlcd.js reads error.message, like the OpenAI SDKs).
func fail(w http.ResponseWriter, status int, typ, msg string) {
	relay.WriteOpenAIError(w, status, typ, msg)
}

// serve proxies one decision call.
func (d *Decisions) serve(w http.ResponseWriter, r *http.Request) {
	cfg := d.cfg.Get()
	start := time.Now()
	raw, err := io.ReadAll(io.LimitReader(r.Body, relay.MaxRequestBody+1))
	if err != nil || len(raw) > relay.MaxRequestBody {
		fail(w, http.StatusRequestEntityTooLarge, relay.ErrInvalidRequest, "request body too large or unreadable")
		return
	}
	body, decodeErr := relay.DecodeBody(r.Header.Get("Content-Encoding"), raw)
	if decodeErr != nil {
		body = raw
	}
	ident := keys.FromContext(r.Context())
	rec := &store.Detail{Record: store.Record{
		ID: relay.NewID(), Time: start, Method: r.Method, Path: r.URL.Path, Protocol: ir.ProtocolSystemOne,
		AuthMode: authMode(r.Header), ConversationID: conversationID(r.Header),
	}, RequestHeaders: relay.RedactHeaders(r.Header)}
	c := clients.Detect(r.Header)
	rec.Client = &c
	if ident != nil {
		c.KeyName = ident.Name
		rec.KeyID, rec.KeyName = ident.ID, ident.Name
		if rec.ConversationID != "" {
			rec.ConversationID = ident.ID + ":" + rec.ConversationID
		}
		if !relay.HasCredentials(r.Header) {
			rec.AuthMode = "gateway-key"
		}
	}
	head, perr := parseRequest(body)
	rec.ClientModel = head.Model
	rec.Decisions = summarize(head, response{}, nil)
	rec.Decisions.Source = SourceClient
	if x, err := ir.ParseSystemOne(body); err == nil && decodeErr == nil {
		rec.XRay, rec.EstTokens, rec.ByKind = x, x.Tokens, x.ByKind
	}
	switch {
	case decodeErr != nil:
		rec.StageErrors = map[string]string{"xray": decodeErr.Error()}
	case perr != nil:
		// Not JSON: the backend answers with its own error.
		rec.StageErrors = map[string]string{"xray": perr.Error()}
	}
	if cfg.LogBodies {
		rec.RequestBody = string(body)
	}
	refuse := func(status int, typ, msg string) {
		rec.Status, rec.Error = status, msg
		rec.DurationMs = time.Since(start).Milliseconds()
		d.finishClient(rec, ident)
		fail(w, status, typ, msg)
	}

	s, err := settingsFrom(cfg)
	if err != nil {
		refuse(http.StatusInternalServerError, relay.ErrAPI, err.Error())
		return
	}
	name, be, reason, err := s.Resolve(cfg, head.Model)
	rec.Route, rec.RouteReason, rec.Decisions.Backend = name, reason, name
	rec.Upstream, rec.Provider = be.BaseURL, be.ProviderName()
	if err != nil {
		refuse(http.StatusBadGateway, relay.ErrAPI, err.Error())
		return
	}
	if ident != nil {
		switch {
		case !ident.AllowsAlias(head.Model):
			refuse(http.StatusForbidden, relay.ErrPermission, fmt.Sprintf(
				"this gateway key may only use the models %s (asked for %q)", strings.Join(ident.Aliases, ", "), head.Model))
			return
		case !ident.AllowsRoute(name):
			refuse(http.StatusForbidden, relay.ErrPermission, "this gateway key may not use decision backend "+name)
			return
		}
		if d.Keys != nil {
			if err := d.Keys.CheckQuota(ident.ID); err != nil {
				refuse(http.StatusTooManyRequests, relay.ErrRateLimit, err.Error())
				return
			}
		}
	}
	if be.Auth == AuthPassthrough && ident != nil && !relay.HasCredentials(r.Header) {
		refuse(http.StatusUnauthorized, relay.ErrAuthentication, "decision backend "+name+
			" forwards the client's own credentials, but this request carried only a gateway key. "+
			"Send the gateway key in the X-Rlcd-Key header next to your own Authorization")
		return
	}

	target := strings.TrimRight(be.BaseURL, "/") + Endpoint
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		refuse(http.StatusBadGateway, relay.ErrAPI, err.Error())
		return
	}
	req.ContentLength = int64(len(raw))
	relay.CopyHeaders(req.Header, r.Header)
	if be.Auth != AuthPassthrough {
		setToken(req.Header, be.ResolvedToken())
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		rec.DurationMs = time.Since(start).Milliseconds()
		if errors.Is(err, context.Canceled) {
			rec.Error = "client cancelled"
		} else {
			rec.Status, rec.Error = http.StatusBadGateway, err.Error()
			fail(w, http.StatusBadGateway, relay.ErrAPI, "upstream: "+err.Error())
		}
		d.finishClient(rec, ident)
		return
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	rec.TTFBMs = time.Since(start).Milliseconds()

	// A decision answer is a small JSON document: read it whole (up to the
	// capture limit) before answering, so the call is priced and charged to
	// its gateway key before the client can send the next one. The bytes
	// go out unchanged; anything past the limit is streamed after them.
	captured, rerr := io.ReadAll(io.LimitReader(resp.Body, relay.MaxCapturedResponse))
	if rerr != nil {
		rec.Error = "upstream read: " + rerr.Error()
	}
	rec.DurationMs = time.Since(start).Milliseconds()
	out, ok := parseResponse(captured)
	sum := summarize(head, out, resp.Header)
	sum.Backend, sum.Source = name, SourceClient
	rec.Decisions = sum
	if ok {
		rec.Usage = out.usage()
		if out.Model != "" {
			rec.Model = out.Model
		}
	}
	if resp.StatusCode >= 400 && rec.Error == "" {
		rec.Error = relay.ErrorMessage(captured)
	}
	d.charge(rec, ident)

	relay.CopyHeaders(w.Header(), resp.Header)
	if resp.ContentLength >= 0 {
		// What arrives is what goes out, so the client gets the backend's
		// framing, not a chunked stream.
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.Header().Set("X-Rlcd-Request-Id", rec.ID)
	w.Header().Set("X-Rlcd-Route", name)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(captured); err != nil && rec.Error == "" {
		rec.Error = "client went away: " + err.Error()
	}
	if len(captured) == relay.MaxCapturedResponse && rerr == nil {
		if msg := relay.Pump(w, resp.Body, func([]byte) {}); msg != "" && rec.Error == "" {
			rec.Error = msg
		}
		rec.DurationMs = time.Since(start).Milliseconds()
	}
	if cfg.LogBodies {
		rec.ResponseBody = string(captured)
	}
	d.store(rec)

	if resp.StatusCode < 300 && rec.Error == "" {
		d.maybeMirror(cfg, s, name, raw, r.Header, rec)
	}
}

// setToken replaces the client's credentials with the backend's token
// (none at all when the backend has no token).
func setToken(h http.Header, tok string) {
	h.Del("Authorization")
	h.Del("X-Api-Key")
	h.Del("Api-Key")
	if tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	}
}

// charge prices the call and charges it to its gateway key.
func (d *Decisions) charge(rec *store.Detail, ident *keys.Identity) {
	if rec.Model == "" {
		rec.Model = rec.ClientModel
	}
	rec.ModelVendor = config.ModelVendor(rec.Model)
	if rec.Usage != nil {
		rec.CostUSD = pricing.Cost(pricing.Table(d.cfg.Get()), rec.Model, rec.Protocol, rec.Usage)
	}
	if ident != nil && d.Keys != nil {
		d.Keys.Record(ident.ID, rec.Usage, rec.CostUSD, rec.Status >= 400 || rec.Error != "")
	}
}

// save applies the bodies policy and stores the record.
func (d *Decisions) saveRecord(rec *store.Detail) error {
	store.ApplyBodies(store.EffectiveBodies(d.cfg.Get()), rec)
	return d.save(rec)
}

// store saves a client's call; a storage failure is logged, since the
// client has its answer anyway.
func (d *Decisions) store(rec *store.Detail) {
	if err := d.saveRecord(rec); err != nil {
		log.Printf("decisions: store: %v", err)
	}
}

// finishClient charges and stores a call that ends before any answer.
func (d *Decisions) finishClient(rec *store.Detail, ident *keys.Identity) {
	d.charge(rec, ident)
	d.store(rec)
}
