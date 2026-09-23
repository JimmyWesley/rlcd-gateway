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
	"strings"
	"sync"
)

// Auth modes for a route.
const (
	AuthPassthrough = "passthrough" // forward the client's Authorization / x-api-key untouched
	AuthKey         = "key"         // drop client credentials, use the route's key
)

// Route kinds. The kind decides which protocol the upstream speaks and how
// requests are shaped (auth header, thinking blocks).
const (
	// KindAnthropic is the Anthropic Messages API (api.anthropic.com).
	KindAnthropic = "anthropic"
	// KindOpenRouter is OpenRouter's Anthropic-compatible Messages endpoint
	// (base URL https://openrouter.ai/api).
	KindOpenRouter = "openrouter"
	// KindOpenAI is any OpenAI-compatible API: OpenAI, OpenRouter, Groq,
	// Together, DeepSeek, Mistral, Ollama, vLLM, LM Studio... The base URL
	// is the one an OpenAI SDK takes (it usually ends in /v1); the gateway
	// appends /chat/completions or /responses.
	KindOpenAI = "openai"
)

// Protocol families a route can speak (see ir.Protocol*).
const (
	protoAnthropic       = "anthropic-messages"
	protoOpenAIChat      = "openai-chat"
	protoOpenAIResponses = "openai-responses"
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
	// Provider names who serves the route (anthropic, openrouter, openai,
	// groq, ...), for the dashboard. Empty means "derive it from base_url".
	Provider string `json:"provider,omitempty"`
	// Headers are added to every upstream request on this route, e.g.
	// OpenRouter's HTTP-Referer and X-Title. They are shown in the dashboard,
	// so they are not the place for secrets: keys go in api_key.
	Headers map[string]string `json:"headers,omitempty"`
}

// Protocols lists the request formats the route's upstream speaks. The
// gateway never translates between formats.
func (r Route) Protocols() []string {
	if r.Kind == KindOpenAI {
		return []string{protoOpenAIChat, protoOpenAIResponses}
	}
	return []string{protoAnthropic}
}

// Speaks reports whether the route accepts requests in protocol ("" means
// Anthropic Messages).
func (r Route) Speaks(protocol string) bool {
	if protocol == "" {
		protocol = protoAnthropic
	}
	for _, p := range r.Protocols() {
		if p == protocol {
			return true
		}
	}
	return false
}

// reservedHeaders may not be set through Route.Headers: the gateway owns
// credentials and the transport owns framing.
var reservedHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "x-rlcd-key": true, "cookie": true, "host": true,
	"content-length": true, "content-type": true, "content-encoding": true, "transfer-encoding": true,
	"connection": true, "keep-alive": true, "te": true, "trailer": true, "upgrade": true,
	"proxy-authorization": true, "proxy-connection": true, "accept-encoding": true,
}

