package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/resilience"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// maxErrorBody is how much of an upstream error response is read to
// classify it. Anything longer is passed through, never retried.
const maxErrorBody = 1 << 20

// maxStreamPeek bounds what is held back from the client while waiting for
// the first content event of a stream.
const maxStreamPeek = 256 << 10

// hop is a route (and the model to send on it) that an attempt goes to.
type hop struct {
	name  string
	route config.Route
	// model replaces the body's model ("" keeps the client's).
	model string
}

func (h hop) openRouter() bool { return h.route.ProviderName() == "openrouter" }

// call is one model request on its way upstream, possibly over several
// attempts.
type call struct {
	p        *Proxy
	w        http.ResponseWriter
	r        *http.Request
	cfg      config.Config
	d        *store.Detail
	ident    *keys.Identity
	preq     *pipeline.Request
	protocol string
	openAI   bool
	endpoint string
	// raw is the client's bytes, body the decoded ones; decoded is false
	// when the body could not be decompressed (it is then forwarded as-is).
	raw, body    []byte
	decoded      bool
	alias        string
	legacy       bool
	primaryModel string
	fail         func(status int, typ, msg string)
	// prunable: a model turn the pipeline may prune (not a compaction).
	prunable bool

	settings resilience.Settings
	policy   resilience.Resolved
	// Recovery state.
	capTokens int      // output limit learned from an output_too_large failure
	ignored   []string // OpenRouter providers excluded on the current hop
	atts      []store.Attempt
	// pendingChange is the emergency prune the next attempt carries;
	// emergencyChange one that did not make the context fit.
	pendingChange, emergencyChange *store.EmergencyPruneChange
}

// refusal is a request the gateway will not send.
type refusal struct {
	status   int
	typ, msg string
}

// prepared is one attempt's plan and what was changed to build it.
type prepared struct {
	plan    Plan
	sent    string // model sent
	changes *store.AttemptChanges
	guard   *store.MaxTokensChange
	limits  resilience.Limits
	// limitNow is the output limit the body carries (0 = none).
	limitNow int
}

func (c *call) engine() *resilience.Engine { return c.p.Resilience }

