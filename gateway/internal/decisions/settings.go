package decisions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// sectionName is the config section this package owns.
const sectionName = "decisions"

// Economy is the implicit backend built from the economy-model (selector)
// config. It serves every decision call when no backend is configured, and
// any name may map to it.
const Economy = "economy"

// Auth modes of a backend.
const (
	// AuthKey drops the client's credentials and sends the backend's token
	// (or none, for an open-rlcd that needs none).
	AuthKey = "key"
	// AuthPassthrough forwards the client's own Authorization (its
	// TypeSafe key, say).
	AuthPassthrough = "passthrough"
)

// Backend is one System One server: TypeSafe's Jev or an open-rlcd.
type Backend struct {
	BaseURL  string `json:"base_url"`
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
	Auth     string `json:"auth"`
	// Provider names who serves it (typesafe, open-rlcd); empty derives it
	// from the base URL.
	Provider string `json:"provider,omitempty"`
}

// ResolvedToken is TokenEnv's value when set, else Token.
func (b Backend) ResolvedToken() string {
	if b.TokenEnv != "" {
		if v := os.Getenv(b.TokenEnv); v != "" {
			return v
		}
	}
	return b.Token
}

// ProviderName is the explicit provider, or typesafe for typesafe.ai and
// open-rlcd for any other System One server.
func (b Backend) ProviderName() string {
	if b.Provider != "" {
		return b.Provider
	}
	if config.ProviderFromURL(b.BaseURL) == "typesafe" {
		return "typesafe"
	}
	return "open-rlcd"
}

// Mirror sends a sampled copy of each client call to a second backend,
// after the client has its answer, to measure agreement (a drop-in parity
// audit). Off unless Backend is set and SampleRate > 0.
type Mirror struct {
	Backend    string  `json:"backend"`
	SampleRate float64 `json:"sample_rate"`
	// Model replaces the request's model on the mirror call ("jev-latest"
	// when mirroring open-rlcd traffic to Jev). Empty sends it unchanged.
	Model string `json:"model,omitempty"`
}

// Settings is the "decisions" config section.
type Settings struct {
	Backends map[string]Backend `json:"backends,omitempty"`
	// DefaultBackend serves models no mapping names; empty means Economy.
	DefaultBackend string `json:"default_backend,omitempty"`
	// Models maps a model name to a backend. A name ending in "*" is a
	// prefix ("jev-*"); an exact name wins, then the longest prefix.
	Models map[string]string `json:"models,omitempty"`
	Mirror *Mirror           `json:"mirror,omitempty"`
	// LogInternal logs the gateway's own economy-model calls (pruning, the
	// router's auto rule) as decisions. Default on.
	LogInternal *bool `json:"log_internal,omitempty"`
}

func (s Settings) logInternal() bool { return s.LogInternal == nil || *s.LogInternal }

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// Validate checks every field. It never mentions a token's value.
func (s Settings) Validate() error {
	for name, b := range s.Backends {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("backend name %q: use letters, digits, '.', '_' or '-' (at most 64)", name)
		}
		if name == Economy {
			return fmt.Errorf("backend name %q is reserved for the economy-model selector", Economy)
		}
		u, err := url.Parse(b.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("backend %q: base_url must be an http(s) URL", name)
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("backend %q: base_url must not carry a query or fragment", name)
		}
		switch b.Auth {
		case AuthKey:
		case AuthPassthrough:
			if b.Token != "" || b.TokenEnv != "" {
				return fmt.Errorf("backend %q: a passthrough backend forwards the client's credentials and takes no token", name)
			}
		default:
			return fmt.Errorf("backend %q: auth must be %q or %q", name, AuthKey, AuthPassthrough)
		}
		if b.TokenEnv != "" && !envRe.MatchString(b.TokenEnv) {
			return fmt.Errorf("backend %q: token_env %q is not an environment variable name", name, b.TokenEnv)
		}
		if strings.ContainsAny(b.Token, "\r\n") {
			return fmt.Errorf("backend %q: token contains a line break", name)
		}
		if b.Provider != "" && !nameRe.MatchString(b.Provider) {
			return fmt.Errorf("backend %q: provider %q is not a valid name", name, b.Provider)
		}
	}
	known := func(n string) bool { _, ok := s.Backends[n]; return ok || n == Economy }
	if s.DefaultBackend != "" && !known(s.DefaultBackend) {
		return fmt.Errorf("default_backend %q is not a backend", s.DefaultBackend)
	}
	for m, b := range s.Models {
		if strings.TrimSpace(m) == "" || m == "*" || strings.Count(m, "*") > 1 || (strings.Contains(m, "*") && !strings.HasSuffix(m, "*")) {
			return fmt.Errorf("model %q: use an exact name or a prefix ending in '*'", m)
		}
		if !known(b) {
			return fmt.Errorf("model %q maps to %q, which is not a backend", m, b)
		}
	}
	if mr := s.Mirror; mr != nil {
		if mr.SampleRate < 0 || mr.SampleRate > 1 {
			return errors.New("mirror.sample_rate must be between 0 and 1")
		}
		if mr.Backend == "" && mr.SampleRate > 0 {
			return errors.New("mirror.backend is required when mirror.sample_rate > 0")
		}
		if mr.Backend != "" && !known(mr.Backend) {
			return fmt.Errorf("mirror.backend %q is not a backend", mr.Backend)
		}
	}
	return nil
}

// settingsFrom reads the section; a section that does not parse or
// validate yields that error, so calls fail loudly instead of going to an
// unintended backend.
func settingsFrom(c config.Config) (Settings, error) {
	var s Settings
	raw := c.Section(sectionName)
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{}, fmt.Errorf("decisions config: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("decisions config: %w", err)
	}
	return s, nil
}

