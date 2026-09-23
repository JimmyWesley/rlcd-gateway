package setup

// Claude Code. Setup writes env.ANTHROPIC_BASE_URL into the user settings
// file (~/.claude/settings.json, or $CLAUDE_CONFIG_DIR/settings.json).
//
// What decides the base URL Claude Code actually uses
// (https://code.claude.com/docs/en/settings, .../env-vars):
//
//   - settings levels, highest first: managed (managed-settings.json and
//     managed-settings.d/ in the system directory, MDM, server-managed),
//     command line (--settings), project local (.claude/settings.local.json),
//     shared project (.claude/settings.json), user (~/.claude/settings.json);
//   - an `env` block follows those levels, and a settings `env` value wins
//     over the same variable exported in the shell ("the settings file value
//     applies"). A shell ANTHROPIC_BASE_URL only matters when no settings
//     file sets it.
//
// Which credential it sends (https://code.claude.com/docs/en/authentication):
// Bedrock/Vertex/Foundry, then ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY,
// apiKeyHelper, CLAUDE_CODE_OAUTH_TOKEN, Anthropic profiles, and last the
// subscription login from /login. Changing the base URL does not change
// that choice, so a subscription stays a subscription through the gateway.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const claudeEnvKey = "ANTHROPIC_BASE_URL"

var claudeBaseURLPath = []string{"env", claudeEnvKey}

func claudeConfigDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

func claudeSettingsPath() (string, error) {
	d, err := claudeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// claudeManagedDir is the system directory for managed settings. It is a
// variable so tests can point it at a temp dir.
var claudeManagedDir = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return "/etc/claude-code"
}

// ClaudeTryCommand runs Claude Code through the gateway once, without editing settings.
func ClaudeTryCommand(gatewayURL string) string {
	return claudeEnvKey + "=" + strings.TrimRight(gatewayURL, "/") + " claude"
}

// Claude sets env.ANTHROPIC_BASE_URL in the user settings file, keeping
// every other setting, writing a timestamped backup and remembering the
// previous value in the gateway's state.
func Claude(gatewayURL string) (string, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(gatewayURL, "/")
	return path, applyJSON("claude", path, []jsonSet{{Path: claudeBaseURLPath, Value: base}}, gatewayValue(gatewayURL))
}

// UndoClaude puts back the ANTHROPIC_BASE_URL that was there before setup
// (or removes it if there was none).
func UndoClaude(gatewayURL string) (path, what string, err error) {
	if path, err = claudeSettingsPath(); err != nil {
		return "", "", err
	}
	what, err = undoJSON("claude", path, [][]string{claudeBaseURLPath}, gatewayValue(gatewayURL))
	return path, what, err
}

// gatewayValue matches a JSON string value pointing at the gateway.
func gatewayValue(gatewayURL string) func(any) bool {
	return func(v any) bool {
		s, ok := v.(string)
		return ok && pointsAt(s, gatewayURL)
	}
}

// claudeLayer is one settings file as far as base URL and auth go.
type claudeLayer struct {
	scope, path string
	exists      bool
	err         string
	env         map[string]string
	helper      bool // apiKeyHelper is set
}

func readClaudeLayer(scope, path string) claudeLayer {
	l := claudeLayer{scope: scope, path: path, env: map[string]string{}}
	obj, _, missing, err := readJSONObject(path)
	if missing {
		return l
	}
	l.exists = true
	if err != nil {
		l.err = err.Error()
		return l
	}
	mergeClaude(&l, obj)
	return l
}

