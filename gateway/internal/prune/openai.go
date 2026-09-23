package prune

// Pruning for the OpenAI formats (Chat Completions and Responses).
//
// The call/result pairs map one to one onto Anthropic's:
//
//	Anthropic                 Chat Completions                   Responses
//	tool_use (id)             assistant tool_calls[] (id)        function_call item (call_id)
//	tool_result (tool_use_id) role "tool" message (tool_call_id) function_call_output item (call_id)
//
// The invariants are the same as on the Anthropic path: every call keeps
// its output (only the output's content becomes the marker; ids and every
// other field stay), calls, reasoning items and media are never touched,
// the latest turns are protected, decisions are sticky with byte-identical
// markers, and the rewritten body is verified before it is forwarded.
//
// Tool definitions are never pruned in these formats, and conversation text
// (user and assistant messages) is only a candidate with
// prune_conversation_text, off by default: a plain chatbot has no tool
// output, and dropping its old turns changes what the model remembers of
// the conversation, which an app may not expect.

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

// oaDoc is an OpenAI-format request decoded generically, like doc.
type oaDoc struct {
	protocol string
	m        map[string]any
	// msgs is Chat's messages or Responses' input items (nil when the
	// Responses input is a plain string: nothing to prune then).
	msgs []any
}

func parseOA(protocol string, body []byte) (*oaDoc, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	d := &oaDoc{protocol: protocol, m: m}
	if d.chat() {
		d.msgs, _ = m["messages"].([]any)
	} else {
		d.msgs, _ = m["input"].([]any)
	}
	return d, nil
}

func (d *oaDoc) chat() bool   { return d.protocol == ir.ProtocolOpenAIChat }
func (d *oaDoc) numMsgs() int { return len(d.msgs) }

func (d *oaDoc) listField() string {
	if d.chat() {
		return "messages"
	}
	return "input"
}

// outputField holds a tool output: a tool message's content, or an output
// item's output.
func (d *oaDoc) outputField() string {
	if d.chat() {
		return "content"
	}
	return "output"
}

func (d *oaDoc) msg(i int) map[string]any {
	if i < 0 || i >= len(d.msgs) {
		return nil
	}
	m, _ := d.msgs[i].(map[string]any)
	return m
}

func isOutputItem(t string) bool {
	return t == "function_call_output" || t == "custom_tool_call_output" || t == "local_shell_call_output"
}

// role is who speaks in message i; tool outputs are "tool" in both formats.
func (d *oaDoc) role(i int) string {
	m := d.msg(i)
	if d.chat() {
		r, _ := m["role"].(string)
		return r
	}
	t, _ := m["type"].(string)
	switch {
	case t == "message" || t == "":
		r, _ := m["role"].(string)
		return r
	case isOutputItem(t):
		return "tool"
	}
	return "assistant" // calls, reasoning, and other model-produced items
}

// protectedFrom is the first message index that must never be touched: the
// last n turns, where a turn starts at a user message or at the first of a
// run of tool outputs (one agent step), as on the Anthropic path.
func (d *oaDoc) protectedFrom(n int) int {
	if n < 1 {
		n = 1
	}
	from := len(d.msgs) - 1
	seen := 0
	for i := len(d.msgs) - 1; i >= 0; i-- {
		r := d.role(i)
		if r == "user" || (r == "tool" && (i == 0 || d.role(i-1) != "tool")) {
			seen++
			from = i
			if seen == n {
				break
			}
		}
	}
	return from
}

type oaCall struct {
	name  string
	input map[string]any
}

