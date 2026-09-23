package prune

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
)

// Decision reasons.
const (
	ReasonProtected  = "protected"
	ReasonSticky     = "sticky"
	ReasonSelector   = "selector"
	ReasonPending    = "pending"   // new since the last epoch, decided at the next one
	ReasonFailOpen   = "fail_open" // the selector failed this epoch: kept
	ReasonNoAnswer   = "no_answer"
	ReasonSuperseded = "drop_superseded_reads"
	// ReasonRecalled: the model recalled this block, so it is never dropped
	// again. A drop already sent stays as its marker (restoring it would
	// rewrite the cached prefix, and the recall result already holds the
	// content).
	ReasonRecalled = "recalled"
	// ReasonEmergency: dropped by an emergency pass after the context
	// overflowed the model's window (see EmergencyPrune).
	ReasonEmergency = "emergency"
)

// askFunc is one batched selector call: noul keep-probability per question id.
type askFunc func(ctx context.Context, state map[string]any, questions map[string]selector.Question) (map[string]float64, error)

type planInput struct {
	reqID  string
	eff    Effective
	st     *convState
	thread *Thread
	items  []*item
	tokens int // estimated context tokens as the client sent it
	ask    askFunc
	goal   string
	recent string
	// emergency ignores epoch gating and re-decides earlier keeps against
	// the (raised) threshold: the context no longer fits the window.
	emergency bool
}

type planResult struct {
	epoch       bool
	candidates  int // blocks the selector or a deterministic rule looked at this epoch
	selectorMs  int64
	selectorErr string
	nextEpochAt int
	changed     bool // the conversation state needs saving
}

