// Package router picks which route serves each request (F2).
//
// Rules live in the "router" config section as an ordered list: the first
// enabled rule whose conditions all hold picks the route; when none does the
// router has no opinion and the active route serves the turn.
//
// Decisions are sticky per conversation by default: the first decision of a
// conversation is reused for all its turns, because switching model
// mid-conversation discards the provider's prompt cache and invalidates the
// signed thinking blocks already in the history. Background requests (title
// generation, quota probes) are independent calls, so they may bypass the
// pin and never create one.
//
// Route and the dashboard's dry run share evaluate, so the explanation shown
// for a request is the same code path that routed it.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Router struct {
	cfg    *config.Store
	st     *store.Store
	sticky *stickyStore

	mu       sync.Mutex // guards the compiled cache and section writes
	lastRaw  string
	compiled *compiled
}

// compiled is the parsed section plus its regexes, rebuilt only when the
// section's bytes change.
type compiled struct {
	s   Settings
	res []*regexp.Regexp
	err error
}

func New(cfg *config.Store, st *store.Store) *Router {
	return &Router{cfg: cfg, st: st, sticky: openSticky(filepath.Join(config.Dir(), "router"))}
}

func (r *Router) settings(c config.Config) *compiled {
	raw := c.Section("router")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.compiled != nil && r.lastRaw == string(raw) {
		return r.compiled
	}
	s, err := parseSettings(raw)
	if err != nil {
		log.Printf("router: config section: %v (routing disabled until fixed)", err)
		s.Rules = nil
	}
	cp := &compiled{s: s, res: make([]*regexp.Regexp, len(s.Rules)), err: err}
	for i, rule := range s.Rules {
		if rule.When.Model != "" {
			cp.res[i], _ = regexp.Compile(rule.When.Model)
		}
	}
	r.lastRaw, r.compiled = string(raw), cp
	return cp
}

func (s Settings) ttl() time.Duration { return time.Duration(s.TTLHours * float64(time.Hour)) }

// Decision sources.
const (
	SourceRule     = "rule"
	SourceAuto     = "auto"
	SourceSticky   = "sticky"
	SourceOverride = "override" // a rule with override_sticky took over a pinned conversation
	SourceNone     = "none"     // no rule matched: active route
	SourceAlias    = "alias"    // the client asked for a model alias
	SourceDecision = "decision" // a decision rule's answer picked the route
)

// Sticky actions: what Route does (or a dry run would do) to the pin.
const (
	StickyCreate  = "create"  // pin the conversation to this decision
	StickyReplace = "replace" // an override rule moved the pin
	StickyUse     = "use"     // an existing pin served the turn
	StickyNone    = "none"
)

// Rule trace results.
const (
	ResultMatched    = "matched"
	ResultNoMatch    = "no_match"
	ResultDisabled   = "disabled"
	ResultSkipped    = "skipped"
	ResultFallback   = "fallback" // auto or decision rule failed or matched no branch; evaluation went on
	ResultElse       = "else"     // decision rule failed or matched no branch; its else target decided
	ResultError      = "error"
	ResultNotReached = "not_reached"
)

