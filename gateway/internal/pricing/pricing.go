// Package pricing prices token usage. Every figure it produces is an
// estimate: list prices change, providers discount, and routed providers
// (OpenRouter, Together, ...) set their own rates. The table can be
// overridden per model prefix in the "prices" field of the prune section.
//
// The two protocol families cache differently, so the cost model is
// protocol-aware:
//
//   - Anthropic Messages caches explicitly (cache_control). Reads cost about
//     0.1x input and 5-minute writes 1.25x.
//   - OpenAI-format providers cache automatically by prefix (prompts of
//     1024 tokens and more on OpenAI). Cached input is discounted and there
//     is no write premium: an uncached token costs plain input.
package pricing

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Price is USD per million tokens. CacheWrite is the 5-minute TTL rate on
// Anthropic; OpenAI-format entries set it equal to Input (no premium).
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// AsOf dates the default table. The dashboard labels every dollar figure
// as an estimate.
const AsOf = "2026-06"

// OpenAIMinCachedPrompt is the prompt size from which OpenAI caches a
// prefix automatically.
const OpenAIMinCachedPrompt = 1024

// Defaults is keyed by model id prefix; the longest matching prefix wins and
// "default" catches everything else. Anthropic rows are first-party list
// prices. OpenAI-compatible rows are the providers' published list prices
// as last checked (first-party where the provider serves the model),
// labelled as estimates; local servers (Ollama, vLLM, LM Studio) cost
// nothing and fall to "default" unless you add a zero row for your model.
var Defaults = map[string]Price{
	// Anthropic: reads ~0.1x input (0.025x on Fable 5.1), writes 1.25x.
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

	// OpenAI: automatic caching, no write premium.
	"gpt-5":        {Input: 1.25, Output: 10, CacheRead: 0.125, CacheWrite: 1.25},
	"gpt-5-mini":   {Input: 0.25, Output: 2, CacheRead: 0.025, CacheWrite: 0.25},
	"gpt-5-nano":   {Input: 0.05, Output: 0.40, CacheRead: 0.005, CacheWrite: 0.05},
	"gpt-4-1":      {Input: 2, Output: 8, CacheRead: 0.50, CacheWrite: 2},
	"gpt-4-1-mini": {Input: 0.40, Output: 1.60, CacheRead: 0.10, CacheWrite: 0.40},
	"gpt-4-1-nano": {Input: 0.10, Output: 0.40, CacheRead: 0.025, CacheWrite: 0.10},
	"gpt-4o":       {Input: 2.50, Output: 10, CacheRead: 1.25, CacheWrite: 2.50},
	"gpt-4o-mini":  {Input: 0.15, Output: 0.60, CacheRead: 0.075, CacheWrite: 0.15},
	"o3":           {Input: 2, Output: 8, CacheRead: 0.50, CacheWrite: 2},
	"o3-mini":      {Input: 1.10, Output: 4.40, CacheRead: 0.55, CacheWrite: 1.10},
	"o4-mini":      {Input: 1.10, Output: 4.40, CacheRead: 0.275, CacheWrite: 1.10},
	// DeepSeek (automatic context caching), Groq, Mistral, Together-hosted Llama.
	"deepseek-chat":     {Input: 0.27, Output: 1.10, CacheRead: 0.07, CacheWrite: 0.27},
	"deepseek-reasoner": {Input: 0.55, Output: 2.19, CacheRead: 0.14, CacheWrite: 0.55},
	"llama-3-3-70b":     {Input: 0.59, Output: 0.79, CacheRead: 0.59, CacheWrite: 0.59},
	"llama-3-1-8b":      {Input: 0.05, Output: 0.08, CacheRead: 0.05, CacheWrite: 0.05},
	"mistral-large":     {Input: 2, Output: 6, CacheRead: 2, CacheWrite: 2},
	"mistral-small":     {Input: 0.10, Output: 0.30, CacheRead: 0.10, CacheWrite: 0.10},
	"qwen3-coder":       {Input: 0.40, Output: 1.60, CacheRead: 0.40, CacheWrite: 0.40},
	"default":           {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
}

// Merge returns the defaults with over applied on top.
func Merge(over map[string]Price) map[string]Price {
	out := make(map[string]Price, len(Defaults)+len(over))
	for k, v := range Defaults {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// Table is the live price table: the defaults plus the overrides stored in
// the prune section's "prices" field.
func Table(c config.Config) map[string]Price {
	var s struct {
		Prices map[string]Price `json:"prices"`
	}
	if raw := c.Section("prune"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	return Merge(s.Prices)
}

// For picks the longest prefix of model in the table. Routed models like
// "anthropic/claude-sonnet-4.5" or "openai/gpt-4.1" are matched on their
// last segment, lower-cased, with dots read as dashes.
func For(table map[string]Price, model string) (Price, string) {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = strings.ReplaceAll(m, ".", "-")
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if k != "default" && strings.HasPrefix(m, k) {
			return table[k], k
		}
	}
	return table["default"], "default"
}

// AutomaticCache reports whether a protocol's providers cache prefixes on
// their own (no cache_control, no write premium).
func AutomaticCache(protocol string) bool { return ir.IsOpenAI(protocol) }

// ForProtocol is For with the protocol's caching rules applied: on
// OpenAI-format requests a cache write costs plain input, whatever the row
// says (the "default" row carries Anthropic's write premium).
func ForProtocol(table map[string]Price, model, protocol string) (Price, string) {
	p, key := For(table, model)
	if AutomaticCache(protocol) {
		p.CacheWrite = p.Input
	}
	return p, key
}

// Cost prices one call's usage in USD. Usage follows the store's split:
// InputTokens is the fresh part, CacheReadTokens the cached part.
func Cost(table map[string]Price, model, protocol string, u *store.Usage) float64 {
	if u == nil {
		return 0
	}
	p, _ := ForProtocol(table, model, protocol)
	usd := float64(u.InputTokens)*p.Input + float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadTokens)*p.CacheRead + float64(u.CacheCreationTokens)*p.CacheWrite
	return Round6(usd / 1e6)
}

// Round6 rounds to a millionth of a dollar.
func Round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }
