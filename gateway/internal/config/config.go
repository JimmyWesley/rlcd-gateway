// Package config loads and saves the gateway configuration.
//
// Two independent things are configured here:
//
//   - Routes: where the *agent's* request goes (the LLM that does the work).
//     A route either passes the client's own credentials through (e.g. a
//     Claude Code subscription) or swaps them for a key the gateway holds
//     (e.g. OpenRouter).
//   - Selector: where the *economy model* runs — the System One model that
//     decides what stays in the context. Jev and open-rlcd share the same
//     API (POST /v1/systemone), so this is one client with a base URL, a
//     token and a model name.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Auth modes for a route.
const (
	AuthPassthrough = "passthrough" // forward the client's Authorization / x-api-key untouched
	AuthKey         = "key"         // drop client credentials, use the route's key
)

// Route kinds. The kind decides request shaping (headers, thinking blocks).
const (
	KindAnthropic  = "anthropic"
	KindOpenRouter = "openrouter"
)

// Selector backends. They only differ in defaults; the wire protocol is the same.
const (
	SelectorOpenRLCDLocal = "open-rlcd-local" // you run open-rlcd yourself and point at it
	SelectorOpenRLCDCloud = "open-rlcd-cloud" // hosted open-rlcd, needs a token
	SelectorJev           = "jev"             // TypeSafe's hosted Jev, needs a token
)

type Route struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	Auth    string `json:"auth"`
	// Key used when Auth == "key". APIKeyEnv wins over APIKey when set and non-empty.
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Model, when set, replaces the "model" field of every request on this route.
	Model string `json:"model,omitempty"`
}

// ResolvedKey returns the key for Auth == "key" routes.
func (r Route) ResolvedKey() string {
	if r.APIKeyEnv != "" {
		if v := os.Getenv(r.APIKeyEnv); v != "" {
			return v
		}
	}
	return r.APIKey
}

type Selector struct {
	Backend  string `json:"backend"`
	BaseURL  string `json:"base_url"`
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
	Model    string `json:"model"`
}

func (s Selector) ResolvedToken() string {
	if s.TokenEnv != "" {
		if v := os.Getenv(s.TokenEnv); v != "" {
			return v
		}
	}
	return s.Token
}

type Config struct {
	Listen      string           `json:"listen"`
	ActiveRoute string           `json:"active_route"`
	Routes      map[string]Route `json:"routes"`
	Selector    Selector         `json:"selector"`
	// Upstream for every non-/v1/messages path (count_tokens, models, ...).
	// These calls carry the client's own credentials, so they always go to
	// the provider the client is actually logged into.
	PassthroughBaseURL string `json:"passthrough_base_url"`
	// Keep full request/response bodies on disk. Turn off to log summaries only.
	LogBodies bool `json:"log_bodies"`
}

// SelectorPreset returns the default selector settings for a backend.
func SelectorPreset(backend string) Selector {
	switch backend {
	case SelectorJev:
		return Selector{Backend: backend, BaseURL: "https://api.typesafe.ai", TokenEnv: "TYPESAFE_API_KEY", Model: "jev-latest"}
	case SelectorOpenRLCDCloud:
		return Selector{Backend: backend, BaseURL: "", TokenEnv: "OPEN_RLCD_API_KEY", Model: "Open-RLCD-text"}
	default:
		return Selector{Backend: SelectorOpenRLCDLocal, BaseURL: "http://127.0.0.1:8000", Model: "Open-RLCD-text"}
	}
}

func Default() *Config {
	return &Config{
		Listen:      "127.0.0.1:4777",
		ActiveRoute: "claude-sub",
		Routes: map[string]Route{
			"claude-sub": {Kind: KindAnthropic, BaseURL: "https://api.anthropic.com", Auth: AuthPassthrough},
			"openrouter": {Kind: KindOpenRouter, BaseURL: "https://openrouter.ai/api", Auth: AuthKey,
				APIKeyEnv: "OPENROUTER_API_KEY", Model: "anthropic/claude-sonnet-4.5"},
		},
		Selector:           SelectorPreset(SelectorOpenRLCDLocal),
		PassthroughBaseURL: "https://api.anthropic.com",
		LogBodies:          true,
	}
}

// Dir is where config and logs live: $RLCD_GATEWAY_HOME or ~/.rlcd-gateway.
func Dir() string {
	if d := os.Getenv("RLCD_GATEWAY_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".rlcd-gateway"
	}
	return filepath.Join(home, ".rlcd-gateway")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

// Store guards the live config: the dashboard can switch routes while
// requests are in flight.
type Store struct {
	mu  sync.RWMutex
	cfg *Config
}

// NewStore wraps a config without touching disk until something changes it.
func NewStore(c *Config) *Store { return &Store{cfg: c} }

// Load reads the config file, writing the defaults first if there is none.
func Load() (*Store, error) {
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		s := &Store{cfg: Default()}
		return s, s.save()
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	return &Store{cfg: cfg}, nil
}

func (c *Config) validate() error {
	if _, ok := c.Routes[c.ActiveRoute]; !ok {
		return fmt.Errorf("active_route %q is not in routes", c.ActiveRoute)
	}
	for name, r := range c.Routes {
		if r.BaseURL == "" {
			return fmt.Errorf("route %q has no base_url", name)
		}
		if r.Auth != AuthPassthrough && r.Auth != AuthKey {
			return fmt.Errorf("route %q: auth must be %q or %q", name, AuthPassthrough, AuthKey)
		}
	}
	return nil
}

// Get returns a copy, safe to read without holding the lock.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.cfg
	c.Routes = make(map[string]Route, len(s.cfg.Routes))
	for k, v := range s.cfg.Routes {
		c.Routes[k] = v
	}
	return c
}

func (s *Store) SetActiveRoute(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cfg.Routes[name]; !ok {
		return fmt.Errorf("unknown route %q", name)
	}
	s.cfg.ActiveRoute = name
	return s.saveLocked()
}

func (s *Store) SetSelector(sel Selector) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Selector = sel
	return s.saveLocked()
}

func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// RouteNames returns route names in stable order, for the dashboard.
func (c Config) RouteNames() []string {
	names := make([]string, 0, len(c.Routes))
	for k := range c.Routes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
