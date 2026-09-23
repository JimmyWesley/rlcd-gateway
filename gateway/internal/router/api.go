package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Register mounts the package's dashboard API under /api/router.
func (r *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/router/rules", r.getRules)
	mux.HandleFunc("PUT /api/router/rules", r.putRules)
	mux.HandleFunc("POST /api/router/rules/test", r.testRule)
	mux.HandleFunc("GET /api/router/rule-templates", r.getTemplates)
	mux.HandleFunc("GET /api/router/routes", r.getRoutes)
	mux.HandleFunc("PUT /api/router/routes/{name}", r.putRoute)
	mux.HandleFunc("DELETE /api/router/routes/{name}", r.deleteRoute)
	mux.HandleFunc("GET /api/router/aliases", r.getAliases)
	mux.HandleFunc("PUT /api/router/aliases", r.putAliases)
	mux.HandleFunc("PUT /api/router/openai-default", r.putOpenAIDefault)
	mux.HandleFunc("POST /api/router/dryrun", r.dryRun)
	mux.HandleFunc("GET /api/router/conversations", r.listConversations)
	mux.HandleFunc("DELETE /api/router/conversations", r.clearConversations)
	mux.HandleFunc("DELETE /api/router/conversations/{id}", r.deleteConversation)
}

// rulesDoc is the GET/PUT /api/router/rules body: the section without the
// route metadata, which /api/router/routes owns.
type rulesDoc struct {
	Sticky           bool    `json:"sticky"`
	TTLHours         float64 `json:"ttl_hours"`
	BackgroundBypass bool    `json:"background_bypass"`
	Rules            []Rule  `json:"rules"`
}

func (r *Router) current() Settings {
	s, _ := parseSettings(r.cfg.Get().Section("router"))
	return s
}

func (r *Router) getRules(w http.ResponseWriter, _ *http.Request) {
	s := r.current()
	writeJSON(w, http.StatusOK, rulesDoc{Sticky: s.Sticky, TTLHours: s.TTLHours, BackgroundBypass: s.BackgroundBypass, Rules: s.Rules})
}

func (r *Router) putRules(w http.ResponseWriter, req *http.Request) {
	var in rulesDoc
	if err := decode(req, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := r.update(func(s *Settings, cfg config.Config) error {
		s.Sticky, s.TTLHours, s.BackgroundBypass, s.Rules = in.Sticky, in.TTLHours, in.BackgroundBypass, in.Rules
		if s.Rules == nil {
			s.Rules = []Rule{}
		}
		if s.TTLHours == 0 {
			s.TTLHours = defaultTTLHours
		}
		return s.validate(cfg)
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	r.getRules(w, req)
}

// update applies fn to the stored section and saves it, serialized so the
// rules and routes endpoints never overwrite each other's changes.
func (r *Router) update(fn func(s *Settings, cfg config.Config) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := r.cfg.Get()
	s, err := parseSettings(cfg.Section("router"))
	if err != nil {
		return fmt.Errorf("stored router section is invalid: %v", err)
	}
	if err := fn(&s, cfg); err != nil {
		return err
	}
	raw, err := marshalSettings(s)
	if err != nil {
		return err
	}
	return r.cfg.SetSection("router", raw)
}

// RouteView never carries a key: only whether one is set.
type RouteView struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	BaseURL     string            `json:"base_url"`
	Auth        string            `json:"auth"`
	Model       string            `json:"model,omitempty"`
	APIKeyEnv   string            `json:"api_key_env,omitempty"`
	HasKey      bool              `json:"has_key"`
	Headers     map[string]string `json:"headers"`
	Provider    string            `json:"provider"`
	Protocols   []string          `json:"protocols"`
	Description string            `json:"description,omitempty"`
	Active      bool              `json:"active"`
	// OpenAIDefault marks the default route for OpenAI-format requests.
	OpenAIDefault bool     `json:"openai_default"`
	UsedBy        []string `json:"used_by"`
}

func viewRoutes(cfg config.Config, s Settings) []RouteView {
	out := []RouteView{}
	for _, name := range cfg.RouteNames() {
		rt := cfg.Routes[name]
		h := rt.Headers
		if h == nil {
			h = map[string]string{}
		}
		out = append(out, RouteView{Name: name, Kind: rt.Kind, BaseURL: rt.BaseURL, Auth: rt.Auth, Model: rt.Model,
			APIKeyEnv: rt.APIKeyEnv, HasKey: rt.ResolvedKey() != "", Headers: h, Provider: rt.ProviderName(),
			Protocols: rt.Protocols(), Description: s.Routes[name].Description, Active: name == cfg.ActiveRoute,
			OpenAIDefault: name == cfg.DefaultOpenAIRoute, UsedBy: rulesUsing(s, name)})
	}
	return out
}

// rulesUsing names the rules and aliases (as "alias <name>") that use route.
func rulesUsing(s Settings, route string) []string {
	out := []string{}
	for _, a := range s.Aliases {
		if a.Route == route {
			out = append(out, "alias "+a.Name)
		}
	}
	for _, rule := range s.Rules {
		if rule.Route == route && kindOf(rule) == KindMatch {
			out = append(out, rule.Name)
			continue
		}
		if kindOf(rule) == KindDecision {
			if contains(decisionTargets(rule, s), route) {
				out = append(out, rule.Name)
			}
			continue
		}
		for _, c := range rule.Candidates {
			if c == route {
				out = append(out, rule.Name)
				break
			}
		}
	}
	return out
}

func (r *Router) getRoutes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, viewRoutes(r.cfg.Get(), r.current()))
}

