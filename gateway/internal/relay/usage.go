package relay

import (
	"bytes"
	"encoding/json"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// UsageReader reads token usage out of a response as it passes. Feed gets
// every chunk; Result is called once the stream has ended.
type UsageReader interface {
	Feed(chunk []byte)
	// Result returns the usage (nil when none was seen), the model the
	// provider reported, and a failure message carried inside the stream.
	Result() (u *store.Usage, model, failure string)
}

// NewUsageReader returns the reader for a protocol's event stream.
func NewUsageReader(protocol string) UsageReader {
	switch protocol {
	case ir.ProtocolOpenAIChat, ir.ProtocolOpenAIResponses:
		return &openAISSE{chat: protocol == ir.ProtocolOpenAIChat}
	}
	return &anthropicSSE{}
}

// JSONUsage reads usage and model out of a non-streamed response body.
func JSONUsage(protocol string, b []byte) (*store.Usage, string) {
	switch protocol {
	case ir.ProtocolOpenAIChat, ir.ProtocolOpenAIResponses:
		var r struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
		}
		if json.Unmarshal(b, &r) != nil {
			return nil, ""
		}
		return r.Usage.toStore(), r.Model
	}
	var r struct {
		Model string       `json:"model"`
		Usage *store.Usage `json:"usage"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil, ""
	}
	return r.Usage, r.Model
}

// lines splits an event stream into lines across chunk boundaries.
type lines struct{ pending []byte }

func (l *lines) feed(chunk []byte, line func([]byte)) {
	l.pending = append(l.pending, chunk...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			return
		}
		ln := bytes.TrimRight(l.pending[:i], "\r")
		l.pending = l.pending[i+1:]
		line(ln)
	}
}

// anthropicSSE reads an Anthropic event stream. message_start carries input
// and cache counts; message_delta carries the running output count (and,
// on some providers, a final input count).
type anthropicSSE struct {
	lines
	u     store.Usage
	seen  bool
	model string
}

type anthropicUsage struct {
	InputTokens         *int `json:"input_tokens"`
	OutputTokens        *int `json:"output_tokens"`
	CacheReadTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationTokens *int `json:"cache_creation_input_tokens"`
}

func (s *anthropicSSE) Feed(chunk []byte) {
	s.feed(chunk, func(line []byte) {
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			s.event(bytes.TrimSpace(data))
		}
	})
}

func (s *anthropicSSE) event(data []byte) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage anthropicUsage `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		s.model = ev.Message.Model
		s.apply(ev.Message.Usage)
	case "message_delta":
		s.apply(ev.Usage)
	}
}

func (s *anthropicSSE) apply(f anthropicUsage) {
	set := func(dst *int, v *int) {
		if v != nil {
			*dst, s.seen = *v, true
		}
	}
	set(&s.u.InputTokens, f.InputTokens)
	set(&s.u.OutputTokens, f.OutputTokens)
	set(&s.u.CacheReadTokens, f.CacheReadTokens)
	set(&s.u.CacheCreationTokens, f.CacheCreationTokens)
}

func (s *anthropicSSE) Result() (*store.Usage, string, string) {
	if !s.seen {
		return nil, s.model, ""
	}
	u := s.u
	return &u, s.model, ""
}

// OpenAI reports input_tokens (or prompt_tokens) including cached tokens.
// The store follows Anthropic's split, where input_tokens is the fresh part
// and cache_read_input_tokens the cached part, so the dashboard's "fresh
// input" and "cache read" mean the same thing for every protocol.
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

// openAISSE reads an OpenAI event stream.
//
// Responses API: the terminal event (response.completed, or
// response.incomplete / response.failed) carries response.usage; the first
// event (response.created) already names the model.
// Chat Completions: with stream_options.include_usage the last chunk before
// [DONE] carries usage; every chunk names the model.
type openAISSE struct {
	lines
	chat    bool
	data    []byte // data lines of the event being read
	usage   *store.Usage
	model   string
	failure string
}

func (s *openAISSE) Feed(chunk []byte) { s.feed(chunk, s.line) }

func (s *openAISSE) line(line []byte) {
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

func (s *openAISSE) dispatch() {
	data := bytes.TrimSpace(s.data)
	s.data = s.data[:0]
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if s.chat {
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

// Result also handles a stream that ended without a final blank line.
func (s *openAISSE) Result() (*store.Usage, string, string) {
	if len(s.pending) > 0 {
		s.line(bytes.TrimRight(s.pending, "\r"))
		s.pending = nil
	}
	s.dispatch()
	return s.usage, s.model, s.failure
}