// run sends the request, recovering from failures while the client has
// seen nothing.
func (c *call) run(first hop, cur []byte) {
	d := c.d
	start := time.Now()
	if e := c.engine(); e != nil {
		c.settings = resilience.FromConfig(c.cfg)
		c.policy = c.settings.Resolve(first.name, c.alias)
	}
	recovering := c.engine() != nil && c.policy.Enabled
	fallbacks := c.fallbacks(first)
	usedFallbacks := 0
	backoffs := 0
	emergencyTried := false
	var emergencyNote string
	h := first

	for n := 1; ; n++ {
		pp, ref := c.prepare(h, cur)
		if ref != nil {
			d.Status, d.Error = ref.status, ref.msg
			c.p.save(d, c.ident)
			c.fail(ref.status, ref.typ, ref.msg)
			return
		}
		if n == 1 {
			d.MaxTokensGuard = pp.guard
		}
		if !recovering {
			// One call, as before (the guard still applies).
			c.p.Forward(c.w, c.r, pp.plan)
			return
		}
		att := store.Attempt{N: n, Route: h.name, Provider: h.route.ProviderName(), Model: pp.sent, Changes: pp.changes}
		t0 := time.Now()
		elapsed := func() time.Duration { return time.Since(start) }
		budgetLeft := time.Duration(c.policy.TimeBudgetMs)*time.Millisecond - elapsed()
		canRetry := n < c.policy.MaxAttempts && budgetLeft > 0

		resp, err := c.p.send(c.r, pp.plan)
		var f resilience.Failure
		var errBody []byte
		var peek *resilience.Peek
		var stopPeek func()
		switch {
		case err != nil && errors.Is(err, context.Canceled):
			att.DurationMs = time.Since(t0).Milliseconds()
			c.finish(append(c.attempts(), att), h, first)
			d.Error = "client cancelled"
			d.DurationMs = elapsed().Milliseconds()
			c.p.save(d, c.ident)
			return
		case err != nil:
			f = resilience.ClassifyTransport(err)
		case resp.StatusCode >= 400:
			errBody, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
			if len(errBody) > maxErrorBody {
				// Too big to be a plain error: pass it on as it is.
				att.Status, att.DurationMs = resp.StatusCode, time.Since(t0).Milliseconds()
				att.Class, att.Action = resilience.ClassUnknown, resilience.ActionNone
				c.commit(append(c.attempts(), att), h, first, pp, resp, io.MultiReader(bytes.NewReader(errBody), resp.Body), start, nil)
				return
			}
			f = resilience.Classify(resp.StatusCode, resp.Header, errBody)
		case canRetry && relay.IsSSE(resp.Header):
			pk, stop := resilience.PeekStream(resp.Body, maxStreamPeek, budgetLeft)
			peek, stopPeek = &pk, stop
			if pk.Failure == nil {
				att.Status, att.DurationMs = resp.StatusCode, time.Since(t0).Milliseconds()
				c.commit(append(c.attempts(), att), h, first, pp, resp,
					io.MultiReader(bytes.NewReader(pk.Buffered), pk.Rest), start, stopPeek)
				return
			}
			f = *pk.Failure
		default:
			att.Status, att.DurationMs = resp.StatusCode, time.Since(t0).Milliseconds()
			c.commit(append(c.attempts(), att), h, first, pp, resp, resp.Body, start, nil)
			return
		}

		// A failure, and the client has seen nothing yet.
		att.DurationMs = time.Since(t0).Milliseconds()
		att.Status = f.Status
		if resp != nil {
			att.Status = resp.StatusCode
		}
		att.Class, att.Message, att.UpstreamProvider = f.Class, f.Message, f.UpstreamProvider
		if f.Window > 0 || f.MaxOutput > 0 {
			c.engine().Registry.Learn(pp.sent, resilience.Limits{ContextWindow: f.Window, MaxOutputTokens: f.MaxOutput})
		}

		state := resilience.State{Attempt: n, Elapsed: elapsed(), OpenRouter: h.openRouter(), Ignored: c.ignored,
			BackoffsUsed: backoffs, FallbacksLeft: len(fallbacks) - usedFallbacks, EmergencyTried: emergencyTried}
		clampTo := 0
		if f.Class == resilience.ClassOutputTooLarge {
			clampTo = resilience.ClampTarget(f, pp.limits, c.estimate(cur), c.policy.MaxTokensGuard.SafetyMarginTokens, pp.limitNow)
			state.CanClamp = clampTo > 0
		}
		dec := resilience.Decide(c.policy.Policy, f, state, c.engine().Rand())
		att.Action, att.ActionDetail = dec.Action, dec.Detail

		// Carry out the action; it may turn out impossible, and then the
		// failure goes to the client.
		next, nextCur := h, cur
		switch dec.Action {
		case resilience.ActionClamp:
			c.capTokens = clampTo
			att.ActionDetail = fmt.Sprintf("retry with an output limit of %d", clampTo)
		case resilience.ActionEmergencyPrune:
			emergencyTried = true
			body, note, change := c.emergency(cur, f, pp.limits)
			emergencyNote = note
			if body == nil {
				att.Action, att.ActionDetail = resilience.ActionGiveUp, "emergency prune: "+note
			} else {
				att.ActionDetail = note
				nextCur = body
				c.pendingChange = change
			}
		case resilience.ActionIgnoreProvider:
			c.ignored = append(c.ignored, f.UpstreamProvider)
		case resilience.ActionBackoff:
			backoffs++
			att.WaitMs = dec.Wait.Milliseconds()
		case resilience.ActionFallback:
			next = fallbacks[usedFallbacks]
			usedFallbacks++
			att.ActionDetail = strings.TrimPrefix(att.ActionDetail+"; ", "; ") + "fall back to route " + next.name
			if next.model != "" {
				att.ActionDetail += " (model " + next.model + ")"
			}
		}
		atts := append(c.attempts(), att)

		if !dec.Retries() || att.Action == resilience.ActionGiveUp {
			c.giveUp(atts, h, first, pp, f, resp, errBody, peek, stopPeek, err, start, emergencyTried, emergencyNote)
			return
		}
		c.setAttempts(atts)
		if resp != nil {
			resp.Body.Close()
		}
		if stopPeek != nil {
			stopPeek()
		}
		if dec.Action == resilience.ActionBackoff {
			if err := c.engine().Sleep(c.r.Context(), dec.Wait); err != nil {
				c.finish(atts, h, first)
				d.Error = "client cancelled"
				d.DurationMs = elapsed().Milliseconds()
				c.p.save(d, c.ident)
				return
			}
		}
		if next.name != h.name || next.model != h.model {
			// Another route: its own limits and providers.
			backoffs, c.capTokens, c.ignored = 0, 0, nil
		}
		h, cur = next, nextCur
	}
}

