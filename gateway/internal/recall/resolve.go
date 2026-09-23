package recall

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Error codes recorded in recall events and used to pick the message the
// model sees. They are part of the event schema: keep them stable.
const (
	ErrBadArgs       = "bad_args"        // neither key+req nor a parsable marker
	ErrDisabled      = "disabled"        // recall is turned off in settings
	ErrUnknownReq    = "unknown_request" // no such request id in the store
	ErrBodyNotLogged = "body_not_logged" // the request exists but its body was not kept
	ErrKeyNotFound   = "key_not_found"   // the request has no block with that key
	ErrExpired       = "expired"         // retention purged the request
	ErrStore         = "store_error"     // the store could not be read or parsed
)

// lookupError is a failed recall. Msg is written for the model: it says what
// went wrong and what to do instead.
type lookupError struct {
	Code string
	Msg  string
}

func (e *lookupError) Error() string { return e.Msg }

// found is a resolved block: its full original content plus what the event
// log and the stats need to know about it.
type found struct {
	Req            string
	BlockKey       string // ir key, e.g. "m4.b0"
	ToolUseID      string
	Tool           string // tool name, for tool_use / tool_result blocks
	Kind           string // ir kind
	ConversationID string
	Content        string
	// KeyID is the gateway key of the request the content came from.
	KeyID string
}

// maxHops bounds how many markers resolve follows when the block it finds
// was itself already a marker in that request (pruned on an earlier turn).
const maxHops = 3

// resolve loads request req from the store and returns the full content of
// the block named by key: an ir key ("m4.b0", "sys.0", "tool.2") or the
// tool_use_id of a tool_result.
func resolve(st *store.Store, req, key string) (*found, error) {
	for hop := 0; ; hop++ {
		f, err := resolveOnce(st, req, key)
		if err != nil {
			return nil, err
		}
		// The body we looked in may already carry a marker for this block
		// (pruned on an earlier turn); follow it to the request that still
		// has the original. Only a block that is nothing but a marker counts,
		// not text that merely quotes one.
		c := strings.TrimSpace(f.Content)
		nreq, nkey, ok := pipeline.ParseMarker(c)
		if !ok || hop >= maxHops || (nreq == req && nkey == key) || !(strings.HasPrefix(c, "[rlcd: ") && strings.HasSuffix(c, "]")) {
			return f, nil
		}
		req, key = nreq, nkey
	}
}

func unknownRequest(req string) error {
	return &lookupError{ErrUnknownReq, fmt.Sprintf(
		"The gateway has no request with id %q. Copy the req value exactly as it appears in the marker (it looks like 20260923T010203-0a1b2c3d4e5f6a7b). If it is right, the log was deleted and the content cannot be restored: re-run the tool or re-read the file instead.", req)}
}

func resolveOnce(st *store.Store, req, key string) (*found, error) {
	d, err := st.Get(req)
	var purged *store.PurgedError
	if errors.As(err, &purged) {
		return nil, &lookupError{ErrExpired, fmt.Sprintf(
			"%s, so the content of request %s cannot be restored (storage retention; its conversation was idle past conversation_ttl or the request predates it). Re-run the tool or re-read the file to get it again.",
			purged.Error(), req)}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, unknownRequest(req)
	}
	if err != nil {
		return nil, &lookupError{ErrStore, fmt.Sprintf("The gateway could not read request %q: %v", req, err)}
	}
	if d.RequestBody == "" {
		return nil, &lookupError{ErrBodyNotLogged, fmt.Sprintf(
			"The gateway did not keep the body of request %s (log_bodies is off, or the storage bodies policy did not keep it), so the omitted content cannot be restored. Re-run the tool or re-read the file to get it again.", req)}
	}
	body := []byte(d.RequestBody)
	x, err := ir.ParseFor(d.Protocol, body)
	if err != nil {
		return nil, &lookupError{ErrStore, fmt.Sprintf("The logged body of request %s could not be parsed: %v", req, err)}
	}
	b := findBlock(x, key)
	if b == nil {
		return nil, &lookupError{ErrKeyNotFound, fmt.Sprintf(
			"Request %s has no block with key %q. Copy the key exactly from the marker: it is either a tool_use_id (toolu_…) or a block key like m4.b0.", req, key)}
	}
	content, err := blockContent(body, *b)
	if ir.IsOpenAI(d.Protocol) {
		content, err = openAIBlockContent(d.Protocol, body, *b)
	}
	if err != nil {
		return nil, &lookupError{ErrStore, fmt.Sprintf("Block %s of request %s could not be read: %v", b.Key, req, err)}
	}
	f := &found{Req: req, BlockKey: b.Key, ToolUseID: b.ToolUseID, Tool: b.Name, Kind: b.Kind,
		ConversationID: d.ConversationID, Content: content, KeyID: d.KeyID}
	if f.Kind == ir.KindToolResult {
		f.Tool = toolName(x, b.ToolUseID)
	}
	return f, nil
}

