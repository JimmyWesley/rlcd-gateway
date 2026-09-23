package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

// Settings is the "router" config section.
type Settings struct {
	// Sticky reuses a conversation's first routing decision for the rest of
	// it. Switching model mid-conversation throws away the provider's prompt
	// cache and invalidates signed thinking blocks.
	Sticky bool `json:"sticky"`
	// TTLHours expires sticky assignments not seen for this long.
	TTLHours float64 `json:"ttl_hours"`
	// BackgroundBypass lets background requests (title generation, quota
	// probes, ...) ignore the conversation's sticky route. They are
	// independent one-shot calls, so they cost no cache when routed apart.
	BackgroundBypass bool   `json:"background_bypass"`
	Rules            []Rule `json:"rules"`
	// Aliases map a model name the client asks for ("smart", "gpt-4o") to
	// a route and an upstream model. An alias match is the simplest rule:
	// it is checked before the rules and before stickiness.
	Aliases []Alias `json:"aliases,omitempty"`
	// Routes holds per-route metadata that config.Route has no field for,
	// keyed by route name.
	Routes map[string]RouteMeta `json:"routes,omitempty"`
}

// Alias is one model alias.
type Alias struct {
	// Name is the model id clients send and GET /v1/models lists.
	Name  string `json:"name"`
	Route string `json:"route"`
	// Model is the upstream model; empty sends Name unchanged (or the
	// route's own model, when it has one).
	Model       string `json:"model,omitempty"`
	Description string `json:"description,omitempty"`
}

var aliasNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

type RouteMeta struct {
	// Description tells the auto rule what the route is good at.
	Description string `json:"description,omitempty"`
}

const (
	KindMatch = "match" // route to Rule.Route when the conditions hold
	KindAuto  = "auto"  // ask the economy model to pick among described routes
	// KindDecision ("decision", decision.go) asks a System One model a
	// typed question and routes by the answer.

	defaultTTLHours    = 72
	defaultAutoTimeout = 1500 // ms
	maxAutoTimeout     = 10000
)

