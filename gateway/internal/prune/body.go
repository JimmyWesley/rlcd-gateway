package prune

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

// doc is the request decoded generically, so a rewrite keeps every field the
// gateway does not know about. Numbers stay json.Number to round-trip exactly.
type doc struct {
	m     map[string]any
	tools []any
	sys   []any // nil when system is absent or a plain string
	msgs  []any
}

func parseDoc(body []byte) (*doc, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	d := &doc{m: m}
	d.tools, _ = m["tools"].([]any)
	d.sys, _ = m["system"].([]any)
	d.msgs, _ = m["messages"].([]any)
	return d, nil
}

func (d *doc) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d.m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// block returns the content block at (msg, index); for string content it
// returns nil and str=true.
func (d *doc) block(msg, index int) (b map[string]any, str bool) {
	if msg < 0 || msg >= len(d.msgs) {
		return nil, false
	}
	m, _ := d.msgs[msg].(map[string]any)
	switch c := m["content"].(type) {
	case string:
		return nil, true
	case []any:
		if index >= 0 && index < len(c) {
			b, _ = c[index].(map[string]any)
		}
	}
	return b, false
}

func (d *doc) messageRole(msg int) string {
	if msg < 0 || msg >= len(d.msgs) {
		return ""
	}
	m, _ := d.msgs[msg].(map[string]any)
	r, _ := m["role"].(string)
	return r
}

// usesCache reports whether the request asks for prompt caching at all.
func (d *doc) usesCache(x *ir.Request) bool {
	if d.m["cache_control"] != nil {
		return true
	}
	for _, b := range x.Blocks {
		if b.Cached {
			return true
		}
	}
	return false
}

// threadFingerprint identifies one message history within a conversation:
// the system prompt plus the first message do not change across its turns.
// Claude Code's billing line is left out: its suffix differs between the
// request types of one session.
func (d *doc) threadFingerprint() string {
	h := sha256.New()
	sys := d.m["system"]
	if s, ok := sys.(string); ok && pipeline.IsBillingLine(s) {
		sys = ""
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			sys = s[i+1:]
		}
	}
	if d.sys != nil {
		kept := make([]any, 0, len(d.sys))
		for _, b := range d.sys {
			bm, _ := b.(map[string]any)
			if t, _ := bm["text"].(string); pipeline.IsBillingLine(t) {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) != len(d.sys) {
			sys = kept
		}
	}
	sb, _ := json.Marshal(sys)
	h.Write(sb)
	if len(d.msgs) > 0 {
		m, _ := d.msgs[0].(map[string]any)
		cb, _ := json.Marshal(m["content"])
		h.Write(cb)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// item is one ir block plus what pruning needs to know about it.
type item struct {
	ir.Block
	// ID is stable across turns: tool_use_id for tool results (positional ir
	// keys shift when the agent compacts), a content hash otherwise.
	ID string
	// MarkerKey is what a marker names and what the selector question is keyed by.
	MarkerKey string
	Text      string
	// For tool results: the tool_use that produced them.
	ToolName  string
	ToolInput map[string]any
	Candidate bool   // a kind pruning may touch at all
	Protected string // why it can never be touched, if so
	// Filled by plan.
	Decision string
	Reason   string
	Score    *float64
	Marker   string
	FirstReq string
	New      bool // dropped for the first time in this request
	After    int  // estimated tokens once pruned
}

func hashID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil))[:16]
}

