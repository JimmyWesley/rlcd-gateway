package setup

// OpenCode reads a global config (~/.config/opencode/opencode.json, or under
// $XDG_CONFIG_HOME), then $OPENCODE_CONFIG, then the project's
// opencode.json, then .opencode directories and $OPENCODE_CONFIG_CONTENT;
// later sources override earlier ones key by key
// (https://opencode.ai/docs/config/).
//
// Setup sets provider.<id>.options.baseURL in the global file
// (https://opencode.ai/docs/providers/):
//
//   - anthropic -> <gateway>/v1. The AI SDK appends /messages, so OpenCode
//     rides the gateway's Anthropic path and gets everything it does;
//   - openai    -> <gateway>/openai/v1 (Responses API), for OpenAI API keys.
//
// OpenCode's ChatGPT Plus/Pro login is served by a plugin whose fetch
// rewrites every request to https://chatgpt.com/backend-api/codex/responses,
// ignoring baseURL, so subscription traffic from OpenCode cannot be pointed
// at the gateway (only a TLS-intercepting proxy could see it; we do not
// build one).
//
// OpenCode also accepts JSONC. Rewriting a file with comments would drop
// them, so setup refuses a file that is not plain JSON.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	opencodeAnthropicPath = []string{"provider", "anthropic", "options", "baseURL"}
	opencodeOpenAIPath    = []string{"provider", "openai", "options", "baseURL"}
)

func opencodeConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func opencodeDataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}

// opencodeConfigPath is opencode.json, or opencode.jsonc when only that exists.
func opencodeConfigPath() (string, error) {
	d, err := opencodeConfigDir()
	if err != nil {
		return "", err
	}
	plain := filepath.Join(d, "opencode.json")
	withComments := filepath.Join(d, "opencode.jsonc")
	if _, err := os.Stat(plain); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(withComments); err == nil {
			return withComments, nil
		}
	}
	return plain, nil
}

func opencodeURLs(gatewayURL string) (anthropic, openai string) {
	g := strings.TrimRight(gatewayURL, "/")
	return g + "/v1", g + "/openai/v1"
}

// OpenCodeTryCommand runs OpenCode through the gateway once, without editing its config.
func OpenCodeTryCommand(gatewayURL string) string {
	a, o := opencodeURLs(gatewayURL)
	return `OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"` + a +
		`"}},"openai":{"options":{"baseURL":"` + o + `"}}}}' opencode`
}

// OpenCode points OpenCode's anthropic and openai providers at the gateway.
func OpenCode(gatewayURL string) (string, error) {
	path, err := opencodeConfigPath()
	if err != nil {
		return "", err
	}
	a, o := opencodeURLs(gatewayURL)
	return path, applyJSON("opencode", path, []jsonSet{
		{Path: opencodeAnthropicPath, Value: a},
		{Path: opencodeOpenAIPath, Value: o},
	}, gatewayValue(gatewayURL))
}

// UndoOpenCode restores the previous baseURL values.
func UndoOpenCode(gatewayURL string) (path, what string, err error) {
	if path, err = opencodeConfigPath(); err != nil {
		return "", "", err
	}
	what, err = undoJSON("opencode", path, [][]string{opencodeAnthropicPath, opencodeOpenAIPath}, gatewayValue(gatewayURL))
	return path, what, err
}

// readLenient parses JSON or JSONC for reading only (status); comments and
// trailing commas are stripped. Never used to write.
func readLenient(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var obj map[string]any
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, true, nil
	}
	if err := json.Unmarshal(stripJSONC(b), &obj); err != nil {
		return nil, true, err
	}
	return obj, true, nil
}