// attempts so far (kept on the call between iterations).
func (c *call) attempts() []store.Attempt { return c.atts }

func (c *call) setAttempts(a []store.Attempt) { c.atts = a }

// estimate is the gateway's token estimate of a body.
func (c *call) estimate(b []byte) int {
	if bytes.Equal(b, c.body) && c.preq.XRay != nil {
		return c.preq.XRay.Tokens
	}
	if x, err := ir.ParseFor(c.protocol, b); err == nil {
		return x.Tokens
	}
	return 0
}

// prepare builds the plan for one attempt on hop h from cur (the body the
// pipeline produced, or its emergency-pruned version): route shaping, the
// max_tokens guard, and the recovery's own changes.
func (c *call) prepare(h hop, cur []byte) (prepared, *refusal) {
	d := c.d
	var pp prepared
	out, stripped, sent := cur, 0, d.ClientModel
	if c.decoded {
		var err error
		out, stripped, sent, err = shapeBody(cur, c.protocol, h.route.Kind, h.model)
		if err != nil {
			// Not JSON we understand: forward as-is rather than break the client.
			out, sent = cur, d.ClientModel
		}
	}
	changes := &store.AttemptChanges{}
	if c.pendingChange != nil {
		changes.EmergencyPrune, changes.CacheInvalidated = c.pendingChange, true
		c.pendingChange = nil
	}
	if e := c.engine(); e != nil && c.decoded {
		res := c.settings.Resolve(h.name, c.alias)
		beta1M := strings.Contains(strings.Join(c.r.Header.Values("Anthropic-Beta"), ","), "context-1m")
		pp.limits = c.settings.LimitsFor(e.Registry, res, sent, h.openRouter(), beta1M)
		if b, err := resilience.DecodeBody(out); err == nil {
			provider := h.route.ProviderName()
			changed := false
			est := c.estimate(cur)
			if g := resilience.ApplyGuard(b, resilience.GuardInput{Protocol: c.protocol, Guard: res.MaxTokensGuard,
				Limits: pp.limits, EstInputTokens: est, FirstParty: c.legacy || provider == "openai" || provider == "anthropic",
				OpenAIAPI: provider == "openai", Agent: c.preq.ClientKind == "agent"}); g != nil {
				pp.guard, changes.MaxTokens, changed = g, g, true
			}
			if c.capTokens > 0 {
				if ch := resilience.ClampOutput(b, c.protocol, provider == "openai", c.capTokens); ch != nil {
					ch.EstInputTokens = est
					changes.MaxTokens, changed = ch, true
				}
			}
			if len(c.ignored) > 0 && h.openRouter() {
				resilience.IgnoreProviders(b, c.ignored)
				changes.IgnoredProviders, changed = append([]string(nil), c.ignored...), true
			}
			if _, v, ok := b.OutputLimit(c.protocol); ok {
				pp.limitNow = v
			}
			if changed {
				out = b.Encode()
			}
		}
	}
	if changes.MaxTokens != nil || changes.EmergencyPrune != nil || len(changes.IgnoredProviders) > 0 {
		pp.changes = changes
	}
	d.Model, d.StrippedThinking, d.ModelVendor = sent, stripped, config.ModelVendor(sent)
	pp.sent = sent
	send, dropEnc := c.raw, false
	d.SentBody = ""
	if !bytes.Equal(out, c.body) {
		send, dropEnc = out, !bytes.Equal(c.body, c.raw)
		if c.cfg.LogBodies {
			d.SentBody = string(out)
		}
	}

	key := ""
	switch {
	case h.route.Auth == config.AuthKey:
		if key = h.route.ResolvedKey(); key == "" {
			return pp, &refusal{status: http.StatusBadGateway, typ: relay.ErrAPI,
				msg: "route " + h.name + " needs a key (api_key or env " + h.route.APIKeyEnv + ")"}
		}
	case c.ident != nil && !relay.HasCredentials(c.r.Header):
		msg := "route " + h.name + " forwards the client's own provider login, but this request carried only a gateway key. " +
			"Send the gateway key in the X-Rlcd-Key header next to your own login, or use a route that holds a provider key"
		if c.legacy {
			msg = "no route holds a provider key for OpenAI-format requests: add a model alias or set a default OpenAI route with a key"
		}
		return pp, &refusal{status: http.StatusUnauthorized, typ: relay.ErrAuthentication, msg: msg}
	}

	target := strings.TrimRight(h.route.BaseURL, "/") + c.r.URL.Path
	if c.openAI {
		target = strings.TrimRight(h.route.BaseURL, "/") + c.endpoint
	}
	pp.plan = Plan{Protocol: c.protocol, Route: h.name, Kind: h.route.Kind, Upstream: h.route.BaseURL, Target: target,
		Auth: h.route.Auth, Key: key, Headers: h.route.Headers, Body: send, DropEncoding: dropEnc, ReadUsage: true,
		Detail: d}
	return pp, nil
}

