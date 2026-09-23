package router

// Decision rules: an n8n-style Switch whose condition a System One model
// answers. The rule asks one typed question (choice, score or noul) about
// a few facts of the request, and its branches map the answer to a
// target: a route, a route with an upstream model, or a model alias.
//
// The call goes through the decisions backends (economy, jev, open-rlcd,
// ...) with the backend's own token, and through the selector's trace, so
// it is audited as an internal decision when log_internal is on. Any
// failure (no backend, timeout, an unusable answer, low confidence) takes
// the rule's else: the next rule by default, or an explicit target.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/clients"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/decisions"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/prune"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
)

// KindDecision routes by a System One model's answer to the rule's question.
const KindDecision = "decision"

// Question types.
const (
	QuestionChoice = "choice"
	QuestionScore  = "score"
	QuestionNoul   = "noul"
)

// When a decision rule is evaluated.
const (
	// EvalConversationStart decides once, when the conversation starts, and
	// pins the answer (the default, like the auto rule).
	EvalConversationStart = "conversation_start"
	// EvalEveryRequest decides on every request, pinned or not. Switching
	// model mid-conversation discards the provider's prompt cache and
	// invalidates signed thinking blocks.
	EvalEveryRequest = "every_request"
	// EvalWhenContextOver decides at conversation start, then once more
	// when the conversation grows past ContextOverTokens, and pins again.
	EvalWhenContextOver = "when_context_over"
)

// Inputs a decision rule can show the model.
const (
	InputLatestUserText  = "latest_user_text"
	InputGoal            = "goal"
	InputRecentToolCalls = "recent_tool_calls"
	InputContextTokens   = "context_tokens"
	InputHasTools        = "has_tools"
	InputHasImages       = "has_images"
	InputClient          = "client"
	InputProtocol        = "protocol"
	InputModelRequested  = "model_requested"
)

var knownInputs = []string{InputLatestUserText, InputGoal, InputRecentToolCalls, InputContextTokens,
	InputHasTools, InputHasImages, InputClient, InputProtocol, InputModelRequested}

// DefaultInputs is what a rule without inputs shows the model.
var DefaultInputs = []string{InputLatestUserText, InputContextTokens, InputClient}

const (
	defaultDecisionTimeout = 2000 // ms
	defaultGoalTurns       = 3
	maxGoalTurns           = 10
	maxHeaderInputChars    = 200
	// questionKey is the question id sent to the backend.
	questionKey = "decision"
)

// sensitiveHeaders never reach a decision model.
var sensitiveHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "x-api-key": true,
	"api-key": true, "cookie": true, "x-rlcd-key": true, "x-rlcd-admin-token": true}

// DecisionQuestion is the System One question a decision rule asks.
type DecisionQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria is a map label → description for choice, an ordered legend
	// (index 0 first) for score, and absent for noul.
	Criteria *Criteria `json:"criteria,omitempty"`
}

// Criteria holds a choice's labels or a score's legend. It reads and
// writes the System One shapes: a JSON object or a JSON array.
type Criteria struct {
	Choices map[string]string
	Legend  []string
}

func (c Criteria) MarshalJSON() ([]byte, error) {
	if c.Legend != nil {
		return json.Marshal(c.Legend)
	}
	if c.Choices != nil {
		return json.Marshal(c.Choices)
	}
	return []byte("null"), nil
}

func (c *Criteria) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) > 0 && b[0] == '[':
		return json.Unmarshal(b, &c.Legend)
	case len(b) > 0 && b[0] == '{':
		return json.Unmarshal(b, &c.Choices)
	case string(b) == "null":
		return nil
	}
	return errors.New("criteria must be an object (choice) or an array (score)")
}

// DecisionInputs selects the facts the model sees.
type DecisionInputs struct {
	// Facts are names from knownInputs; empty means DefaultInputs.
	Facts []string `json:"facts,omitempty"`
	// GoalTurns is how many user turns the goal covers (default 3).
	GoalTurns int `json:"goal_turns,omitempty"`
	// Headers are request headers to include (never credentials).
	Headers []string `json:"headers,omitempty"`
}

