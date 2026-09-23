package prune

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Modes.
const (
	ModeShadow  = "shadow"  // compute and report, forward the client's body untouched
	ModeEnforce = "enforce" // forward the pruned body
)

// Presets.
const (
	PresetConservative = "conservative"
	PresetBalanced     = "balanced"
	PresetAggressive   = "aggressive"
)

// DefaultCriteria is the selector instruction. It was tuned against
// open-rlcd: relevant code and errors score 0.6-0.8, noise under 0.2.
const DefaultCriteria = "A coding agent is working on the goal. Below is one block from earlier in its context. " +
	"Will the agent still need this block's content to finish the goal? Answer yes if it holds code, errors, " +
	"requirements or facts the agent will refer to again; no if it is stale, irrelevant or already acted on."

// Settings is the "prune" config section as stored. Pointer fields are
// explicit overrides; nil means "use the preset's value".
type Settings struct {
	Enabled             *bool    `json:"enabled,omitempty"`
	Mode                string   `json:"mode,omitempty"`
	Preset              string   `json:"preset,omitempty"`
	KeepErrors          *bool    `json:"keep_errors,omitempty"`
	KeepEdits           *bool    `json:"keep_edits,omitempty"`
	DropSupersededReads *bool    `json:"drop_superseded_reads,omitempty"`
	KeepLastNTurns      *int     `json:"keep_last_n_turns,omitempty"`
	MinBlockTokens      *int     `json:"min_block_tokens,omitempty"`
	AlwaysKeepUserText  *bool    `json:"always_keep_user_text,omitempty"`
	KeepThreshold       *float64 `json:"keep_threshold,omitempty"`
	Criteria            string   `json:"criteria,omitempty"`
	PruneTools          *bool    `json:"prune_tools,omitempty"`
	PruneSystem         *bool    `json:"prune_system,omitempty"`
	EpochTokens         *int     `json:"epoch_tokens,omitempty"`
	FloorTokens         *int     `json:"floor_tokens,omitempty"`
	SelectorTimeoutMs   *int     `json:"selector_timeout_ms,omitempty"`
	// EditTools and ReadTools name the agent's tools; empty means the defaults.
	EditTools []string `json:"edit_tools,omitempty"`
	ReadTools []string `json:"read_tools,omitempty"`
	// Prices overrides or extends the default price table, by model prefix.
	Prices map[string]Price `json:"prices,omitempty"`
}

// Effective is Settings resolved against its preset: what the pruner runs with.
type Effective struct {
	Enabled             bool             `json:"enabled"`
	Mode                string           `json:"mode"`
	Preset              string           `json:"preset"`
	KeepErrors          bool             `json:"keep_errors"`
	KeepEdits           bool             `json:"keep_edits"`
	DropSupersededReads bool             `json:"drop_superseded_reads"`
	KeepLastNTurns      int              `json:"keep_last_n_turns"`
	MinBlockTokens      int              `json:"min_block_tokens"`
	AlwaysKeepUserText  bool             `json:"always_keep_user_text"`
	KeepThreshold       float64          `json:"keep_threshold"`
	Criteria            string           `json:"criteria"`
	PruneTools          bool             `json:"prune_tools"`
	PruneSystem         bool             `json:"prune_system"`
	EpochTokens         int              `json:"epoch_tokens"`
	FloorTokens         int              `json:"floor_tokens"`
	SelectorTimeoutMs   int              `json:"selector_timeout_ms"`
	EditTools           []string         `json:"edit_tools"`
	ReadTools           []string         `json:"read_tools"`
	Prices              map[string]Price `json:"prices"`
}

// Preset is the set of values a preset contributes.
type Preset struct {
	Name                string  `json:"name"`
	Description         string  `json:"description"`
	KeepErrors          bool    `json:"keep_errors"`
	KeepEdits           bool    `json:"keep_edits"`
	DropSupersededReads bool    `json:"drop_superseded_reads"`
	KeepLastNTurns      int     `json:"keep_last_n_turns"`
	MinBlockTokens      int     `json:"min_block_tokens"`
	AlwaysKeepUserText  bool    `json:"always_keep_user_text"`
	KeepThreshold       float64 `json:"keep_threshold"`
	EpochTokens         int     `json:"epoch_tokens"`
	FloorTokens         int     `json:"floor_tokens"`
}

var presets = []Preset{
	{Name: PresetConservative, Description: "Only drops what is clearly noise. Few, large epochs.",
		KeepErrors: true, KeepEdits: true, DropSupersededReads: true, KeepLastNTurns: 8, MinBlockTokens: 800,
		AlwaysKeepUserText: true, KeepThreshold: 0.10, EpochTokens: 30000, FloorTokens: 60000},
	{Name: PresetBalanced, Description: "Drops stale tool output the goal no longer needs.",
		KeepErrors: true, KeepEdits: true, DropSupersededReads: true, KeepLastNTurns: 4, MinBlockTokens: 400,
		AlwaysKeepUserText: true, KeepThreshold: 0.25, EpochTokens: 20000, FloorTokens: 30000},
	{Name: PresetAggressive, Description: "Keeps only what the selector is fairly sure about. Relies on recall.",
		KeepErrors: false, KeepEdits: false, DropSupersededReads: true, KeepLastNTurns: 2, MinBlockTokens: 200,
		AlwaysKeepUserText: false, KeepThreshold: 0.40, EpochTokens: 10000, FloorTokens: 20000},
}

