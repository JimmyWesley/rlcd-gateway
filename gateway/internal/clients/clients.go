// Package clients tells who made a request, from its headers alone, so the
// dashboard can show "client → gateway → provider · model" for every call.
//
// Detection is best effort and never affects routing. Evidence for each
// rule is in the comment next to it; anything unrecognised is "unknown".
// The ids are stable slugs the UI maps to icons.
package clients

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Kinds.
const (
	KindAgent   = "agent"
	KindSDK     = "sdk"
	KindCLI     = "cli"
	KindBrowser = "browser"
	KindUnknown = "unknown"
)

var versionRe = regexp.MustCompile(`^[vV]?([0-9][0-9A-Za-z.+_-]*)`)

// after returns the version following "name/" in a User-Agent, if any.
func after(ua, name string) string {
	i := strings.Index(strings.ToLower(ua), strings.ToLower(name)+"/")
	if i < 0 {
		return ""
	}
	m := versionRe.FindStringSubmatch(ua[i+len(name)+1:])
	if m == nil {
		return ""
	}
	return m[1]
}

// Stainless-generated SDKs (the official OpenAI and Anthropic SDKs) send
// X-Stainless-Lang and X-Stainless-Package-Version; their User-Agent is
// "OpenAI/Python 1.51.0", "Anthropic/JS 0.39.0" and so on.
var langSlug = map[string]string{
	"python": "python", "js": "node", "node": "node", "typescript": "node", "go": "go",
	"java": "java", "kotlin": "kotlin", "ruby": "ruby", "csharp": "dotnet", "dotnet": "dotnet", "php": "php",
}

var langName = map[string]string{
	"python": "Python", "node": "Node", "go": "Go", "java": "Java", "kotlin": "Kotlin",
	"ruby": "Ruby", "dotnet": ".NET", "php": "PHP",
}

// Detect identifies the client behind h.
func Detect(h http.Header) store.Client {
	ua := h.Get("User-Agent")
	lua := strings.ToLower(ua)
	switch {
	// Claude Code: "claude-cli/2.1.280 (external, cli)", plus X-App: cli and
	// X-Claude-Code-Session-Id. It is built on the Anthropic TS SDK, so it
	// also sends Stainless headers; this rule must come first.
	case strings.HasPrefix(lua, "claude-cli/") || h.Get("X-Claude-Code-Session-Id") != "":
		return store.Client{ID: "claude-code", Name: "Claude Code", Version: after(ua, "claude-cli"), Kind: KindAgent}
	// Codex CLI: "originator: codex_cli_rs" (codex_exec, codex_vscode for
	// the other front ends) and User-Agent "codex_cli_rs/0.46.0 (...)".
	case strings.HasPrefix(strings.ToLower(h.Get("Originator")), "codex") || strings.HasPrefix(lua, "codex"):
		name := h.Get("Originator")
		if name == "" {
			name, _, _ = strings.Cut(ua, "/")
		}
		return store.Client{ID: "codex", Name: "Codex", Version: after(ua, name), Kind: KindAgent}
	// OpenCode names itself in its User-Agent ("opencode/0.15.2 ...").
	case strings.Contains(lua, "opencode"):
		return store.Client{ID: "opencode", Name: "OpenCode", Version: after(ua, "opencode"), Kind: KindAgent}
	// LiteLLM and LangChain wrap the official SDKs; they only show up when
	// they name themselves in the User-Agent or in their own headers.
	case strings.Contains(lua, "litellm") || hasHeaderPrefix(h, "X-Litellm"):
		return store.Client{ID: "litellm", Name: "LiteLLM", Version: after(ua, "litellm"), Kind: KindSDK}
	case strings.Contains(lua, "langchain"):
		return store.Client{ID: "langchain", Name: "LangChain", Version: after(ua, "langchain"), Kind: KindSDK}
	}
	if lang := strings.ToLower(h.Get("X-Stainless-Lang")); lang != "" || strings.HasPrefix(lua, "openai/") || strings.HasPrefix(lua, "anthropic/") {
		vendor, vname := "openai", "OpenAI"
		if strings.HasPrefix(lua, "anthropic/") || h.Get("Anthropic-Version") != "" {
			vendor, vname = "anthropic", "Anthropic"
		}
		slug := langSlug[lang]
		if slug == "" {
			// "OpenAI/Python 1.51.0": the language follows the slash.
			if _, rest, ok := strings.Cut(ua, "/"); ok {
				l, _, _ := strings.Cut(strings.ToLower(rest), " ")
				slug = langSlug[l]
			}
		}
		if slug == "" {
			slug = "sdk"
		}
		version := h.Get("X-Stainless-Package-Version")
		if version == "" {
			if f := strings.Fields(ua); len(f) > 1 {
				version = f[1]
			}
		}
		name := vname + " SDK"
		if n := langName[slug]; n != "" {
			name = vname + " " + n + " SDK"
		}
		return store.Client{ID: vendor + "-" + slug, Name: name, Version: version, Kind: KindSDK}
	}
	switch {
	case strings.Contains(lua, "ai-sdk/"):
		// Vercel AI SDK: "ai-sdk/provider-utils/3.0.1 runtime/node/22".
		return store.Client{ID: "ai-sdk", Name: "Vercel AI SDK", Version: after(ua, "provider-utils"), Kind: KindSDK}
	case strings.HasPrefix(lua, "curl/"):
		return store.Client{ID: "curl", Name: "curl", Version: after(ua, "curl"), Kind: KindCLI}
	case strings.HasPrefix(lua, "httpie/"):
		return store.Client{ID: "httpie", Name: "HTTPie", Version: after(ua, "httpie"), Kind: KindCLI}
	case strings.HasPrefix(lua, "wget/"):
		return store.Client{ID: "wget", Name: "Wget", Version: after(ua, "wget"), Kind: KindCLI}
	case strings.HasPrefix(lua, "python-requests/"):
		return store.Client{ID: "python-requests", Name: "Python requests", Version: after(ua, "python-requests"), Kind: KindSDK}
	case strings.HasPrefix(lua, "python-httpx/"):
		return store.Client{ID: "python-httpx", Name: "Python httpx", Version: after(ua, "python-httpx"), Kind: KindSDK}
	case strings.HasPrefix(lua, "go-http-client/"):
		return store.Client{ID: "go-http", Name: "Go net/http", Version: after(ua, "go-http-client"), Kind: KindSDK}
	case strings.HasPrefix(lua, "mozilla/"):
		return store.Client{ID: "browser", Name: browserName(lua), Kind: KindBrowser}
	}
	return store.Client{ID: "unknown", Name: "Unknown client", Kind: KindUnknown}
}

func hasHeaderPrefix(h http.Header, prefix string) bool {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), prefix) {
			return true
		}
	}
	return false
}

func browserName(lua string) string {
	switch {
	case strings.Contains(lua, "edg/"):
		return "Edge"
	case strings.Contains(lua, "firefox/"):
		return "Firefox"
	case strings.Contains(lua, "chrome/"):
		return "Chrome"
	case strings.Contains(lua, "safari/"):
		return "Safari"
	}
	return "Browser"
}
