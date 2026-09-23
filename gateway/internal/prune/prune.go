// Package prune decides which blocks of the context the model actually needs
// and rebuilds the request without the rest (F1).
//
// The economy model (System One, via internal/selector) only answers "should
// this block stay?" per block key; it never writes text. A dropped block's
// content is replaced by pipeline.Marker, which names the request whose
// logged body still holds the original, so the recall tool can restore it.
//
// Prompt caching is the central constraint. Anthropic bills cached prefix
// reads at ~0.1x input and writes at 1.25x, and any byte change invalidates
// everything after it, so a pruner that changes the prefix every turn costs
// more than it saves. Hence:
//
//   - decisions are sticky per conversation: a dropped block is replaced by
//     the same marker bytes on every later turn, a kept block is not asked
//     about again;
//   - new decisions are only taken in epochs, once the context has grown by
//     epoch_tokens since the last one, and only about blocks that appeared
//     since, so the prefix before them stays cached;
//   - blocks are identified by tool_use_id (tool results) or content hash,
//     never by position, so compaction by the agent does not scramble them.
package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Pruner struct {
	cfg      *config.Store
	st       *store.Store
	states   *states
	feedback *feedbackStore
	// ask overrides the selector call (tests); nil uses the configured selector.
	ask func(sel config.Selector) askFunc
}

func New(cfg *config.Store, st *store.Store) *Pruner { return newPruner(cfg, st, config.Dir()) }

func newPruner(cfg *config.Store, st *store.Store, home string) *Pruner {
	dir := filepath.Join(home, "prune")
	return &Pruner{cfg: cfg, st: st, states: newStates(dir), feedback: newFeedbackStore(dir)}
}

func (p *Pruner) Name() string { return "prune" }

// selectorAsk adapts the System One client to askFunc.
func selectorAsk(sel config.Selector) askFunc {
	c := selector.New(sel)
	return func(ctx context.Context, state map[string]any, qs map[string]selector.Question) (map[string]float64, error) {
		res, err := c.Ask(ctx, state, qs)
		if err != nil {
			return nil, err
		}
		out := make(map[string]float64, len(res.Answers))
		for k, a := range res.Answers {
			switch {
			case a.Noul != nil:
				out[k] = *a.Noul
			case a.Score != nil:
				out[k] = *a.Score
			}
		}
		return out, nil
	}
}

func (p *Pruner) asker(sel config.Selector) askFunc {
	if p.ask != nil {
		return p.ask(sel)
	}
	return selectorAsk(sel)
}

// Summary is the small per-request report shown in the request list.
type Summary struct {
	Mode string `json:"mode"`
	// Applied is true when the forwarded body was actually pruned.
	Applied    bool `json:"applied"`
	EpochRan   bool `json:"epoch_ran"`
	Candidates int  `json:"candidates"`
	Dropped    int  `json:"dropped"`
	NewDrops   int  `json:"new_drops"`
	// Token counts are estimates (chars/4) from the X-ray, not provider counts.
	EstTokensBefore int `json:"est_tokens_before"`
	EstTokensAfter  int `json:"est_tokens_after"`
	SavedTokens     int `json:"saved_tokens"`
	// Dollar figures price this turn's input, cache reads and writes included.
	EstCostBefore     float64 `json:"est_cost_before"`
	EstCostAfter      float64 `json:"est_cost_after"`
	CacheInvalidating bool    `json:"cache_invalidating"`
	SelectorMs        int64   `json:"selector_ms"`
	SelectorError     string  `json:"selector_error,omitempty"`
	NextEpochAt       int     `json:"next_epoch_at"`
	Warning           string  `json:"warning,omitempty"`
}

// BlockReport is one row of the kept/dropped diff.
type BlockReport struct {
	Key       string   `json:"key"` // marker / selector key: tool_use_id or ir key
	ID        string   `json:"id"`  // stable id across turns
	IRKey     string   `json:"ir_key"`
	ToolUseID string   `json:"tool_use_id,omitempty"`
	Kind      string   `json:"kind"`
	Role      string   `json:"role,omitempty"`
	Name      string   `json:"name,omitempty"` // tool name (tool, or the call behind a tool_result)
	What      string   `json:"what,omitempty"` // how the block was described to the selector
	Msg       int      `json:"msg"`
	IsError   bool     `json:"is_error,omitempty"`
	Tokens    int      `json:"tokens"`
	After     int      `json:"after"`
	Decision  string   `json:"decision"`
	Reason    string   `json:"reason"`
	Protected string   `json:"protected,omitempty"`
	Score     *float64 `json:"score,omitempty"`
	Preview   string   `json:"preview"`
	Marker    string   `json:"marker,omitempty"`
	FirstReq  string   `json:"first_req,omitempty"`
	New       bool     `json:"new,omitempty"`
}

// Detail is the large per-request report.
type Detail struct {
	Summary
	Preset    string        `json:"preset"`
	Threshold float64       `json:"threshold"`
	Epoch     int           `json:"epoch"`
	Goal      string        `json:"goal"`
	Recent    string        `json:"recent_activity"`
	Cost      costEstimate  `json:"cost"`
	Blocks    []BlockReport `json:"blocks"`
}