type RuleTrace struct {
	Index  int     `json:"index"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Route  string  `json:"route,omitempty"`
	Result string  `json:"result"`
	Reason string  `json:"reason,omitempty"`
	Checks []Check `json:"checks,omitempty"`
	// Decision is a decision rule's question and answer.
	Decision *DecisionTrace `json:"decision,omitempty"`
}

type Evaluation struct {
	Decision pipeline.RouteDecision `json:"decision"`
	// OK=false means the active route serves the request.
	OK           bool        `json:"ok"`
	Source       string      `json:"source"`
	StickyAction string      `json:"sticky_action"`
	Sticky       *Assignment `json:"sticky,omitempty"`
	Facts        *Facts      `json:"facts"`
	Trace        []RuleTrace `json:"trace"`
	// Notes are side remarks (a pin to a deleted route was dropped...).
	Notes []string `json:"notes,omitempty"`
	// DryRun is true for a dry run: decision rules were asked for real,
	// but nothing was pinned.
	DryRun bool `json:"dry_run,omitempty"`
	rule   string
	// marks are when_context_over rules that re-decided on this request.
	marks []string
}

type evalOpts struct {
	// IgnoreSticky evaluates as if the conversation had no pin (dry run:
	// "what would a new conversation get").
	IgnoreSticky bool
	// DryRun marks decision rule calls as a dry run's.
	DryRun bool
}

// Route implements pipeline.Router.
func (r *Router) Route(ctx context.Context, req *pipeline.Request) (pipeline.RouteDecision, bool) {
	ev := r.evaluate(ctx, req, evalOpts{})
	r.commit(req.ConversationID, ev)
	return ev.Decision, ev.OK
}

func (r *Router) commit(conv string, ev *Evaluation) {
	if conv == "" {
		return
	}
	switch ev.StickyAction {
	case StickyUse:
		r.sticky.touch(conv)
		if len(ev.marks) > 0 {
			r.sticky.mark(conv, ev.marks)
		}
	case StickyCreate, StickyReplace:
		a := Assignment{ConversationID: conv, Rule: ev.rule, Reason: ev.Decision.Reason}
		if ev.OK {
			a.Route, a.Model = ev.Decision.Route, ev.Decision.Model
		}
		if ev.Sticky != nil {
			a.Created, a.Redecided = ev.Sticky.Created, append([]string(nil), ev.Sticky.Redecided...)
		}
		for _, m := range ev.marks {
			if !contains(a.Redecided, m) {
				a.Redecided = append(a.Redecided, m)
			}
		}
		r.sticky.put(a, ev.StickyAction == StickyReplace)
	}
}

func (r *Router) evaluate(ctx context.Context, req *pipeline.Request, opts evalOpts) *Evaluation {
	cfg := req.Config
	cp := r.settings(cfg)
	s := cp.s
	f := extractFacts(req)
	ev := &Evaluation{Facts: f, Source: SourceNone, StickyAction: StickyNone, Trace: []RuleTrace{}, DryRun: opts.DryRun}
	if cp.err != nil {
		ev.Notes = append(ev.Notes, "router section is invalid: "+cp.err.Error())
	}

	// An alias is the client naming its route: it wins over rules and pins,
	// and never creates a pin (the client asks for it on every turn).
	if a, ok := s.alias(f.Model); ok {
		ev.Source = SourceAlias
		ev.Decision.Alias = a.Name
		rt, exists := cfg.Routes[a.Route]
		switch {
		case !exists:
			ev.Decision.Error = fmt.Sprintf("model alias %q points at route %q, which does not exist", a.Name, a.Route)
			ev.Decision.Reason = "alias '" + a.Name + "': route missing"
		case !rt.Speaks(f.Protocol):
			ev.Decision.Error = fmt.Sprintf("model alias %q: %s", a.Name, pipeline.CrossProtocolError(a.Route, rt, f.Protocol))
			ev.Decision.Reason = "alias '" + a.Name + "': route speaks another protocol"
		default:
			ev.OK = true
			ev.Decision.Route, ev.Decision.Model = a.Route, a.Model
			ev.Decision.Reason = fmt.Sprintf("alias '%s' → route '%s'", a.Name, a.Route)
			if a.Model != "" {
				ev.Decision.Reason += fmt.Sprintf(", model '%s'", a.Model)
			}
		}
		return ev
	}

	var pin *Assignment
	switch {
	case !s.Sticky || f.ConversationID == "":
	case f.Background && s.BackgroundBypass:
		ev.Notes = append(ev.Notes, "background request: bypasses stickiness")
	case opts.IgnoreSticky:
	default:
		pin = r.sticky.get(f.ConversationID, s.ttl())
		if pin != nil && pin.Route != "" {
			rt, ok := cfg.Routes[pin.Route]
			switch {
			case !ok:
				ev.Notes = append(ev.Notes, fmt.Sprintf("sticky route %q no longer exists; re-evaluating", pin.Route))
				r.sticky.delete(f.ConversationID)
				pin = nil
			case !rt.Speaks(f.Protocol):
				ev.Notes = append(ev.Notes, fmt.Sprintf("sticky route %q does not speak %s; ignoring the pin", pin.Route, f.Protocol))
				pin = nil
			}
		}
	}
	ev.Sticky = pin
	// The auto rule answers "which model for this conversation", so it only
	// runs at conversation start. With stickiness that is "not pinned yet";
	// without it, the first turn.
	start := pin == nil && (s.Sticky || f.Messages <= 1)

	decided := -1
	for i, rule := range s.Rules {
		t := RuleTrace{Index: i, Name: rule.Name, Kind: kindOf(rule), Route: rule.Route}
		switch {
		case !rule.Enabled:
			t.Result = ResultDisabled
		case decided >= 0:
			t.Result = ResultNotReached
		case kindOf(rule) == KindDecision:
			if r.evalDecision(ctx, req, s, cp.res[i], rule, f, pin, start, opts, ev, &t) {
				decided = i
			}
		case pin != nil && !rule.OverrideSticky:
			t.Result, t.Reason = ResultSkipped, "conversation is pinned and this rule does not override stickiness"
		default:
			checks, ok := matchRule(rule.When, cp.res[i], f)
			t.Checks = checks
			switch {
			case !ok:
				t.Result, t.Reason = ResultNoMatch, failed(checks)
			case kindOf(rule) == KindAuto:
				switch {
				case f.Background:
					t.Result, t.Reason = ResultSkipped, "auto rule does not run for background requests"
				case !start:
					t.Result, t.Reason = ResultSkipped, "auto rule only runs at conversation start"
				default:
					route, why, err := r.auto(ctx, cfg, s, rule, f)
					if err != nil {
						t.Result, t.Reason = ResultFallback, err.Error()
						break
					}
					t.Result, t.Route, t.Reason = ResultMatched, route, why
					ev.Decision = pipeline.RouteDecision{Route: route,
						Reason: fmt.Sprintf("auto rule '%s' chose '%s' %s", rule.Name, route, why)}
					ev.Source, decided = SourceAuto, i
				}
			default:
				rt, exists := cfg.Routes[rule.Route]
				if !exists {
					t.Result, t.Reason = ResultError, fmt.Sprintf("route %q does not exist", rule.Route)
					break
				}
				if !rt.Speaks(f.Protocol) {
					t.Result, t.Reason = ResultSkipped, fmt.Sprintf("route %q speaks %s, not %s (no translation between protocols)",
						rule.Route, strings.Join(rt.Protocols(), " / "), f.Protocol)
					break
				}
				t.Result, t.Reason = ResultMatched, passed(checks)
				ev.Decision = pipeline.RouteDecision{Route: rule.Route,
					Reason: fmt.Sprintf("rule '%s' matched: %s", rule.Name, passed(checks))}
				ev.Source, decided = SourceRule, i
				if pin != nil {
					ev.Source = SourceOverride
					ev.Decision.Reason = fmt.Sprintf("rule '%s' overrode sticky route: %s", rule.Name, passed(checks))
				}
			}
		}
		ev.Trace = append(ev.Trace, t)
	}

	canPin := s.Sticky && f.ConversationID != "" && !f.Background
	switch {
	case decided >= 0:
		ev.OK, ev.rule = true, s.Rules[decided].Name
		if canPin {
			ev.StickyAction = pick(pin != nil, StickyReplace, StickyCreate)
		}
	case pin != nil:
		ev.Source, ev.StickyAction, ev.rule = SourceSticky, StickyUse, pin.Rule
		if pin.Route == "" {
			ev.Decision.Reason = "sticky: conversation started on the active route (no rule matched)"
			break
		}
		ev.OK = true
		ev.Decision.Route, ev.Decision.Model = pin.Route, pin.Model
		ev.Decision.Reason = fmt.Sprintf("sticky: conversation started on '%s'", pin.Route)
		if pin.Model != "" {
			ev.Decision.Reason = fmt.Sprintf("sticky: conversation started on '%s:%s'", pin.Route, pin.Model)
		}
		if pin.Rule != "" {
			ev.Decision.Reason += fmt.Sprintf(" (rule '%s')", pin.Rule)
		}
	default:
		ev.Decision.Reason = "no rule matched: active route"
		if canPin {
			// Pin "follows the active route" too, so a rule does not start
			// matching halfway through a conversation that began without one.
			ev.StickyAction = StickyCreate
		}
	}
	return ev
}

func kindOf(r Rule) string {
	if r.Kind == KindAuto || r.Kind == KindDecision {
		return r.Kind
	}
	return KindMatch
}

// evalDecision evaluates a decision rule into t; it reports whether the
// rule decided the route.
func (r *Router) evalDecision(ctx context.Context, req *pipeline.Request, s Settings, re *regexp.Regexp, rule Rule,
	f *Facts, pin *Assignment, start bool, opts evalOpts, ev *Evaluation, t *RuleTrace) bool {
	mode := rule.evaluateMode()
	over := mode == EvalWhenContextOver && f.ContextTokens > rule.ContextOverTokens
	switch {
	case start, mode == EvalEveryRequest:
	case over && !pin.redecided(rule.Name):
	case over:
		t.Result, t.Reason = ResultSkipped, fmt.Sprintf("already re-decided once past %d tokens", rule.ContextOverTokens)
		return false
	case mode == EvalWhenContextOver:
		t.Result, t.Reason = ResultSkipped, fmt.Sprintf("context ~%d ≤ %d tokens: re-decides once the conversation grows past it",
			f.ContextTokens, rule.ContextOverTokens)
		return false
	case pin != nil:
		t.Result, t.Reason = ResultSkipped, "conversation is pinned; this decision rule runs at conversation start"
		return false
	default:
		t.Result, t.Reason = ResultSkipped, "this decision rule runs at conversation start"
		return false
	}
	checks, ok := matchRule(rule.When, re, f)
	t.Checks = checks
	switch {
	case !ok:
		t.Result, t.Reason = ResultNoMatch, failed(checks)
		return false
	case f.Background:
		t.Result, t.Reason = ResultSkipped, "decision rules do not run for background requests"
		return false
	case !decisionSpeaks(req.Config, s, rule, f.Protocol):
		t.Result, t.Reason = ResultSkipped, fmt.Sprintf("none of this rule's targets speaks %s (no translation between protocols)",
			protocolName(f.Protocol))
		return false
	}
	dt := r.decide(ctx, req.Config, s, rule, req, f, opts.DryRun)
	t.Decision = dt
	if over {
		ev.marks = append(ev.marks, rule.Name)
	}
	if dt.Outcome == OutcomeFallThrough {
		t.Result, t.Reason = ResultFallback, dt.Summary
		return false
	}
	t.Result, t.Route, t.Reason = ResultMatched, dt.Route, dt.Summary
	if dt.Outcome == OutcomeElse {
		t.Result = ResultElse
	}
	ev.Decision = pipeline.RouteDecision{Route: dt.Route, Model: dt.UpstreamModel,
		Reason: fmt.Sprintf("decision rule '%s': %s", rule.Name, dt.Summary)}
	if pin != nil {
		ev.Decision.Reason = fmt.Sprintf("decision rule '%s' re-decided: %s", rule.Name, dt.Summary)
	}
	ev.Source = SourceDecision
	return true
}

// passed summarizes the conditions that held: the "why" of a match.
func passed(checks []Check) string {
	if len(checks) == 0 {
		return "no conditions (catch-all)"
	}
	var parts []string
	for _, c := range checks {
		parts = append(parts, c.Detail)
	}
	return strings.Join(parts, ", ")
}

// failed names the first condition that did not hold.
func failed(checks []Check) string {
	for _, c := range checks {
		if !c.OK {
			return c.Condition + " failed: " + c.Detail
		}
	}
	return ""
}

// marshalSettings encodes the section, never writing "rules": null.
func marshalSettings(s Settings) (json.RawMessage, error) {
	if s.Rules == nil {
		s.Rules = []Rule{}
	}
	return json.Marshal(s)
}

// alias finds the alias the client asked for.
func (s Settings) alias(model string) (Alias, bool) {
	if model == "" {
		return Alias{}, false
	}
	for _, a := range s.Aliases {
		if a.Name == model {
			return a, true
		}
	}
	return Alias{}, false
}

// Models implements pipeline.ModelLister: the aliases, for GET /v1/models.
func (r *Router) Models(cfg config.Config) []pipeline.Model {
	s := r.settings(cfg).s
	out := make([]pipeline.Model, 0, len(s.Aliases))
	for _, a := range s.Aliases {
		rt, ok := cfg.Routes[a.Route]
		if !ok {
			continue
		}
		out = append(out, pipeline.Model{ID: a.Name, Route: a.Route, Model: a.Model, Description: a.Description,
			Protocols: rt.Protocols()})
	}
	return out
}