// stripJSONC removes // and /* */ comments and trailing commas outside strings.
func stripJSONC(b []byte) []byte {
	var out []byte
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			// Drop a trailing comma before the closing bracket.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func opencodeStatus(gatewayURL, project string) AgentStatus {
	st := AgentStatus{
		Name: "opencode", Title: "OpenCode",
		Changes:      "Sets provider.anthropic.options.baseURL and provider.openai.options.baseURL in your global OpenCode config.",
		TryCommand:   OpenCodeTryCommand(gatewayURL),
		SetupCommand: "rlcd-gateway setup opencode", UndoCommand: "rlcd-gateway undo opencode",
	}
	path, err := opencodeConfigPath()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.ConfigPath = path
	st.Binary, st.Version = findBinary("opencode")
	_, dirErr := os.Stat(filepath.Dir(path))
	st.Installed = st.Binary != "" || dirErr == nil

	type layer struct{ scope, path string }
	layers := []layer{{"global", path}}
	if c := os.Getenv("OPENCODE_CONFIG"); c != "" {
		layers = append(layers, layer{"custom (OPENCODE_CONFIG)", c})
	}
	if project != "" {
		layers = append(layers, layer{"project", filepath.Join(project, "opencode.json")},
			layer{"project", filepath.Join(project, "opencode.jsonc")})
	}
	// Later layers override earlier ones.
	for _, l := range layers {
		obj, exists, err := readLenient(l.path)
		src := Source{Scope: l.scope, Path: l.path, Exists: exists}
		if err != nil {
			src.Error = err.Error()
			st.Warnings = append(st.Warnings, l.path+": "+err.Error())
		}
		if v, ok := lookup(obj, opencodeAnthropicPath); ok {
			if s, isStr := v.(string); isStr {
				src.Set, src.BaseURL, src.Gateway = true, redactURL(s), pointsAt(s, gatewayURL)
				st.EffectiveSource, st.Current, st.Effective = l.scope, src.BaseURL, src.Gateway
			}
		}
		if l.scope == "global" {
			st.ConfigExists = exists
			st.Configured = src.Gateway
			if exists && err == nil && strings.HasSuffix(path, ".jsonc") {
				if _, _, _, strict := readJSONObject(path); strict != nil {
					st.Warnings = append(st.Warnings, "opencode.jsonc has comments; setup will refuse to rewrite it. Use the one-off command or edit it by hand.")
				}
			}
		}
		if exists || l.scope == "global" {
			st.Sources = append(st.Sources, src)
		}
	}
	if os.Getenv("OPENCODE_CONFIG_CONTENT") != "" {
		st.Notes = append(st.Notes, "OPENCODE_CONFIG_CONTENT is set in the gateway's environment and overrides config files.")
	}
	if st.Configured && !st.Effective {
		st.Warnings = append(st.Warnings, "The global config points at the gateway, but "+st.EffectiveSource+" config sets another anthropic baseURL, and that wins.")
	}
	if project == "" {
		st.Notes = append(st.Notes, "A project opencode.json overrides the global config; pass a project folder to check it.")
	}
	var chatgpt bool
	st.Auth, chatgpt = opencodeAuth()
	if chatgpt {
		st.Warnings = append(st.Warnings, "OpenCode's ChatGPT login sends requests straight to chatgpt.com (its plugin ignores baseURL), so that traffic cannot go through the gateway. OpenAI API keys and Anthropic do.")
	}
	return st
}

// opencodeAuth reports the credential types in OpenCode's auth.json (never
// values); chatgpt is true when OpenAI is a ChatGPT subscription login.
func opencodeAuth() (auth *Auth, chatgpt bool) {
	d, err := opencodeDataDir()
	if err != nil {
		return &Auth{Mode: "unknown", Label: "Not detected"}, false
	}
	b, err := os.ReadFile(filepath.Join(d, "auth.json"))
	if err != nil {
		return &Auth{Mode: "unknown", Label: "Not detected",
			Note: "No OpenCode credentials found (run /connect in OpenCode). Keys from environment variables are forwarded too."}, false
	}
	var entries map[string]struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &entries) != nil {
		return &Auth{Mode: "unknown", Label: "Not detected"}, false
	}
	var parts []string
	mode := "api-key"
	for _, id := range []string{"anthropic", "openai"} {
		e, ok := entries[id]
		if !ok {
			continue
		}
		kind := "API key"
		if e.Type == "oauth" {
			kind, mode = "subscription login", "subscription"
			chatgpt = chatgpt || id == "openai"
		}
		parts = append(parts, id+": "+kind)
	}
	if len(parts) == 0 {
		return &Auth{Mode: "unknown", Label: "No anthropic or openai credentials"}, false
	}
	return &Auth{Mode: mode, Label: strings.Join(parts, ", "), Source: "auth.json (" + strings.Join(parts, ", ") + ")",
		KeepsSubscription: mode == "subscription",
		Note:              "The gateway forwards whatever credential OpenCode sends."}, chatgpt
}