type routeInput struct {
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	Auth      string `json:"auth"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	APIKeyEnv string `json:"api_key_env"`
	// ClearKey removes the stored key; an empty APIKey alone keeps it.
	ClearKey    bool              `json:"clear_key"`
	Description string            `json:"description"`
	Headers     map[string]string `json:"headers"`
	Provider    string            `json:"provider"`
}

var routeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// mergeRoute builds the route to store. An empty key keeps the existing one
// only when the base URL is unchanged: a key must never follow a route to a
// different host.
func mergeRoute(in routeInput, cur config.Route, exists bool) (config.Route, error) {
	rt := config.Route{Kind: in.Kind, BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"), Auth: in.Auth,
		Model: strings.TrimSpace(in.Model), APIKey: in.APIKey, APIKeyEnv: strings.TrimSpace(in.APIKeyEnv),
		Provider: strings.TrimSpace(in.Provider)}
	if rt.Kind != config.KindAnthropic && rt.Kind != config.KindOpenRouter && rt.Kind != config.KindOpenAI {
		return rt, fmt.Errorf("kind must be %q, %q or %q", config.KindAnthropic, config.KindOpenRouter, config.KindOpenAI)
	}
	if len(in.Headers) > 0 {
		rt.Headers = map[string]string{}
		for k, v := range in.Headers {
			if k = strings.TrimSpace(k); k != "" {
				rt.Headers[k] = strings.TrimSpace(v)
			}
		}
		if err := config.ValidateHeaders(rt.Headers); err != nil {
			return rt, err
		}
	}
	if rt.Auth != config.AuthPassthrough && rt.Auth != config.AuthKey {
		return rt, fmt.Errorf("auth must be %q or %q", config.AuthPassthrough, config.AuthKey)
	}
	u, err := url.Parse(rt.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return rt, fmt.Errorf("base_url must be an http(s) URL")
	}
	if rt.APIKey == "" && !in.ClearKey && exists && sameEndpoint(cur.BaseURL, rt.BaseURL) {
		rt.APIKey = cur.APIKey
	}
	if rt.Auth == config.AuthPassthrough {
		// A passthrough route forwards the client's own login; holding a key
		// for it would only be a secret at rest for nothing.
		rt.APIKey, rt.APIKeyEnv = "", ""
	}
	return rt, nil
}

func sameEndpoint(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func (r *Router) putRoute(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if !routeNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "route name: letters, digits, '.', '_' or '-' (max 64)")
		return
	}
	var in routeInput
	if err := decode(req, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur, exists := r.cfg.Get().Routes[name]
	rt, err := mergeRoute(in, cur, exists)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.cfg.UpsertRoute(name, rt); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	desc := strings.TrimSpace(in.Description)
	if err := r.update(func(s *Settings, _ config.Config) error {
		if s.Routes == nil {
			s.Routes = map[string]RouteMeta{}
		}
		if desc == "" {
			delete(s.Routes, name)
		} else {
			s.Routes[name] = RouteMeta{Description: desc}
		}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.getRoutes(w, req)
}

func (r *Router) deleteRoute(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	cfg := r.cfg.Get()
	if _, ok := cfg.Routes[name]; !ok {
		writeError(w, http.StatusNotFound, "no such route")
		return
	}
	if used := rulesUsing(r.current(), name); len(used) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("route %q is used by: %s", name, strings.Join(used, ", ")))
		return
	}
	if err := r.cfg.DeleteRoute(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = r.update(func(s *Settings, _ config.Config) error {
		delete(s.Routes, name)
		return nil
	})
	r.sticky.dropRoute(name)
	r.getRoutes(w, req)
}

// AliasView is an alias plus what the dashboard needs to show it.
type AliasView struct {
	Alias
	Protocols []string `json:"protocols"`
	Provider  string   `json:"provider"`
}

func (r *Router) getAliases(w http.ResponseWriter, _ *http.Request) {
	cfg := r.cfg.Get()
	out := []AliasView{}
	for _, a := range r.current().Aliases {
		rt := cfg.Routes[a.Route]
		out = append(out, AliasView{Alias: a, Protocols: rt.Protocols(), Provider: rt.ProviderName()})
	}
	writeJSON(w, http.StatusOK, out)
}

// putAliases replaces the whole alias list.
func (r *Router) putAliases(w http.ResponseWriter, req *http.Request) {
	var in []Alias
	if err := decode(req, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for i := range in {
		in[i].Name, in[i].Route = strings.TrimSpace(in[i].Name), strings.TrimSpace(in[i].Route)
		in[i].Model, in[i].Description = strings.TrimSpace(in[i].Model), strings.TrimSpace(in[i].Description)
	}
	if err := r.update(func(s *Settings, cfg config.Config) error {
		s.Aliases = in
		return s.validate(cfg)
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	r.getAliases(w, req)
}

// putOpenAIDefault sets the route for OpenAI-format requests that no alias
// or rule claims; {"name": ""} restores the credential-based default.
func (r *Router) putOpenAIDefault(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(req, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.cfg.SetDefaultOpenAIRoute(strings.TrimSpace(in.Name)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	r.getRoutes(w, req)
}

// DryRunResult explains how a logged request would be routed now.
type DryRunResult struct {
	RequestID string `json:"request_id"`
	*Evaluation
	// EffectiveRoute is what the proxy would use (the active route when OK=false).
	EffectiveRoute string `json:"effective_route"`
	IgnoreSticky   bool   `json:"ignore_sticky"`
	// Logged is what actually happened when the request went through.
	Logged struct {
		Route       string `json:"route"`
		RouteReason string `json:"route_reason,omitempty"`
	} `json:"logged"`
}

// DryRun evaluates a logged request with the same code as Route, without
// touching sticky state.
func (r *Router) DryRun(ctx context.Context, id string, ignoreSticky bool) (*DryRunResult, error) {
	preq, d, err := r.loadRequest(id)
	if err != nil {
		return nil, err
	}
	cfg := preq.Config
	ev := r.evaluate(ctx, preq, evalOpts{IgnoreSticky: ignoreSticky, DryRun: true})
	out := &DryRunResult{RequestID: id, Evaluation: ev, EffectiveRoute: cfg.ActiveRoute, IgnoreSticky: ignoreSticky}
	if ev.OK {
		out.EffectiveRoute = ev.Decision.Route
	}
	out.Logged.Route, out.Logged.RouteReason = d.Route, d.RouteReason
	return out, nil
}

// loadRequest rebuilds a logged request as the router saw it.
func (r *Router) loadRequest(id string) (*pipeline.Request, *store.Detail, error) {
	d, err := r.st.Get(id)
	if err != nil {
		return nil, nil, err
	}
	if d.RequestBody == "" {
		return nil, nil, errNoBody
	}
	body := []byte(d.RequestBody)
	h := http.Header{}
	for k, v := range d.RequestHeaders {
		h.Set(k, v)
	}
	preq := &pipeline.Request{ID: id, Protocol: d.Protocol, Body: body, Headers: h, Config: r.cfg.Get(),
		ConversationID: d.ConversationID, KeyID: d.KeyID}
	if d.Client != nil {
		preq.ClientKind = d.Client.Kind
	}
	if preq.ConversationID == "" {
		preq.ConversationID = pipeline.ConversationIDFor(d.Protocol, h, body)
	}
	if x, err := ir.ParseFor(d.Protocol, body); err == nil {
		preq.XRay = x
	}
	return preq, d, nil
}

var errNoBody = errors.New("this request's body was not logged (log_bodies is off), so it cannot be replayed")

func (r *Router) dryRun(w http.ResponseWriter, req *http.Request) {
	var in struct {
		RequestID    string `json:"request_id"`
		IgnoreSticky bool   `json:"ignore_sticky"`
	}
	if err := decode(req, &in); err != nil || in.RequestID == "" {
		writeError(w, http.StatusBadRequest, "request_id is required")
		return
	}
	res, err := r.DryRun(req.Context(), in.RequestID, in.IgnoreSticky)
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such request")
	case errors.Is(err, errNoBody):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

func (r *Router) listConversations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, r.sticky.list(r.current().ttl()))
}

func (r *Router) deleteConversation(w http.ResponseWriter, req *http.Request) {
	if !r.sticky.delete(req.PathValue("id")) {
		writeError(w, http.StatusNotFound, "no sticky assignment for that conversation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (r *Router) clearConversations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"removed": r.sticky.clear()})
}

func decode(req *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, req.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
