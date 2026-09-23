package resilience

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Output-limit fields, by protocol.
const (
	FieldMaxTokens           = "max_tokens"
	FieldMaxCompletionTokens = "max_completion_tokens"
	FieldMaxOutputTokens     = "max_output_tokens"
)

// Guard change reasons.
const (
	ReasonFilled        = "filled_missing"
	ReasonAboveMaxOut   = "above_max_output"
	ReasonExceedsWindow = "exceeds_context_window"
	ReasonProviderError = "provider_error"
)

// GuardInput is one request as the guard sees it.
type GuardInput struct {
	Protocol string
	Guard    Guard
	Limits   Limits
	// EstInputTokens is the gateway's estimate of the prompt.
	EstInputTokens int
	// FirstParty is true for api.openai.com and api.anthropic.com, which
	// choose a safe default themselves.
	FirstParty bool
	// OpenAIAPI is true when the upstream is OpenAI itself, which wants
	// max_completion_tokens on chat completions.
	OpenAIAPI bool
	// Agent is true for coding agents: a missing limit is filled with the
	// largest value that fits, never with the chat default.
	Agent bool
}

// Body is a request decoded for editing. Numbers stay json.Number so
// everything the guard does not touch is re-encoded as it came.
type Body struct {
	m map[string]any
}

// DecodeBody parses a JSON object body.
func DecodeBody(b []byte) (*Body, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return &Body{m: m}, nil
}

