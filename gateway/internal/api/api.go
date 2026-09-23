// Package api is the dashboard's JSON API, served under /api on the same
// port as the proxy. Secrets never leave through it: routes and selector
// report whether a key is set, not the key.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type API struct {
	Config *config.Store
	Store  *store.Store
	// Listen is the address actually bound, which -listen can override.
	Listen string
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/requests", a.listRequests)
	mux.HandleFunc("GET /api/requests/{id}", a.getRequest)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/stats", a.stats)
	mux.HandleFunc("GET /api/config", a.getConfig)
	mux.HandleFunc("PUT /api/route", a.setRoute)
	mux.HandleFunc("PUT /api/selector", a.setSelector)
	mux.HandleFunc("GET /api/selector/presets", a.selectorPresets)
	mux.HandleFunc("POST /api/selector/test", a.testSelector)
}

func (a *API) listRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Store.Recent())
}

func (a *API) getRequest(w http.ResponseWriter, r *http.Request) {
	d, err := a.Store.Get(r.PathValue("id"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "no such request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// events streams each finished request to the dashboard as it happens.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := a.Store.Subscribe()
	defer cancel()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case rec := <-ch:
			b, _ := json.Marshal(rec)
			fmt.Fprintf(w, "event: request\ndata: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

type totals struct {
	Requests            int `json:"requests"`
	Errors              int `json:"errors"`
	EstTokens           int `json:"est_tokens"`
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	var t totals
	byRoute := map[string]*totals{}
	for _, rec := range a.Store.Recent() {
		rt := byRoute[rec.Route]
		if rt == nil {
			rt = &totals{}
			byRoute[rec.Route] = rt
		}
		for _, x := range []*totals{&t, rt} {
			x.Requests++
			if rec.Status >= 400 || rec.Error != "" {
				x.Errors++
			}
			x.EstTokens += rec.EstTokens
			if u := rec.Usage; u != nil {
				x.InputTokens += u.InputTokens
				x.OutputTokens += u.OutputTokens
				x.CacheReadTokens += u.CacheReadTokens
				x.CacheCreationTokens += u.CacheCreationTokens
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": t, "by_route": byRoute})
}

type routeView struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	Auth      string `json:"auth"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	HasKey    bool   `json:"has_key"`
}

type selectorView struct {
	Backend  string `json:"backend"`
	BaseURL  string `json:"base_url"`
	TokenEnv string `json:"token_env,omitempty"`
	Model    string `json:"model"`
	HasToken bool   `json:"has_token"`
}

func viewSelector(s config.Selector) selectorView {
	return selectorView{Backend: s.Backend, BaseURL: s.BaseURL, TokenEnv: s.TokenEnv,
		Model: s.Model, HasToken: s.ResolvedToken() != ""}
}

func (a *API) getConfig(w http.ResponseWriter, r *http.Request) {
	c := a.Config.Get()
	routes := []routeView{}
	for _, name := range c.RouteNames() {
		rt := c.Routes[name]
		routes = append(routes, routeView{Name: name, Kind: rt.Kind, BaseURL: rt.BaseURL, Auth: rt.Auth,
			Model: rt.Model, APIKeyEnv: rt.APIKeyEnv, HasKey: rt.ResolvedKey() != ""})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"listen": a.Listen, "active_route": c.ActiveRoute, "routes": routes,
		"selector": viewSelector(c.Selector), "config_path": config.Path(), "log_bodies": c.LogBodies,
	})
}

func (a *API) setRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.Config.SetActiveRoute(in.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"active_route": in.Name})
}

// setSelector updates the economy-model backend. An empty token keeps the
// current one, so the form never has to echo a secret back.
func (a *API) setSelector(w http.ResponseWriter, r *http.Request) {
	var in struct {
		config.Selector
		ClearToken bool `json:"clear_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := a.Config.Get().Selector
	sel := in.Selector
	// Only carry the old token to the same endpoint: switching from Jev to a
	// local server must not start sending the TypeSafe token there.
	if sel.Token == "" && !in.ClearToken && sel.Backend == cur.Backend && sel.BaseURL == cur.BaseURL {
		sel.Token = cur.Token
	}
	if err := a.Config.SetSelector(sel); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewSelector(sel))
}

func (a *API) selectorPresets(w http.ResponseWriter, r *http.Request) {
	out := []selectorView{}
	for _, b := range []string{config.SelectorOpenRLCDLocal, config.SelectorOpenRLCDCloud, config.SelectorJev} {
		out = append(out, viewSelector(config.SelectorPreset(b)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) testSelector(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := selector.New(a.Config.Get().Selector).Probe(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
