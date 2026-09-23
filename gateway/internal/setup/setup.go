// Package setup points an agent at the gateway, and back.
//
// Only the base URL changes. The agent keeps its own login (subscription or
// API key); the gateway decides per request whether to forward it.
//
// Every edit follows the same rules: change only the keys we own, keep
// everything else in the file, write a timestamped backup next to it first,
// refuse a file we cannot parse, and make undo put back exactly what was
// there before. See claude.go, codex.go and opencode.go.
package setup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Run points an agent at the gateway (undo=false) or back (undo=true) and
// returns a message for the user.
func Run(agent string, undo bool, gatewayURL string) (string, error) {
	switch agent {
	case "claude":
		if undo {
			path, what, err := UndoClaude(gatewayURL)
			return "Claude Code: " + what + " in " + path + ".", err
		}
		path, err := Claude(gatewayURL)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Claude Code now uses %s (env.ANTHROPIC_BASE_URL in %s).\n"+
			"Your login is unchanged: a subscription stays a subscription. Undo with: rlcd-gateway undo claude",
			strings.TrimRight(gatewayURL, "/"), path)
		return msg + warnings(claudeStatus(gatewayURL, cwd())), nil
	case "codex":
		if undo {
			path, err := UndoCodex()
			return "Codex: removed the gateway provider from " + path + " and restored the previous model_provider.", err
		}
		path, err := Codex(gatewayURL)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Codex now uses the %q provider at %s (in %s).\n"+
			"It sends your own login (ChatGPT or API key). Undo with: rlcd-gateway undo codex",
			codexProviderID, CodexBaseURL(gatewayURL), path)
		return msg + warnings(codexStatus(gatewayURL, cwd())), nil
	case "opencode":
		if undo {
			path, what, err := UndoOpenCode(gatewayURL)
			return "OpenCode: " + what + " in " + path + ".", err
		}
		path, err := OpenCode(gatewayURL)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("OpenCode's anthropic and openai providers now use the gateway (in %s).\n"+
			"Your login is unchanged. Undo with: rlcd-gateway undo opencode", path)
		return msg + warnings(opencodeStatus(gatewayURL, cwd())), nil
	}
	return "", fmt.Errorf("unknown agent %q (supported: %s)", agent, strings.Join(Agents(), ", "))
}

// Agents lists the agents Run supports.
func Agents() []string { return []string{"claude", "codex", "opencode"} }

func cwd() string {
	d, _ := os.Getwd()
	return d
}

func warnings(st AgentStatus) string {
	var b strings.Builder
	for _, w := range st.Warnings {
		b.WriteString("\nWarning: " + w)
	}
	return b.String()
}

// Source is one place that can set the agent's base URL.
type Source struct {
	Scope   string `json:"scope"` // managed | project-local | project | user | global | environment | ...
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Set     bool   `json:"set"`                // it sets the base URL
	BaseURL string `json:"base_url,omitempty"` // credentials in the URL are removed
	Gateway bool   `json:"gateway"`            // ... and it points at the gateway
	Error   string `json:"error,omitempty"`
}

// Auth describes the credential the agent will send. It never holds a secret.
type Auth struct {
	Mode              string `json:"mode"` // subscription | api-key | bearer | api-key-helper | profile | cloud | unknown
	Label             string `json:"label"`
	Source            string `json:"source,omitempty"` // where it comes from, e.g. "ANTHROPIC_API_KEY in ~/.claude/settings.json"
	KeepsSubscription bool   `json:"keeps_subscription"`
	Note              string `json:"note,omitempty"`
}

