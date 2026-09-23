// Package setup points an agent at the gateway, and back.
//
// Only the base URL changes. The agent keeps its own login (subscription or
// API key); the gateway decides per request whether to forward it.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const claudeEnvKey = "ANTHROPIC_BASE_URL"

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Claude sets env.ANTHROPIC_BASE_URL in ~/.claude/settings.json, keeping
// every other setting and leaving a timestamped backup next to it.
func Claude(baseURL string) (string, error) {
	return editClaude(func(env map[string]any) { env[claudeEnvKey] = baseURL })
}

// UndoClaude removes the override, so Claude Code talks to Anthropic directly again.
func UndoClaude() (string, error) {
	return editClaude(func(env map[string]any) { delete(env, claudeEnvKey) })
}

func editClaude(change func(env map[string]any)) (string, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return "", err
	}
	settings := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if err := json.Unmarshal(b, &settings); err != nil {
			return "", fmt.Errorf("%s is not valid JSON, refusing to touch it: %w", path, err)
		}
		backup := fmt.Sprintf("%s.rlcd-backup-%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, b, 0o600); err != nil {
			return "", err
		}
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	change(env)
	if len(env) == 0 {
		delete(settings, "env")
	} else {
		settings["env"] = env
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(out, '\n'), 0o600)
}

// Run points an agent at the gateway (undo=false) or back (undo=true) and
// returns a message for the user. New agents are added here.
func Run(agent string, undo bool, gatewayURL string) (string, error) {
	switch agent {
	case "claude":
		if undo {
			path, err := UndoClaude()
			return "Removed the gateway override from " + path + ".", err
		}
		path, err := Claude(gatewayURL)
		return fmt.Sprintf("Claude Code now uses %s (in %s).\nYour login is unchanged. Undo with: rlcd-gateway undo claude", gatewayURL, path), err
	}
	return "", fmt.Errorf("unknown agent %q (supported: %s)", agent, strings.Join(Agents(), ", "))
}

// Agents lists the agents Run supports.
func Agents() []string { return []string{"claude"} }
