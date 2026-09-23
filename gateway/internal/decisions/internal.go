package decisions

import (
	"context"
	"errors"
	"fmt"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/clients"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// ObserveInternal receives the gateway's own economy-model calls (the
// pruner's and the router's auto rule, see selector.WithTrace) and logs
// them as decisions with client "rlcd-gateway". It never blocks and never
// fails the turn: the call is queued for a background writer, and when the
// queue is full, logging is off, or storing fails, a counter goes up and
// nothing else happens.
func (d *Decisions) ObserveInternal(c selector.Call) {
	if s, err := d.Settings(); err == nil && !s.logInternal() {
		return
	}
	d.internalOnce.Do(func() { go d.internalWriter() })
	select {
	case d.internalQ <- c:
	default:
		d.internalDropped.Add(1)
	}
}

func (d *Decisions) internalWriter() {
	for c := range d.internalQ {
		if err := d.logInternal(c); err != nil {
			d.internalFailed.Add(1)
		} else {
			d.internalLogged.Add(1)
		}
	}
}

func (d *Decisions) logInternal(c selector.Call) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	cfg := d.cfg.Get()
	backend, provider := Economy, economy(cfg).ProviderName()
	if c.BackendName != "" && c.BackendName != Economy {
		backend = c.BackendName
		if s, err := settingsFrom(cfg); err == nil {
			if b, ok := s.Backends[backend]; ok {
				provider = b.ProviderName()
			}
		}
	}
	rec := &store.Detail{Record: store.Record{
		ID: relay.NewID(), Time: c.Start, Method: "POST", Path: Endpoint, Protocol: ir.ProtocolSystemOne,
		Route: backend, Upstream: c.BaseURL, AuthMode: "none", ConversationID: c.ConversationID,
		ClientModel: c.Model, Status: c.Status, DurationMs: c.Duration.Milliseconds(), TTFBMs: c.Duration.Milliseconds(),
		RouteReason: "the gateway's own decision call (" + c.Source + ")",
	}, RequestHeaders: map[string]string{"Content-Type": "application/json"}}
	if c.HasToken {
		rec.AuthMode = "bearer"
		rec.RequestHeaders["Authorization"] = "Bearer •••"
	}
	cl := clients.Gateway()
	rec.Client = &cl
	rec.Provider = provider
	head, _ := parseRequest(c.Body)
	if x, err := ir.ParseSystemOne(c.Body); err == nil {
		rec.XRay, rec.EstTokens, rec.ByKind = x, x.Tokens, x.ByKind
	}
	out, ok := parseResponse(c.Response)
	sum := summarize(head, out, c.Header)
	sum.Backend, sum.Source, sum.ParentID = backend, c.Source, c.ParentID
	if sum.Source == "" {
		sum.Source = "gateway"
	}
	rec.Decisions = sum
	if ok && c.Status < 300 {
		rec.Usage = out.usage()
		rec.Model = out.Model
	}
	switch {
	case c.Err != nil && errors.Is(c.Err, context.DeadlineExceeded):
		rec.Error = "selector timed out: " + c.Err.Error()
	case c.Err != nil:
		rec.Error = c.Err.Error()
	}
	if cfg.LogBodies {
		rec.RequestBody, rec.ResponseBody = string(c.Body), string(c.Response)
	}
	d.charge(rec, nil)
	return d.saveRecord(rec)
}