func presetByName(name string) (Preset, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

var (
	defaultEditTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch", "str_replace_based_edit_tool"}
	defaultReadTools = []string{"Read", "read_file", "view"}
)

const defaultSelectorTimeoutMs = 3000

// parseSettings reads the stored section; absent or empty means all defaults.
func parseSettings(raw json.RawMessage) (Settings, error) {
	var s Settings
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	err := json.Unmarshal(raw, &s)
	return s, err
}

func (s Settings) validate() error {
	if s.Mode != "" && s.Mode != ModeShadow && s.Mode != ModeEnforce {
		return fmt.Errorf("mode must be %q or %q", ModeShadow, ModeEnforce)
	}
	if s.Preset != "" {
		if _, ok := presetByName(s.Preset); !ok {
			return fmt.Errorf("unknown preset %q", s.Preset)
		}
	}
	if t := s.KeepThreshold; t != nil && (*t < 0 || *t > 1) {
		return fmt.Errorf("keep_threshold must be between 0 and 1")
	}
	for name, v := range map[string]*int{"keep_last_n_turns": s.KeepLastNTurns, "min_block_tokens": s.MinBlockTokens,
		"epoch_tokens": s.EpochTokens, "floor_tokens": s.FloorTokens, "selector_timeout_ms": s.SelectorTimeoutMs} {
		if v != nil && *v < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if v := s.SelectorTimeoutMs; v != nil && *v > 30000 {
		return fmt.Errorf("selector_timeout_ms must be at most 30000: the agent waits for it")
	}
	return nil
}

// resolve applies the preset, then the explicit overrides.
func (s Settings) resolve() Effective {
	name := s.Preset
	p, ok := presetByName(name)
	if !ok {
		name = PresetBalanced
		p, _ = presetByName(name)
	}
	e := Effective{
		Enabled: true, Mode: ModeShadow, Preset: name,
		KeepErrors: p.KeepErrors, KeepEdits: p.KeepEdits, DropSupersededReads: p.DropSupersededReads,
		KeepLastNTurns: p.KeepLastNTurns, MinBlockTokens: p.MinBlockTokens, AlwaysKeepUserText: p.AlwaysKeepUserText,
		KeepThreshold: p.KeepThreshold, Criteria: DefaultCriteria, EpochTokens: p.EpochTokens, FloorTokens: p.FloorTokens,
		SelectorTimeoutMs: defaultSelectorTimeoutMs, EditTools: defaultEditTools, ReadTools: defaultReadTools,
		Prices: mergePrices(s.Prices),
	}
	setB := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setB(&e.Enabled, s.Enabled)
	if s.Mode != "" {
		e.Mode = s.Mode
	}
	setB(&e.KeepErrors, s.KeepErrors)
	setB(&e.KeepEdits, s.KeepEdits)
	setB(&e.DropSupersededReads, s.DropSupersededReads)
	setI(&e.KeepLastNTurns, s.KeepLastNTurns)
	setI(&e.MinBlockTokens, s.MinBlockTokens)
	setB(&e.AlwaysKeepUserText, s.AlwaysKeepUserText)
	if s.KeepThreshold != nil {
		e.KeepThreshold = *s.KeepThreshold
	}
	if strings.TrimSpace(s.Criteria) != "" {
		e.Criteria = s.Criteria
	}
	setB(&e.PruneTools, s.PruneTools)
	setB(&e.PruneSystem, s.PruneSystem)
	setI(&e.EpochTokens, s.EpochTokens)
	setI(&e.FloorTokens, s.FloorTokens)
	setI(&e.SelectorTimeoutMs, s.SelectorTimeoutMs)
	if e.SelectorTimeoutMs == 0 {
		e.SelectorTimeoutMs = defaultSelectorTimeoutMs
	}
	if len(s.EditTools) > 0 {
		e.EditTools = s.EditTools
	}
	if len(s.ReadTools) > 0 {
		e.ReadTools = s.ReadTools
	}
	return e
}

// Price is USD per million tokens. CacheWrite is the 5-minute TTL rate.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// PricesAsOf dates the default table. These are Anthropic first-party list
// prices; the dashboard labels every dollar figure as an estimate.
const PricesAsOf = "2026-06"

// defaultPrices is keyed by model id prefix; the longest matching prefix
// wins and "default" catches everything else. Cache reads are ~0.1x input
// (0.025x on Fable 5.1) and 5-minute cache writes 1.25x.
var defaultPrices = map[string]Price{
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5},
	"claude-mythos-5-1": {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
	"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4":     {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-1":   {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite: 18.75},
	"claude-opus-4-0":   {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite: 18.75},
	"claude-opus-4-2":   {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite: 18.75}, // claude-opus-4-2025xxxx
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.5},
	"claude-sonnet-4":   {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
	"claude-haiku-4-5":  {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite: 1.25},
	"default":           {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
}

func mergePrices(over map[string]Price) map[string]Price {
	out := make(map[string]Price, len(defaultPrices)+len(over))
	for k, v := range defaultPrices {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// priceFor picks the longest prefix of model in the table. Routed models
// like "anthropic/claude-sonnet-4.5" are matched on their last segment with
// dots read as dashes.
func priceFor(table map[string]Price, model string) (Price, string) {
	m := model
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = strings.ReplaceAll(m, ".", "-")
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if k != "default" && strings.HasPrefix(m, k) {
			return table[k], k
		}
	}
	return table["default"], "default"
}