// plan decides every item. Order of precedence:
//
//  1. structural protection (latest turns, thinking, tool_use, recall results, ...)
//  2. a stored drop: stays dropped with the identical marker, forever
//     2b. a block the model recalled: kept, and recorded as kept for good
//  3. keep flags (errors, edits, user text, small blocks)
//  4. a stored keep from an earlier epoch: not re-litigated
//  5. anything else is new since the last epoch: decided only when an epoch
//     runs (superseded reads first, then the selector), else kept as pending.
//
// New drops therefore only land on blocks that appeared after the previous
// epoch, so the prefix up to there stays byte-identical and cached.
func plan(ctx context.Context, in planInput) planResult {
	var res planResult
	eff, st := in.eff, in.st
	var pending, rescored []*item
	for _, it := range in.items {
		it.After = it.Tokens
		d := st.Decisions[it.ID]
		switch {
		case !it.Candidate || it.Protected != "":
			it.Decision, it.Reason = "keep", ReasonProtected
		case d != nil && d.Decision == "drop":
			it.Decision, it.Reason, it.Score = "drop", ReasonSticky, d.Score
			it.Marker, it.FirstReq, it.MarkerKey = d.Marker, d.FirstReq, d.Key
			it.Emergency = d.Emergency
		case it.Recalled:
			it.Decision, it.Reason = "keep", ReasonRecalled
			if d == nil || d.Reason != ReasonRecalled {
				st.Decisions[it.ID] = &Decision{Decision: "keep", Reason: ReasonRecalled, Key: it.MarkerKey,
					Tokens: it.Tokens, At: time.Now()}
				res.changed = true
			}
		case eff.KeepErrors && it.IsError:
			it.Decision, it.Reason = "keep", "keep_errors"
		case eff.KeepEdits && it.Kind == ir.KindToolResult && isEditTool(eff, it.ToolName):
			it.Decision, it.Reason = "keep", "keep_edits"
		case eff.AlwaysKeepUserText && it.Kind == ir.KindText && it.Role == "user":
			it.Decision, it.Reason = "keep", "always_keep_user_text"
		case it.Tokens < eff.MinBlockTokens:
			it.Decision, it.Reason = "keep", "min_block_tokens"
		case d != nil:
			it.Decision, it.Reason, it.Score = "keep", ReasonSticky, d.Score
			if in.emergency && d.Reason == ReasonSelector && d.Score != nil && *d.Score < eff.KeepThreshold {
				// Kept at the configured threshold, below the emergency one.
				rescored = append(rescored, it)
			}
		default:
			it.Decision, it.Reason = "keep", ReasonPending
			pending = append(pending, it)
		}
	}

	// Epoch gating. After the agent compacts, the context shrinks below the
	// last epoch's size; growth is then measured from the new, smaller size.
	th := in.thread
	if th.Epochs > 0 && in.tokens < th.LastEpochTokens {
		th.LastEpochTokens = in.tokens
		res.changed = true
	}
	res.nextEpochAt = eff.FloorTokens
	if th.Epochs > 0 && th.LastEpochTokens+eff.EpochTokens > res.nextEpochAt {
		res.nextEpochAt = th.LastEpochTokens + eff.EpochTokens
	}
	res.epoch = in.tokens >= res.nextEpochAt && len(pending) > 0
	if in.emergency {
		res.epoch = len(pending) > 0 || len(rescored) > 0
	}
	if !res.epoch {
		for _, it := range in.items {
			finish(it)
		}
		return res
	}
	res.changed = true
	epochNo := th.Epochs + 1
	th.Epochs, th.LastEpochTokens, th.LastEpochAt = epochNo, in.tokens, time.Now()
	res.candidates = len(pending) + len(rescored)
	res.nextEpochAt = in.tokens + eff.EpochTokens
	for _, it := range rescored {
		it.Decision, it.Reason = "drop", ReasonEmergency
	}

	// Deterministic, free and unambiguous: a file read again later.
	var ask []*item
	for _, it := range pending {
		if eff.DropSupersededReads && supersededRead(eff, in.items, it) {
			it.Decision, it.Reason = "drop", ReasonSuperseded
			continue
		}
		ask = append(ask, it)
	}

	if len(ask) > 0 {
		qs := make(map[string]selector.Question, len(ask))
		for _, it := range ask {
			qs[it.MarkerKey] = selector.Question{Type: "noul", Instructions: eff.Criteria + "\n\n" + describe(it)}
		}
		state := map[string]any{"goal": in.goal, "recent_activity": in.recent}
		cctx, cancel := context.WithTimeout(ctx, time.Duration(eff.SelectorTimeoutMs)*time.Millisecond)
		start := time.Now()
		scores, err := in.ask(cctx, state, qs)
		cancel()
		res.selectorMs = time.Since(start).Milliseconds()
		if err != nil {
			// Fail open: keep everything new this epoch, including the
			// deterministic drops, and record nothing, so the blocks are
			// decided again at the next epoch. An emergency pass still
			// applies what it could decide from stored scores.
			res.selectorErr = err.Error()
			for _, it := range pending {
				it.Decision, it.Reason = "keep", ReasonFailOpen
			}
			if in.emergency {
				storeDecisions(in, rescored, epochNo)
			}
			for _, it := range in.items {
				finish(it)
			}
			return res
		}
		for _, it := range ask {
			s, ok := scores[it.MarkerKey]
			if !ok {
				it.Reason = ReasonNoAnswer
				continue
			}
			it.Score = &s
			it.Reason = ReasonSelector
			if s < eff.KeepThreshold {
				it.Decision = "drop"
			}
		}
	}

	storeDecisions(in, append(pending, rescored...), epochNo)
	for _, it := range in.items {
		finish(it)
	}
	return res
}

// storeDecisions records the decisions taken this epoch. A drop gets its marker
// now, naming this request, and keeps it forever.
func storeDecisions(in planInput, items []*item, epochNo int) {
	now := time.Now()
	for _, it := range items {
		if it.Reason == ReasonNoAnswer {
			continue
		}
		d := &Decision{Decision: it.Decision, Reason: it.Reason, Score: it.Score, Key: it.MarkerKey,
			Tokens: it.Tokens, Epoch: epochNo, At: now}
		if it.Decision == "drop" {
			it.New, it.FirstReq = true, in.reqID
			it.Marker = pipeline.Marker(in.reqID, it.MarkerKey, it.Tokens)
			d.FirstReq, d.Marker = in.reqID, it.Marker
			if in.emergency {
				it.Emergency, d.Emergency = true, true
			}
		}
		in.st.Decisions[it.ID] = d
	}
}

// finish fills the pruned size of an item.
func finish(it *item) {
	if it.Decision != "drop" {
		it.After = it.Tokens
		return
	}
	if it.Kind == ir.KindTool {
		it.After = 0 // removed from the tools list
		return
	}
	it.After = ir.EstimateTokens(len(it.Marker))
}

