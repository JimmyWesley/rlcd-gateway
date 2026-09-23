package resilience

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// Section is the config section name.
const Section = "resilience"

// Backoff is the retry-in-place policy for rate limits, overloads and
// provider errors.
type Backoff struct {
	// BaseMs doubles per retry, up to MaxMs; a Retry-After from the
	// provider wins when it is longer.
	BaseMs int `json:"base_ms"`
	MaxMs  int `json:"max_ms"`
	// Jitter is the random fraction (0-1) added or removed from each wait.
	Jitter float64 `json:"jitter"`
	// Retries is how many times one route is retried after a backoff
	// before the fallbacks are tried.
	Retries int `json:"retries"`
}

// Fill modes for a request that sets no output limit.
const (
	FillAuto   = "auto"   // only for providers that pick their own default (aggregators, OpenAI-compatible hosts)
	FillAlways = "always" // every route with known limits
	FillNever  = "never"
)

// Guard is the preventive max_tokens guard.
type Guard struct {
	Enabled bool `json:"enabled"`
	// FillMissing: auto, always or never. The first-party OpenAI and
	// Anthropic APIs compute a safe default themselves, so "auto" leaves
	// them alone; aggregators pass the choice to whichever provider serves
	// the call, which may default to the whole window (the GMICloud case).
	FillMissing string `json:"fill_missing"`
	// DefaultMaxTokens is the limit set on a chat app's request that has
	// none (capped by the model's max output and what fits the window).
	// A coding agent gets the largest value that fits instead.
	DefaultMaxTokens int `json:"default_max_tokens"`
	// Clamp lowers a limit above the model's max output, or one that does
	// not fit the window with the estimated input.
	Clamp bool `json:"clamp"`
	// SafetyMarginTokens is kept free on top of the estimated input (which
	// is itself raised by 10%): the estimate is not the provider's tokenizer.
	SafetyMarginTokens int `json:"safety_margin_tokens"`
}

// Emergency is the context-overflow recovery.
type Emergency struct {
	Enabled bool `json:"enabled"`
	// KeepThreshold replaces the pruning preset's: a block the economy
	// model scores below it is dropped. It is higher than any preset's, so
	// the emergency pass drops more; epochs are ignored, and every
	// invariant (tool pairs, protected recent turns, recall results) holds.
	KeepThreshold float64 `json:"keep_threshold"`
}

// Policy is what governs one request's recovery.
type Policy struct {
	// Enabled turns recovery (retries, fallbacks) on. The guard has its
	// own switch.
	Enabled bool `json:"enabled"`
	// MaxAttempts caps upstream calls per request, the first included.
	MaxAttempts int `json:"max_attempts"`
	// TimeBudgetMs bounds the time spent on recovery: no retry starts, and
	// no backoff is taken, past it.
	TimeBudgetMs   int       `json:"time_budget_ms"`
	Backoff        Backoff   `json:"backoff"`
	MaxTokensGuard Guard     `json:"max_tokens_guard"`
	EmergencyPrune Emergency `json:"emergency_prune"`
	// IgnoreProvider retries an OpenRouter call once without the provider
	// that failed it (OpenRouter's provider.ignore).
	IgnoreProvider bool `json:"ignore_provider"`
}

