package ir

// X-ray of OpenAI-format requests, in the same Block shape and key scheme as
// the Anthropic parser, so the Context X-ray, routing and pruning work the
// same way for every protocol:
//
//   - "sys.0" is the Responses API's top-level instructions;
//   - "tool.<i>" is each tool definition;
//   - "m<i>.b<j>" is part j of input item (or chat message) i.
//
// In the Responses API every input item is its own "message": a message
// item's content parts are its blocks, and a function_call or
// function_call_output item is a single block (b0) whose tool_use_id is the
// call_id, so a call and its output can be found together.
//
// References:
//   https://platform.openai.com/docs/api-reference/responses/create
//   https://platform.openai.com/docs/api-reference/chat/create

import (
	"encoding/json"
	"fmt"
)

// imageChars is the flat size counted for an image, as in internal/
const imageChars = 6000

type builder struct{ req *Request }

func newBuilder(model string, stream bool, maxTokens, messages int) *builder {
	return &builder{req: &Request{Model: model, Stream: stream, MaxTokens: maxTokens,
		Messages: messages, ByKind: map[string]int{}}}
}

func (b *builder) add(blk Block) {
	blk.Tokens = EstimateTokens(blk.Chars)
	b.req.Blocks = append(b.req.Blocks, blk)
	b.req.Tokens += blk.Tokens
	b.req.ByKind[blk.Kind] += blk.Tokens
}

func (b *builder) tools(tools []json.RawMessage) {
	for i, t := range tools {
		var tool struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Function    struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		}
		_ = json.Unmarshal(t, &tool)
		name, desc := tool.Name, tool.Description
		if tool.Function.Name != "" { // Chat Completions nests the function
			name, desc = tool.Function.Name, tool.Function.Description
		}
		if name == "" { // built-in tools (web_search, local_shell, ...) only have a type
			name = tool.Type
		}
		b.add(Block{Key: fmt.Sprintf("tool.%d", i), Kind: KindTool, Msg: -1, Index: i,
			Name: name, Chars: len(t), Preview: preview(desc)})
	}
}

// ParseResponses builds the X-ray of an OpenAI Responses API request.
func ParseResponses(body []byte) (*Request, error) {
	var raw struct {
		Model           string            `json:"model"`
		Stream          bool              `json:"stream"`
		MaxOutputTokens int               `json:"max_output_tokens"`
		Instructions    string            `json:"instructions"`
		Tools           []json.RawMessage `json:"tools"`
		Input           json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	var items []json.RawMessage
	var text string
	inputIsText := json.Unmarshal(raw.Input, &text) == nil
	if !inputIsText && len(raw.Input) > 0 && string(raw.Input) != "null" {
		if err := json.Unmarshal(raw.Input, &items); err != nil {
			return nil, fmt.Errorf("parse responses input: %w", err)
		}
	}
	n := len(items)
	if inputIsText {
		n = 1
	}
	b := newBuilder(raw.Model, raw.Stream, raw.MaxOutputTokens, n)
	if raw.Instructions != "" {
		b.add(Block{Key: "sys.0", Kind: KindSystem, Msg: -1, Index: 0,
			Chars: len(raw.Instructions), Preview: preview(raw.Instructions)})
	}
	b.tools(raw.Tools)
	if inputIsText {
		b.add(Block{Key: "m0.b0", Kind: KindText, Role: "user", Msg: 0, Index: 0,
			Chars: len(text), Preview: preview(text)})
	}
	for i, it := range items {
		b.responseItem(i, it)
	}
	return b.req, nil
}

func (b *builder) responseItem(mi int, raw json.RawMessage) {
	var it struct {
		Type             string          `json:"type"`
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		Name             string          `json:"name"`
		CallID           string          `json:"call_id"`
		Arguments        string          `json:"arguments"`
		Input            string          `json:"input"`
		Action           json.RawMessage `json:"action"`
		Output           json.RawMessage `json:"output"`
		Status           string          `json:"status"`
		EncryptedContent string          `json:"encrypted_content"`
		Summary          []struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	_ = json.Unmarshal(raw, &it)
	one := func(blk Block) {
		blk.Key, blk.Msg, blk.Index = fmt.Sprintf("m%d.b0", mi), mi, 0
		b.add(blk)
	}
	switch it.Type {
	case "message", "":
		if it.Role == "" {
			one(Block{Kind: KindOther, Chars: len(raw), Preview: "[" + it.Type + "]"})
			return
		}
		b.content(mi, it.Role, it.Content)
	case "function_call", "custom_tool_call", "local_shell_call":
		args := it.Arguments
		if it.Type == "custom_tool_call" {
			args = it.Input
		} else if it.Type == "local_shell_call" {
			args, it.Name = string(it.Action), "local_shell"
		}
		one(Block{Kind: KindToolUse, Role: "assistant", Name: it.Name, ToolUseID: it.CallID,
			Chars: len(args), Preview: preview(args)})
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		out := outputText(it.Output)
		one(Block{Kind: KindToolResult, Role: "user", ToolUseID: it.CallID,
			IsError: it.Status == "failed" || it.Status == "incomplete", Chars: len(out), Preview: preview(out)})
	case "reasoning":
		text := ""
		for _, s := range it.Summary {
			text += s.Text
		}
		p := preview(text)
		if it.EncryptedContent != "" && text == "" {
			p = "[encrypted reasoning]"
		}
		// Encrypted reasoning is opaque; only readable text is counted.
		one(Block{Kind: KindThinking, Role: "assistant", Chars: len(text), Preview: p})
	default:
		// web_search_call, item_reference, compaction, ...
		one(Block{Kind: KindOther, Role: it.Role, Chars: len(raw), Preview: "[" + it.Type + "]"})
	}
}

// content handles a message's content: a string or a list of parts.
func (b *builder) content(mi int, role string, c json.RawMessage) {
	kindText := KindText
	if role == "system" || role == "developer" {
		kindText = KindSystem
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		b.add(Block{Key: fmt.Sprintf("m%d.b0", mi), Kind: kindText, Role: role, Msg: mi, Index: 0,
			Chars: len(s), Preview: preview(s)})
		return
	}
	var parts []json.RawMessage
	_ = json.Unmarshal(c, &parts)
	for bi, p := range parts {
		blk := contentPart(p, kindText)
		blk.Key, blk.Role, blk.Msg, blk.Index = fmt.Sprintf("m%d.b%d", mi, bi), role, mi, bi
		b.add(blk)
	}
}

func contentPart(p json.RawMessage, kindText string) Block {
	var part struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	}
	_ = json.Unmarshal(p, &part)
	switch part.Type {
	case "input_text", "output_text", "text", "summary_text":
		return Block{Kind: kindText, Chars: len(part.Text), Preview: preview(part.Text)}
	case "refusal":
		return Block{Kind: KindText, Chars: len(part.Refusal), Preview: preview(part.Refusal)}
	case "input_image", "image_url", "input_file", "file", "input_audio":
		// Base64 payloads are billed by pixels/pages, not characters.
		return Block{Kind: KindImage, Chars: imageChars, Preview: "[" + part.Type + "]"}
	}
	return Block{Kind: KindOther, Chars: len(p), Preview: "[" + part.Type + "]"}
}

// outputText flattens a tool output: a string or a list of content parts.
func outputText(c json.RawMessage) string {
	if len(c) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(c, &parts) != nil {
		return string(c)
	}
	out := ""
	for _, p := range parts {
		if p.Text != "" {
			out += p.Text
		} else {
			out += "[" + p.Type + "]"
		}
	}
	return out
}

// ParseChat builds the X-ray of an OpenAI Chat Completions request. System
// and developer messages stay in place as messages, with kind "system".
// An assistant message's tool_calls follow its content parts; a "tool"
// message is a tool_result whose tool_use_id is its tool_call_id.
func ParseChat(body []byte) (*Request, error) {
	var raw struct {
		Model               string            `json:"model"`
		Stream              bool              `json:"stream"`
		MaxTokens           int               `json:"max_tokens"`
		MaxCompletionTokens int               `json:"max_completion_tokens"`
		Tools               []json.RawMessage `json:"tools"`
		Messages            []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse chat request: %w", err)
	}
	maxTokens := raw.MaxCompletionTokens
	if maxTokens == 0 {
		maxTokens = raw.MaxTokens
	}
	b := newBuilder(raw.Model, raw.Stream, maxTokens, len(raw.Messages))
	b.tools(raw.Tools)
	for mi, m := range raw.Messages {
		if m.Role == "tool" {
			out := outputText(m.Content)
			b.add(Block{Key: fmt.Sprintf("m%d.b0", mi), Kind: KindToolResult, Role: m.Role, Msg: mi,
				ToolUseID: m.ToolCallID, Chars: len(out), Preview: preview(out)})
			continue
		}
		start := len(b.req.Blocks)
		if len(m.Content) > 0 && string(m.Content) != "null" {
			b.content(mi, m.Role, m.Content)
		}
		next := len(b.req.Blocks) - start
		for _, tc := range m.ToolCalls {
			b.add(Block{Key: fmt.Sprintf("m%d.b%d", mi, next), Kind: KindToolUse, Role: m.Role, Msg: mi,
				Index: next, Name: tc.Function.Name, ToolUseID: tc.ID,
				Chars: len(tc.Function.Arguments), Preview: preview(tc.Function.Arguments)})
			next++
		}
	}
	return b.req, nil
}