// fallbacks lists the usable fallback hops of the request's route (or
// alias), in order. A fallback that cannot serve this request (another
// protocol, no key, not allowed for the gateway key) is left out.
func (c *call) fallbacks(first hop) []hop {
	if c.engine() == nil {
		return nil
	}
	var out []hop
	for _, fb := range c.policy.Fallbacks {
		name, model := resilience.SplitFallback(fb)
		rt, ok := c.cfg.Routes[name]
		switch {
		case !ok, !rt.Speaks(c.protocol):
			continue
		case c.ident != nil && !c.ident.AllowsRoute(name):
			continue
		case rt.Auth == config.AuthKey && rt.ResolvedKey() == "":
			continue
		case rt.Auth != config.AuthKey && c.ident != nil && !relay.HasCredentials(c.r.Header):
			continue
		}
		if model == "" {
			model = rt.Model
		}
		if model == "" {
			// An alias's upstream model: the alias name means nothing upstream.
			model = c.primaryModel
		}
		if name == first.name && model == first.model {
			continue
		}
		out = append(out, hop{name: name, route: rt, model: model})
	}
	return out
}

// emergency runs the emergency prune. It returns the new body (nil when it
// did not run or did not help) and what it did, in words.
func (c *call) emergency(cur []byte, f resilience.Failure, l resilience.Limits) ([]byte, string, *store.EmergencyPruneChange) {
	em := c.p.Hooks.Emergency
	switch {
	case em == nil:
		return nil, "pruning is not available", nil
	case !c.decoded:
		return nil, "the request body could not be decoded", nil
	case !c.prunable:
		return nil, "this endpoint is never pruned (a compaction request must see the whole history)", nil
	}
	res, err := em.EmergencyPrune(c.r.Context(), c.preq, pipeline.EmergencyOptions{KeepThreshold: c.policy.EmergencyPrune.KeepThreshold})
	if err != nil {
		return nil, "failed: " + err.Error(), nil
	}
	d := c.d
	if res.Summary != nil {
		if d.Stages == nil {
			d.Stages = map[string]json.RawMessage{}
		}
		d.Stages["prune_emergency"] = res.Summary
	}
	if res.Detail != nil {
		if d.StageDetails == nil {
			d.StageDetails = map[string]json.RawMessage{}
		}
		d.StageDetails["prune_emergency"] = res.Detail
	}
	if res.KeepBody {
		d.KeepBody = true
	}
	if res.Body == nil {
		return nil, res.Skipped, nil
	}
	before, after := c.estimate(cur), c.estimate(res.Body)
	ch := &store.EmergencyPruneChange{Dropped: res.NewDrops, SavedTokens: before - after, TokensBefore: before,
		TokensAfter: after, Threshold: c.policy.EmergencyPrune.KeepThreshold}
	note := fmt.Sprintf("dropped %d blocks, ~%d tokens (%d → %d); prompt cache invalidated", res.NewDrops, ch.SavedTokens, before, after)
	window := f.Window
	if window == 0 {
		window = l.ContextWindow
	}
	if window > 0 && after > window {
		c.emergencyChange = ch
		return nil, note + fmt.Sprintf("; still ~%d tokens over the %d-token window", after-window, window), nil
	}
	return res.Body, note, ch
}