// Branch is one Switch output: when the answer matches, go to Then.
type Branch struct {
	// Label names the output in the dashboard ("code", "hard").
	Label string     `json:"label,omitempty"`
	When  BranchWhen `json:"when"`
	Then  Target     `json:"then"`
}

// BranchWhen matches an answer:
//
//	choice: {"equals": "code"} or {"in": ["chat", "other"]}
//	score:  {"op": ">=" | "<=" | "==", "value": 2} or {"op": "between", "min": 1, "max": 2} on the legend index
//	noul:   {"op": ">=" | "<", "value": 0.7} on p(yes)
type BranchWhen struct {
	Equals string   `json:"equals,omitempty"`
	In     []string `json:"in,omitempty"`
	Op     string   `json:"op,omitempty"`
	Value  *float64 `json:"value,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// Target is where a branch sends the request: a route (with an optional
// upstream model) or a model alias. As a rule's else, NextRule (or an
// empty target) falls through to the next rule.
type Target struct {
	Route    string `json:"route,omitempty"`
	Model    string `json:"model,omitempty"`
	Alias    string `json:"alias,omitempty"`
	NextRule bool   `json:"next_rule,omitempty"`
}

func (t Target) empty() bool { return t.Route == "" && t.Alias == "" }

// String is the target as the reason shows it: "opus", "cheap:qwen/qwen3",
// "alias 'smart'".
func (t Target) String() string {
	switch {
	case t.Alias != "":
		return "alias '" + t.Alias + "'"
	case t.Model != "":
		return t.Route + ":" + t.Model
	case t.Route != "":
		return t.Route
	}
	return "next rule"
}

// Decision outcomes.
const (
	OutcomeBranch      = "branch"       // a branch matched
	OutcomeElse        = "else"         // no branch or a failure, explicit else target
	OutcomeFallThrough = "fall_through" // no branch or a failure, next rule
)

// DecisionTrace is what a decision rule asked and got, for the rule trace,
// the dry run and the test endpoint.
type DecisionTrace struct {
	QuestionType string `json:"question_type"`
	// Answer is the choice, the score's legend index, or p(yes) for noul.
	Answer string `json:"answer,omitempty"`
	Choice string `json:"choice,omitempty"`
	Index  *int   `json:"index,omitempty"`
	// MaxIndex is the score legend's highest index.
	MaxIndex int      `json:"max_index,omitempty"`
	Label    string   `json:"label,omitempty"`
	Score    *float64 `json:"score,omitempty"`
	Noul     *float64 `json:"noul,omitempty"`
	// Confidence is the backend's for choice and score, max(p, 1-p) for noul.
	Confidence    *float64           `json:"confidence,omitempty"`
	MinConfidence float64            `json:"min_confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Backend       string             `json:"backend"`
	Provider      string             `json:"provider,omitempty"`
	Model         string             `json:"model,omitempty"`
	LatencyMs     int64              `json:"latency_ms"`
	ForwardMs     *float64           `json:"forward_ms,omitempty"`
	// Branch is the index of the matching branch, -1 when none did.
	Branch      int    `json:"branch"`
	BranchLabel string `json:"branch_label,omitempty"`
	Outcome     string `json:"outcome"`
	// Target is where the rule sends the request (nil on fall through);
	// Route and UpstreamModel are what it resolved to.
	Target        *Target `json:"target,omitempty"`
	Route         string  `json:"route,omitempty"`
	UpstreamModel string  `json:"upstream_model,omitempty"`
	// Error says why the answer was not used (failure, low confidence...).
	Error string `json:"error,omitempty"`
	// DryRun marks an evaluation made for a dry run or a test: nothing was
	// pinned.
	DryRun bool `json:"dry_run,omitempty"`
	// State is what the model saw.
	State map[string]any `json:"state,omitempty"`
	// Summary is the one-line reason fragment ("score 2/3 (conf 0.81, jev 190 ms) → opus").
	Summary string `json:"summary"`
}

