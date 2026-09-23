package config

import (
	"net"
	"net/url"
	"strings"
)

// Providers the dashboard knows a logo for. Anything else is "custom".
var Providers = []string{"anthropic", "openrouter", "openai", "groq", "together", "deepseek", "mistral",
	"google", "ollama", "vllm", "lmstudio", "custom"}

// ProviderName is the route's provider: the explicit one, else one derived
// from the base URL.
func (r Route) ProviderName() string {
	if r.Provider != "" {
		return r.Provider
	}
	return ProviderFromURL(r.BaseURL)
}

// ProviderFromURL recognises the well-known API hosts, and the default
// ports of the local servers (Ollama 11434, LM Studio 1234, vLLM 8000).
func ProviderFromURL(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "custom"
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	has := func(s string) bool { return host == s || strings.HasSuffix(host, "."+s) }
	switch {
	case has("anthropic.com"):
		return "anthropic"
	case has("openrouter.ai"):
		return "openrouter"
	case has("openai.com") || has("chatgpt.com"):
		return "openai"
	case has("groq.com"):
		return "groq"
	case has("together.xyz") || has("together.ai"):
		return "together"
	case has("deepseek.com"):
		return "deepseek"
	case has("mistral.ai"):
		return "mistral"
	case has("googleapis.com") || has("google.com"):
		return "google"
	case strings.Contains(host, "ollama") || port == "11434":
		return "ollama"
	case strings.Contains(host, "lmstudio") || port == "1234":
		return "lmstudio"
	case strings.Contains(host, "vllm") || (port == "8000" && isLocalHost(host)):
		return "vllm"
	}
	return "custom"
}

func isLocalHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// vendorOrgs normalises the organisation part of routed model ids
// ("meta-llama/llama-3.3-70b" → meta).
var vendorOrgs = map[string]string{
	"anthropic": "anthropic", "openai": "openai", "google": "google", "meta-llama": "meta", "meta": "meta",
	"mistralai": "mistral", "mistral": "mistral", "qwen": "qwen", "deepseek": "deepseek", "deepseek-ai": "deepseek",
	"x-ai": "xai", "xai": "xai", "z-ai": "zhipu", "thudm": "zhipu", "moonshotai": "moonshot", "microsoft": "microsoft",
	"cohere": "cohere", "nvidia": "nvidia", "amazon": "amazon", "perplexity": "perplexity", "minimax": "minimax",
}

// vendorPrefixes recognise bare model names ("claude-…", "gpt-4.1", "llama3.1:8b").
var vendorPrefixes = []struct{ prefix, vendor string }{
	{"claude", "anthropic"}, {"gpt", "openai"}, {"chatgpt", "openai"}, {"o1", "openai"}, {"o3", "openai"},
	{"o4", "openai"}, {"codex", "openai"}, {"text-embedding", "openai"}, {"gemini", "google"}, {"gemma", "google"},
	{"llama", "meta"}, {"meta-llama", "meta"}, {"mistral", "mistral"}, {"mixtral", "mistral"}, {"codestral", "mistral"},
	{"ministral", "mistral"}, {"magistral", "mistral"}, {"devstral", "mistral"}, {"qwen", "qwen"}, {"qwq", "qwen"},
	{"deepseek", "deepseek"}, {"grok", "xai"}, {"glm", "zhipu"}, {"kimi", "moonshot"}, {"moonshot", "moonshot"},
	{"phi", "microsoft"}, {"command", "cohere"}, {"nemotron", "nvidia"},
}

// ModelVendor is who made a model, parsed from its id: "qwen/qwen3-235b" →
// qwen, "anthropic/claude-sonnet-4.5" → anthropic, "gpt-4.1" → openai.
// It returns "" when the id says nothing recognisable.
func ModelVendor(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	if org, rest, ok := strings.Cut(m, "/"); ok {
		if v, known := vendorOrgs[org]; known {
			return v
		}
		m = rest[strings.LastIndex(rest, "/")+1:]
	}
	for _, p := range vendorPrefixes {
		if strings.HasPrefix(m, p.prefix) {
			return p.vendor
		}
	}
	return ""
}
