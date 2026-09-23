// Package adapters serves agents that do not speak the Anthropic Messages
// API: Codex (OpenAI Responses API) and OpenCode's OpenAI providers (Chat
// Completions or Responses).
//
// Like the Anthropic path, this is a passthrough: the agent keeps its own
// login and the gateway forwards the client's credentials untouched. Two
// upstreams are known:
//
//   - openai_base_url (default https://api.openai.com/v1) for API keys;
//   - chatgpt_base_url (default https://chatgpt.com/backend-api/codex) for a
//     ChatGPT subscription login. Codex sends that login (an OAuth access
//     token plus a ChatGPT-Account-ID header) to whatever base_url a custom
//     provider with requires_openai_auth = true names, so the gateway can
//     forward it to the ChatGPT backend. See the README for sources.
//
// The upstream is picked per request from the credential the client sent.
// Every call is stored like an Anthropic one, with an X-ray of the request
// (see xray.go). Pruning and routing do not apply to these formats yet.
//
// Endpoints:
//
//	POST /v1/responses, POST /v1/chat/completions   plain OpenAI base URL
//	/openai/v1/...                                  everything under it (models, compact, ...)
//	GET  /api/adapters, PUT /api/adapters            settings
//	GET  /api/agents, POST /api/agents/{name}/setup|undo   agent setup (see agents.go)
package adapters

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const (
	DefaultOpenAIBaseURL  = "https://api.openai.com/v1"
	DefaultChatGPTBaseURL = "https://chatgpt.com/backend-api/codex"

	// Prefix is the base path agents are pointed at: <gateway>/openai/v1.
	// Using a prefix of our own keeps OpenAI credentials away from the
	// Anthropic passthrough that serves the rest of /v1.
	Prefix = "/openai"

	sectionName = "adapters"
)

// Settings is the "adapters" config section.
type Settings struct {
	OpenAIBaseURL  string `json:"openai_base_url"`
	ChatGPTBaseURL string `json:"chatgpt_base_url"`
}

func (s Settings) withDefaults() Settings {
	if s.OpenAIBaseURL == "" {
		s.OpenAIBaseURL = DefaultOpenAIBaseURL
	}
	if s.ChatGPTBaseURL == "" {
		s.ChatGPTBaseURL = DefaultChatGPTBaseURL
	}
	return s
}

type Adapters struct {
	cfg    *config.Store
	st     *store.Store
	Client *http.Client
}

func New(cfg *config.Store, st *store.Store) *Adapters {
	return &Adapters{cfg: cfg, st: st, Client: &http.Client{
		// No overall timeout: a streamed turn can run for minutes. The
		// client's request context cancels it instead.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 10 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}}
}

// Register mounts the adapter endpoints and their dashboard API.
func (a *Adapters) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/responses", a.serveOpenAI)
	mux.HandleFunc("POST /v1/chat/completions", a.serveOpenAI)
	mux.HandleFunc(Prefix+"/", a.serveOpenAI)

	mux.HandleFunc("GET /api/adapters", a.getSettings)
	mux.HandleFunc("PUT /api/adapters", a.putSettings)
	a.registerAgents(mux)
}

// Settings returns the current settings with defaults applied.
func (a *Adapters) Settings() Settings {
	var s Settings
	if raw := a.cfg.Get().Section(sectionName); len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	return s.withDefaults()
}

func (a *Adapters) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Settings())
}

func (a *Adapters) putSettings(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&s); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	for _, u := range []string{s.OpenAIBaseURL, s.ChatGPTBaseURL} {
		if u == "" {
			continue
		}
		if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			writeJSONError(w, http.StatusBadRequest, "not an http(s) URL: "+u)
			return
		}
	}
	s.OpenAIBaseURL = strings.TrimRight(s.OpenAIBaseURL, "/")
	s.ChatGPTBaseURL = strings.TrimRight(s.ChatGPTBaseURL, "/")
	raw, _ := json.Marshal(s)
	if err := a.cfg.SetSection(sectionName, raw); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.withDefaults())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError uses the dashboard API's error shape ({"error": "..."}).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
