package adapters

import (
	"bytes"
	"encoding/json"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// OpenAI reports input_tokens (or prompt_tokens) including cached tokens.
// The store follows Anthropic's split, where input_tokens is the fresh part
// and cache_read_input_tokens the cached part, so the dashboard's "fresh
// input" and "cache read" mean the same thing for every agent.
type openAIUsage struct {
	// Responses API
	InputTokens        *int `json:"input_tokens"`
	OutputTokens       *int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	// Chat Completions
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *openAIUsage) toStore() *store.Usage {
	if u == nil {
		return nil
	}
	switch {
	case u.InputTokens != nil || u.OutputTokens != nil:
		return split(deref(u.InputTokens), u.InputTokensDetails.CachedTokens, deref(u.OutputTokens))
	case u.PromptTokens != nil || u.CompletionTokens != nil:
		return split(deref(u.PromptTokens), u.PromptTokensDetails.CachedTokens, deref(u.CompletionTokens))
	}
	return nil
}

func split(input, cached, output int) *store.Usage {
	cached = min(max(cached, 0), input)
	return &store.Usage{InputTokens: input - cached, CacheReadTokens: cached, OutputTokens: output}
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// sseUsage reads usage out of an OpenAI event stream as it passes.
//
// Responses API: the terminal event (response.completed, or
// response.incomplete / response.failed) carries response.usage; the first
// event (response.created) already names the model.
// Chat Completions: with stream_options.include_usage the last chunk before
// [DONE] carries usage; every chunk names the model.
type sseUsage struct {
	kind    string
	pending []byte
	data    []byte // data lines of the event being read
	usage   *store.Usage
	model   string
	failure string
}

func (s *sseUsage) feed(chunk []byte) {
	s.pending = append(s.pending, chunk...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimRight(s.pending[:i], "\r")
		s.pending = s.pending[i+1:]
		s.line(line)
	}
}

func (s *sseUsage) line(line []byte) {
	if len(line) == 0 {
		s.dispatch()
		return
	}
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		if len(s.data) > 0 {
			s.data = append(s.data, '\n')
		}
		s.data = append(s.data, bytes.TrimPrefix(data, []byte(" "))...)
	}
}

// flush handles a stream that ended without a final blank line.
func (s *sseUsage) flush() {
	if len(s.pending) > 0 {
		s.line(bytes.TrimRight(s.pending, "\r"))
		s.pending = nil
	}
	s.dispatch()
}

func (s *sseUsage) dispatch() {
	data := bytes.TrimSpace(s.data)
	s.data = s.data[:0]
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if s.kind == kindChat {
		var c struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
		}
		if json.Unmarshal(data, &c) != nil {
			return
		}
		if c.Model != "" {
			s.model = c.Model
		}
		if u := c.Usage.toStore(); u != nil {
			s.usage = u
		}
		return
	}
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if ev.Response.Model != "" {
		s.model = ev.Response.Model
	}
	switch ev.Type {
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		if u := ev.Response.Usage.toStore(); u != nil {
			s.usage = u
		}
		if ev.Response.Error != nil && ev.Response.Error.Message != "" {
			s.failure = ev.Response.Error.Message
		}
	case "error":
		if ev.Message != "" {
			s.failure = ev.Message
		}
	}
}

func (s *sseUsage) result() *store.Usage { return s.usage }

// jsonUsage handles non-streamed responses (both APIs put usage and model at the top level).
func jsonUsage(b []byte) (*store.Usage, string) {
	var r struct {
		Model string       `json:"model"`
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil, ""
	}
	return r.Usage.toStore(), r.Model
}
