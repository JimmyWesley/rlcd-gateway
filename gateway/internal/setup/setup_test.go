package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gw = "http://127.0.0.1:4803"

// sandbox points every location setup reads or writes at temp dirs, so
// tests never see the real ~/.claude, ~/.codex, ~/.config/opencode or
// ~/.rlcd-gateway, and clears the variables that would change the outcome.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RLCD_GATEWAY_HOME", filepath.Join(home, ".rlcd-gateway"))
	t.Setenv("PATH", t.TempDir()) // no real agent binaries are run
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_PROFILE",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(k, "")
	}
	managed := filepath.Join(home, "managed")
	old := claudeManagedDir
	claudeManagedDir = func() string { return managed }
	t.Cleanup(func() { claudeManagedDir = old })
	return home
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func backups(t *testing.T, path string) []string {
	m, _ := filepath.Glob(path + ".rlcd-backup-*")
	return m
}

// --- Codex ---------------------------------------------------------------

const codexFixture = `# my codex config
model = "gpt-5-codex"
model_provider = "azure"   # work account
approval_policy = "on-request"
notes = """
[not a table]
model_provider = "fake"
"""
sandbox_writable = [
  "/tmp",  # scratch
  "/var",
]

[model_providers.azure]
name = "Azure"
base_url = "https://example.openai.azure.com/openai"
env_key = "AZURE_OPENAI_API_KEY"
query_params = { api-version = "2025-04-01-preview" }

[profiles.fast]
model = "gpt-5-mini"
`

func TestCodexRoundTrip(t *testing.T) {
	cases := map[string]string{
		"full":               codexFixture,
		"no model_provider":  "model = \"o3\"\n\n[tui]\nnotifications = true\n",
		"no final newline":   "model = \"o3\"\nmodel_provider = \"azure\"",
		"crlf":               "model = \"o3\"\r\nmodel_provider = \"azure\"\r\n[tui]\r\nx = 1\r\n",
		"empty":              "",
		"only tables":        "[tui]\nnotifications = true",
		"provider last line": "[tui]\nx = 1\n",
	}
	for name, orig := range cases {
		t.Run(name, func(t *testing.T) {
			home := sandbox(t)
			path := filepath.Join(home, ".codex", "config.toml")
			write(t, path, orig)

			msg, err := Run("codex", false, gw)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, "rlcd_gateway") {
				t.Errorf("message: %s", msg)
			}
			after := read(t, path)
			cs, err := readCodex(after)
			if err != nil {
				t.Fatalf("edited file does not parse: %v\n%s", err, after)
			}
			if cs.provider != "rlcd_gateway" || cs.baseURL != gw+"/openai/v1" {
				t.Errorf("state after setup: %+v\n%s", cs, after)
			}
			if len(backups(t, path)) != 1 {
				t.Errorf("want one backup, got %v", backups(t, path))
			}
			// Everything else is still there.
			for _, l := range strings.Split(orig, "\n") {
				l = strings.TrimRight(l, "\r")
				if l != "" && !strings.HasPrefix(l, "model_provider") && !strings.Contains(after, l) {
					t.Errorf("line lost: %q", l)
				}
			}

			// Setup is idempotent and keeps the original line.
			if _, err := Run("codex", false, gw); err != nil {
				t.Fatal(err)
			}
			if again := read(t, path); again != after {
				t.Errorf("second setup changed the file:\n%s\n---\n%s", after, again)
			}
			st := codexStatus(gw, "")
			if !st.Configured || !st.Effective || st.Current != gw+"/openai/v1" {
				t.Errorf("status: %+v", st)
			}

			if _, err := Run("codex", true, gw); err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); got != orig {
				t.Errorf("undo did not restore the original bytes:\n%q\nwant\n%q", got, orig)
			}
			if st := codexStatus(gw, ""); st.Configured {
				t.Error("still configured after undo")
			}
		})
	}
}

