// Package pipeline defines the extension points of the request path, so
// features (routing, pruning, recall, protocol adapters) live in their own
// packages and plug in without editing the proxy.
//
// Every model call runs the same path, whatever its protocol (Anthropic
// Messages, OpenAI Chat Completions, OpenAI Responses). The proxy:
//
//  1. builds a Request (protocol, body as the client sent it, X-ray,
//     conversation id, gateway key),
//  2. asks the Router which route serves it (aliases first, then rules,
//     falling back to the protocol's default route),
//  3. runs each Transformer in order on the body (pruning lives here),
//  4. applies route shaping (model swap, thinking strip) and forwards.
//
// A request is only ever forwarded in its own protocol: a route that speaks
// another one is refused, never translated.
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
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// Request is what every stage sees. Treat it as read-only.
type Request struct {
	ID string
	// Protocol is one of the ir.Protocol* constants.
	Protocol string
	// Body is the request JSON exactly as the client sent it (decompressed
	// when the client compressed it), in Protocol's format.
	Body    []byte
	XRay    *ir.Request
	Headers http.Header
	Config  config.Config
	// ConversationID is stable across the turns of one agent session. Use it
	// for anything that must be sticky (pruning decisions, routing choice).
	// Requests made with a gateway key get ids scoped to that key, so two
	// users never share decisions.
	ConversationID string
	// KeyID is the gateway key that authenticated the request ("" without one).
	KeyID string
	// ClientKind is the detected caller type (clients.Kind*: agent, sdk,
	// cli, browser, unknown). Stages use it to pick defaults suited to a
	// coding agent or to a chat app.
	ClientKind string
	// Route and UpstreamModel are set once routing is done, for the
	// transformers (e.g. to price the model that will actually serve).
	Route         string
	UpstreamModel string
}

// ProtocolOf returns the request's protocol, Anthropic when unset.
func (r *Request) ProtocolOf() string {
	if r.Protocol == "" {
		return ir.ProtocolAnthropic
	}
	return r.Protocol
}

// RouteDecision is a Router's answer.
type RouteDecision struct {
	Route  string `json:"route"`
	Reason string `json:"reason"`
	// Model, when set, is the upstream model to send (from an alias); it
	// wins over the route's own model.
	Model string `json:"model,omitempty"`
	// Alias is the model alias the client asked for, if any.
	Alias string `json:"alias,omitempty"`
	// Error, when set, refuses the request with this message (e.g. an alias
	// whose route speaks another protocol).
	Error string `json:"error,omitempty"`
}

// Router picks a route. ok=false means "no opinion, use the active route";
// a Reason returned with ok=false is still recorded (e.g. "no rule matched").
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
	// KeepBody says a later lookup depends on this request's logged body
	// (pruning names it in a marker, and recall reads it), so the storage
	// bodies policy must keep it.
	KeepBody bool
}

// Transformer rewrites the body before it is forwarded. It receives the
// output of the previous transformer. On error the proxy logs it and
// forwards the previous body unchanged: a transformer must never break the
// agent's turn.
type Transformer interface {
	Name() string
	Transform(ctx context.Context, r *Request, body []byte) (*Result, error)
}

// Model is one entry of GET /v1/models: a model alias the gateway serves.
type Model struct {
	ID          string   `json:"id"`
	Route       string   `json:"route"`
	Model       string   `json:"model,omitempty"` // upstream model; "" sends the id as-is
	Description string   `json:"description,omitempty"`
	Protocols   []string `json:"protocols"`
}

// ModelLister lists the model aliases clients may ask for.
type ModelLister interface {
	Models(cfg config.Config) []Model
}

// RecallEvent is one successful rlcd_recall: the model needed a block that
// pruning had dropped. The recall package logs them; the pruner reads them
// as "should keep" feedback.
type RecallEvent struct {
	Time           time.Time
	ConversationID string
	// Req and Key are what the marker named: the request that first dropped
	// the block and its marker key (tool_use_id / call id, or ir key).
	Req, Key  string
	BlockKey  string
	ToolUseID string
	Tool      string
	Kind      string
	Tokens    int
}

// RecallLog lists successful recalls, oldest first.
type RecallLog interface {
	Recalls() []RecallEvent
}

// EmergencyOptions tune an emergency pruning pass.
type EmergencyOptions struct {
	// KeepThreshold replaces the configured one: blocks scored below it are
	// dropped.
	KeepThreshold float64
}

// EmergencyResult is what an emergency pass did.
type EmergencyResult struct {
	// Body is the pruned request, in the client's protocol, built from the
	// client's body; nil when the pass did not run or dropped nothing new.
	Body []byte
	// Skipped says why the pass did not run or changed nothing.
	Skipped string
	// NewDrops counts the blocks this pass dropped.
	NewDrops int
	// Summary and Detail are the pass's reports (same shapes as the
	// transformer's), KeepBody as in Result.
	Summary  json.RawMessage
	Detail   json.RawMessage
	KeepBody bool
}

// EmergencyPruner prunes a request that overflowed the model's context
// window, harder than its settings would, and makes the new drops the
// conversation's sticky state so the next turn does not overflow again.
type EmergencyPruner interface {
	EmergencyPrune(ctx context.Context, r *Request, opts EmergencyOptions) (*EmergencyResult, error)
}

