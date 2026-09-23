// Package adapters mounts the OpenAI-format endpoints: Codex (Responses
// API), OpenCode's OpenAI providers and any app using an OpenAI SDK (Chat
// Completions or Responses).
//
// Model calls run through the same engine as the Anthropic path
// (internal/proxy): aliases and routing rules, pruning, route shaping, usage
// and X-ray. When nothing routes a request elsewhere, it is a passthrough:
// the client keeps its own login and the gateway forwards it untouched to
// one of two upstreams:
//
//   - openai_base_url (default https://api.openai.com/v1) for API keys;
//   - chatgpt_base_url (default https://chatgpt.com/backend-api/codex) for a
//     ChatGPT subscription login. Codex sends that login (an OAuth access
//     token plus a ChatGPT-Account-ID header) to whatever base_url a custom
//     provider with requires_openai_auth = true names, so the gateway can
//     forward it to the ChatGPT backend. See the README for sources.
//
// That upstream is picked per request from the credential the client sent.
// A default_openai_route in the config replaces it.
//
// Endpoints:
//
//	POST /v1/responses, POST /v1/chat/completions   plain OpenAI base URL
//	POST /openai/v1/responses, .../chat/completions  the same under the prefix
//	GET  /openai/v1/models                           model aliases (see proxy.Models)
//	POST /v1/embeddings, /openai/v1/...              everything else, passed through
//	GET  /api/adapters, PUT /api/adapters            settings
//	GET  /api/agents, POST /api/agents/{name}/setup|undo   agent setup (see agents.go)
package adapters

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/proxy"
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
	cfg *config.Store
	st  *store.Store
	px  *proxy.Proxy
}

// New builds the adapters with an engine of their own (no pipeline hooks).
// The gateway shares its main engine through UseEngine.
func New(cfg *config.Store, st *store.Store) *Adapters {
	a := &Adapters{cfg: cfg, st: st}
	a.UseEngine(proxy.New(cfg, st))
	return a
}

// UseEngine makes OpenAI-format calls run through px (and its hooks), and
// gives px this package's fallback upstreams.
func (a *Adapters) UseEngine(px *proxy.Proxy) {
	px.OpenAIDefault = a.Fallback
	a.px = px
}

func (a *Adapters) engine() *proxy.Proxy { return a.px }

// Register mounts the adapter endpoints and their dashboard API.
func (a *Adapters) Register(mux *http.ServeMux) {
	chat, responses := a.serveModel(ir.ProtocolOpenAIChat), a.serveModel(ir.ProtocolOpenAIResponses)
	mux.HandleFunc("POST /v1/responses", responses)
	mux.HandleFunc("POST /v1/chat/completions", chat)
	mux.HandleFunc("POST "+Prefix+"/v1/responses", responses)
	mux.HandleFunc("POST "+Prefix+"/v1/chat/completions", chat)
	mux.HandleFunc("POST "+Prefix+"/v1/responses/compact", responses)
	mux.HandleFunc("GET "+Prefix+"/v1/models", a.serveModels)
	mux.HandleFunc("POST /v1/embeddings", a.passthrough)
	mux.HandleFunc(Prefix+"/", a.passthrough)

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