func isEditTool(eff Effective, name string) bool {
	for _, t := range eff.EditTools {
		if t == name {
			return true
		}
	}
	return false
}

func isReadTool(eff Effective, name string) bool {
	for _, t := range eff.ReadTools {
		if t == name {
			return true
		}
	}
	return false
}

// readTarget returns the path a read/write tool call touches and whether it
// covers the whole file (no offset/limit/range).
func readTarget(in map[string]any) (path string, whole bool, rng string) {
	for _, k := range []string{"file_path", "path", "filePath", "notebook_path"} {
		if s, _ := in[k].(string); s != "" {
			path = s
			break
		}
	}
	var parts []string
	for _, k := range []string{"offset", "limit", "view_range", "pages", "start_line", "end_line"} {
		if v, ok := in[k]; ok && v != nil {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return path, len(parts) == 0, strings.Join(parts, ",")
}

// supersededRead: it is the result of a read of a file that a later call
// reads again (the whole file, or the same range) or overwrites with Write.
// The later call carries the newer content, so this copy is stale.
func supersededRead(eff Effective, items []*item, it *item) bool {
	if it.Kind != ir.KindToolResult || it.IsError || !isReadTool(eff, it.ToolName) {
		return false
	}
	path, _, rng := readTarget(it.ToolInput)
	if path == "" {
		return false
	}
	for _, o := range items {
		if o.Kind != ir.KindToolResult || o.Msg <= it.Msg || o.IsError {
			continue
		}
		p, whole, r := readTarget(o.ToolInput)
		if p != path {
			continue
		}
		if isReadTool(eff, o.ToolName) && (whole || r == rng) {
			return true
		}
		if o.ToolName == "Write" {
			return true
		}
	}
	return false
}

// describe is one selector question's block: what it is and a head+tail
// preview. The selector never sees (or writes) more than this.
func describe(it *item) string {
	return question(describeWhat(it), it.Tokens, selectorPreview(it.Text))
}

func question(what string, tokens int, preview string) string {
	return fmt.Sprintf("Block (%s, ~%d tokens):\n%s", what, tokens, preview)
}

func selectorPreview(s string) string { return headTail(s, 700, 300) }

func describeWhat(it *item) string {
	var what string
	switch it.Kind {
	case ir.KindToolResult:
		what = "tool_result of " + callSummary(it.ToolName, it.ToolInput)
		if it.IsError {
			what += " (error)"
		}
	case ir.KindText:
		what = it.Role + " text"
	case ir.KindSystem:
		what = "system prompt section"
	case ir.KindTool:
		what = "tool definition " + it.Name + " (the agent could no longer call it)"
	default:
		what = it.Kind
	}
	return what
}

func callSummary(name string, in map[string]any) string {
	if name == "" {
		return "an unknown tool"
	}
	for _, k := range []string{"file_path", "path", "command", "pattern", "url", "query", "description", "prompt"} {
		if s, _ := in[k].(string); s != "" {
			return name + " " + clip(s, 100)
		}
	}
	return name
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// goalAndRecent builds the selector state: the goal is the latest human text
// (Claude Code's <system-reminder> blocks are not the human), and the recent
// activity is the last few tool calls.
func goalAndRecent(d *doc, turns int) (goal, recent string) {
	if turns < 1 {
		turns = defaultGoalTurns
	}
	var goals []string
	for i := len(d.msgs) - 1; i >= 0 && len(goals) < turns; i-- {
		if d.messageRole(i) != "user" {
			continue
		}
		m, _ := d.msgs[i].(map[string]any)
		var texts []string
		switch c := m["content"].(type) {
		case string:
			texts = append(texts, c)
		case []any:
			for _, p := range c {
				b, _ := p.(map[string]any)
				if t, _ := b["type"].(string); t == "text" {
					s, _ := b["text"].(string)
					texts = append(texts, s)
				}
			}
		}
		for j := len(texts) - 1; j >= 0 && len(goals) < turns; j-- {
			t := strings.TrimSpace(texts[j])
			if t == "" || strings.HasPrefix(t, "<system-reminder>") || strings.HasPrefix(t, "<command-") {
				continue
			}
			goals = append(goals, headTail(t, 1200, 300))
		}
	}
	// Oldest first reads naturally: the earlier request, then the follow-up.
	for i, j := 0, len(goals)-1; i < j; i, j = i+1, j-1 {
		goals[i], goals[j] = goals[j], goals[i]
	}
	goal = strings.Join(goals, "\n---\n")

	var calls []string
	for i := len(d.msgs) - 1; i >= 0 && len(calls) < 8; i-- {
		m, _ := d.msgs[i].(map[string]any)
		parts, _ := m["content"].([]any)
		for j := len(parts) - 1; j >= 0 && len(calls) < 8; j-- {
			b, _ := parts[j].(map[string]any)
			if t, _ := b["type"].(string); t == "tool_use" {
				name, _ := b["name"].(string)
				in, _ := b["input"].(map[string]any)
				calls = append(calls, callSummary(name, in))
			}
		}
	}
	for i, j := 0, len(calls)-1; i < j; i, j = i+1, j-1 {
		calls[i], calls[j] = calls[j], calls[i]
	}
	recent = strings.Join(calls, "; ")
	return goal, recent
}

// costEstimate prices this turn's input with and without pruning.
//
// Anthropic bills cached prefix reads at ~0.1x and cache writes at 1.25x of
// input. In steady state everything but the newest message was cached by
// the previous turn. A change at position p (a new drop, or the first turn
// that enforces) invalidates the cache from p on, so the rest of the prompt
// is written again: that is what makes an epoch cost more on its own turn.
type costEstimate struct {
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	Priced string  `json:"priced_as"`
	Cached bool    `json:"cached"`
	// Automatic is true for providers that cache prefixes on their own.
	Automatic   bool `json:"automatic_cache,omitempty"`
	InvalidFrom int  `json:"invalid_from_tokens"` // -1 when the prefix stays intact
}

// renderOrder is how the provider lays out the prompt for caching:
// tools, then system, then messages.
func renderOrder(items []*item) []*item {
	out := make([]*item, 0, len(items))
	for _, k := range []string{ir.KindTool, ir.KindSystem} {
		for _, it := range items {
			if it.Kind == k {
				out = append(out, it)
			}
		}
	}
	for _, it := range items {
		if it.Msg >= 0 {
			out = append(out, it)
		}
	}
	return out
}

// On OpenAI-format requests the cache is automatic: a token that is not
// read from the cache costs plain input (no write premium), which
// pricing.ForProtocol encodes as CacheWrite = Input.
func estimateCost(eff Effective, model, protocol string, items []*item, cached bool, changed map[string]bool, lastMsg int) costEstimate {
	p, key := pricing.ForProtocol(eff.Prices, model, protocol)
	c := costEstimate{Priced: key, Cached: cached, Automatic: pricing.AutomaticCache(protocol), InvalidFrom: -1}
	var before, after, lastB, lastA int
	pos := 0
	for _, it := range renderOrder(items) {
		if c.InvalidFrom < 0 && changed[it.ID] {
			c.InvalidFrom = pos
		}
		pos += it.After
		before += it.Tokens
		after += it.After
		if it.Msg == lastMsg && lastMsg >= 0 {
			lastB += it.Tokens
			lastA += it.After
		}
	}
	perTok := func(usd float64) float64 { return usd / 1e6 }
	if !cached {
		c.Before = float64(before) * perTok(p.Input)
		c.After = float64(after) * perTok(p.Input)
		return c
	}
	c.Before = float64(before-lastB)*perTok(p.CacheRead) + float64(lastB)*perTok(p.CacheWrite)
	read := after - lastA
	if c.InvalidFrom >= 0 && c.InvalidFrom < read {
		read = c.InvalidFrom
	}
	c.After = float64(read)*perTok(p.CacheRead) + float64(after-read)*perTok(p.CacheWrite)
	return c
}

// appliedIDs lists the dropped ids, sorted, to compare with the last turn.
func appliedIDs(items []*item) []string {
	var ids []string
	seen := map[string]bool{}
	for _, it := range items {
		if it.Decision == "drop" && !seen[it.ID] {
			seen[it.ID] = true
			ids = append(ids, it.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
