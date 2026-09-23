package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

// RuleTestInput is POST /api/router/rules/test: one decision rule, saved or
// not, and either a logged request or an ad-hoc text.
type RuleTestInput struct {
	Rule      Rule   `json:"rule"`
	RequestID string `json:"request_id,omitempty"`
	Text      string `json:"text,omitempty"`
	// Protocol is the format of the ad-hoc request built from Text
	// (default anthropic-messages).
	Protocol string `json:"protocol,omitempty"`
}

// RuleTestResult is what the rule would do with that request. Nothing is
// pinned and no other rule is consulted.
type RuleTestResult struct {
	Rule      string `json:"rule"`
	Input     string `json:"input"` // "request" or "text"
	RequestID string `json:"request_id,omitempty"`
	Protocol  string `json:"protocol"`
	Facts     *Facts `json:"facts"`
	// Conditions are the rule's when checks; the question is asked even
	// when they fail, and ConditionsOK says whether routing would ask it.
	Conditions   []Check        `json:"conditions"`
	ConditionsOK bool           `json:"conditions_ok"`
	Decision     *DecisionTrace `json:"decision"`
	// Route and Model are where the request would go; empty when the rule
	// falls through to the next rule.
	Route  string   `json:"route,omitempty"`
	Model  string   `json:"model,omitempty"`
	Reason string   `json:"reason"`
	Notes  []string `json:"notes,omitempty"`
}

const maxTestText = 20000

func (r *Router) testRule(w http.ResponseWriter, req *http.Request) {
	var in RuleTestInput
	if err := decode(req, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, status, err := r.TestRule(req.Context(), in)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// TestRule evaluates one decision rule definition against a logged request
// or a text, for real, as a dry run.
func (r *Router) TestRule(ctx context.Context, in RuleTestInput) (*RuleTestResult, int, error) {
	rule := in.Rule
	if strings.TrimSpace(rule.Name) == "" {
		rule.Name = "test"
	}
	if rule.Kind != KindDecision {
		return nil, http.StatusBadRequest, fmt.Errorf("only decision rules can be tested (kind %q)", KindDecision)
	}
	rule.Enabled = true
	cfg := r.cfg.Get()
	cur := r.current()
	s := Settings{Sticky: cur.Sticky, TTLHours: cur.TTLHours, Rules: []Rule{rule}, Aliases: cur.Aliases, Routes: cur.Routes}
	if err := s.validate(cfg); err != nil {
		return nil, http.StatusBadRequest, err
	}
	out := &RuleTestResult{Rule: rule.Name}
	var preq *pipeline.Request
	switch {
	case in.RequestID != "" && in.Text != "":
		return nil, http.StatusBadRequest, errors.New("give request_id or text, not both")
	case in.RequestID != "":
		p, _, err := r.loadRequest(in.RequestID)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, http.StatusNotFound, errors.New("no such request")
		case errors.Is(err, errNoBody):
			return nil, http.StatusUnprocessableEntity, err
		case err != nil:
			return nil, http.StatusInternalServerError, err
		}
		preq, out.Input, out.RequestID = p, "request", in.RequestID
	case strings.TrimSpace(in.Text) != "":
		if len(in.Text) > maxTestText {
			return nil, http.StatusBadRequest, fmt.Errorf("text is limited to %d bytes", maxTestText)
		}
		protocol := in.Protocol
		if protocol == "" {
			// The format the rule's targets speak, so the test reaches them.
			protocol = targetProtocol(cfg, s, rule)
		}
		p, err := textRequest(in.Text, protocol)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		p.Config = cfg
		preq, out.Input = p, "text"
		out.Notes = append(out.Notes, "ad-hoc text: a one-message request with no tools, system prompt or history")
	default:
		return nil, http.StatusBadRequest, errors.New("request_id or text is required")
	}
	f := extractFacts(preq)
	out.Protocol, out.Facts = f.Protocol, f
	var re *regexp.Regexp
	if rule.When.Model != "" {
		re, _ = regexp.Compile(rule.When.Model)
	}
	out.Conditions, out.ConditionsOK = matchRule(rule.When, re, f)
	if out.Conditions == nil {
		out.Conditions = []Check{}
	}
	if !out.ConditionsOK {
		out.Notes = append(out.Notes, "the rule's conditions do not hold for this request, so routing would not ask; asked anyway")
	}
	if f.Background && out.Input == "request" {
		out.Notes = append(out.Notes, "this is a background request: routing never asks decision rules for those")
	}
	if !decisionSpeaks(cfg, s, rule, f.Protocol) {
		out.Notes = append(out.Notes, fmt.Sprintf("none of the rule's targets speaks %s: routing would skip the rule", f.Protocol))
	}
	dt := r.decide(ctx, cfg, s, rule, preq, f, true)
	out.Decision = dt
	out.Route, out.Model = dt.Route, dt.UpstreamModel
	out.Reason = fmt.Sprintf("decision rule '%s': %s", rule.Name, dt.Summary)
	return out, 0, nil
}

// textRequest builds a one-message request in protocol around text.
func textRequest(text, protocol string) (*pipeline.Request, error) {
	if protocol == "" {
		protocol = ir.ProtocolAnthropic
	}
	var body map[string]any
	msgs := []any{map[string]any{"role": "user", "content": text}}
	switch protocol {
	case ir.ProtocolAnthropic:
		body = map[string]any{"model": "rule-test", "max_tokens": 1024, "messages": msgs}
	case ir.ProtocolOpenAIChat:
		body = map[string]any{"model": "rule-test", "messages": msgs}
	case ir.ProtocolOpenAIResponses:
		body = map[string]any{"model": "rule-test", "input": text}
	default:
		return nil, fmt.Errorf("protocol must be %q, %q or %q", ir.ProtocolAnthropic, ir.ProtocolOpenAIChat, ir.ProtocolOpenAIResponses)
	}
	b, _ := json.Marshal(body)
	p := &pipeline.Request{ID: "rule-test", Protocol: protocol, Body: b, Headers: http.Header{}}
	p.XRay, _ = ir.ParseFor(protocol, b)
	return p, nil
}

// targetProtocol is the first request format the rule's first target
// speaks (Anthropic when it has none).
func targetProtocol(cfg config.Config, s Settings, rule Rule) string {
	for _, b := range rule.Branches {
		route := b.Then.Route
		if b.Then.Alias != "" {
			a, _ := s.alias(b.Then.Alias)
			route = a.Route
		}
		if rt, ok := cfg.Routes[route]; ok {
			return rt.Protocols()[0]
		}
	}
	return ir.ProtocolAnthropic
}
