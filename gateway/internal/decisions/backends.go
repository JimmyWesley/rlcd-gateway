package decisions

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// Default models when a caller names a backend but no model.
const (
	defaultJevModel      = "jev-latest"
	defaultOpenRLCDModel = "Open-RLCD-text"
)

// BackendNames lists the backends a gateway feature may call: economy
// first, then the configured ones by name.
func BackendNames(c config.Config) []string {
	out := []string{Economy}
	s, err := settingsFrom(c)
	if err != nil {
		return out
	}
	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return append(out, names...)
}

// SelectorFor builds the client settings for a call the gateway itself
// makes to a decisions backend (the router's decision rules): the
// backend's base URL and token, and model (or a default for the backend:
// the economy model's own, an exact model mapped to the backend, or the
// provider's usual one). provider names who serves it.
//
// A passthrough backend is refused: it forwards the client's own
// credentials, and a call the gateway makes on its own has none.
func SelectorFor(c config.Config, backend, model string) (sel config.Selector, provider string, err error) {
	if backend == "" || backend == Economy {
		sel = c.Selector
		if model != "" {
			sel.Model = model
		}
		if sel.BaseURL == "" {
			return sel, "", fmt.Errorf("the economy model has no base_url configured")
		}
		return sel, economy(c).ProviderName(), nil
	}
	s, err := settingsFrom(c)
	if err != nil {
		return sel, "", err
	}
	b, ok := s.Backends[backend]
	if !ok {
		return sel, "", fmt.Errorf("decisions backend %q does not exist (known: %s)", backend, strings.Join(BackendNames(c), ", "))
	}
	if b.Auth == AuthPassthrough {
		return sel, "", fmt.Errorf("decisions backend %q forwards the client's own credentials (auth passthrough); "+
			"a decision rule needs a backend with auth \"key\" and a token or token_env", backend)
	}
	if model == "" {
		model = defaultModel(s, backend, b)
	}
	return config.Selector{Backend: backend, BaseURL: b.BaseURL, Token: b.ResolvedToken(), Model: model}, b.ProviderName(), nil
}

// CheckBackend reports whether the gateway may call backend on its own:
// it exists and is not a passthrough backend. An economy model without a
// base URL passes (it fails at call time, like the auto rule).
func CheckBackend(c config.Config, backend string) error {
	if backend == "" || backend == Economy {
		return nil
	}
	_, _, err := SelectorFor(c, backend, "x")
	return err
}

// defaultModel is the first exact model mapped to the backend, else the
// provider's usual model.
func defaultModel(s Settings, name string, b Backend) string {
	var mapped []string
	for m, to := range s.Models {
		if to == name && !strings.HasSuffix(m, "*") {
			mapped = append(mapped, m)
		}
	}
	if len(mapped) > 0 {
		sort.Strings(mapped)
		return mapped[0]
	}
	if b.ProviderName() == "typesafe" {
		return defaultJevModel
	}
	return defaultOpenRLCDModel
}