// economy is the implicit backend: the selector's base URL and token.
func economy(c config.Config) Backend {
	b := Backend{BaseURL: c.Selector.BaseURL, Token: c.Selector.ResolvedToken(), Auth: AuthKey}
	if c.Selector.Backend == config.SelectorJev {
		b.Provider = "typesafe"
	}
	return b
}

// backend returns a backend by name (Economy included).
func (s Settings) backend(c config.Config, name string) (Backend, bool) {
	if name == Economy {
		return economy(c), true
	}
	b, ok := s.Backends[name]
	return b, ok
}

// Resolve picks the backend for model: an exact mapping, the longest
// prefix mapping, else the default backend. reason explains the choice.
func (s Settings) Resolve(c config.Config, model string) (name string, b Backend, reason string, err error) {
	name, reason = s.DefaultBackend, "default backend"
	if name == "" {
		name, reason = Economy, "default backend (the economy-model selector)"
	}
	if to, ok := s.Models[model]; ok && model != "" {
		name, reason = to, fmt.Sprintf("model %q", model)
	} else {
		// Prefixes are distinct map keys, so the longest match is unique.
		bestLen := -1
		for m, to := range s.Models {
			if p, ok := strings.CutSuffix(m, "*"); ok && strings.HasPrefix(model, p) && len(p) > bestLen {
				bestLen, name, reason = len(p), to, fmt.Sprintf("model prefix %q", m)
			}
		}
	}
	b, ok := s.backend(c, name)
	if !ok {
		return name, b, reason, fmt.Errorf("decision backend %q does not exist", name)
	}
	if b.BaseURL == "" {
		return name, b, reason, fmt.Errorf("decision backend %q has no base_url (set the economy model, or add a decisions backend)", name)
	}
	return name, b, reason, nil
}

// BackendView is how the API shows a backend: never the token.
type BackendView struct {
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Auth     string `json:"auth"`
	TokenEnv string `json:"token_env,omitempty"`
	HasToken bool   `json:"has_token"`
	Provider string `json:"provider"`
	// Implicit is true for the economy backend, which comes from the
	// selector config and is edited there.
	Implicit bool `json:"implicit,omitempty"`
}

// SettingsView is GET/PUT /api/decisions/settings.
type SettingsView struct {
	Backends       []BackendView     `json:"backends"`
	DefaultBackend string            `json:"default_backend"`
	Models         map[string]string `json:"models"`
	Mirror         *Mirror           `json:"mirror"`
	LogInternal    bool              `json:"log_internal"`
	// Error is set when the stored section is invalid; decision calls are
	// refused until it is fixed.
	Error string `json:"error,omitempty"`
}

func view(c config.Config, s Settings) SettingsView {
	e := economy(c)
	v := SettingsView{DefaultBackend: s.DefaultBackend, Models: s.Models, Mirror: s.Mirror, LogInternal: s.logInternal(),
		Backends: []BackendView{{Name: Economy, BaseURL: e.BaseURL, Auth: AuthKey, TokenEnv: c.Selector.TokenEnv,
			HasToken: e.Token != "", Provider: e.ProviderName(), Implicit: true}}}
	if v.DefaultBackend == "" {
		v.DefaultBackend = Economy
	}
	if v.Models == nil {
		v.Models = map[string]string{}
	}
	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := s.Backends[n]
		v.Backends = append(v.Backends, BackendView{Name: n, BaseURL: b.BaseURL, Auth: b.Auth, TokenEnv: b.TokenEnv,
			HasToken: b.ResolvedToken() != "", Provider: b.ProviderName()})
	}
	return v
}

// backendInput is a backend in a PUT. An absent or empty token keeps the
// stored one; clear_token removes it.
type backendInput struct {
	BaseURL    string `json:"base_url"`
	Token      string `json:"token"`
	TokenEnv   string `json:"token_env"`
	Auth       string `json:"auth"`
	Provider   string `json:"provider"`
	ClearToken bool   `json:"clear_token"`
}

type settingsInput struct {
	Backends       map[string]backendInput `json:"backends"`
	DefaultBackend string                  `json:"default_backend"`
	Models         map[string]string       `json:"models"`
	Mirror         *Mirror                 `json:"mirror"`
	LogInternal    *bool                   `json:"log_internal"`
}

// merge turns a PUT into the settings to store, keeping stored tokens.
func (in settingsInput) merge(cur Settings) Settings {
	next := Settings{DefaultBackend: in.DefaultBackend, Models: in.Models, Mirror: in.Mirror, LogInternal: in.LogInternal}
	if next.DefaultBackend == Economy {
		next.DefaultBackend = ""
	}
	if len(in.Backends) > 0 {
		next.Backends = map[string]Backend{}
	}
	for n, b := range in.Backends {
		out := Backend{BaseURL: strings.TrimRight(strings.TrimSpace(b.BaseURL), "/"), Token: b.Token,
			TokenEnv: strings.TrimSpace(b.TokenEnv), Auth: b.Auth, Provider: b.Provider}
		if out.Auth == "" {
			out.Auth = AuthKey
		}
		if out.Token == "" && !b.ClearToken {
			out.Token = cur.Backends[n].Token
		}
		if out.Auth == AuthPassthrough {
			out.Token = ""
		}
		next.Backends[n] = out
	}
	if next.Mirror != nil && next.Mirror.Backend == "" && next.Mirror.SampleRate == 0 {
		next.Mirror = nil
	}
	return next
}