// commit relays a response the client will get, and saves the record.
func (c *call) commit(atts []store.Attempt, h hop, first hop, pp prepared, resp *http.Response, body io.Reader,
	start time.Time, stop func()) {
	defer func() {
		resp.Body.Close()
		if stop != nil {
			stop()
		}
	}()
	c.finish(atts, h, first)
	n := len(atts)
	c.p.deliver(c.w, pp.plan, c.d, resp, body, start, func(hd http.Header) {
		if n > 1 {
			hd.Set("X-Rlcd-Attempts", strconv.Itoa(n))
		}
	})
	c.p.save(c.d, c.ident)
}

// finish fills the record's recovery summary.
func (c *call) finish(atts []store.Attempt, h hop, first hop) {
	d := c.d
	last := atts[len(atts)-1]
	if len(atts) > 1 || last.Class != "" || last.Changes != nil {
		d.Attempts = atts
	}
	d.Retried = len(atts) > 1
	d.Recovered = d.Retried && last.Class == "" && last.Status > 0 && last.Status < 400
	d.ErrorClass = last.Class
	if h.name != first.name {
		d.PrimaryRoute, d.FallbackRoute = first.name, h.name
		d.Route, d.Upstream, d.Provider = h.name, h.route.BaseURL, h.route.ProviderName()
		d.RouteReason = strings.TrimPrefix(d.RouteReason+"; ", "; ") + "fallback from " + first.name + " to " + h.name
	}
}

// giveUp hands the last failure to the client.
func (c *call) giveUp(atts []store.Attempt, h, first hop, pp prepared, f resilience.Failure, resp *http.Response,
	errBody []byte, peek *resilience.Peek, stop func(), sendErr error, start time.Time, emergencyTried bool, note string) {
	d := c.d
	c.finish(atts, h, first)
	// A context overflow the emergency prune could not fix gets a clear
	// explanation; the provider's own message comes first, so clients that
	// recognise it (to compact the conversation) still do.
	if f.Class == resilience.ClassContextOverflow && emergencyTried {
		if resp != nil {
			resp.Body.Close()
		}
		if stop != nil {
			stop()
		}
		status := http.StatusBadRequest
		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			status = resp.StatusCode
		}
		msg := c.overflowMessage(f, pp, note)
		d.Status, d.Error = status, msg
		d.TTFBMs, d.DurationMs = time.Since(start).Milliseconds(), time.Since(start).Milliseconds()
		c.writeOverflow(status, msg)
		c.p.save(d, c.ident)
		return
	}
	switch {
	case sendErr != nil:
		d.Status, d.Error = http.StatusBadGateway, sendErr.Error()
		d.DurationMs = time.Since(start).Milliseconds()
		c.fail(http.StatusBadGateway, relay.ErrAPI, "upstream: "+sendErr.Error())
		c.p.save(d, c.ident)
	case peek != nil:
		c.commit(atts, h, first, pp, resp, io.MultiReader(bytes.NewReader(peek.Buffered), peek.Rest), start, stop)
	default:
		c.commit(atts, h, first, pp, resp, io.MultiReader(bytes.NewReader(errBody), resp.Body), start, stop)
	}
}

func (c *call) overflowMessage(f resilience.Failure, pp prepared, note string) string {
	window := f.Window
	if window == 0 {
		window = pp.limits.ContextWindow
	}
	input := f.Input
	if input == 0 && c.emergencyChange != nil {
		input = c.emergencyChange.TokensAfter
	}
	var parts []string
	if window > 0 {
		parts = append(parts, fmt.Sprintf("the model's context window is %d tokens", window))
	}
	if input > 0 {
		parts = append(parts, fmt.Sprintf("the input is ~%d tokens", input))
	}
	parts = append(parts, "tried: emergency prune ("+note+")")
	msg := f.Message
	if msg == "" {
		msg = "the conversation does not fit the model's context window"
	}
	return msg + " [rlcd-gateway: " + strings.Join(parts, "; ") +
		". Compact or restart the conversation, or route it to a model with a larger window]"
}

// writeOverflow writes the overflow error in the client's protocol,
// without the "rlcd-gateway: " prefix so the provider's words lead.
func (c *call) writeOverflow(status int, msg string) {
	w := c.w
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Rlcd-Request-Id", c.d.ID)
	w.WriteHeader(status)
	if c.openAI {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"type": relay.ErrInvalidRequest, "code": "context_length_exceeded", "message": msg}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error",
		"error": map[string]string{"type": relay.ErrInvalidRequest, "message": msg}})
}