func (r Rule) evaluateMode() string {
	if r.Evaluate == "" {
		return EvalConversationStart
	}
	return r.Evaluate
}

func (r Rule) inputs() (facts []string, goalTurns int, headers []string) {
	facts, goalTurns = DefaultInputs, defaultGoalTurns
	if r.Inputs != nil {
		if len(r.Inputs.Facts) > 0 {
			facts = r.Inputs.Facts
		}
		if r.Inputs.GoalTurns > 0 {
			goalTurns = r.Inputs.GoalTurns
		}
		headers = r.Inputs.Headers
	}
	return facts, goalTurns, headers
}

// decisionState builds the state the model sees from the rule's inputs.
func decisionState(rule Rule, req *pipeline.Request, f *Facts) (map[string]any, error) {
	facts, turns, headers := rule.inputs()
	state := map[string]any{}
	protocol := req.ProtocolOf()
	var goalErr error
	goal := func(n int) (string, string) {
		g, recent, err := prune.ConversationGoal(protocol, req.Body, n)
		if err != nil {
			goalErr = err
		}
		return g, recent
	}
	text := false
	for _, in := range facts {
		switch in {
		case InputLatestUserText:
			g, _ := goal(1)
			if g == "" {
				g = f.Prompt
			}
			if g = strings.TrimSpace(g); g != "" {
				state["user_message"] = clip(g, maxPromptChars)
				text = true
			}
		case InputGoal:
			if g, _ := goal(turns); strings.TrimSpace(g) != "" {
				state["conversation_goal"] = clip(g, 2*maxPromptChars)
				text = true
			}
		case InputRecentToolCalls:
			if _, recent := goal(1); recent != "" {
				state["recent_tool_calls"] = clip(recent, maxPromptChars)
			}
		case InputContextTokens:
			state["context_tokens"] = f.ContextTokens
		case InputHasTools:
			state["has_tools"] = f.Tools > 0
		case InputHasImages:
			state["has_images"] = f.HasImages
		case InputClient:
			c := clients.Detect(req.Headers)
			kind := c.Kind
			if kind == "" || kind == "unknown" {
				if req.ClientKind != "" {
					kind = req.ClientKind
				}
			}
			state["client"] = map[string]string{"id": c.ID, "kind": kind}
		case InputProtocol:
			state["protocol"] = protocol
		case InputModelRequested:
			state["model_requested"] = f.Model
		}
	}
	if len(headers) > 0 {
		hs := map[string]string{}
		for _, h := range headers {
			if v, ok := headerValue(req.Headers, h); ok && !sensitiveHeaders[strings.ToLower(h)] {
				hs[strings.ToLower(h)] = clip(v, maxHeaderInputChars)
			}
		}
		if len(hs) > 0 {
			state["headers"] = hs
		}
	}
	needsText := false
	for _, in := range facts {
		if in == InputLatestUserText || in == InputGoal {
			needsText = true
		}
	}
	if needsText && !text {
		if goalErr != nil {
			return state, fmt.Errorf("could not read the user's text: %v", goalErr)
		}
		return state, errors.New("no user text to judge")
	}
	return state, nil
}

func (q DecisionQuestion) selectorQuestion() selector.Question {
	sq := selector.Question{Type: q.Type, Instructions: q.Instructions}
	if q.Criteria != nil {
		if q.Type == QuestionScore {
			sq.Criteria = q.Criteria.Legend
		} else if q.Type == QuestionChoice {
			sq.Criteria = q.Criteria.Choices
		}
	}
	return sq
}