// buildItems pairs the ir blocks with their full text and stable ids, and
// marks what is structurally untouchable. Order is ir order (system, tools,
// messages).
func buildItems(d *doc, x *ir.Request, eff Effective) []*item {
	type use struct {
		name  string
		input map[string]any
	}
	uses := map[string]use{}
	for _, raw := range d.msgs {
		m, _ := raw.(map[string]any)
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			b, _ := p.(map[string]any)
			if t, _ := b["type"].(string); t == "tool_use" || t == "server_tool_use" {
				id, _ := b["id"].(string)
				name, _ := b["name"].(string)
				in, _ := b["input"].(map[string]any)
				uses[id] = use{name, in}
			}
		}
	}
	// Tools the history has called must stay: dropping one would leave
	// tool_use blocks naming a tool the request no longer defines.
	usedTools := map[string]bool{}
	for _, u := range uses {
		usedTools[u.name] = true
	}
	if tc, _ := d.m["tool_choice"].(map[string]any); tc != nil {
		if n, _ := tc["name"].(string); n != "" {
			usedTools[n] = true
		}
	}

	protectFrom := protectedFrom(d, eff.KeepLastNTurns)
	// Identical texts ("continue", a repeated reminder) hash alike; the
	// occurrence number keeps their ids apart and stable as history grows.
	seen := map[string]int{}
	dedupe := func(id string) string {
		seen[id]++
		if n := seen[id]; n > 1 {
			return fmt.Sprintf("%s#%d", id, n)
		}
		return id
	}
	items := make([]*item, 0, len(x.Blocks))
	for _, b := range x.Blocks {
		it := &item{Block: b, MarkerKey: b.Key, ID: "ir:" + b.Key}
		switch b.Kind {
		case ir.KindSystem:
			if len(d.sys) > b.Index {
				sb, _ := d.sys[b.Index].(map[string]any)
				it.Text, _ = sb["text"].(string)
			}
			it.ID = dedupe(hashID("sys:", it.Text))
			it.Candidate = eff.PruneSystem
			switch {
			case d.sys == nil || b.Index == 0:
				it.Protected = "first system block"
			case b.Cached:
				it.Protected = "cache breakpoint"
			}
		case ir.KindTool:
			tb, _ := d.tools[b.Index].(map[string]any)
			it.Text, _ = tb["description"].(string)
			it.ID = "tool:" + b.Name
			it.Candidate = eff.PruneTools
			switch {
			case usedTools[b.Name]:
				it.Protected = "called in history"
			case b.Cached:
				it.Protected = "cache breakpoint"
			case isRecallTool(b.Name):
				it.Protected = "recall tool"
			case tb["type"] != nil && tb["type"] != "custom":
				it.Protected = "server tool"
			}
		case ir.KindToolResult:
			blk, _ := d.block(b.Msg, b.Index)
			it.Text = resultText(blk["content"])
			it.ID = "tr:" + b.ToolUseID
			it.MarkerKey = b.ToolUseID
			u := uses[b.ToolUseID]
			it.ToolName, it.ToolInput = u.name, u.input
			it.Candidate = b.ToolUseID != ""
			if isRecallTool(u.name) {
				it.Protected = "recall result"
			}
		case ir.KindText:
			blk, str := d.block(b.Msg, b.Index)
			if str {
				m, _ := d.msgs[b.Msg].(map[string]any)
				it.Text, _ = m["content"].(string)
			} else {
				it.Text, _ = blk["text"].(string)
			}
			it.ID = dedupe(hashID("txt:", b.Role, it.Text))
			it.Candidate = true
		default:
			// thinking (signed), tool_use (structure), images, unknown types.
			it.Protected = b.Kind
		}
		if b.Msg >= 0 && b.Msg >= protectFrom {
			it.Protected = "recent turn"
		}
		if _, _, ok := pipeline.ParseMarker(it.Text); ok && it.Protected == "" {
			// Already a marker (or text quoting one): nothing to gain.
			it.Protected = "marker"
		}
		items = append(items, it)
	}
	return items
}

// protectedFrom is the first message index that must never be touched: the
// latest user message always, plus the last n turns, where a turn starts at
// a user message (in an agent loop every tool result message is a turn).
func protectedFrom(d *doc, n int) int {
	if n < 1 {
		n = 1
	}
	from := len(d.msgs) - 1
	seen := 0
	for i := len(d.msgs) - 1; i >= 0; i-- {
		if d.messageRole(i) == "user" {
			seen++
			from = i
			if seen == n {
				break
			}
		}
	}
	if last := len(d.msgs) - 1; from > last {
		from = last
	}
	return from
}

func isRecallTool(name string) bool { return strings.Contains(name, "rlcd_recall") }

func resultText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, p := range v {
			b, _ := p.(map[string]any)
			if t, _ := b["type"].(string); t == "text" {
				s, _ := b["text"].(string)
				sb.WriteString(s)
			} else {
				sb.WriteString("[" + t + "]")
			}
		}
		return sb.String()
	}
	return ""
}