type Rule struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Kind is "match" (default), "auto" or "decision".
	Kind  string `json:"kind,omitempty"`
	Route string `json:"route,omitempty"`
	// Candidates restricts an auto rule to these routes; empty means every
	// route that has a description.
	Candidates    []string `json:"candidates,omitempty"`
	TimeoutMs     int      `json:"timeout_ms,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	// OverrideSticky lets a match rule take over a conversation that is
	// already pinned (e.g. the context outgrew the pinned model's window).
	OverrideSticky bool  `json:"override_sticky,omitempty"`
	When           Match `json:"when"`

	// Decision rules (decision.go). When holds preconditions: the question
	// is only asked when they all hold.
	Question *DecisionQuestion `json:"question,omitempty"`
	Inputs   *DecisionInputs   `json:"inputs,omitempty"`
	// Backend is "economy" (default) or a decisions backend (jev,
	// open-rlcd, ...); BackendModel overrides its model.
	Backend      string   `json:"backend,omitempty"`
	BackendModel string   `json:"backend_model,omitempty"`
	Branches     []Branch `json:"branches,omitempty"`
	// Else is where a failure, a low confidence or an answer no branch
	// matches goes: the next rule (nil, {} or {"next_rule": true}) or a
	// target.
	Else *Target `json:"else,omitempty"`
	// Evaluate is conversation_start (default), every_request or
	// when_context_over (with ContextOverTokens).
	Evaluate          string `json:"evaluate,omitempty"`
	ContextOverTokens int    `json:"context_over_tokens,omitempty"`
}

// Match holds the conditions of a rule. Every condition that is set must
// hold; a rule with none matches every request.
type Match struct {
	// Model is a regular expression (RE2) on the model the client asked for.
	Model string `json:"model,omitempty"`
	// Protocol restricts the rule to one request format:
	// "anthropic-messages", "openai-chat", "openai-responses", or "openai"
	// for both OpenAI formats.
	Protocol         string       `json:"protocol,omitempty"`
	MinContextTokens int          `json:"min_context_tokens,omitempty"`
	MaxContextTokens int          `json:"max_context_tokens,omitempty"`
	HasTools         *bool        `json:"has_tools,omitempty"`
	HasImages        *bool        `json:"has_images,omitempty"`
	HasThinking      *bool        `json:"has_thinking,omitempty"`
	Background       *bool        `json:"background,omitempty"`
	MaxTokensLTE     int          `json:"max_tokens_lte,omitempty"`
	Headers          []HeaderCond `json:"headers,omitempty"`
	Conversation     string       `json:"conversation,omitempty"`
}

// HeaderCond matches one request header; the name is case-insensitive.
// With neither Equals nor Contains it only requires the header to be present.
type HeaderCond struct {
	Name     string `json:"name"`
	Equals   string `json:"equals,omitempty"`
	Contains string `json:"contains,omitempty"`
}

func defaultSettings() Settings {
	return Settings{Sticky: true, TTLHours: defaultTTLHours, BackgroundBypass: true, Rules: []Rule{}}
}

// parseSettings reads the section; missing fields keep their defaults.
func parseSettings(raw json.RawMessage) (Settings, error) {
	s := defaultSettings()
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return defaultSettings(), err
	}
	if s.Rules == nil {
		s.Rules = []Rule{}
	}
	if s.TTLHours <= 0 {
		s.TTLHours = defaultTTLHours
	}
	return s, nil
}

// validate checks what a PUT may store. Unknown routes are rejected so a
// typo never silently falls through to the active route.
func (s Settings) validate(cfg config.Config) error {
	seen := map[string]bool{}
	for i, r := range s.Rules {
		label := fmt.Sprintf("rule %d", i+1)
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("%s needs a name", label)
		}
		label = fmt.Sprintf("rule %q", r.Name)
		if seen[r.Name] {
			return fmt.Errorf("%s: duplicate name", label)
		}
		seen[r.Name] = true
		switch p := r.When.Protocol; p {
		case "", ir.ProtocolAnthropic, ir.ProtocolOpenAIChat, ir.ProtocolOpenAIResponses, ProtocolOpenAI:
		default:
			return fmt.Errorf("%s: protocol must be %q, %q, %q or %q", label,
				ir.ProtocolAnthropic, ir.ProtocolOpenAIChat, ir.ProtocolOpenAIResponses, ProtocolOpenAI)
		}
		switch r.Kind {
		case "", KindMatch:
			rt, ok := cfg.Routes[r.Route]
			if !ok {
				return fmt.Errorf("%s: unknown route %q", label, r.Route)
			}
			if p := r.When.Protocol; p != "" && !speaksAny(rt, p) {
				return fmt.Errorf("%s: route %q speaks %s, but the rule only matches %s requests. "+
					"The gateway does not translate between protocols", label, r.Route, strings.Join(rt.Protocols(), " / "), p)
			}
		case KindAuto:
			if r.OverrideSticky {
				return fmt.Errorf("%s: an auto rule only runs at conversation start, so it cannot override stickiness", label)
			}
			for _, c := range r.Candidates {
				if _, ok := cfg.Routes[c]; !ok {
					return fmt.Errorf("%s: unknown candidate route %q", label, c)
				}
			}
			if r.TimeoutMs < 0 || r.TimeoutMs > maxAutoTimeout {
				return fmt.Errorf("%s: timeout_ms must be between 0 and %d", label, maxAutoTimeout)
			}
			if r.MinConfidence < 0 || r.MinConfidence > 1 {
				return fmt.Errorf("%s: min_confidence must be between 0 and 1", label)
			}
		case KindDecision:
			if err := validateDecision(label, r, s, cfg); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: kind must be %q, %q or %q", label, KindMatch, KindAuto, KindDecision)
		}
		if r.Kind != KindDecision && (r.Question != nil || len(r.Branches) > 0 || r.Else != nil || r.Evaluate != "") {
			return fmt.Errorf("%s: question, branches, else and evaluate only apply to decision rules", label)
		}
		if r.When.Model != "" {
			if _, err := regexp.Compile(r.When.Model); err != nil {
				return fmt.Errorf("%s: model regex: %v", label, err)
			}
		}
		if r.When.MinContextTokens < 0 || r.When.MaxContextTokens < 0 || r.When.MaxTokensLTE < 0 {
			return fmt.Errorf("%s: token limits cannot be negative", label)
		}
		for _, h := range r.When.Headers {
			if strings.TrimSpace(h.Name) == "" {
				return fmt.Errorf("%s: header condition needs a name", label)
			}
		}
	}
	if s.TTLHours < 0 {
		return fmt.Errorf("ttl_hours cannot be negative")
	}
	names := map[string]bool{}
	for i, a := range s.Aliases {
		if !aliasNameRe.MatchString(a.Name) {
			return fmt.Errorf("alias %d: name %q: letters, digits and . _ : / @ - (max 128)", i+1, a.Name)
		}
		if names[a.Name] {
			return fmt.Errorf("alias %q: duplicate name", a.Name)
		}
		names[a.Name] = true
		if _, ok := cfg.Routes[a.Route]; !ok {
			return fmt.Errorf("alias %q: unknown route %q", a.Name, a.Route)
		}
	}
	return nil
}

// ProtocolOpenAI in a rule's protocol condition matches both OpenAI formats.
const ProtocolOpenAI = "openai"

func protocolMatches(cond, protocol string) bool {
	if cond == ProtocolOpenAI {
		return ir.IsOpenAI(protocol)
	}
	return cond == protocol
}

// speaksAny reports whether the route can serve some request the protocol
// condition matches.
func speaksAny(rt config.Route, cond string) bool {
	if cond == ProtocolOpenAI {
		return rt.Speaks(ir.ProtocolOpenAIChat) || rt.Speaks(ir.ProtocolOpenAIResponses)
	}
	return rt.Speaks(cond)
}

// Facts is what the rules look at, extracted once per request.
type Facts struct {
	Model         string `json:"model"`
	ContextTokens int    `json:"context_tokens"`
	Messages      int    `json:"messages"`
	MaxTokens     int    `json:"max_tokens"`
	Tools         int    `json:"tools"`
	HasImages     bool   `json:"has_images"`
	// ThinkingParam is the request's thinking.type ("enabled", "adaptive",
	// "disabled" or "" when absent); ThinkingBlocks counts thinking blocks in
	// the history. HasThinking is true when either is present.
	ThinkingParam  string `json:"thinking_param,omitempty"`
	ThinkingBlocks int    `json:"thinking_blocks,omitempty"`
	HasThinking    bool   `json:"has_thinking"`
	// CacheControl is true when any block carries a prompt-cache marker.
	CacheControl      bool     `json:"cache_control"`
	Background        bool     `json:"background"`
	BackgroundSignals []string `json:"background_signals,omitempty"`
	ConversationID    string   `json:"conversation_id"`
	Protocol          string   `json:"protocol"`
	// Prompt is the latest user text, for the auto rule (not serialized).
	Prompt  string `json:"-"`
	headers http.Header
}

func extractFacts(req *pipeline.Request) *Facts {
	protocol := req.ProtocolOf()
	f := &Facts{ConversationID: req.ConversationID, Protocol: protocol, headers: req.Headers}
	x := req.XRay
	if x == nil {
		x, _ = ir.ParseFor(protocol, req.Body)
	}
	if x != nil {
		f.Model, f.ContextTokens, f.Messages, f.MaxTokens = x.Model, x.Tokens, x.Messages, x.MaxTokens
		for _, b := range x.Blocks {
			switch b.Kind {
			case ir.KindTool:
				f.Tools++
			case ir.KindImage:
				f.HasImages = true
			case ir.KindThinking:
				f.ThinkingBlocks++
			}
			if b.Cached {
				f.CacheControl = true
			}
		}
	}
	if ir.IsOpenAI(protocol) {
		var head struct {
			ReasoningEffort string `json:"reasoning_effort"`
			Reasoning       struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.Unmarshal(req.Body, &head)
		f.ThinkingParam = head.ReasoningEffort
		if head.Reasoning.Effort != "" {
			f.ThinkingParam = head.Reasoning.Effort
		}
		f.HasThinking = f.ThinkingBlocks > 0 || (f.ThinkingParam != "" && f.ThinkingParam != "none" && f.ThinkingParam != "minimal")
		f.Prompt = openAIPrompt(protocol, req.Body)
		// Background detection describes Claude Code's side calls; a plain
		// chat completion without tools is an ordinary turn.
		return f
	}
	var head struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(req.Body, &head)
	f.ThinkingParam = head.Thinking.Type
	f.HasThinking = f.ThinkingBlocks > 0 || (f.ThinkingParam != "" && f.ThinkingParam != "disabled")
	for i := len(head.Messages) - 1; i >= 0; i-- {
		if head.Messages[i].Role == "user" {
			f.Prompt = userText(head.Messages[i].Content)
			break
		}
	}
	f.Background, f.BackgroundSignals = detectBackground(f)
	return f
}

// openAIPrompt is the latest user text of a Chat or Responses request.
func openAIPrompt(protocol string, body []byte) string {
	var head struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	_ = json.Unmarshal(body, &head)
	msgs := head.Messages
	if protocol == ir.ProtocolOpenAIResponses {
		var s string
		if json.Unmarshal(head.Input, &s) == nil {
			return s
		}
		_ = json.Unmarshal(head.Input, &msgs)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(msgs[i], &m)
		if m.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			return s
		}
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(m.Content, &parts)
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// detectBackground recognizes the side requests Claude Code sends next to the
// main loop. Evidence from the Claude Code 2.1.x bundle:
//
//   - Quota probes (querySource "quota_check") and the token-count fallback
//     send max_tokens: 1 with a one-word user message ("quota", "count").
//   - One-shot side queries (session title, tool-use summary labels, topic
//     and comment triage...) go through a helper that sends tools: [],
//     thinking disabled, exactly one user message, prompt caching off (no
//     cache_control), and the small fast model (Haiku unless
//     ANTHROPIC_SMALL_FAST_MODEL says otherwise). Their max_tokens is NOT
//     reliably small: without an override it is the model's default output
//     limit, so max_tokens is not used as a signal.
//   - Forked helpers (prompt suggestions, away summary, rename) reuse the
//     main loop's exact prefix, tools included, to hit its prompt cache.
//     They are deliberately not background: they belong to the conversation
//     and must stay on its route to keep that cache.
//
// Main-loop and subagent turns always carry tools and cache_control, so they
// never match. A plain one-shot API call without tools from another client
// will look like background too; that is the intended meaning.
func detectBackground(f *Facts) (bool, []string) {
	if f.MaxTokens == 1 {
		return true, []string{"max_tokens 1: quota or token-count probe"}
	}
	if f.Tools > 0 || f.HasThinking || f.Messages != 1 || f.CacheControl {
		return false, nil
	}
	sig := []string{"no tools", "no thinking", "1 message", "no cache_control"}
	if strings.Contains(strings.ToLower(f.Model), "haiku") {
		sig = append(sig, "small model")
	}
	return true, sig
}

// userText flattens a user message to its text, skipping the
// <system-reminder> blocks Claude Code prepends, so the auto rule sees what
// the person actually typed.
func userText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(c, &parts)
	var out []string
	for _, p := range parts {
		if p.Type == "text" && !strings.HasPrefix(strings.TrimSpace(p.Text), "<system-reminder>") {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// Check is one evaluated condition, for the reason and the dry-run trace.
type Check struct {
	Condition string `json:"condition"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
}