// decide asks the rule's question and maps the answer to a target. It
// never fails: a failure is recorded in the trace and resolved by else.
func (r *Router) decide(ctx context.Context, cfg config.Config, s Settings, rule Rule, req *pipeline.Request, f *Facts, dry bool) *DecisionTrace {
	q := rule.Question
	dt := &DecisionTrace{Branch: -1, Backend: rule.Backend, MinConfidence: rule.MinConfidence, DryRun: dry}
	if dt.Backend == "" {
		dt.Backend = decisions.Economy
	}
	if q != nil {
		dt.QuestionType = q.Type
	}
	dt.Error = r.ask(ctx, cfg, rule, req, f, dt)
	if dt.Error == "" {
		matchBranch(rule, dt)
	}
	if dt.Error != "" || dt.Branch < 0 {
		if rule.Else == nil || rule.Else.NextRule || rule.Else.empty() {
			dt.Outcome = OutcomeFallThrough
		} else {
			t := *rule.Else
			dt.Outcome, dt.Target = OutcomeElse, &t
		}
	} else {
		dt.Outcome = OutcomeBranch
		t := rule.Branches[dt.Branch].Then
		dt.Target = &t
		dt.BranchLabel = rule.Branches[dt.Branch].Label
	}
	if dt.Target != nil {
		route, model, err := resolveTarget(cfg, s, *dt.Target, f.Protocol)
		if err != nil {
			// A broken target is a failure too; a broken else falls through.
			if dt.Error == "" {
				dt.Error = err.Error()
			} else {
				dt.Error += "; " + err.Error()
			}
			if dt.Outcome == OutcomeBranch && rule.Else != nil && !rule.Else.NextRule && !rule.Else.empty() {
				t := *rule.Else
				dt.Outcome, dt.Target = OutcomeElse, &t
				route, model, err = resolveTarget(cfg, s, t, f.Protocol)
			}
			if err != nil {
				dt.Outcome, dt.Target = OutcomeFallThrough, nil
			}
		}
		if dt.Target != nil {
			dt.Route, dt.UpstreamModel = route, model
		}
	}
	dt.Summary = dt.summarize()
	return dt
}

// ask calls the backend and fills the answer; it returns why the answer
// cannot be used ("" when it can).
func (r *Router) ask(ctx context.Context, cfg config.Config, rule Rule, req *pipeline.Request, f *Facts, dt *DecisionTrace) string {
	q := rule.Question
	switch {
	case q == nil:
		return "the rule has no question"
	case q.Type == QuestionChoice && (q.Criteria == nil || len(q.Criteria.Choices) == 0),
		q.Type == QuestionScore && (q.Criteria == nil || len(q.Criteria.Legend) == 0):
		return "the question has no criteria"
	}
	state, err := decisionState(rule, req, f)
	dt.State = state
	if err != nil {
		return err.Error()
	}
	sel, provider, err := decisions.SelectorFor(cfg, rule.Backend, rule.BackendModel)
	dt.Provider, dt.Model = provider, sel.Model
	if err != nil {
		return err.Error()
	}
	timeout := time.Duration(rule.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultDecisionTimeout * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	source := "router"
	if dt.DryRun {
		source = "router-dryrun"
	}
	start := time.Now()
	name := rule.Backend
	if name == decisions.Economy {
		name = ""
	}
	res, err := selector.NewNamed(name, sel).Ask(selector.WithSource(ctx, source), state,
		map[string]selector.Question{questionKey: rule.Question.selectorQuestion()})
	dt.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return fmt.Sprintf("timed out after %s", timeout)
		}
		return err.Error()
	}
	dt.ForwardMs = res.ForwardMs
	ans, ok := res.Answers[questionKey]
	if !ok {
		return "the backend returned no answer"
	}
	dt.Probabilities = ans.Probabilities
	switch q.Type {
	case QuestionChoice:
		if ans.Choice == "" {
			return "the backend returned no choice"
		}
		dt.Choice, dt.Answer, dt.Confidence = ans.Choice, ans.Choice, ans.Confidence
		if _, known := q.Criteria.Choices[ans.Choice]; !known {
			return fmt.Sprintf("the backend chose unknown label %q", ans.Choice)
		}
	case QuestionScore:
		if ans.Score == nil {
			return "the backend returned no score"
		}
		n := len(q.Criteria.Legend)
		i := int(math.Round(*ans.Score))
		i = max(0, min(n-1, i))
		dt.Score, dt.Index, dt.Answer, dt.Label, dt.MaxIndex = ans.Score, &i, strconv.Itoa(i), q.Criteria.Legend[i], n-1
		dt.Confidence = ans.Confidence
		if dt.Confidence == nil {
			if p, ok := ans.Probabilities[strconv.Itoa(i)]; ok {
				dt.Confidence = &p
			}
		}
	case QuestionNoul:
		if ans.Noul == nil {
			return "the backend returned no noul"
		}
		p := *ans.Noul
		c := math.Max(p, 1-p)
		dt.Noul, dt.Confidence, dt.Answer = &p, &c, strconv.FormatFloat(p, 'f', 2, 64)
	}
	if rule.MinConfidence > 0 {
		if dt.Confidence == nil {
			return fmt.Sprintf("the backend gave no confidence (min_confidence %.2f)", rule.MinConfidence)
		}
		if *dt.Confidence < rule.MinConfidence {
			return fmt.Sprintf("confidence %.2f < min_confidence %.2f", *dt.Confidence, rule.MinConfidence)
		}
	}
	return ""
}