func (p *Pruner) Transform(ctx context.Context, r *pipeline.Request, body []byte) (*pipeline.Result, error) {
	cfg := r.Config
	s, err := parseSettings(cfg.Section("prune"))
	if err != nil {
		return nil, fmt.Errorf("prune settings: %w", err)
	}
	eff := s.resolve()
	if !eff.Enabled {
		return nil, nil
	}
	x, err := ir.Parse(body)
	if err != nil {
		return nil, nil // not a body we understand; the proxy forwards it as-is
	}
	d, err := parseDoc(body)
	if err != nil || len(d.msgs) == 0 {
		return nil, nil
	}
	conv := r.ConversationID
	if conv == "" {
		conv = pipeline.ConversationID(body)
	}

	unlock := p.states.lock(conv)
	defer unlock()
	st, err := p.states.load(conv)
	if err != nil {
		return nil, fmt.Errorf("prune state: %w", err)
	}
	fp := d.threadFingerprint()
	th := st.Threads[fp]
	if th == nil {
		th = &Thread{}
		st.Threads[fp] = th
	}

	items := buildItems(d, x, eff)
	goal, recent := goalAndRecent(d)
	res := plan(ctx, planInput{reqID: r.ID, eff: eff, st: st, thread: th, items: items,
		tokens: x.Tokens, ask: p.asker(cfg.Selector), goal: goal, recent: recent})

	sum := Summary{Mode: eff.Mode, EpochRan: res.epoch, Candidates: res.candidates,
		SelectorMs: res.selectorMs, SelectorError: res.selectorErr, NextEpochAt: res.nextEpochAt}
	enforce := eff.Mode == ModeEnforce
	if enforce && !cfg.LogBodies {
		// A marker points at the logged body of the request that first
		// dropped the block; without logs nothing could be recalled.
		enforce = false
		sum.Warning = "log_bodies is off, so pruned content could not be recalled: running in shadow"
	}

	// What changes against the last forwarded body decides the cache cost.
	now := appliedIDs(items)
	changed := map[string]bool{}
	if enforce {
		prev := map[string]bool{}
		for _, id := range th.Applied {
			prev[id] = true
		}
		cur := map[string]bool{}
		for _, id := range now {
			cur[id] = true
			if !prev[id] {
				changed[id] = true
			}
		}
		for id := range prev {
			if !cur[id] {
				changed[id] = true // restored: also a prefix change
			}
		}
	} else {
		for _, it := range items {
			if it.New {
				changed[it.ID] = true
			}
		}
	}
	present := false
	for _, it := range items {
		if changed[it.ID] {
			present = true
			break
		}
	}

	var out []byte
	if enforce && len(now) > 0 {
		if err := apply(d, items); err != nil {
			return nil, fmt.Errorf("prune apply: %w", err)
		}
		if out, err = d.encode(); err != nil {
			return nil, fmt.Errorf("prune encode: %w", err)
		}
		if err := verify(body, out); err != nil {
			// Never forward a body the provider would reject; decisions are
			// not saved either, so the next turn tries again from scratch.
			return nil, fmt.Errorf("prune invariant: %w", err)
		}
		sum.Applied = true
	}
	var applied []string
	if enforce {
		applied = now
	}
	if !equalStrings(th.Applied, applied) {
		th.Applied = applied
		res.changed = true
	}
	if res.changed {
		if err := p.states.save(st); err != nil {
			// A decision that is not persisted would not be sticky: better
			// not to prune at all than to change the prefix every turn.
			return nil, fmt.Errorf("prune state: %w", err)
		}
	}

	lastMsg := len(d.msgs) - 1
	cost := estimateCost(eff, x.Model, items, d.usesCache(x), changed, lastMsg)
	sum.EstTokensBefore = x.Tokens
	for _, it := range items {
		sum.EstTokensAfter += it.After
		if it.Decision == "drop" {
			sum.Dropped++
			if it.New {
				sum.NewDrops++
			}
		}
	}
	sum.SavedTokens = sum.EstTokensBefore - sum.EstTokensAfter
	sum.EstCostBefore, sum.EstCostAfter = round6(cost.Before), round6(cost.After)
	sum.CacheInvalidating = present

	det := Detail{Summary: sum, Preset: eff.Preset, Threshold: eff.KeepThreshold, Epoch: th.Epochs,
		Goal: goal, Recent: recent, Cost: cost, Blocks: make([]BlockReport, 0, len(items))}
	for _, it := range items {
		det.Blocks = append(det.Blocks, report(it))
	}
	sb, _ := json.Marshal(sum)
	db, _ := json.Marshal(det)
	return &pipeline.Result{Body: out, Summary: sb, Detail: db}, nil
}

func report(it *item) BlockReport {
	b := BlockReport{Key: it.MarkerKey, ID: it.ID, IRKey: it.Key, ToolUseID: it.ToolUseID, Kind: it.Kind,
		Role: it.Role, Name: it.Name, Msg: it.Msg, IsError: it.IsError, Tokens: it.Tokens, After: it.After,
		Decision: it.Decision, Reason: it.Reason, Protected: it.Protected, Score: it.Score,
		Marker: it.Marker, FirstReq: it.FirstReq, New: it.New}
	if it.Kind == ir.KindToolResult {
		b.Name = it.ToolName
	}
	if it.Candidate && it.Text != "" {
		b.What, b.Preview = describeWhat(it), selectorPreview(it.Text)
	} else {
		b.Preview = it.Block.Preview
	}
	return b
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }

// Register mounts the package's dashboard API under /api/prune.
func (p *Pruner) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/prune/config", p.getConfig)
	mux.HandleFunc("PUT /api/prune/config", p.putConfig)
	mux.HandleFunc("GET /api/prune/presets", p.getPresets)
	mux.HandleFunc("GET /api/prune/feedback", p.listFeedback)
	mux.HandleFunc("POST /api/prune/feedback", p.addFeedback)
	mux.HandleFunc("DELETE /api/prune/feedback/{id}", p.deleteFeedback)
	mux.HandleFunc("POST /api/prune/replay", p.replay)
	mux.HandleFunc("GET /api/prune/stats", p.stats)
}