// apply rewrites d in place: dropped tool results and texts get their
// content replaced by the marker (every other field, including tool_use_id,
// is_error and cache_control, stays), dropped tools are removed.
func apply(d *doc, items []*item) error {
	dropTool := map[int]bool{}
	for _, it := range items {
		if it.Decision != "drop" {
			continue
		}
		switch it.Kind {
		case ir.KindToolResult:
			b, _ := d.block(it.Msg, it.Index)
			if b == nil {
				return fmt.Errorf("tool_result %s not found at %s", it.ToolUseID, it.Key)
			}
			b["content"] = []any{map[string]any{"type": "text", "text": it.Marker}}
		case ir.KindText:
			b, str := d.block(it.Msg, it.Index)
			if str {
				m, _ := d.msgs[it.Msg].(map[string]any)
				m["content"] = it.Marker
				continue
			}
			if b == nil {
				return fmt.Errorf("text block %s not found", it.Key)
			}
			b["text"] = it.Marker
			delete(b, "citations") // they point into the text that is gone
		case ir.KindSystem:
			if it.Index >= len(d.sys) {
				return fmt.Errorf("system block %s not found", it.Key)
			}
			sb, _ := d.sys[it.Index].(map[string]any)
			sb["text"] = it.Marker
		case ir.KindTool:
			dropTool[it.Index] = true
		}
	}
	if len(dropTool) > 0 {
		kept := make([]any, 0, len(d.tools))
		for i, t := range d.tools {
			if !dropTool[i] {
				kept = append(kept, t)
			}
		}
		d.tools = kept
		d.m["tools"] = kept
	}
	return nil
}

// verify checks the invariants the provider enforces with a 400, comparing
// the rewritten body to the original: same messages and roles, same block
// types in the same order, every tool_result still paired by tool_use_id
// with its is_error, thinking blocks and the last message byte-identical,
// no empty content, and only unused tools removed.
func verify(orig, out []byte) error {
	a, err := parseDoc(orig)
	if err != nil {
		return err
	}
	b, err := parseDoc(out)
	if err != nil {
		return fmt.Errorf("rewritten body is not JSON: %w", err)
	}
	if len(a.msgs) != len(b.msgs) {
		return errors.New("message count changed")
	}
	enc := func(v any) string { j, _ := json.Marshal(v); return string(j) }
	for i := range a.msgs {
		am, _ := a.msgs[i].(map[string]any)
		bm, _ := b.msgs[i].(map[string]any)
		if am["role"] != bm["role"] {
			return fmt.Errorf("message %d: role changed", i)
		}
		if i == len(a.msgs)-1 && enc(am) != enc(bm) {
			return errors.New("last message changed")
		}
		ac, aList := am["content"].([]any)
		bc, bList := bm["content"].([]any)
		if aList != bList {
			return fmt.Errorf("message %d: content shape changed", i)
		}
		if !aList {
			if s, _ := bm["content"].(string); s == "" {
				return fmt.Errorf("message %d: empty content", i)
			}
			continue
		}
		if len(ac) != len(bc) || len(bc) == 0 {
			return fmt.Errorf("message %d: block count changed or empty", i)
		}
		for j := range ac {
			x, _ := ac[j].(map[string]any)
			y, _ := bc[j].(map[string]any)
			if x["type"] != y["type"] {
				return fmt.Errorf("m%d.b%d: block type changed", i, j)
			}
			switch x["type"] {
			case "thinking", "redacted_thinking", "tool_use", "server_tool_use", "image", "document":
				if enc(x) != enc(y) {
					return fmt.Errorf("m%d.b%d: %v block changed", i, j, x["type"])
				}
			case "tool_result":
				if x["tool_use_id"] != y["tool_use_id"] || x["is_error"] != y["is_error"] {
					return fmt.Errorf("m%d.b%d: tool_result pairing changed", i, j)
				}
				if resultText(y["content"]) == "" && resultText(x["content"]) != "" {
					return fmt.Errorf("m%d.b%d: tool_result emptied", i, j)
				}
			case "text":
				if s, _ := y["text"].(string); s == "" && x["text"] != "" {
					return fmt.Errorf("m%d.b%d: text emptied", i, j)
				}
			}
		}
	}
	if len(a.sys) != len(b.sys) {
		return errors.New("system block count changed")
	}
	names := map[string]bool{}
	for _, t := range a.tools {
		tm, _ := t.(map[string]any)
		n, _ := tm["name"].(string)
		names[n] = true
	}
	for _, t := range b.tools {
		tm, _ := t.(map[string]any)
		n, _ := tm["name"].(string)
		if !names[n] {
			return fmt.Errorf("tool %q appeared", n)
		}
	}
	return nil
}

// headTail keeps the start and the end of s, where tool output usually says
// what happened (the command, then the result or the error).
func headTail(s string, head, tail int) string {
	r := []rune(s)
	if len(r) <= head+tail+20 {
		return s
	}
	return string(r[:head]) + fmt.Sprintf("\n… [%d chars omitted] …\n", len(r)-head-tail) + string(r[len(r)-tail:])
}