// matchBranch sets dt.Branch to the first branch the answer matches.
func matchBranch(rule Rule, dt *DecisionTrace) {
	for i, b := range rule.Branches {
		if branchMatches(rule.Question.Type, b.When, dt) {
			dt.Branch = i
			return
		}
	}
}

func branchMatches(typ string, w BranchWhen, dt *DecisionTrace) bool {
	switch typ {
	case QuestionChoice:
		if w.Equals != "" {
			return dt.Choice == w.Equals
		}
		for _, l := range w.In {
			if dt.Choice == l {
				return true
			}
		}
	case QuestionScore:
		if dt.Index == nil {
			return false
		}
		v := float64(*dt.Index)
		switch w.Op {
		case ">=":
			return w.Value != nil && v >= *w.Value
		case "<=":
			return w.Value != nil && v <= *w.Value
		case "==":
			return w.Value != nil && v == *w.Value
		case "between":
			return w.Min != nil && w.Max != nil && v >= *w.Min && v <= *w.Max
		}
	case QuestionNoul:
		if dt.Noul == nil || w.Value == nil {
			return false
		}
		switch w.Op {
		case ">=":
			return *dt.Noul >= *w.Value
		case "<":
			return *dt.Noul < *w.Value
		}
	}
	return false
}

// resolveTarget turns a target into a route and an upstream model that can
// serve protocol.
func resolveTarget(cfg config.Config, s Settings, t Target, protocol string) (route, model string, err error) {
	route, model = t.Route, t.Model
	if t.Alias != "" {
		a, ok := s.alias(t.Alias)
		if !ok {
			return "", "", fmt.Errorf("alias %q does not exist", t.Alias)
		}
		route, model = a.Route, a.Model
	}
	rt, ok := cfg.Routes[route]
	if !ok {
		return "", "", fmt.Errorf("route %q does not exist", route)
	}
	if !rt.Speaks(protocol) {
		return "", "", fmt.Errorf("route %q speaks %s, not %s (no translation between protocols)",
			route, strings.Join(rt.Protocols(), " / "), protocol)
	}
	return route, model, nil
}