func mergeClaude(l *claudeLayer, obj map[string]any) {
	if env, ok := obj["env"].(map[string]any); ok {
		for k, v := range env {
			if s, ok := v.(string); ok {
				l.env[k] = s
			} else if v != nil {
				l.env[k] = strings.Trim(string(mustJSON(v)), `"`)
			}
		}
	}
	if h, ok := obj["apiKeyHelper"].(string); ok && h != "" {
		l.helper = true
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// readClaudeManaged merges managed-settings.json and managed-settings.d/*.json.
func readClaudeManaged() claudeLayer {
	dir := claudeManagedDir()
	main := filepath.Join(dir, "managed-settings.json")
	l := readClaudeLayer("managed", main)
	drop, _ := filepath.Glob(filepath.Join(dir, "managed-settings.d", "*.json"))
	sort.Strings(drop)
	for _, f := range drop {
		d := readClaudeLayer("managed", f)
		if !d.exists {
			continue
		}
		if !l.exists {
			l.path = f
		}
		l.exists = true
		if d.err != "" && l.err == "" {
			l.err = d.err
		}
		for k, v := range d.env {
			l.env[k] = v
		}
		l.helper = l.helper || d.helper
	}
	return l
}

func claudeStatus(gatewayURL, project string) AgentStatus {
	st := AgentStatus{
		Name: "claude", Title: "Claude Code",
		Changes:      "Sets env.ANTHROPIC_BASE_URL in your user settings. Your login is not touched.",
		TryCommand:   ClaudeTryCommand(gatewayURL),
		SetupCommand: "rlcd-gateway setup claude", UndoCommand: "rlcd-gateway undo claude",
	}
	dir, err := claudeConfigDir()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.ConfigPath = filepath.Join(dir, "settings.json")
	st.Binary, st.Version = findBinary("claude")
	_, dirErr := os.Stat(dir)
	st.Installed = st.Binary != "" || dirErr == nil

	layers := []claudeLayer{readClaudeManaged()}
	if project != "" {
		layers = append(layers,
			readClaudeLayer("project-local", filepath.Join(project, ".claude", "settings.local.json")),
			readClaudeLayer("project", filepath.Join(project, ".claude", "settings.json")))
	}
	user := readClaudeLayer("user", st.ConfigPath)
	layers = append(layers, user)
	st.ConfigExists = user.exists

	// Base URL: the highest settings file that sets it, else the shell.
	for _, l := range layers {
		src := Source{Scope: l.scope, Path: l.path, Exists: l.exists, Error: l.err}
		if v, ok := l.env[claudeEnvKey]; ok {
			src.Set, src.BaseURL, src.Gateway = true, redactURL(v), pointsAt(v, gatewayURL)
			if st.EffectiveSource == "" {
				st.EffectiveSource, st.Current, st.Effective = l.scope, src.BaseURL, src.Gateway
			}
		}
		if l.err != "" {
			st.Warnings = append(st.Warnings, l.err)
		}
		st.Sources = append(st.Sources, src)
	}
	shell := Source{Scope: "environment", Path: "gateway process environment"}
	if v := os.Getenv(claudeEnvKey); v != "" {
		shell.Set, shell.BaseURL, shell.Gateway = true, redactURL(v), pointsAt(v, gatewayURL)
	}
	st.Sources = append(st.Sources, shell)
	if st.EffectiveSource == "" && shell.Set {
		st.EffectiveSource, st.Current, st.Effective = "environment", shell.BaseURL, shell.Gateway
	}
	if v, ok := user.env[claudeEnvKey]; ok && pointsAt(v, gatewayURL) {
		st.Configured = true
	}

	if st.Configured && !st.Effective {
		st.Warnings = append(st.Warnings, "Your user settings point at the gateway, but "+scopeLabel(st.EffectiveSource)+
			" sets ANTHROPIC_BASE_URL to "+orDefault(st.Current)+", and that wins.")
	}
	if shell.Set && !shell.Gateway && st.EffectiveSource != "environment" && st.Effective {
		st.Notes = append(st.Notes, "ANTHROPIC_BASE_URL is also exported in the environment the gateway was started from; settings files win over it.")
	}
	if shell.Set && !shell.Gateway && st.EffectiveSource == "environment" {
		st.Warnings = append(st.Warnings, "ANTHROPIC_BASE_URL is exported in the environment the gateway was started from and points elsewhere ("+shell.BaseURL+"). `setup claude` would override it, because settings files win over the shell.")
	}
	if project == "" {
		st.Notes = append(st.Notes, "Project settings (.claude/settings.json, .claude/settings.local.json) override your user settings; pass a project folder to check them.")
	}
	st.Notes = append(st.Notes,
		"Settings from MDM profiles or the claude.ai admin console are not visible here; run /status in Claude Code to see every setting source.",
		"Behind a custom base URL Claude Code turns MCP tool search off by default (see the ANTHROPIC_BASE_URL entry in the Claude Code environment variable docs).")

	st.Auth = claudeAuth(dir, layers)
	if st.Auth.Mode == "cloud" {
		st.Warnings = append(st.Warnings, "Claude Code is set to use a cloud provider ("+st.Auth.Source+"); those requests do not use ANTHROPIC_BASE_URL and will not reach the gateway.")
	}
	return st
}

// claudeAuth reports which credential Claude Code will send, following its
// documented precedence. Only the presence of a credential is reported,
// never its value.
func claudeAuth(dir string, layers []claudeLayer) *Auth {
	// Settings env wins over the shell; higher layers win over lower ones.
	type origin struct{ value, where string }
	env := map[string]origin{}
	for _, k := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_PROFILE"} {
		if v := os.Getenv(k); v != "" {
			env[k] = origin{v, "the gateway's environment"}
		}
	}
	helper := ""
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		for k, v := range l.env {
			if v != "" {
				env[k] = origin{v, l.path}
			}
		}
		if l.helper {
			helper = l.path
		}
	}
	truthy := func(k string) (string, bool) {
		o, ok := env[k]
		v := strings.ToLower(o.value)
		return o.where, ok && v != "0" && v != "false" && v != ""
	}
	sub := "Your subscription login is kept: the gateway forwards it to Anthropic as-is."
	for _, p := range []struct{ key, name string }{
		{"CLAUDE_CODE_USE_BEDROCK", "Amazon Bedrock"}, {"CLAUDE_CODE_USE_VERTEX", "Google Vertex"}, {"CLAUDE_CODE_USE_FOUNDRY", "Microsoft Foundry"},
	} {
		if where, ok := truthy(p.key); ok {
			return &Auth{Mode: "cloud", Label: p.name, Source: p.key + " in " + where}
		}
	}
	if o, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		return &Auth{Mode: "bearer", Label: "Bearer token", Source: "ANTHROPIC_AUTH_TOKEN in " + o.where,
			Note: "Sent as Authorization: Bearer. The gateway forwards it unchanged."}
	}
	if o, ok := env["ANTHROPIC_API_KEY"]; ok {
		return &Auth{Mode: "api-key", Label: "API key", Source: "ANTHROPIC_API_KEY in " + o.where,
			Note: "Claude Code uses the key instead of a subscription once you approve it. Unset it to use your subscription."}
	}
	if helper != "" {
		return &Auth{Mode: "api-key-helper", Label: "apiKeyHelper script", Source: "apiKeyHelper in " + helper}
	}
	if o, ok := env["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		return &Auth{Mode: "subscription", Label: "Subscription token", Source: "CLAUDE_CODE_OAUTH_TOKEN in " + o.where,
			KeepsSubscription: true, Note: sub}
	}
	if o, ok := env["ANTHROPIC_PROFILE"]; ok {
		return &Auth{Mode: "profile", Label: "Anthropic profile", Source: "ANTHROPIC_PROFILE in " + o.where}
	}
	if claudeLoggedIn(dir) {
		return &Auth{Mode: "subscription", Label: "Subscription login", Source: "/login",
			KeepsSubscription: true, Note: sub}
	}
	return &Auth{Mode: "unknown", Label: "Not detected",
		Note: "No credential found in settings or the gateway's environment. If you use /login, " + sub}
}

// claudeLoggedIn looks for a /login credential without reading it: the
// credentials file (Linux, or the macOS fallback) or the account entry in
// .claude.json (present on every platform when logged in with claude.ai).
func claudeLoggedIn(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil {
		return true
	}
	stateFile := filepath.Join(dir, ".claude.json")
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			stateFile = filepath.Join(home, ".claude.json")
		}
	}
	b, err := os.ReadFile(stateFile)
	if err != nil {
		return false
	}
	var s struct {
		OAuthAccount json.RawMessage `json:"oauthAccount"`
	}
	return json.Unmarshal(b, &s) == nil && len(s.OAuthAccount) > 0 && string(s.OAuthAccount) != "null"
}

func scopeLabel(scope string) string {
	switch scope {
	case "managed":
		return "managed settings"
	case "project-local":
		return ".claude/settings.local.json"
	case "project":
		return ".claude/settings.json"
	case "environment":
		return "the environment"
	}
	return scope + " settings"
}

func orDefault(u string) string {
	if u == "" {
		return "the provider default"
	}
	return u
}