// calls maps call ids to the tool and its parsed arguments.
func (d *oaDoc) calls() (map[string]oaCall, []oaCall) {
	byID := map[string]oaCall{}
	var order []oaCall
	args := func(s string) map[string]any {
		var in map[string]any
		_ = json.Unmarshal([]byte(s), &in)
		return in
	}
	for i := range d.msgs {
		m := d.msg(i)
		if d.chat() {
			tcs, _ := m["tool_calls"].([]any)
			for _, raw := range tcs {
				tc, _ := raw.(map[string]any)
				fn, _ := tc["function"].(map[string]any)
				id, _ := tc["id"].(string)
				name, _ := fn["name"].(string)
				a, _ := fn["arguments"].(string)
				c := oaCall{name, args(a)}
				byID[id] = c
				order = append(order, c)
			}
			continue
		}
		t, _ := m["type"].(string)
		id, _ := m["call_id"].(string)
		var c oaCall
		switch t {
		case "function_call":
			name, _ := m["name"].(string)
			a, _ := m["arguments"].(string)
			c = oaCall{name, args(a)}
		case "custom_tool_call":
			name, _ := m["name"].(string)
			in, _ := m["input"].(string)
			c = oaCall{name, map[string]any{"input": in}}
		case "local_shell_call":
			act, _ := m["action"].(map[string]any)
			c = oaCall{"local_shell", act}
		default:
			continue
		}
		byID[id] = c
		order = append(order, c)
	}
	return byID, order
}

// flatten turns content (a string or a list of parts) into text.
func flatten(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, p := range v {
			b, _ := p.(map[string]any)
			if s, ok := b["text"].(string); ok {
				sb.WriteString(s)
			} else if s, ok := b["refusal"].(string); ok {
				sb.WriteString(s)
			} else {
				t, _ := b["type"].(string)
				sb.WriteString("[" + t + "]")
			}
		}
		return sb.String()
	}
	return ""
}

// partText is the text of a message's content block (Index), or of the
// whole content when it is a string.
func (d *oaDoc) partText(b ir.Block) string {
	m := d.msg(b.Msg)
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		if b.Index < len(c) {
			p, _ := c[b.Index].(map[string]any)
			if s, ok := p["text"].(string); ok {
				return s
			}
			s, _ := p["refusal"].(string)
			return s
		}
	}
	return ""
}

func (d *oaDoc) items(x *ir.Request, eff Effective) []*item {
	calls, _ := d.calls()
	protectFrom := d.protectedFrom(eff.KeepLastNTurns)
	// Some servers number call ids per response ("call_0"), so one id can
	// appear twice in a conversation. Such results are keyed by content too,
	// and their markers name the block key instead of the ambiguous id.
	idCount := map[string]int{}
	for _, b := range x.Blocks {
		if b.Kind == ir.KindToolResult {
			idCount[b.ToolUseID]++
		}
	}
	seen := map[string]int{}
	dedupe := func(id string) string {
		seen[id]++
		if n := seen[id]; n > 1 {
			return fmt.Sprintf("%s#%d", id, n)
		}
		return id
	}
	firstSystem := true
	items := make([]*item, 0, len(x.Blocks))
	for _, b := range x.Blocks {
		it := &item{Block: b, MarkerKey: b.Key, ID: "ir:" + b.Key}
		switch b.Kind {
		case ir.KindSystem:
			if b.Msg < 0 {
				it.Text, _ = d.m["instructions"].(string)
				it.ID, it.Protected = "sys:instructions", "instructions"
			} else {
				it.Text = d.partText(b)
				it.ID = dedupe(hashID("sys:", b.Role, it.Text))
				it.Candidate = eff.PruneSystem
				if firstSystem {
					it.Protected = "first system message"
				}
			}
			firstSystem = false
		case ir.KindTool:
			it.ID = "tool:" + b.Name
			it.Protected = "tool definition"
		case ir.KindToolResult:
			it.Text = flatten(d.msg(b.Msg)[d.outputField()])
			it.ID = "tr:" + b.ToolUseID
			it.MarkerKey = b.ToolUseID
			if idCount[b.ToolUseID] > 1 || b.ToolUseID == "" {
				it.ID = dedupe(hashID("tr:"+b.ToolUseID+":", it.Text))
				it.MarkerKey = b.Key
			}
			c := calls[b.ToolUseID]
			it.ToolName, it.ToolInput = c.name, c.input
			it.Candidate = true
			if isRecallTool(c.name) {
				it.Protected = "recall result"
			}
		case ir.KindText:
			it.Text = d.partText(b)
			it.ID = dedupe(hashID("txt:", b.Role, it.Text))
			it.Candidate = eff.PruneConversationText
			if !it.Candidate {
				it.Protected = "conversation text (prune_conversation_text is off)"
			}
		default:
			// reasoning, calls (structure), images and files, unknown items.
			it.Protected = b.Kind
		}
		if b.Msg >= 0 && b.Msg >= protectFrom {
			it.Protected = "recent turn"
		}
		if _, _, ok := pipeline.ParseMarker(it.Text); ok && it.Protected == "" {
			it.Protected = "marker"
		}
		items = append(items, it)
	}
	return items
}

func (d *oaDoc) goalAndRecent() (goal, recent string) {
	var goals []string
	for i := len(d.msgs) - 1; i >= 0 && len(goals) < 2; i-- {
		if d.role(i) != "user" {
			continue
		}
		t := strings.TrimSpace(flatten(d.msg(i)["content"]))
		if t == "" {
			continue
		}
		goals = append(goals, headTail(t, 1200, 300))
	}
	for i, j := 0, len(goals)-1; i < j; i, j = i+1, j-1 {
		goals[i], goals[j] = goals[j], goals[i]
	}
	_, order := d.calls()
	if len(order) > 8 {
		order = order[len(order)-8:]
	}
	parts := make([]string, 0, len(order))
	for _, c := range order {
		parts = append(parts, callSummary(c.name, c.input))
	}
	return strings.Join(goals, "\n---\n"), strings.Join(parts, "; ")
}

// fingerprint identifies one message history: the leading system messages
// (or instructions) and the first user message do not change across turns.
func (d *oaDoc) fingerprint() string {
	h := sha256.New()
	if !d.chat() {
		ib, _ := json.Marshal(d.m["instructions"])
		h.Write(ib)
	}
	for i := range d.msgs {
		r := d.role(i)
		if r != "user" && r != "system" && r != "developer" {
			continue
		}
		mb, _ := json.Marshal(d.msgs[i])
		h.Write(mb)
		if r == "user" {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func (d *oaDoc) cached(x *ir.Request) bool { return oaCached(x) }

func (d *oaDoc) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d.m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// apply replaces the content of every dropped block with its marker. A
// string stays a string; a list of parts becomes one text part.
func (d *oaDoc) apply(items []*item) error {
	textPart := "text"
	if !d.chat() {
		textPart = "input_text"
	}
	for _, it := range items {
		if it.Decision != "drop" {
			continue
		}
		m := d.msg(it.Msg)
		if m == nil {
			return fmt.Errorf("%s: message %d not found", it.Key, it.Msg)
		}
		switch it.Kind {
		case ir.KindToolResult:
			f := d.outputField()
			if _, list := m[f].([]any); list {
				m[f] = []any{map[string]any{"type": textPart, "text": it.Marker}}
			} else {
				m[f] = it.Marker
			}
		case ir.KindText, ir.KindSystem:
			switch c := m["content"].(type) {
			case string:
				m["content"] = it.Marker
			case []any:
				if it.Index >= len(c) {
					return fmt.Errorf("%s: part not found", it.Key)
				}
				p, _ := c[it.Index].(map[string]any)
				if _, ok := p["refusal"]; ok {
					p["refusal"] = it.Marker
				} else {
					p["text"] = it.Marker
				}
				if _, ok := p["annotations"]; ok {
					p["annotations"] = []any{} // they point into the text that is gone
				}
			default:
				return fmt.Errorf("%s: no content to replace", it.Key)
			}
		default:
			return fmt.Errorf("%s: %s blocks are never pruned", it.Key, it.Kind)
		}
	}
	return nil
}

func encJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// without returns m's JSON with fields left out.
func without(m map[string]any, fields ...string) string {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	for _, f := range fields {
		delete(c, f)
	}
	return encJSON(c)
}

// verify checks what the providers enforce, and more: every field outside
// the message list is untouched; same messages or items, roles and types in
// the same order; calls, reasoning and media byte-identical; every output
// still carries its call id; the last message byte-identical; nothing
// emptied.
func (d *oaDoc) verify(orig, out []byte) error {
	a, err := parseOA(d.protocol, orig)
	if err != nil {
		return err
	}
	b, err := parseOA(d.protocol, out)
	if err != nil {
		return fmt.Errorf("rewritten body is not JSON: %w", err)
	}
	list := a.listField()
	if without(a.m, list) != without(b.m, list) {
		return errors.New("a field outside " + list + " changed")
	}
	if len(a.msgs) != len(b.msgs) {
		return errors.New("message count changed")
	}
	for i := range a.msgs {
		am, bm := a.msg(i), b.msg(i)
		if am == nil || bm == nil {
			if encJSON(a.msgs[i]) != encJSON(b.msgs[i]) {
				return fmt.Errorf("message %d changed", i)
			}
			continue
		}
		if i == len(a.msgs)-1 && encJSON(am) != encJSON(bm) {
			return errors.New("last message changed")
		}
		if a.role(i) != b.role(i) || am["type"] != bm["type"] {
			return fmt.Errorf("message %d: role or type changed", i)
		}
		if a.role(i) == "tool" {
			f := a.outputField()
			if without(am, f) != without(bm, f) {
				return fmt.Errorf("message %d: tool output lost its call id or another field", i)
			}
			if flatten(bm[f]) == "" && flatten(am[f]) != "" {
				return fmt.Errorf("message %d: tool output emptied", i)
			}
			continue
		}
		if t, _ := am["type"].(string); !a.chat() && t != "message" && t != "" {
			if encJSON(am) != encJSON(bm) {
				return fmt.Errorf("item %d: %s changed", i, t)
			}
			continue
		}
		if without(am, "content") != without(bm, "content") {
			return fmt.Errorf("message %d: a field other than content changed (tool_calls, name, ...)", i)
		}
		if err := sameParts(i, am["content"], bm["content"]); err != nil {
			return err
		}
	}
	return nil
}

// sameParts checks that content kept its shape: same part count and types,
// non-text parts identical, no text emptied.
func sameParts(i int, x, y any) error {
	switch xv := x.(type) {
	case string:
		ys, ok := y.(string)
		if !ok {
			return fmt.Errorf("message %d: content shape changed", i)
		}
		if ys == "" && xv != "" {
			return fmt.Errorf("message %d: content emptied", i)
		}
		return nil
	case []any:
		yv, ok := y.([]any)
		if !ok || len(xv) != len(yv) {
			return fmt.Errorf("message %d: content parts changed", i)
		}
		for j := range xv {
			xp, _ := xv[j].(map[string]any)
			yp, _ := yv[j].(map[string]any)
			if xp["type"] != yp["type"] {
				return fmt.Errorf("m%d.b%d: part type changed", i, j)
			}
			_, xt := xp["text"].(string)
			_, xr := xp["refusal"].(string)
			if !xt && !xr {
				if encJSON(xp) != encJSON(yp) {
					return fmt.Errorf("m%d.b%d: %v part changed", i, j, xp["type"])
				}
				continue
			}
			if without(xp, "text", "refusal", "annotations") != without(yp, "text", "refusal", "annotations") {
				return fmt.Errorf("m%d.b%d: part fields changed", i, j)
			}
			if flatten([]any{yp}) == "" && flatten([]any{xp}) != "" {
				return fmt.Errorf("m%d.b%d: text emptied", i, j)
			}
		}
		return nil
	}
	if encJSON(x) != encJSON(y) {
		return fmt.Errorf("message %d: content changed", i)
	}
	return nil
}