// Override is a route's or an alias's settings: any Policy field (nested
// objects merge field by field), plus fallbacks and model limits.
type Override struct {
	Policy
	// Fallbacks are tried in order when the route keeps failing: route
	// names, optionally "route:model" to send another model on that route.
	// Only routes that speak the request's protocol are used.
	Fallbacks []string `json:"fallbacks,omitempty"`
	// ContextWindow and MaxOutputTokens override the registry for every
	// model on the route (or behind the alias).
	ContextWindow   int `json:"context_window,omitempty"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// Settings is the "resilience" config section.
type Settings struct {
	Policy
	// OpenRouterCatalog fetches OpenRouter's public model list for limits.
	OpenRouterCatalog bool    `json:"openrouter_catalog"`
	CatalogTTLHours   float64 `json:"catalog_ttl_hours"`
	// Models overrides limits by upstream model id.
	Models map[string]Limits `json:"models,omitempty"`
	// Routes and Aliases hold partial overrides (see Override), keyed by
	// route or alias name. They are kept as sent, so a field that is not
	// set follows the global policy.
	Routes  map[string]json.RawMessage `json:"routes,omitempty"`
	Aliases map[string]json.RawMessage `json:"aliases,omitempty"`
}

// DefaultPolicy is the global policy when nothing is configured.
func DefaultPolicy() Policy {
	return Policy{
		Enabled: true, MaxAttempts: 4, TimeBudgetMs: 30000,
		Backoff:        Backoff{BaseMs: 500, MaxMs: 8000, Jitter: 0.3, Retries: 2},
		MaxTokensGuard: Guard{Enabled: true, FillMissing: FillAuto, DefaultMaxTokens: 4096, Clamp: true, SafetyMarginTokens: 256},
		EmergencyPrune: Emergency{Enabled: true, KeepThreshold: 0.5},
		IgnoreProvider: true,
	}
}

// DefaultSettings is the section's default.
func DefaultSettings() Settings {
	return Settings{Policy: DefaultPolicy(), OpenRouterCatalog: true, CatalogTTLHours: 24}
}

// Parse reads the section over the defaults: a field the section does not
// set keeps its default.
func Parse(raw json.RawMessage) (Settings, error) {
	s := DefaultSettings()
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return DefaultSettings(), err
	}
	return s, nil
}

// FromConfig reads the section of cfg; an invalid section yields the defaults.
func FromConfig(c config.Config) Settings {
	s, _ := Parse(c.Section(Section))
	return s
}

// Resolved is the policy for one request, with its route's and alias's
// overrides applied.
type Resolved struct {
	Policy
	Fallbacks []string
	// Limits overridden by configuration (0 = not overridden).
	WindowOverride, MaxOutOverride int
	OverrideSource                 string
}

// Resolve applies the route's override, then the alias's.
func (s Settings) Resolve(route, alias string) Resolved {
	r := Resolved{Policy: s.Policy}
	apply := func(raw json.RawMessage, src string) {
		if len(raw) == 0 {
			return
		}
		o := struct {
			*Policy
			Fallbacks       *[]string `json:"fallbacks"`
			ContextWindow   int       `json:"context_window"`
			MaxOutputTokens int       `json:"max_output_tokens"`
		}{Policy: &r.Policy}
		if json.Unmarshal(raw, &o) != nil {
			return
		}
		if o.Fallbacks != nil {
			r.Fallbacks = *o.Fallbacks
		}
		if o.ContextWindow > 0 {
			r.WindowOverride, r.OverrideSource = o.ContextWindow, src
		}
		if o.MaxOutputTokens > 0 {
			r.MaxOutOverride, r.OverrideSource = o.MaxOutputTokens, src
		}
	}
	apply(s.Routes[route], SourceRoute)
	if alias != "" {
		apply(s.Aliases[alias], SourceAlias)
	}
	return r
}

// LimitsFor merges the configured overrides over the registry.
func (s Settings) LimitsFor(reg *Registry, res Resolved, model string, openRouter, beta1M bool) Limits {
	l := reg.Lookup(model, openRouter, beta1M)
	if o, ok := s.Models[model]; ok {
		if o.ContextWindow > 0 {
			l.ContextWindow, l.Source = o.ContextWindow, SourceModel
		}
		if o.MaxOutputTokens > 0 {
			l.MaxOutputTokens, l.Source = o.MaxOutputTokens, SourceModel
		}
	}
	if res.WindowOverride > 0 {
		l.ContextWindow, l.Source = res.WindowOverride, res.OverrideSource
	}
	if res.MaxOutOverride > 0 {
		l.MaxOutputTokens, l.Source = res.MaxOutOverride, res.OverrideSource
	}
	return l
}

// SplitFallback reads "route" or "route:model".
func SplitFallback(s string) (route, model string) {
	route, model, _ = strings.Cut(strings.TrimSpace(s), ":")
	return route, model
}

// aliasRoutes reads the router section's aliases (name → route) without
// importing the router.
func aliasRoutes(c config.Config) map[string]string {
	var r struct {
		Aliases []struct {
			Name  string `json:"name"`
			Route string `json:"route"`
		} `json:"aliases"`
	}
	_ = json.Unmarshal(c.Section("router"), &r)
	out := map[string]string{}
	for _, a := range r.Aliases {
		out[a.Name] = a.Route
	}
	return out
}

func (p Policy) validate(where string) error {
	bad := func(format string, a ...any) error { return fmt.Errorf(where+format, a...) }
	switch {
	case p.MaxAttempts < 1 || p.MaxAttempts > 10:
		return bad("max_attempts must be between 1 and 10")
	case p.TimeBudgetMs < 0 || p.TimeBudgetMs > 600000:
		return bad("time_budget_ms must be between 0 and 600000")
	case p.Backoff.BaseMs < 0 || p.Backoff.BaseMs > 60000:
		return bad("backoff.base_ms must be between 0 and 60000")
	case p.Backoff.MaxMs < p.Backoff.BaseMs || p.Backoff.MaxMs > 120000:
		return bad("backoff.max_ms must be between backoff.base_ms and 120000")
	case p.Backoff.Jitter < 0 || p.Backoff.Jitter > 1:
		return bad("backoff.jitter must be between 0 and 1")
	case p.Backoff.Retries < 0 || p.Backoff.Retries > 5:
		return bad("backoff.retries must be between 0 and 5")
	case p.MaxTokensGuard.FillMissing != FillAuto && p.MaxTokensGuard.FillMissing != FillAlways &&
		p.MaxTokensGuard.FillMissing != FillNever:
		return bad("max_tokens_guard.fill_missing must be %q, %q or %q", FillAuto, FillAlways, FillNever)
	case p.MaxTokensGuard.DefaultMaxTokens < 1 || p.MaxTokensGuard.DefaultMaxTokens > 1000000:
		return bad("max_tokens_guard.default_max_tokens must be between 1 and 1000000")
	case p.MaxTokensGuard.SafetyMarginTokens < 0 || p.MaxTokensGuard.SafetyMarginTokens > 100000:
		return bad("max_tokens_guard.safety_margin_tokens must be between 0 and 100000")
	case p.EmergencyPrune.KeepThreshold < 0 || p.EmergencyPrune.KeepThreshold > 1:
		return bad("emergency_prune.keep_threshold must be between 0 and 1")
	}
	return nil
}

func validLimits(where string, window, maxOut int) error {
	switch {
	case window < 0 || window > 100000000:
		return fmt.Errorf("%scontext_window must be between 0 and 100000000", where)
	case maxOut < 0 || maxOut > 100000000:
		return fmt.Errorf("%smax_output_tokens must be between 0 and 100000000", where)
	case window > 0 && maxOut > window:
		return fmt.Errorf("%smax_output_tokens must not exceed context_window", where)
	}
	return nil
}

// strictOverride decodes an override, refusing unknown fields.
func strictOverride(raw json.RawMessage, base Policy) (Override, error) {
	o := Override{Policy: base}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, err
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return o, err
	}
	if _, ok := probe.(map[string]any); !ok {
		return o, fmt.Errorf("must be an object")
	}
	return o, nil
}

// Validate checks the whole section against the routes and aliases in c.
func (s Settings) Validate(c config.Config) error {
	if err := s.Policy.validate(""); err != nil {
		return err
	}
	if s.CatalogTTLHours < 1 || s.CatalogTTLHours > 24*30 {
		return fmt.Errorf("catalog_ttl_hours must be between 1 and 720")
	}
	for id, l := range s.Models {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("models: empty model id")
		}
		if err := validLimits("models["+id+"]: ", l.ContextWindow, l.MaxOutputTokens); err != nil {
			return err
		}
	}
	checkFallbacks := func(where string, primary config.Route, primaryName string, fbs []string) error {
		seen := map[string]bool{}
		for _, fb := range fbs {
			name, _ := SplitFallback(fb)
			rt, ok := c.Routes[name]
			switch {
			case name == "":
				return fmt.Errorf("%sfallbacks: empty route name", where)
			case !ok:
				return fmt.Errorf("%sfallbacks: route %q does not exist", where, name)
			case seen[fb]:
				return fmt.Errorf("%sfallbacks: %q is listed twice", where, fb)
			case name == primaryName && !strings.Contains(fb, ":"):
				return fmt.Errorf("%sfallbacks: a route cannot fall back to itself", where)
			case !shareProtocol(primary, rt):
				return fmt.Errorf("%sfallbacks: route %q speaks %s, not %s (no translation between protocols)", where, name,
					strings.Join(rt.Protocols(), " / "), strings.Join(primary.Protocols(), " / "))
			}
			seen[fb] = true
		}
		if len(fbs) > 8 {
			return fmt.Errorf("%sat most 8 fallbacks", where)
		}
		return nil
	}
	for _, name := range sortedKeys(s.Routes) {
		where := "routes[" + name + "]: "
		rt, ok := c.Routes[name]
		if !ok {
			return fmt.Errorf("%sroute does not exist", where)
		}
		o, err := strictOverride(s.Routes[name], s.Policy)
		if err != nil {
			return fmt.Errorf("%s%v", where, err)
		}
		if err := o.Policy.validate(where); err != nil {
			return err
		}
		if err := validLimits(where, o.ContextWindow, o.MaxOutputTokens); err != nil {
			return err
		}
		if err := checkFallbacks(where, rt, name, o.Fallbacks); err != nil {
			return err
		}
	}
	aliases := aliasRoutes(c)
	for _, name := range sortedKeys(s.Aliases) {
		where := "aliases[" + name + "]: "
		routeName, ok := aliases[name]
		if !ok {
			return fmt.Errorf("%salias does not exist", where)
		}
		o, err := strictOverride(s.Aliases[name], s.Policy)
		if err != nil {
			return fmt.Errorf("%s%v", where, err)
		}
		if err := o.Policy.validate(where); err != nil {
			return err
		}
		if err := validLimits(where, o.ContextWindow, o.MaxOutputTokens); err != nil {
			return err
		}
		if rt, ok := c.Routes[routeName]; ok {
			if err := checkFallbacks(where, rt, routeName, o.Fallbacks); err != nil {
				return err
			}
		}
	}
	return nil
}

func shareProtocol(a, b config.Route) bool {
	for _, p := range a.Protocols() {
		if b.Speaks(p) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
