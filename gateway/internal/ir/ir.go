// Package ir turns a request (Anthropic Messages, OpenAI Chat Completions
// or OpenAI Responses) into a flat list of keyed blocks. Every piece of context the model would see — system prompt, each
// tool definition, each content block of each message — gets a stable key.
//
// This is the foundation of pruning: the selector only ever returns keys,
// never text, and the request is rebuilt from the keys it kept. In F0 the
// same list feeds the dashboard's X-ray of each request.
package ir

import (
	"encoding/json"
	"fmt"
)

// Protocols (dialects) the gateway understands. A request is parsed, routed
// and pruned in its own protocol; the gateway never translates between them.
const (
	ProtocolAnthropic       = "anthropic-messages"
	ProtocolOpenAIChat      = "openai-chat"
	ProtocolOpenAIResponses = "openai-responses"
)

// IsOpenAI reports whether a protocol is one of the OpenAI formats.
func IsOpenAI(protocol string) bool {
	return protocol == ProtocolOpenAIChat || protocol == ProtocolOpenAIResponses
}

// ParseFor parses body in the given protocol ("" means Anthropic).
func ParseFor(protocol string, body []byte) (*Request, error) {
	switch protocol {
	case ProtocolOpenAIChat:
		return ParseChat(body)
	case ProtocolOpenAIResponses:
		return ParseResponses(body)
	case ProtocolSystemOne:
		return ParseSystemOne(body)
	}
	return Parse(body)
}

// Block kinds.
const (
	KindSystem     = "system"
	KindTool       = "tool"
	KindText       = "text"
	KindToolUse    = "tool_use"
	KindToolResult = "tool_result"
	KindThinking   = "thinking"
	KindImage      = "image"
	KindOther      = "other"
)

type Block struct {
	Key  string `json:"key"`  // "sys.0", "tool.3", "m12.b1"
	Kind string `json:"kind"` // one of the Kind* constants
	Role string `json:"role,omitempty"`
	// Msg and Index locate the block in the original request (-1 for system/tools).
	Msg   int    `json:"msg"`
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"` // tool name, for tool / tool_use; question type
	// Labels are a System One question's criteria labels.
	Labels []string `json:"labels,omitempty"`
	// ToolUseID links a tool_result to its tool_use; they must be pruned together.
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Chars     int    `json:"chars"`
	Tokens    int    `json:"tokens"` // estimate, see EstimateTokens
	Preview   string `json:"preview"`
	// Cached marks a block carrying cache_control: the end of a cached prefix.
	Cached bool `json:"cached,omitempty"`
}

type Request struct {
	Model     string  `json:"model"`
	Stream    bool    `json:"stream"`
	MaxTokens int     `json:"max_tokens"`
	Messages  int     `json:"messages"`
	Blocks    []Block `json:"blocks"`
	Tokens    int     `json:"tokens"`
	// ByKind sums estimated tokens per block kind, for the stacked bar.
	ByKind map[string]int `json:"by_kind"`
}

// EstimateTokens is a rough chars/4 estimate. It is labelled as an estimate
// in the UI; exact counts come from the provider's usage on the response.
func EstimateTokens(chars int) int {
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4
}

type rawRequest struct {
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Tools     []json.RawMessage `json:"tools"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// Parse builds the block list. It never fails on unknown block types: they
// become KindOther so the X-ray still accounts for their size.
func Parse(body []byte) (*Request, error) {
	var raw rawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	req := &Request{
		Model: raw.Model, Stream: raw.Stream, MaxTokens: raw.MaxTokens,
		Messages: len(raw.Messages), ByKind: map[string]int{},
	}
	add := func(b Block) {
		b.Tokens = EstimateTokens(b.Chars)
		req.Blocks = append(req.Blocks, b)
		req.Tokens += b.Tokens
		req.ByKind[b.Kind] += b.Tokens
	}

	// system: a string or a list of text blocks.
	if len(raw.System) > 0 && string(raw.System) != "null" {
		var s string
		if json.Unmarshal(raw.System, &s) == nil {
			add(Block{Key: "sys.0", Kind: KindSystem, Msg: -1, Index: 0, Chars: len(s), Preview: preview(s)})
		} else {
			var parts []map[string]any
			if json.Unmarshal(raw.System, &parts) == nil {
				for i, p := range parts {
					t, _ := p["text"].(string)
					add(Block{Key: fmt.Sprintf("sys.%d", i), Kind: KindSystem, Msg: -1, Index: i,
						Chars: len(t), Preview: preview(t), Cached: p["cache_control"] != nil})
				}
			}
		}
	}

	for i, t := range raw.Tools {
		var tool struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			CacheControl any    `json:"cache_control"`
		}
		_ = json.Unmarshal(t, &tool)
		add(Block{Key: fmt.Sprintf("tool.%d", i), Kind: KindTool, Msg: -1, Index: i, Name: tool.Name,
			Chars: len(t), Preview: preview(tool.Description), Cached: tool.CacheControl != nil})
	}

	for mi, m := range raw.Messages {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			add(Block{Key: fmt.Sprintf("m%d.b0", mi), Kind: KindText, Role: m.Role, Msg: mi, Index: 0,
				Chars: len(s), Preview: preview(s)})
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for bi, p := range parts {
			b := parseBlock(p)
			b.Key = fmt.Sprintf("m%d.b%d", mi, bi)
			b.Role, b.Msg, b.Index = m.Role, mi, bi
			add(b)
		}
	}
	return req, nil
}

func parseBlock(p json.RawMessage) Block {
	var head struct {
		Type         string          `json:"type"`
		Text         string          `json:"text"`
		Thinking     string          `json:"thinking"`
		Name         string          `json:"name"`
		ID           string          `json:"id"`
		ToolUseID    string          `json:"tool_use_id"`
		IsError      bool            `json:"is_error"`
		Input        json.RawMessage `json:"input"`
		Content      json.RawMessage `json:"content"`
		CacheControl any             `json:"cache_control"`
	}
	_ = json.Unmarshal(p, &head)
	b := Block{Chars: len(p), Cached: head.CacheControl != nil}
	switch head.Type {
	case "text":
		b.Kind, b.Chars, b.Preview = KindText, len(head.Text), preview(head.Text)
	case "thinking", "redacted_thinking":
		b.Kind, b.Chars, b.Preview = KindThinking, len(head.Thinking), preview(head.Thinking)
	case "tool_use":
		b.Kind, b.Name, b.ToolUseID = KindToolUse, head.Name, head.ID
		b.Preview = preview(string(head.Input))
	case "tool_result":
		b.Kind, b.ToolUseID, b.IsError = KindToolResult, head.ToolUseID, head.IsError
		text := resultText(head.Content)
		b.Chars, b.Preview = len(text), preview(text)
	case "image", "document":
		// Base64 payloads are billed by pixels/pages, not characters; count a
		// flat placeholder instead of inflating the estimate by megabytes.
		b.Kind, b.Chars, b.Preview = KindImage, imageChars, "["+head.Type+"]"
	default:
		b.Kind, b.Preview = KindOther, "["+head.Type+"]"
	}
	return b
}

// resultText flattens a tool_result's content (string or list of blocks).
func resultText(c json.RawMessage) string {
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
		if p.Type == "text" {
			out += p.Text
		} else {
			out += "[" + p.Type + "]"
		}
	}
	return out
}

func preview(s string) string {
	const n = 160
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