// summarize is the reason fragment: "score 2/3 (conf 0.81, jev 190 ms) → opus".
func (dt *DecisionTrace) summarize() string {
	var ans string
	switch {
	case dt.QuestionType == QuestionChoice && dt.Choice != "":
		ans = fmt.Sprintf("choice '%s'", dt.Choice)
	case dt.QuestionType == QuestionScore && dt.Index != nil:
		ans = fmt.Sprintf("score %d/%d", *dt.Index, dt.MaxIndex)
		if dt.Label != "" {
			ans += fmt.Sprintf(" '%s'", firstWords(dt.Label))
		}
	case dt.QuestionType == QuestionNoul && dt.Noul != nil:
		ans = fmt.Sprintf("noul %.2f", *dt.Noul)
	}
	meta := []string{}
	if dt.Confidence != nil {
		meta = append(meta, fmt.Sprintf("conf %.2f", *dt.Confidence))
	}
	meta = append(meta, fmt.Sprintf("%s %d ms", dt.Backend, dt.LatencyMs))
	out := ""
	if ans != "" {
		out = ans + " (" + strings.Join(meta, ", ") + ")"
	}
	if dt.Error != "" {
		if out != "" {
			out += ": "
		}
		out += dt.Error
		if ans == "" {
			out += fmt.Sprintf(" (%s %d ms)", dt.Backend, dt.LatencyMs)
		}
	} else if dt.Outcome != OutcomeBranch && ans != "" {
		out += ": no branch matched"
	}
	if dt.DryRun {
		out = "[dry run] " + out
	}
	switch dt.Outcome {
	case OutcomeBranch:
		out += " → " + dt.Target.String()
	case OutcomeElse:
		out += " → else " + dt.Target.String()
	default:
		out += " → next rule"
	}
	return out
}

// firstWords shortens a legend entry ("hard: deep multi-step reasoning")
// to its head ("hard").
func firstWords(s string) string {
	if i := strings.IndexAny(s, ":—-("); i > 0 {
		s = s[:i]
	}
	return clip(strings.TrimSpace(s), 30)
}

// --- validation ---