// AgentStatus is what the dashboard shows for one agent.
type AgentStatus struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Installed    bool   `json:"installed"`
	Binary       string `json:"binary,omitempty"`
	Version      string `json:"version,omitempty"`
	ConfigPath   string `json:"config_path"` // the file setup changes
	ConfigExists bool   `json:"config_exists"`
	// Configured: the file setup owns points at the gateway.
	Configured bool `json:"configured"`
	// Effective: taking every known source into account, the agent will use
	// the gateway. It can differ from Configured (see Warnings).
	Effective       bool     `json:"effective"`
	EffectiveSource string   `json:"effective_source,omitempty"`
	Current         string   `json:"current,omitempty"` // effective base URL, "" = the provider default
	Sources         []Source `json:"sources,omitempty"`
	Auth            *Auth    `json:"auth,omitempty"`
	Changes         string   `json:"changes"`
	TryCommand      string   `json:"try_command"`
	SetupCommand    string   `json:"setup_command"`
	UndoCommand     string   `json:"undo_command"`
	Warnings        []string `json:"warnings,omitempty"`
	Notes           []string `json:"notes,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// Status reports every agent, Claude Code first. project, when set, is a
// project folder whose own settings files are taken into account.
func Status(gatewayURL, project string) []AgentStatus {
	if project != "" {
		if fi, err := os.Stat(project); err != nil || !fi.IsDir() || !filepath.IsAbs(project) {
			project = ""
		}
	}
	return []AgentStatus{
		claudeStatus(gatewayURL, project),
		codexStatus(gatewayURL, project),
		opencodeStatus(gatewayURL, project),
	}
}

func codexStatus(gatewayURL, project string) AgentStatus {
	st := AgentStatus{
		Name: "codex", Title: "Codex CLI",
		Changes:      "Adds a [model_providers." + codexProviderID + "] table and selects it with model_provider, in two marked blocks. Your login is not touched.",
		TryCommand:   CodexTryCommand(gatewayURL),
		SetupCommand: "rlcd-gateway setup codex", UndoCommand: "rlcd-gateway undo codex",
	}
	path, err := codexConfigPath()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.ConfigPath = path
	st.Binary, st.Version = findBinary("codex")
	_, dirErr := os.Stat(filepath.Dir(path))
	st.Installed = st.Binary != "" || dirErr == nil

	layers := []struct{ scope, path string }{{"user", path}}
	if project != "" {
		layers = append(layers, struct{ scope, path string }{"project", filepath.Join(project, ".codex", "config.toml")})
	}
	for _, l := range layers {
		b, err := os.ReadFile(l.path)
		src := Source{Scope: l.scope, Path: l.path, Exists: err == nil}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			src.Error = err.Error()
		}
		if err == nil {
			cs, perr := readCodex(string(b))
			if perr != nil {
				src.Error = "cannot parse: " + perr.Error()
				st.Warnings = append(st.Warnings, l.path+" cannot be parsed; setup will refuse to touch it ("+perr.Error()+").")
			} else {
				if cs.provider != "" || cs.baseURL != "" {
					src.Set, src.BaseURL, src.Gateway = true, redactURL(cs.baseURL), cs.baseURL != "" && pointsAt(cs.baseURL, gatewayURL)
					st.EffectiveSource, st.Current, st.Effective = l.scope, src.BaseURL, src.Gateway
				}
				if cs.profile != "" {
					st.Warnings = append(st.Warnings, l.path+" selects profile "+strconvQuote(cs.profile)+"; if that profile sets model_provider, it wins.")
				}
			}
		}
		if l.scope == "user" {
			st.ConfigExists = src.Exists
			st.Configured = src.Gateway
		}
		if src.Exists || l.scope == "user" {
			st.Sources = append(st.Sources, src)
		}
	}
	if st.Configured && !st.Effective {
		st.Warnings = append(st.Warnings, "The project's .codex/config.toml selects another provider, and it wins for trusted projects.")
	}
	st.Auth = codexAuth()
	if st.Auth.KeepsSubscription {
		st.Notes = append(st.Notes, "ChatGPT subscription calls are forwarded to chatgpt.com/backend-api/codex with your own token.")
	}
	st.Notes = append(st.Notes, "Pruning and routing do not apply to Codex yet; its traffic and Context X-ray show up in the Traffic tab.")
	return st
}

// codexAuth reads the login type from $CODEX_HOME/auth.json (never values).
func codexAuth() *Auth {
	d, err := codexHome()
	if err != nil {
		return &Auth{Mode: "unknown", Label: "Not detected"}
	}
	obj, _, err := readLenient(filepath.Join(d, "auth.json"))
	if obj == nil || err != nil {
		return &Auth{Mode: "unknown", Label: "Not detected", Note: "Run `codex login` first; the gateway forwards whatever login Codex sends."}
	}
	if t, ok := obj["tokens"].(map[string]any); ok && len(t) > 0 {
		return &Auth{Mode: "subscription", Label: "ChatGPT login", Source: "auth.json", KeepsSubscription: true,
			Note: "Your ChatGPT subscription is kept: the gateway forwards the token to the ChatGPT backend."}
	}
	if k, ok := obj["OPENAI_API_KEY"].(string); ok && k != "" {
		return &Auth{Mode: "api-key", Label: "OpenAI API key", Source: "auth.json", Note: "Forwarded to api.openai.com unchanged."}
	}
	return &Auth{Mode: "unknown", Label: "Not detected"}
}

// redactURL drops credentials embedded in a URL (user:pass@host).
func redactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return u
	}
	p.User = nil
	return p.String()
}

var versions sync.Map // binary path -> version

// findBinary looks the agent up on PATH and asks it for its version once
// (cached; at most two seconds).
func findBinary(name string) (path, version string) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", ""
	}
	if v, ok := versions.Load(path); ok {
		return path, v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err == nil {
		version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if len(version) > 80 {
			version = version[:80]
		}
	}
	versions.Store(path, version)
	return path, version
}