// Encode writes the body without HTML escaping, so text reads back as sent.
func (b *Body) Encode() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(b.m)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func (b *Body) intField(k string) (int, bool) {
	switch v := b.m[k].(type) {
	case json.Number:
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return int(n), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func (b *Body) setInt(k string, v int) { b.m[k] = json.Number(strconv.Itoa(v)) }

// LimitFields lists the output-limit fields of a protocol that the body
// sets, in the order they are checked.
func (b *Body) LimitFields(protocol string) []string {
	var out []string
	for _, f := range fieldsFor(protocol) {
		if _, ok := b.intField(f); ok {
			out = append(out, f)
		}
	}
	return out
}

func fieldsFor(protocol string) []string {
	switch protocol {
	case ir.ProtocolOpenAIChat:
		return []string{FieldMaxCompletionTokens, FieldMaxTokens}
	case ir.ProtocolOpenAIResponses:
		return []string{FieldMaxOutputTokens}
	}
	return []string{FieldMaxTokens}
}

// OutputLimit is the body's output limit (the smallest set field).
func (b *Body) OutputLimit(protocol string) (field string, v int, ok bool) {
	for _, f := range b.LimitFields(protocol) {
		n, _ := b.intField(f)
		if !ok || n < v {
			field, v, ok = f, n, true
		}
	}
	return field, v, ok
}

// fillField is the field a missing limit is set in.
func fillField(protocol string, openAIAPI bool) string {
	switch protocol {
	case ir.ProtocolOpenAIChat:
		if openAIAPI {
			return FieldMaxCompletionTokens
		}
		return FieldMaxTokens
	case ir.ProtocolOpenAIResponses:
		return FieldMaxOutputTokens
	}
	return FieldMaxTokens
}

// fit is the largest output that fits the window next to the estimated
// input, or 0 when the window is unknown.
func fit(l Limits, estInput, margin int) int {
	if l.ContextWindow <= 0 {
		return 0
	}
	f := l.ContextWindow - estInput - estInput/10 - margin
	if f < 0 {
		return -1
	}
	return f
}

// ApplyGuard sets a missing output limit and clamps one the model cannot
// honour. It returns the change made, or nil (and b untouched).
func ApplyGuard(b *Body, in GuardInput) *store.MaxTokensChange {
	g := in.Guard
	if !g.Enabled || !in.Limits.Known() {
		return nil
	}
	room := fit(in.Limits, in.EstInputTokens, g.SafetyMarginTokens)
	change := func(field string, from *int, to int, reason string) *store.MaxTokensChange {
		return &store.MaxTokensChange{Field: field, From: from, To: to, Reason: reason, LimitSource: in.Limits.Source,
			ContextWindow: in.Limits.ContextWindow, MaxOutputTokens: in.Limits.MaxOutputTokens, EstInputTokens: in.EstInputTokens}
	}
	fields := b.LimitFields(in.Protocol)
	if len(fields) == 0 {
		// Anthropic requires max_tokens: its absence is the client's 400.
		if in.Protocol == ir.ProtocolAnthropic || in.Protocol == "" {
			return nil
		}
		if g.FillMissing == FillNever || (g.FillMissing == FillAuto && in.FirstParty) {
			return nil
		}
		v := in.Limits.MaxOutputTokens
		if !in.Agent {
			v = minPos(v, g.DefaultMaxTokens)
		}
		if room > 0 {
			v = minPos(v, room)
		}
		if room < 0 || v < minUsefulOutput {
			return nil // it will not fit whatever the limit: recovery decides
		}
		field := fillField(in.Protocol, in.OpenAIAPI)
		b.setInt(field, v)
		return change(field, nil, v, ReasonFilled)
	}
	if !g.Clamp {
		return nil
	}
	limit, reason := in.Limits.MaxOutputTokens, ReasonAboveMaxOut
	if room > 0 && (limit <= 0 || room < limit) {
		limit, reason = room, ReasonExceedsWindow
	}
	if limit < minUsefulOutput {
		return nil
	}
	var out *store.MaxTokensChange
	for _, f := range fields {
		v, _ := b.intField(f)
		if v <= limit {
			continue
		}
		c := b.clampTo(in.Protocol, f, v, limit, reason)
		if c == nil {
			continue
		}
		c.LimitSource, c.ContextWindow, c.MaxOutputTokens, c.EstInputTokens = in.Limits.Source,
			in.Limits.ContextWindow, in.Limits.MaxOutputTokens, in.EstInputTokens
		if out == nil {
			out = c
		}
	}
	return out
}

// clampTo lowers field to limit. An Anthropic thinking budget must stay
// below max_tokens (and at least 1024), so it is lowered too, or the
// clamp is skipped when that is impossible.
func (b *Body) clampTo(protocol, field string, from, limit int, reason string) *store.MaxTokensChange {
	c := &store.MaxTokensChange{Field: field, From: &from, To: limit, Reason: reason}
	if protocol == ir.ProtocolAnthropic || protocol == "" {
		if th, _ := b.m["thinking"].(map[string]any); th != nil {
			if bt, ok := (&Body{m: th}).intField("budget_tokens"); ok && bt >= limit {
				nb := limit - 1
				if nb < 1024 {
					return nil
				}
				(&Body{m: th}).setInt("budget_tokens", nb)
				c.ThinkingBudgetFrom, c.ThinkingBudgetTo = bt, nb
			}
		}
	}
	b.setInt(field, limit)
	return c
}

// ClampOutput sets every output-limit field to at most limit (setting one
// when none is set). It is the recovery for output_too_large.
func ClampOutput(b *Body, protocol string, openAIAPI bool, limit int) *store.MaxTokensChange {
	fields := b.LimitFields(protocol)
	if len(fields) == 0 {
		f := fillField(protocol, openAIAPI)
		b.setInt(f, limit)
		return &store.MaxTokensChange{Field: f, To: limit, Reason: ReasonProviderError}
	}
	var out *store.MaxTokensChange
	for _, f := range fields {
		v, _ := b.intField(f)
		if v <= limit {
			continue
		}
		if c := b.clampTo(protocol, f, v, limit, ReasonProviderError); c != nil && out == nil {
			out = c
		}
	}
	return out
}

// IgnoreProviders adds names to OpenRouter's provider.ignore list,
// keeping whatever provider preferences the client sent.
func IgnoreProviders(b *Body, names []string) {
	if len(names) == 0 {
		return
	}
	prov, _ := b.m["provider"].(map[string]any)
	if prov == nil {
		prov = map[string]any{}
	}
	var list []any
	if cur, ok := prov["ignore"].([]any); ok {
		list = cur
	}
	have := map[string]bool{}
	for _, v := range list {
		if s, ok := v.(string); ok {
			have[s] = true
		}
	}
	for _, n := range names {
		if !have[n] {
			list = append(list, n)
			have[n] = true
		}
	}
	prov["ignore"] = list
	b.m["provider"] = prov
}