func validateDecision(label string, r Rule, s Settings, cfg config.Config) error {
	if r.OverrideSticky {
		return fmt.Errorf("%s: a decision rule sets when it runs with evaluate, not override_sticky", label)
	}
	q := r.Question
	if q == nil {
		return fmt.Errorf("%s: a decision rule needs a question", label)
	}
	if strings.TrimSpace(q.Instructions) == "" {
		return fmt.Errorf("%s: the question needs instructions", label)
	}
	switch q.Type {
	case QuestionChoice:
		if q.Criteria == nil || len(q.Criteria.Choices) < 2 || q.Criteria.Legend != nil {
			return fmt.Errorf("%s: a choice question needs criteria: an object of at least two labels and their descriptions", label)
		}
		for k := range q.Criteria.Choices {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("%s: a choice label cannot be empty", label)
			}
		}
	case QuestionScore:
		if q.Criteria == nil || len(q.Criteria.Legend) < 2 || q.Criteria.Choices != nil {
			return fmt.Errorf("%s: a score question needs criteria: an ordered array of at least two legend entries", label)
		}
	case QuestionNoul:
		if q.Criteria != nil && (q.Criteria.Choices != nil || q.Criteria.Legend != nil) {
			return fmt.Errorf("%s: a noul question takes no criteria", label)
		}
	default:
		return fmt.Errorf("%s: question type must be %q, %q or %q", label, QuestionChoice, QuestionScore, QuestionNoul)
	}
	if in := r.Inputs; in != nil {
		for _, f := range in.Facts {
			if !contains(knownInputs, f) {
				return fmt.Errorf("%s: unknown input %q (known: %s)", label, f, strings.Join(knownInputs, ", "))
			}
		}
		if in.GoalTurns < 0 || in.GoalTurns > maxGoalTurns {
			return fmt.Errorf("%s: goal_turns must be between 0 and %d", label, maxGoalTurns)
		}
		for _, h := range in.Headers {
			if strings.TrimSpace(h) == "" {
				return fmt.Errorf("%s: an input header needs a name", label)
			}
			if sensitiveHeaders[strings.ToLower(strings.TrimSpace(h))] {
				return fmt.Errorf("%s: header %q carries credentials and is never sent to a decision model", label, h)
			}
		}
	}
	if err := decisions.CheckBackend(cfg, r.Backend); err != nil {
		return fmt.Errorf("%s: %v", label, err)
	}
	if r.TimeoutMs < 0 || r.TimeoutMs > maxAutoTimeout {
		return fmt.Errorf("%s: timeout_ms must be between 0 and %d", label, maxAutoTimeout)
	}
	if r.MinConfidence < 0 || r.MinConfidence > 1 {
		return fmt.Errorf("%s: min_confidence must be between 0 and 1", label)
	}
	switch r.Evaluate {
	case "", EvalConversationStart, EvalEveryRequest:
		if r.ContextOverTokens != 0 {
			return fmt.Errorf("%s: context_over_tokens only applies to evaluate %q", label, EvalWhenContextOver)
		}
	case EvalWhenContextOver:
		if r.ContextOverTokens <= 0 {
			return fmt.Errorf("%s: evaluate %q needs context_over_tokens > 0", label, EvalWhenContextOver)
		}
	default:
		return fmt.Errorf("%s: evaluate must be %q, %q or %q", label, EvalConversationStart, EvalEveryRequest, EvalWhenContextOver)
	}
	if len(r.Branches) == 0 {
		return fmt.Errorf("%s: a decision rule needs at least one branch", label)
	}
	type dest struct {
		where string
		route string
		rt    config.Route
	}
	var dests []dest
	checkTarget := func(where string, t Target, isElse bool) error {
		if isElse && (t.NextRule || t.empty()) {
			if t.NextRule && (!t.empty() || t.Model != "") {
				return fmt.Errorf("%s: else is either next_rule or a target, not both", label)
			}
			if t.Model != "" {
				return fmt.Errorf("%s: else has a model but no route", label)
			}
			return nil
		}
		if t.NextRule {
			return fmt.Errorf("%s: %s: next_rule is only valid as the else", label, where)
		}
		switch {
		case t.Route != "" && t.Alias != "":
			return fmt.Errorf("%s: %s: a target is a route or an alias, not both", label, where)
		case t.Alias != "" && t.Model != "":
			return fmt.Errorf("%s: %s: an alias already names its model", label, where)
		case t.empty():
			return fmt.Errorf("%s: %s needs a route or an alias", label, where)
		}
		route := t.Route
		if t.Alias != "" {
			a, ok := s.alias(t.Alias)
			if !ok {
				return fmt.Errorf("%s: %s: unknown alias %q", label, where, t.Alias)
			}
			route = a.Route
		}
		rt, ok := cfg.Routes[route]
		if !ok {
			return fmt.Errorf("%s: %s: unknown route %q", label, where, route)
		}
		if p := r.When.Protocol; p != "" && !speaksAny(rt, p) {
			return fmt.Errorf("%s: %s sends to route %q, which speaks %s, but the rule only matches %s requests. "+
				"The gateway does not translate between protocols", label, where, route, strings.Join(rt.Protocols(), " / "), p)
		}
		dests = append(dests, dest{where: where, route: route, rt: rt})
		return nil
	}
	labels := map[string]bool{}
	if q.Criteria != nil {
		for k := range q.Criteria.Choices {
			labels[k] = true
		}
	}
	for i, b := range r.Branches {
		where := fmt.Sprintf("branch %d", i+1)
		if b.Label != "" {
			where = fmt.Sprintf("branch %d (%s)", i+1, b.Label)
		}
		if err := validateWhen(q, b.When); err != nil {
			return fmt.Errorf("%s: %s: %v", label, where, err)
		}
		if err := checkTarget(where, b.Then, false); err != nil {
			return err
		}
	}
	if r.Else != nil {
		if err := checkTarget("else", *r.Else, true); err != nil {
			return err
		}
	}
	// Every target must serve the same request formats, or some requests
	// the rule decides on could only go to a route that cannot take them.
	for _, d := range dests[1:] {
		first := dests[0]
		if !sameProtocols(first.rt, d.rt) {
			return fmt.Errorf("%s: %s sends to route %q, which speaks %s, but %s sends to route %q, which speaks %s. "+
				"An Anthropic-format request can't go to an OpenAI-format route (the gateway does not translate between "+
				"protocols): use targets of one format, or one rule per format with when.protocol",
				label, d.where, d.route, strings.Join(d.rt.Protocols(), " / "), first.where, first.route,
				strings.Join(first.rt.Protocols(), " / "))
		}
	}
	return nil
}

