// Package pipeline defines the extension points of the request path, so
// features (routing, pruning, recall, protocol adapters) live in their own
// packages and plug in without editing the proxy.
//
// For every POST /v1/messages the proxy:
//
//  1. builds a Request (body as the client sent it, X-ray, conversation id),
//  2. asks the Router which route serves it (falling back to the active route),
//  3. runs each Transformer in order on the body (pruning lives here),
//  4. applies route shaping (model swap, thinking strip) and forwards.
//
// Every stage reports what it did; reports are stored with the request so
// the dashboard can show why a turn was routed or pruned the way it was.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// Request is what every stage sees. Treat it as read-only.
type Request struct {
	ID string
	// Body is the Anthropic Messages JSON exactly as the client sent it.
	Body    []byte
	XRay    *ir.Request
	Headers http.Header
	Config  config.Config
	// ConversationID is stable across the turns of one agent session. Use it
	// for anything that must be sticky (pruning decisions, routing choice).
	ConversationID string
}

// RouteDecision is a Router's answer.
type RouteDecision struct {
	Route  string `json:"route"`
	Reason string `json:"reason"`
}

// Router picks a route. ok=false means "no opinion, use the active route".
type Router interface {
	Route(ctx context.Context, r *Request) (d RouteDecision, ok bool)
}

// Result is a Transformer's output.
type Result struct {
	// Body is the new request body; nil means unchanged.
	Body []byte
	// Summary is small and goes into the request list (e.g. tokens saved).
	Summary json.RawMessage
	// Detail can be large and is only loaded with the request detail
	// (e.g. the per-block kept/dropped diff).
	Detail json.RawMessage
}

// Transformer rewrites the body before it is forwarded. It receives the
// output of the previous transformer. On error the proxy logs it and
// forwards the previous body unchanged: a transformer must never break the
// agent's turn.
type Transformer interface {
	Name() string
	Transform(ctx context.Context, r *Request, body []byte) (*Result, error)
}

// Hooks is the set of plugged-in stages. The zero value is a plain proxy.
type Hooks struct {
	Router       Router
	Transformers []Transformer
}

// Marker is the text that replaces pruned content. It carries everything
// needed to fetch the original back through the recall tool, so keep the
// format stable: other packages parse it with ParseMarker.
func Marker(requestID, key string, tokens int) string {
	return fmt.Sprintf("[rlcd: ~%d tokens omitted · key %s · req %s · call rlcd_recall to restore]", tokens, key, requestID)
}

var markerRe = regexp.MustCompile(`\[rlcd: ~(\d+) tokens omitted · key (\S+) · req (\S+) · call rlcd_recall to restore\]`)

// ParseMarker extracts (requestID, key) from a marker, if s contains one.
func ParseMarker(s string) (requestID, key string, ok bool) {
	m := markerRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	return m[3], m[2], true
}

// ConversationID derives a stable id for the agent session.
//
// Claude Code sends metadata.user_id containing "_session_<uuid>"; that is
// used when present. Otherwise the id is a hash of the system prompt and the
// first user message, which do not change across the turns of a session.
func ConversationID(body []byte) string {
	var head struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &head)
	if m := sessionRe.FindStringSubmatch(head.Metadata.UserID); m != nil {
		return "cc-" + m[1]
	}
	h := sha256.New()
	h.Write(head.System)
	for _, m := range head.Messages {
		if m.Role == "user" {
			h.Write(m.Content)
			break
		}
	}
	return "h-" + hex.EncodeToString(h.Sum(nil))[:16]
}

var sessionRe = regexp.MustCompile(`_session_([0-9a-fA-F-]{8,})`)