// ValidateHeaders checks a route's extra headers.
func ValidateHeaders(h map[string]string) error {
	for k, v := range h {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") {
			return fmt.Errorf("header name %q is not valid", k)
		}
		if reservedHeaders[strings.ToLower(k)] {
			return fmt.Errorf("header %q is set by the gateway and cannot be configured", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("header %q: value contains a line break", k)
		}
	}
	return nil
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
	// DefaultOpenAIRoute serves OpenAI-format requests that no alias or rule
	// claims. Empty keeps the F3 behavior: forward to OpenAI, or to the
	// ChatGPT backend for a ChatGPT login (the "adapters" section).
	DefaultOpenAIRoute string `json:"default_openai_route,omitempty"`
	// RequireKeys makes a gateway key mandatory on the proxy paths even on
	// loopback. Listening beyond loopback always requires one.
	RequireKeys bool `json:"require_keys,omitempty"`
	// AllowedHosts are extra Host names the gateway answers to when it
	// listens beyond loopback (e.g. "gateway.lan"). IP addresses and
	// localhost are always accepted; any other name is refused, which is
	// what stops DNS rebinding.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	// Sections holds each feature package's own settings, keyed by package
	// ("prune", "router", "recall", ...). Packages parse their own schema and
	// write through SetSection, so config.go never has to know about them.
	Sections map[string]json.RawMessage `json:"sections,omitempty"`
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
	// Unmarshal merges into existing maps, so start routes empty: a route the
	// user deleted must not come back from the defaults on the next start.
	cfg.Routes = nil
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	if cfg.Routes == nil {
		cfg.Routes = Default().Routes
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	return &Store{cfg: cfg}, nil
}

func (c *Config) validate() error {
	active, ok := c.Routes[c.ActiveRoute]
	if !ok {
		return fmt.Errorf("active_route %q is not in routes", c.ActiveRoute)
	}
	if !active.Speaks(protoAnthropic) {
		return fmt.Errorf("active_route %q speaks the OpenAI format; the active route serves Anthropic Messages requests (set default_openai_route for OpenAI-format traffic)", c.ActiveRoute)
	}
	if c.DefaultOpenAIRoute != "" {
		r, ok := c.Routes[c.DefaultOpenAIRoute]
		if !ok {
			return fmt.Errorf("default_openai_route %q is not in routes", c.DefaultOpenAIRoute)
		}
		if !r.Speaks(protoOpenAIChat) {
			return fmt.Errorf("default_openai_route %q speaks Anthropic Messages; OpenAI-format requests need a route of kind %q (cross-protocol translation is not supported)", c.DefaultOpenAIRoute, KindOpenAI)
		}
	}
	for name, r := range c.Routes {
		if r.BaseURL == "" {
			return fmt.Errorf("route %q has no base_url", name)
		}
		if r.Auth != AuthPassthrough && r.Auth != AuthKey {
			return fmt.Errorf("route %q: auth must be %q or %q", name, AuthPassthrough, AuthKey)
		}
		if err := ValidateHeaders(r.Headers); err != nil {
			return fmt.Errorf("route %q: %w", name, err)
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
	c.AllowedHosts = append([]string(nil), s.cfg.AllowedHosts...)
	c.Sections = make(map[string]json.RawMessage, len(s.cfg.Sections))
	for k, v := range s.cfg.Sections {
		c.Sections[k] = v
	}
	return c
}

// Section returns a feature's raw settings (nil if never saved).
func (c Config) Section(name string) json.RawMessage { return c.Sections[name] }

// SetSection replaces a feature's settings and persists the config.
func (s *Store) SetSection(name string, raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Sections == nil {
		s.cfg.Sections = map[string]json.RawMessage{}
	}
	s.cfg.Sections[name] = raw
	return s.saveLocked()
}

// UpsertRoute adds or replaces a route and persists the config.
func (s *Store) UpsertRoute(name string, r Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.cfg
	next.Routes = map[string]Route{}
	for k, v := range s.cfg.Routes {
		next.Routes[k] = v
	}
	next.Routes[name] = r
	if err := next.validate(); err != nil {
		return err
	}
	s.cfg.Routes = next.Routes
	return s.saveLocked()
}

// DeleteRoute removes a route; the active and default OpenAI routes cannot be deleted.
func (s *Store) DeleteRoute(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == s.cfg.ActiveRoute {
		return fmt.Errorf("cannot delete the active route %q", name)
	}
	if name == s.cfg.DefaultOpenAIRoute {
		return fmt.Errorf("cannot delete %q: it is the default OpenAI route", name)
	}
	delete(s.cfg.Routes, name)
	return s.saveLocked()
}

func (s *Store) SetActiveRoute(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.cfg.Routes[name]
	if !ok {
		return fmt.Errorf("unknown route %q", name)
	}
	if !r.Speaks(protoAnthropic) {
		return fmt.Errorf("route %q speaks the OpenAI format; the active route serves Anthropic Messages requests. Make it the default OpenAI route instead", name)
	}
	s.cfg.ActiveRoute = name
	return s.saveLocked()
}

// SetDefaultOpenAIRoute picks the route for OpenAI-format requests that no
// alias or rule claims; "" restores the credential-based default.
func (s *Store) SetDefaultOpenAIRoute(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.cfg
	next.DefaultOpenAIRoute = name
	if err := next.validate(); err != nil {
		return err
	}
	s.cfg.DefaultOpenAIRoute = name
	return s.saveLocked()
}

// SetRequireKeys turns mandatory gateway keys on or off.
func (s *Store) SetRequireKeys(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.RequireKeys = on
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