func sameProtocols(a, b config.Route) bool {
	pa, pb := a.Protocols(), b.Protocols()
	sort.Strings(pa)
	sort.Strings(pb)
	return strings.Join(pa, ",") == strings.Join(pb, ",")
}

func validateWhen(q *DecisionQuestion, w BranchWhen) error {
	switch q.Type {
	case QuestionChoice:
		if w.Op != "" || w.Value != nil || w.Min != nil || w.Max != nil {
			return errors.New("a choice branch uses equals or in")
		}
		if (w.Equals == "") == (len(w.In) == 0) {
			return errors.New("a choice branch needs equals (one label) or in (a set of labels)")
		}
		for _, l := range append([]string{w.Equals}, w.In...) {
			if _, ok := q.Criteria.Choices[l]; l != "" && !ok {
				return fmt.Errorf("label %q is not one of the question's criteria", l)
			}
		}
	case QuestionScore:
		if w.Equals != "" || len(w.In) > 0 {
			return errors.New("a score branch uses op and value (or min and max)")
		}
		n := float64(len(q.Criteria.Legend) - 1)
		inRange := func(v *float64) bool { return v != nil && *v >= 0 && *v <= n && *v == math.Trunc(*v) }
		switch w.Op {
		case ">=", "<=", "==":
			if !inRange(w.Value) {
				return fmt.Errorf("value must be a legend index from 0 to %d", int(n))
			}
		case "between":
			if !inRange(w.Min) || !inRange(w.Max) || *w.Min > *w.Max {
				return fmt.Errorf("between needs min ≤ max, legend indexes from 0 to %d", int(n))
			}
		default:
			return errors.New(`a score branch's op must be ">=", "<=", "==" or "between"`)
		}
	case QuestionNoul:
		if w.Equals != "" || len(w.In) > 0 || w.Min != nil || w.Max != nil {
			return errors.New("a noul branch uses op and value")
		}
		if w.Op != ">=" && w.Op != "<" {
			return errors.New(`a noul branch's op must be ">=" or "<"`)
		}
		if w.Value == nil || *w.Value < 0 || *w.Value > 1 {
			return errors.New("a noul branch's value must be a probability between 0 and 1")
		}
	}
	return nil
}

// decisionTargets lists the routes a decision rule can send to.
func decisionTargets(r Rule, s Settings) []string {
	var out []string
	add := func(t Target) {
		if t.Route != "" {
			out = append(out, t.Route)
		}
	}
	for _, b := range r.Branches {
		add(b.Then)
	}
	if r.Else != nil {
		add(*r.Else)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// decisionSpeaks reports whether some target of the rule can serve a
// request in protocol: when none can, asking the question is pointless.
func decisionSpeaks(cfg config.Config, s Settings, r Rule, protocol string) bool {
	check := func(t Target) bool {
		route := t.Route
		if t.Alias != "" {
			a, ok := s.alias(t.Alias)
			if !ok {
				return false
			}
			route = a.Route
		}
		rt, ok := cfg.Routes[route]
		return ok && rt.Speaks(protocol)
	}
	for _, b := range r.Branches {
		if check(b.Then) {
			return true
		}
	}
	if r.Else != nil && !r.Else.empty() && check(*r.Else) {
		return true
	}
	return false
}

// protocolName names a request format ("" is Anthropic).
func protocolName(p string) string {
	if p == "" {
		return ir.ProtocolAnthropic
	}
	return p
}