// findBlock matches an ir key first, then a tool_use_id. A tool_use_id names
// the tool_result (that is what gets pruned); the tool_use is the fallback.
func findBlock(x *ir.Request, key string) *ir.Block {
	var use *ir.Block
	for i := range x.Blocks {
		b := &x.Blocks[i]
		if b.Key == key {
			return b
		}
	}
	for i := range x.Blocks {
		b := &x.Blocks[i]
		if b.ToolUseID != key {
			continue
		}
		if b.Kind == ir.KindToolResult {
			return b
		}
		if b.Kind == ir.KindToolUse && use == nil {
			use = b
		}
	}
	return use
}

func toolName(x *ir.Request, id string) string {
	for _, b := range x.Blocks {
		if b.Kind == ir.KindToolUse && b.ToolUseID == id {
			return b.Name
		}
	}
	return ""
}

type rawBody struct {
	System   json.RawMessage   `json:"system"`
	Tools    []json.RawMessage `json:"tools"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// blockContent extracts a block's full content from the raw request body.
// The ir only keeps previews, so this goes back to the JSON using the
// block's location (Msg, Index).
func blockContent(body []byte, b ir.Block) (string, error) {
	var raw rawBody
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	switch b.Kind {
	case ir.KindSystem:
		var s string
		if json.Unmarshal(raw.System, &s) == nil {
			return s, nil
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw.System, &parts); err != nil || b.Index >= len(parts) {
			return "", fmt.Errorf("system block %d not found", b.Index)
		}
		return parts[b.Index].Text, nil
	case ir.KindTool:
		if b.Index >= len(raw.Tools) {
			return "", fmt.Errorf("tool %d not found", b.Index)
		}
		return indentJSON(raw.Tools[b.Index]), nil
	}
	if b.Msg < 0 || b.Msg >= len(raw.Messages) {
		return "", fmt.Errorf("message %d not found", b.Msg)
	}
	c := raw.Messages[b.Msg].Content
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(c, &parts); err != nil || b.Index >= len(parts) {
		return "", fmt.Errorf("block %d of message %d not found", b.Index, b.Msg)
	}
	return renderBlock(parts[b.Index]), nil
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	Source    struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	Title string `json:"title"`
}

// renderBlock turns one content block into the text the model gets back.
func renderBlock(p json.RawMessage) string {
	var b rawBlock
	_ = json.Unmarshal(p, &b)
	switch b.Type {
	case "text":
		return b.Text
	case "thinking":
		return b.Thinking
	case "redacted_thinking":
		return "[redacted thinking: encrypted by the provider, it cannot be restored as text]"
	case "tool_use":
		return fmt.Sprintf("tool_use %s (id %s) input:\n%s", b.Name, b.ID, indentJSON(b.Input))
	case "tool_result":
		out := resultContent(b.Content)
		if b.IsError {
			out = "(the tool reported an error)\n" + out
		}
		return out
	case "image", "document":
		return describeMedia(b)
	default:
		return indentJSON(p)
	}
}

// resultContent flattens a tool_result's content: a string, or a list whose
// text parts are joined and whose other parts are described.
func resultContent(c json.RawMessage) string {
	if len(c) == 0 || string(c) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(c, &parts) != nil {
		return string(c)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		var b rawBlock
		_ = json.Unmarshal(p, &b)
		switch b.Type {
		case "text":
			out = append(out, b.Text)
		case "image", "document":
			out = append(out, describeMedia(b))
		default:
			out = append(out, "["+b.Type+" block, not restorable as text]")
		}
	}
	return strings.Join(out, "\n")
}

func describeMedia(b rawBlock) string {
	var what []string
	if b.Source.MediaType != "" {
		what = append(what, b.Source.MediaType)
	}
	if b.Title != "" {
		what = append(what, strconv.Quote(b.Title))
	}
	switch {
	case b.Source.Data != "":
		what = append(what, fmt.Sprintf("%d bytes of base64", len(b.Source.Data)))
	case b.Source.URL != "":
		what = append(what, "url "+b.Source.URL)
	}
	s := "[" + b.Type
	if len(what) > 0 {
		s += ": " + strings.Join(what, ", ")
	}
	return s + " — not restorable as text]"
}

func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return string(raw)
	}
	return buf.String()
}

// openAIBlockContent extracts a block's full content from an OpenAI-format
// body (Chat messages or Responses input items), located by (Msg, Index).
func openAIBlockContent(protocol string, body []byte, b ir.Block) (string, error) {
	var raw struct {
		Instructions string            `json:"instructions"`
		Tools        []json.RawMessage `json:"tools"`
		Messages     []json.RawMessage `json:"messages"`
		Input        json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	switch {
	case b.Kind == ir.KindSystem && b.Msg < 0:
		return raw.Instructions, nil
	case b.Kind == ir.KindTool:
		if b.Index >= len(raw.Tools) {
			return "", fmt.Errorf("tool %d not found", b.Index)
		}
		return indentJSON(raw.Tools[b.Index]), nil
	}
	msgs := raw.Messages
	if protocol == ir.ProtocolOpenAIResponses {
		var s string
		if json.Unmarshal(raw.Input, &s) == nil {
			return s, nil
		}
		if err := json.Unmarshal(raw.Input, &msgs); err != nil {
			return "", err
		}
	}
	if b.Msg < 0 || b.Msg >= len(msgs) {
		return "", fmt.Errorf("message %d not found", b.Msg)
	}
	var m struct {
		Type      string          `json:"type"`
		Content   json.RawMessage `json:"content"`
		Output    json.RawMessage `json:"output"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(msgs[b.Msg], &m)
	switch b.Kind {
	case ir.KindToolResult:
		out := m.Content
		if protocol == ir.ProtocolOpenAIResponses {
			out = m.Output
		}
		return oaText(out), nil
	case ir.KindToolUse:
		if protocol == ir.ProtocolOpenAIResponses {
			return fmt.Sprintf("%s %s (call_id %s) arguments:\n%s", m.Type, m.Name, m.CallID, m.Arguments), nil
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == b.ToolUseID {
				return fmt.Sprintf("tool call %s (id %s) arguments:\n%s", tc.Function.Name, tc.ID, tc.Function.Arguments), nil
			}
		}
		return "", fmt.Errorf("tool call %s not found", b.ToolUseID)
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(m.Content, &parts); err != nil || b.Index >= len(parts) {
		return indentJSON(msgs[b.Msg]), nil
	}
	return oaText(parts[b.Index]), nil
}

// oaText flattens OpenAI content: a string, one part, or a list of parts.
func oaText(c json.RawMessage) string {
	if len(c) == 0 || string(c) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(c, &parts) != nil {
		parts = []json.RawMessage{c}
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		var part struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		}
		_ = json.Unmarshal(p, &part)
		switch {
		case part.Text != "":
			out = append(out, part.Text)
		case part.Refusal != "":
			out = append(out, part.Refusal)
		default:
			out = append(out, "["+part.Type+" part, not restorable as text]")
		}
	}
	return strings.Join(out, "\n")
}