// Hooks is the set of plugged-in stages. The zero value is a plain proxy.
type Hooks struct {
	Router       Router
	Transformers []Transformer
	Models       ModelLister
	// Emergency, when set, is used to recover from a context overflow.
	Emergency EmergencyPruner
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

// ConversationID derives a stable id for an Anthropic Messages session.
//
// Precedence (Claude Code has changed how it names its session over time):
//
//  1. the X-Claude-Code-Session-Id header (Claude Code 2.1.2xx and later);
//  2. a session id inside metadata.user_id when that is a JSON-encoded
//     object (the same releases send {"device_id":…,"session_id":…});
//  3. the legacy "_session_<uuid>" suffix of metadata.user_id;
//  4. a hash of the system prompt and the first user message, which do not
//     change across the turns of a session. Claude Code's billing line
//     ("x-anthropic-billing-header: cc_version=…") is skipped: its suffix
//     differs between the main loop and background calls of one session.
func ConversationID(h http.Header, body []byte) string {
	if v := strings.TrimSpace(h.Get("X-Claude-Code-Session-Id")); v != "" {
		return "cc-" + v
	}
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
	if id := sessionFromUserID(head.Metadata.UserID); id != "" {
		return "cc-" + id
	}
	if m := sessionRe.FindStringSubmatch(head.Metadata.UserID); m != nil {
		return "cc-" + m[1]
	}
	h2 := sha256.New()
	h2.Write(StableSystem(head.System))
	for _, m := range head.Messages {
		if m.Role == "user" {
			h2.Write(m.Content)
			break
		}
	}
	return "h-" + hex.EncodeToString(h2.Sum(nil))[:16]
}

var sessionRe = regexp.MustCompile(`_session_([0-9a-fA-F-]{8,})`)

// sessionFromUserID reads a session id out of a JSON-encoded user_id.
func sessionFromUserID(userID string) string {
	userID = strings.TrimSpace(userID)
	if !strings.HasPrefix(userID, "{") {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(userID), &m) != nil {
		return ""
	}
	for _, k := range []string{"session_id", "sessionId", "session_uuid", "sessionID"} {
		if s, _ := m[k].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// BillingPrefix starts the per-request billing line Claude Code puts in the
// system prompt. Its value changes between request types of one session.
const BillingPrefix = "x-anthropic-billing-header:"

// IsBillingLine reports whether a system text is Claude Code's billing line.
func IsBillingLine(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), BillingPrefix)
}

// StableSystem returns the bytes of an Anthropic system prompt (a string or
// a list of text blocks) without the billing line, for hashing.
func StableSystem(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if IsBillingLine(s) {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				return []byte(s[i+1:])
			}
			return nil
		}
		return raw
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return raw
	}
	var out []byte
	skipped := false
	for _, p := range parts {
		var b struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(p, &b)
		if IsBillingLine(b.Text) {
			skipped = true
			continue
		}
		out = append(out, p...)
	}
	if !skipped {
		// Unchanged bytes keep the ids of sessions logged before this rule.
		return raw
	}
	return out
}

// ConversationIDFor derives the conversation id in the request's protocol.
func ConversationIDFor(protocol string, h http.Header, body []byte) string {
	if ir.IsOpenAI(protocol) {
		return openAIConversationID(h, protocol, body)
	}
	return ConversationID(h, body)
}

// openAIConversationID is stable across the turns of one session. Codex
// sends its session id as a header and as prompt_cache_key; any client can
// send a Conversation-Id header or a prompt_cache_key. Otherwise the id
// hashes the end user ("user" or "safety_identifier", when the app sets
// one), the instructions and the first user input, like the Anthropic path:
// two chats that open with the same words for the same user share an id.
func openAIConversationID(h http.Header, protocol string, body []byte) string {
	for _, k := range []string{"Session_id", "Session-Id", "Conversation_id", "Conversation-Id"} {
		if v := h.Values(k); len(v) > 0 && v[0] != "" {
			return "cx-" + v[0]
		}
	}
	var head struct {
		PromptCacheKey   string            `json:"prompt_cache_key"`
		User             string            `json:"user"`
		SafetyIdentifier string            `json:"safety_identifier"`
		Instructions     json.RawMessage   `json:"instructions"`
		Input            json.RawMessage   `json:"input"`
		Messages         []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &head)
	if head.PromptCacheKey != "" {
		return "cx-" + head.PromptCacheKey
	}
	sum := sha256.New()
	if u := head.User + head.SafetyIdentifier; u != "" {
		sum.Write([]byte("user:" + u + "\x00"))
	}
	if protocol == ir.ProtocolOpenAIChat {
		// The system messages and the first user message.
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

// CrossProtocolError explains why a route cannot serve a request: the
// gateway never translates between protocols.
func CrossProtocolError(name string, route config.Route, protocol string) string {
	if ir.IsOpenAI(protocol) {
		return fmt.Sprintf("route %q speaks the Anthropic Messages API and this is an OpenAI-format request (%s). "+
			"The gateway does not translate between protocols: use a route of kind \"openai\" "+
			"(OpenRouter serves Claude models over the OpenAI format at https://openrouter.ai/api/v1)", name, protocol)
	}
	return fmt.Sprintf("route %q speaks the OpenAI format and this is an Anthropic Messages request. "+
		"The gateway does not translate between protocols: use a route of kind \"anthropic\" or \"openrouter\"", name)
}