func TestCodexMissingFile(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if _, err := Codex(gw); err != nil {
		t.Fatal(err)
	}
	if cs, err := readCodex(read(t, path)); err != nil || cs.provider != codexProviderID {
		t.Fatalf("%+v %v", cs, err)
	}
	if _, err := UndoCodex(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "" {
		t.Errorf("undo left %q", got)
	}
}

func TestCodexRefusesWhatItCannotParse(t *testing.T) {
	for name, content := range map[string]string{
		"unterminated string": "model = \"gpt-5\n",
		"junk after value":    "model = \"gpt-5\" oops\n",
		"unclosed array":      "x = [1, 2\n",
		"bad header":          "[model_providers.x\n",
		"missing value":       "model =\n",
		"ours already":        "[model_providers.rlcd_gateway]\nname = \"mine\"\n",
		"inline providers":    "model_providers = { a = { name = \"x\" } }\n",
		"broken block":        markTopBegin + "\nmodel_provider = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := sandbox(t)
			path := filepath.Join(home, ".codex", "config.toml")
			write(t, path, content)
			if _, err := Codex(gw); err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Errorf("err = %v", err)
			}
			if read(t, path) != content {
				t.Error("file changed")
			}
			if len(backups(t, path)) != 0 {
				t.Error("backup written for a refused edit")
			}
		})
	}
}

