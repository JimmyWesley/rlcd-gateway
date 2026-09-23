package proxy

import (
	"bytes"
	"encoding/json"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// sseUsage reads token usage out of an Anthropic event stream as it passes.
// message_start carries input and cache counts; message_delta carries the
// running output count (and, on some providers, a final input count).
type sseUsage struct {
	pending []byte
	u       store.Usage
	seen    bool
	model   string
}

type usageFields struct {
	InputTokens         *int `json:"input_tokens"`
	OutputTokens        *int `json:"output_tokens"`
	CacheReadTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationTokens *int `json:"cache_creation_input_tokens"`
}

func (s *sseUsage) feed(chunk []byte) {
	s.pending = append(s.pending, chunk...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(s.pending[:i], "\r")
		s.pending = s.pending[i+1:]
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			s.event(bytes.TrimSpace(data))
		}
	}
}

func (s *sseUsage) event(data []byte) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Model string      `json:"model"`
			Usage usageFields `json:"usage"`
		} `json:"message"`
		Usage usageFields `json:"usage"`
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

func (s *sseUsage) apply(f usageFields) {
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

func (s *sseUsage) result() *store.Usage {
	if !s.seen {
		return nil
	}
	u := s.u
	return &u
}

// jsonUsage handles non-streamed responses.
func jsonUsage(b []byte) *store.Usage {
	var r struct {
		Usage *store.Usage `json:"usage"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return r.Usage
}
