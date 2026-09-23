package resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SettingsView is GET and PUT /api/resilience/settings.
type SettingsView struct {
	// Settings is the stored section over the defaults.
	Settings Settings `json:"settings"`
	// Defaults is the global policy when nothing is set.
	Defaults Settings `json:"defaults"`
	// Effective is the resolved policy of every route and alias that
	// exists (the global policy with its override applied).
	Effective EffectiveView `json:"effective"`
	Catalog   CatalogStatus `json:"catalog"`
	// Classes and Actions are the names the records and stats use.
	Classes     []string `json:"classes"`
	Actions     []string `json:"actions"`
	BuiltinAsOf string   `json:"builtin_as_of"`
}

// EffectiveView lists resolved policies by route and alias name.
type EffectiveView struct {
	Routes  map[string]EffectivePolicy `json:"routes"`
	Aliases map[string]EffectivePolicy `json:"aliases"`
}

// EffectivePolicy is Resolved, as JSON.
type EffectivePolicy struct {
	Policy
	Fallbacks       []string `json:"fallbacks"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
}

func effective(r Resolved) EffectivePolicy {
	fb := r.Fallbacks
	if fb == nil {
		fb = []string{}
	}
	return EffectivePolicy{Policy: r.Policy, Fallbacks: fb, ContextWindow: r.WindowOverride, MaxOutputTokens: r.MaxOutOverride}
}

func (e *Engine) view() SettingsView {
	c := e.Config.Get()
	s := FromConfig(c)
	v := SettingsView{Settings: s, Defaults: DefaultSettings(), Catalog: e.Registry.Status(),
		Classes: Classes, Actions: Actions, BuiltinAsOf: BuiltinAsOf,
		Effective: EffectiveView{Routes: map[string]EffectivePolicy{}, Aliases: map[string]EffectivePolicy{}}}
	for name := range c.Routes {
		v.Effective.Routes[name] = effective(s.Resolve(name, ""))
	}
	for alias, route := range aliasRoutes(c) {
		v.Effective.Aliases[alias] = effective(s.Resolve(route, alias))
	}
	if v.Settings.Models == nil {
		v.Settings.Models = map[string]Limits{}
	}
	if v.Settings.Routes == nil {
		v.Settings.Routes = map[string]json.RawMessage{}
	}
	if v.Settings.Aliases == nil {
		v.Settings.Aliases = map[string]json.RawMessage{}
	}
	return v
}

// Register mounts the resilience API:
//
//	GET  /api/resilience/settings          global policy, overrides, effective policies, catalog
//	PUT  /api/resilience/settings          partial update, validated ({"routes": {"x": null}} removes an override)
//	GET  /api/resilience/stats?days=30     recoveries by class and action, top failing providers and models
//	GET  /api/resilience/limits?model=m&route=r&alias=a
//	                                       the limits the guard would use
//	POST /api/resilience/catalog/refresh   fetch OpenRouter's model list now
func (e *Engine) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/resilience/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.view())
	})
	mux.HandleFunc("PUT /api/resilience/settings", e.putSettings)
	mux.HandleFunc("GET /api/resilience/stats", e.getStats)
	mux.HandleFunc("GET /api/resilience/limits", e.getLimits)
	mux.HandleFunc("POST /api/resilience/catalog/refresh", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if err := e.Registry.Refresh(ctx); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, e.Registry.Status())
	})
}

// ApplyPatch merges a partial settings document over cur. Keys of routes,
// aliases and models set to null are removed.
func ApplyPatch(cur Settings, patch []byte) (Settings, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(patch, &probe); err != nil {
		return cur, err
	}
	next := cur
	// Copy the maps: unmarshalling merges into them.
	next.Models = map[string]Limits{}
	for k, v := range cur.Models {
		next.Models[k] = v
	}
	next.Routes, next.Aliases = cloneRaw(cur.Routes), cloneRaw(cur.Aliases)
	dec := json.NewDecoder(bytes.NewReader(patch))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return cur, err
	}
	var nulls struct {
		Models  map[string]json.RawMessage `json:"models"`
		Routes  map[string]json.RawMessage `json:"routes"`
		Aliases map[string]json.RawMessage `json:"aliases"`
	}
	_ = json.Unmarshal(patch, &nulls)
	drop := func(raw map[string]json.RawMessage, del func(string)) {
		for k, v := range raw {
			if strings.TrimSpace(string(v)) == "null" {
				del(k)
			}
		}
	}
	drop(nulls.Models, func(k string) { delete(next.Models, k) })
	drop(nulls.Routes, func(k string) { delete(next.Routes, k) })
	drop(nulls.Aliases, func(k string) { delete(next.Aliases, k) })
	for k, v := range next.Routes {
		if len(v) == 0 || string(v) == "null" {
			delete(next.Routes, k)
		}
	}
	for k, v := range next.Aliases {
		if len(v) == 0 || string(v) == "null" {
			delete(next.Aliases, k)
		}
	}
	return next, nil
}

func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *Engine) putSettings(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := e.Config.Get()
	cur, err := Parse(c.Section(Section))
	if err != nil {
		cur = DefaultSettings()
	}
	next, err := ApplyPatch(cur, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := next.Validate(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := json.Marshal(next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := e.Config.SetSection(Section, b); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	e.Registry.SetTTL(time.Duration(next.CatalogTTLHours * float64(time.Hour)))
	writeJSON(w, http.StatusOK, e.view())
}

func (e *Engine) getStats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 3650 {
			writeError(w, http.StatusBadRequest, "days must be a number of days (0 = everything)")
			return
		}
		days = n
	}
	s, err := ComputeStats(e.Store.Scan, days, e.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// LimitsView is GET /api/resilience/limits.
type LimitsView struct {
	Model  string `json:"model"`
	Route  string `json:"route,omitempty"`
	Alias  string `json:"alias,omitempty"`
	Limits Limits `json:"limits"`
	Known  bool   `json:"known"`
}

func (e *Engine) getLimits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	model, route, alias := q.Get("model"), q.Get("route"), q.Get("alias")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	c := e.Config.Get()
	if alias != "" && route == "" {
		route = aliasRoutes(c)[alias]
	}
	openRouter := false
	if rt, ok := c.Routes[route]; ok {
		openRouter = rt.ProviderName() == "openrouter"
	} else if route == "" && strings.Contains(model, "/") {
		openRouter = true
	}
	s := FromConfig(c)
	l := s.LimitsFor(e.Registry, s.Resolve(route, alias), model, openRouter, q.Get("beta_1m") == "true")
	writeJSON(w, http.StatusOK, LimitsView{Model: model, Route: route, Alias: alias, Limits: l, Known: l.Known()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