func TestTOMLScanner(t *testing.T) {
	stmts, err := parseTOML(codexFixture)
	if err != nil {
		t.Fatal(err)
	}
	var top []string
	for _, s := range stmts {
		if !s.header && len(s.path) == 0 {
			top = append(top, strings.Join(s.key, "."))
		}
	}
	if strings.Join(top, ",") != "model,model_provider,approval_policy,notes,sandbox_writable" {
		t.Errorf("top-level keys: %v", top)
	}
	for _, ok := range []string{
		"a = 1979-05-27 07:32:00Z\n", "a = 'lit'\n", "a = '''\nx'''\n", "\"quoted key\" = true\n",
		"a.b.c = -1.5e3\n", "a = [ [1], {x = 1} ]\n", "a = \"\"\"x\"\"\"\"\n",
	} {
		if _, err := parseTOML(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// --- Claude Code -----------------------------------------------------------

func TestClaudeRoundTripRestoresPreviousValue(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	orig := "{\n    \"model\": \"opus\",\n    \"env\": {\"ANTHROPIC_BASE_URL\": \"https://proxy.corp.example\", \"FOO\": \"1\"},\n    \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n"
	write(t, path, orig)

	if _, err := Run("claude", false, gw); err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	env := s["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != gw || env["FOO"] != "1" || s["model"] != "opus" {
		t.Fatalf("after setup: %v", s)
	}
	if len(backups(t, path)) != 1 {
		t.Error("no backup")
	}
	// Setup twice must not forget the value from before the gateway.
	if _, err := Run("claude", false, gw); err != nil {
		t.Fatal(err)
	}
	if st := claudeStatus(gw, ""); !st.Configured || !st.Effective || st.EffectiveSource != "user" {
		t.Errorf("status: %+v", st)
	}

	// Untouched since setup: the original bytes come back.
	if _, err := Run("claude", true, gw); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != orig {
		t.Errorf("undo:\n%s\nwant\n%s", got, orig)
	}
	if _, err := os.Stat(statePath("claude")); err == nil {
		t.Error("state left behind")
	}
}

func TestClaudeUndoAfterUserEdits(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	write(t, path, `{"env":{"ANTHROPIC_BASE_URL":"https://proxy.corp.example"}}`)
	if _, err := Claude(gw); err != nil {
		t.Fatal(err)
	}
	// The user changes something else in the meantime.
	var s map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	s["theme"] = "dark"
	b, _ := json.Marshal(s)
	write(t, path, string(b))

	if _, _, err := UndoClaude(gw); err != nil {
		t.Fatal(err)
	}
	s = nil
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	if s["theme"] != "dark" || s["env"].(map[string]any)["ANTHROPIC_BASE_URL"] != "https://proxy.corp.example" {
		t.Errorf("after undo: %v", s)
	}
}

func TestClaudeNoPreviousValue(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	write(t, path, `{"model":"sonnet"}`)
	if _, err := Claude(gw); err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(read(t, path), `"sonnet"`, `"opus"`, 1)) // edit so the backup path is not used
	if _, _, err := UndoClaude(gw); err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	if _, ok := s["env"]; ok || s["model"] != "opus" {
		t.Errorf("after undo: %v", s)
	}
}

func TestClaudeMissingFileAndNoState(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if _, err := Claude(gw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := UndoClaude(gw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("undo kept a file setup had created")
	}

	// Without state, undo only removes a value that points at the gateway.
	write(t, path, `{"env":{"ANTHROPIC_BASE_URL":"https://proxy.corp.example"}}`)
	if _, what, err := UndoClaude(gw); err != nil || !strings.Contains(what, "left unchanged") {
		t.Errorf("%q %v", what, err)
	}
	write(t, path, `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:4803"},"model":"x"}`)
	if _, _, err := UndoClaude(gw); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); strings.Contains(got, "env") {
		t.Errorf("gateway value kept: %s", got)
	}
}

func TestClaudeRefusesInvalidJSON(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	bad := "{\n  // comment\n  \"model\": \"opus\"\n}\n"
	write(t, path, bad)
	if _, err := Run("claude", false, gw); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("err = %v", err)
	}
	if read(t, path) != bad || len(backups(t, path)) != 0 {
		t.Error("file touched")
	}
}

func TestClaudeStatusPrecedenceAndAuth(t *testing.T) {
	home := sandbox(t)
	project := t.TempDir()
	user := filepath.Join(home, ".claude", "settings.json")
	write(t, user, `{"env":{"ANTHROPIC_BASE_URL":"`+gw+`","ANTHROPIC_API_KEY":"sk-ant-api03-SECRETSECRET"}}`)

	st := Status(gw, project)[0]
	if st.Name != "claude" || !st.Configured || !st.Effective {
		t.Fatalf("user only: %+v", st)
	}
	if st.Auth.Mode != "api-key" || !strings.Contains(st.Auth.Source, "settings.json") || st.Auth.KeepsSubscription {
		t.Errorf("auth: %+v", st.Auth)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "SECRETSECRET") {
		t.Fatal("status leaks the API key")
	}

	// A project-local file wins over the user file.
	write(t, filepath.Join(project, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://other.example"}}`)
	st = claudeStatus(gw, project)
	if !st.Configured || st.Effective || st.EffectiveSource != "project-local" || len(st.Warnings) == 0 {
		t.Errorf("project-local override: %+v", st)
	}

	// Managed settings win over everything.
	os.Remove(filepath.Join(project, ".claude", "settings.local.json"))
	write(t, filepath.Join(claudeManagedDir(), "managed-settings.d", "10-proxy.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://managed.example"}}`)
	st = claudeStatus(gw, project)
	if st.EffectiveSource != "managed" || st.Effective || st.Current != "https://managed.example" {
		t.Errorf("managed override: %+v", st)
	}
	os.RemoveAll(claudeManagedDir())

	// A shell export does not beat a settings file.
	t.Setenv("ANTHROPIC_BASE_URL", "https://shell.example")
	if st = claudeStatus(gw, ""); !st.Effective || st.EffectiveSource != "user" {
		t.Errorf("shell vs settings: %+v", st)
	}
	// ... but applies when no file sets it.
	write(t, user, `{}`)
	if st = claudeStatus(gw, ""); st.Effective || st.EffectiveSource != "environment" || len(st.Warnings) == 0 {
		t.Errorf("shell only: %+v", st)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "")

	// Subscription login detection, without reading the credential.
	if st = claudeStatus(gw, ""); st.Auth.Mode != "unknown" {
		t.Errorf("no login: %+v", st.Auth)
	}
	write(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"me@example.com"}}`)
	st = claudeStatus(gw, "")
	if st.Auth.Mode != "subscription" || !st.Auth.KeepsSubscription {
		t.Errorf("subscription: %+v", st.Auth)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "me@example.com") {
		t.Error("status leaks the account email")
	}
	// Cloud providers bypass ANTHROPIC_BASE_URL.
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	if st = claudeStatus(gw, ""); st.Auth.Mode != "cloud" || len(st.Warnings) == 0 {
		t.Errorf("bedrock: %+v", st)
	}
}

// --- OpenCode ---------------------------------------------------------------

func TestOpenCodeRoundTrip(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	orig := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "provider": {
    "anthropic": {"options": {"baseURL": "https://api.anthropic.com/v1", "timeout": 600000}},
    "ollama": {"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": "http://localhost:11434/v1"}}
  }
}
`
	write(t, path, orig)
	if _, err := Run("opencode", false, gw); err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	if v, _ := lookup(s, opencodeAnthropicPath); v != gw+"/v1" {
		t.Errorf("anthropic baseURL = %v", v)
	}
	if v, _ := lookup(s, opencodeOpenAIPath); v != gw+"/openai/v1" {
		t.Errorf("openai baseURL = %v", v)
	}
	if v, _ := lookup(s, []string{"provider", "ollama", "options", "baseURL"}); v != "http://localhost:11434/v1" {
		t.Errorf("other provider changed: %v", v)
	}
	if v, _ := lookup(s, []string{"provider", "anthropic", "options", "timeout"}); v != float64(600000) {
		t.Errorf("sibling option lost: %v", v)
	}
	if st := opencodeStatus(gw, ""); !st.Configured || !st.Effective {
		t.Errorf("status: %+v", st)
	}
	if _, err := Run("opencode", true, gw); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != orig {
		t.Errorf("undo:\n%s\nwant\n%s", got, orig)
	}
}

func TestOpenCodeSemanticUndoPrunesCreatedObjects(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, `{"provider":{"anthropic":{"options":{"baseURL":"https://mirror.example/v1"}}}}`)
	if _, err := OpenCode(gw); err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	s["theme"] = "tokyonight"
	b, _ := json.Marshal(s)
	write(t, path, string(b))
	if _, _, err := UndoOpenCode(gw); err != nil {
		t.Fatal(err)
	}
	s = nil
	_ = json.Unmarshal([]byte(read(t, path)), &s)
	if v, _ := lookup(s, opencodeAnthropicPath); v != "https://mirror.example/v1" {
		t.Errorf("previous baseURL not restored: %v", v)
	}
	if _, ok := lookup(s, []string{"provider", "openai"}); ok {
		t.Errorf("objects setup created were kept: %v", s)
	}
	if s["theme"] != "tokyonight" {
		t.Error("user edit lost")
	}
}

func TestOpenCodeRefusesJSONC(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	jsonc := "{\n  // my settings\n  \"model\": \"x\",\n}\n"
	write(t, path, jsonc)
	if _, err := Run("opencode", false, gw); err == nil {
		t.Fatal("rewrote a JSONC file")
	}
	if read(t, path) != jsonc {
		t.Error("file changed")
	}
	st := opencodeStatus(gw, "")
	if st.ConfigPath != path || len(st.Warnings) == 0 {
		t.Errorf("status: %+v", st)
	}
}

func TestOpenCodeProjectOverrideAndChatGPTWarning(t *testing.T) {
	home := sandbox(t)
	project := t.TempDir()
	if _, err := OpenCode(gw); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(project, "opencode.json"), `{"provider":{"anthropic":{"options":{"baseURL":"https://elsewhere.example/v1"}}}}`)
	st := opencodeStatus(gw, project)
	if !st.Configured || st.Effective || st.EffectiveSource != "project" {
		t.Errorf("project override: %+v", st)
	}
	write(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"openai":{"type":"oauth","access":"tok-SECRET","refresh":"r"}}`)
	st = opencodeStatus(gw, "")
	found := false
	for _, w := range st.Warnings {
		found = found || strings.Contains(w, "chatgpt.com")
	}
	if !found || st.Auth.Mode != "subscription" {
		t.Errorf("ChatGPT login warning missing: %+v", st)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "tok-SECRET") {
		t.Error("status leaks a token")
	}
}

func TestRunUnknownAgentAndAgents(t *testing.T) {
	sandbox(t)
	if _, err := Run("cursor", false, gw); err == nil {
		t.Error("unknown agent accepted")
	}
	if strings.Join(Agents(), ",") != "claude,codex,opencode" {
		t.Errorf("agents: %v", Agents())
	}
	st := Status(gw, "")
	if len(st) != 3 || st[0].Name != "claude" || st[0].TryCommand != "ANTHROPIC_BASE_URL="+gw+" claude" {
		t.Errorf("status order/commands: %+v", st)
	}
	if !strings.Contains(st[1].TryCommand, `base_url="`+gw+`/openai/v1"`) || !strings.Contains(st[2].TryCommand, gw+"/v1") {
		t.Errorf("try commands: %q %q", st[1].TryCommand, st[2].TryCommand)
	}
}

func TestPointsAt(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:4803":           true,
		"http://localhost:4803/openai/v1": true,
		"http://[::1]:4803":               true,
		"http://127.0.0.1:4777":           false,
		"https://api.anthropic.com":       false,
		"":                                false,
	} {
		if got := pointsAt(u, gw); got != want {
			t.Errorf("pointsAt(%q) = %v", u, got)
		}
	}
}