// matchRule evaluates every set condition (all of them, so the trace is
// complete) and reports whether they all held.
func matchRule(m Match, re *regexp.Regexp, f *Facts) ([]Check, bool) {
	var checks []Check
	add := func(cond string, ok bool, detail string) {
		checks = append(checks, Check{Condition: cond, OK: ok, Detail: detail})
	}
	if m.Model != "" {
		if re == nil {
			add("model ~ "+m.Model, false, "invalid regex")
		} else {
			add("model ~ "+m.Model, re.MatchString(f.Model), "model "+orNone(f.Model))
		}
	}
	if m.MinContextTokens > 0 {
		add(fmt.Sprintf("context ≥ %d tokens", m.MinContextTokens), f.ContextTokens >= m.MinContextTokens,
			fmt.Sprintf("context ~%d tokens", f.ContextTokens))
	}
	if m.MaxContextTokens > 0 {
		add(fmt.Sprintf("context ≤ %d tokens", m.MaxContextTokens), f.ContextTokens <= m.MaxContextTokens,
			fmt.Sprintf("context ~%d tokens", f.ContextTokens))
	}
	if m.HasTools != nil {
		detail := "no tools"
		if f.Tools > 0 {
			detail = fmt.Sprintf("%d tools", f.Tools)
		}
		add("has tools = "+yesNo(*m.HasTools), (f.Tools > 0) == *m.HasTools, detail)
	}
	if m.HasImages != nil {
		add("has images = "+yesNo(*m.HasImages), f.HasImages == *m.HasImages, pick(f.HasImages, "has images", "no images"))
	}
	if m.HasThinking != nil {
		detail := "no thinking"
		if f.HasThinking {
			detail = "thinking"
			if f.ThinkingParam != "" && f.ThinkingParam != "disabled" {
				detail += " " + f.ThinkingParam
			}
			if f.ThinkingBlocks > 0 {
				detail += fmt.Sprintf(", %d thinking blocks", f.ThinkingBlocks)
			}
		}
		add("has thinking = "+yesNo(*m.HasThinking), f.HasThinking == *m.HasThinking, detail)
	}
	if m.Background != nil {
		detail := "not a background request"
		if f.Background {
			detail = "background request (" + strings.Join(f.BackgroundSignals, ", ") + ")"
		}
		add("background = "+yesNo(*m.Background), f.Background == *m.Background, detail)
	}
	if m.MaxTokensLTE > 0 {
		add(fmt.Sprintf("max_tokens ≤ %d", m.MaxTokensLTE), f.MaxTokens > 0 && f.MaxTokens <= m.MaxTokensLTE,
			fmt.Sprintf("max_tokens %d", f.MaxTokens))
	}
	for _, h := range m.Headers {
		v, present := headerValue(f.headers, h.Name)
		cond, ok := "header "+h.Name+" present", present
		switch {
		case h.Equals != "":
			cond, ok = fmt.Sprintf("header %s = %q", h.Name, h.Equals), present && v == h.Equals
		case h.Contains != "":
			cond, ok = fmt.Sprintf("header %s contains %q", h.Name, h.Contains), present && strings.Contains(v, h.Contains)
		}
		detail := "header " + h.Name + " absent"
		if present {
			detail = fmt.Sprintf("header %s %q", h.Name, clip(v, 60))
		}
		add(cond, ok, detail)
	}
	if m.Protocol != "" {
		add("protocol = "+m.Protocol, protocolMatches(m.Protocol, f.Protocol), "protocol "+f.Protocol)
	}
	if m.Conversation != "" {
		add("conversation = "+m.Conversation, f.ConversationID == m.Conversation, "conversation "+f.ConversationID)
	}
	for _, c := range checks {
		if !c.OK {
			return checks, false
		}
	}
	return checks, true
}

func headerValue(h http.Header, name string) (string, bool) {
	vs, ok := h[http.CanonicalHeaderKey(name)]
	if !ok {
		// Dry runs rebuild headers from the log, whose keys are as received.
		for k, v := range h {
			if strings.EqualFold(k, name) {
				vs, ok = v, true
				break
			}
		}
	}
	if !ok {
		return "", false
	}
	return strings.Join(vs, ", "), true
}

func yesNo(b bool) string    { return pick(b, "yes", "no") }
func orNone(s string) string { return pick(s != "", s, "(none)") }

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
