package selector

import (
	"context"
	"net/http"
	"time"
)

// Call is one finished System One call the gateway made on its own (the
// pruner's or the router's), handed to the observer in the context so it
// can be audited like a client's decision call.
type Call struct {
	// Source is who asked ("prune", "router"; see WithSource).
	Source string
	// ParentID and ConversationID name the request being handled.
	ParentID       string
	ConversationID string
	Backend        string // the selector backend preset
	// BackendName is the decisions backend that answered ("" for the
	// economy model).
	BackendName string
	BaseURL     string
	Model       string
	HasToken    bool
	Body        []byte
	// Status, Header and Response are zero when no response arrived.
	Status   int
	Header   http.Header
	Response []byte
	Start    time.Time
	Duration time.Duration
	Err      error
}

// Trace is what WithTrace attaches to a request's context.
type Trace struct {
	// Observe gets every call made under the context. It runs on the
	// caller's goroutine and must return at once (queue, never block).
	Observe        func(Call)
	ParentID       string
	ConversationID string
}

type traceKey struct{}
type sourceKey struct{}

// WithTrace makes every selector call under ctx observed.
func WithTrace(ctx context.Context, t Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

// WithSource names who makes the selector calls under ctx.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

func observe(ctx context.Context, c Call) {
	t, ok := ctx.Value(traceKey{}).(Trace)
	if !ok || t.Observe == nil {
		return
	}
	c.Source, _ = ctx.Value(sourceKey{}).(string)
	c.ParentID, c.ConversationID = t.ParentID, t.ConversationID
	defer func() { _ = recover() }() // auditing must never break a turn
	t.Observe(c)
}
