package clients

import (
	"net/http"
	"testing"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestDetectRealHeaderShapes(t *testing.T) {
	cases := []struct {
		name          string
		h             http.Header
		id, ver, kind string
	}{
		{"claude code 2.1.280", hdr("User-Agent", "claude-cli/2.1.280 (external, cli)", "X-App", "cli",
			"X-Claude-Code-Session-Id", "00000000-0000-0000-0000-000000000001",
			"X-Stainless-Lang", "js", "X-Stainless-Package-Version", "0.60.0", "Anthropic-Version", "2023-06-01"),
			"claude-code", "2.1.280", KindAgent},
		{"codex", hdr("Originator", "codex_cli_rs", "User-Agent", "codex_cli_rs/0.46.0 (Mac OS 15.6.1; arm64) iTerm.app/3.5.14"),
			"codex", "0.46.0", KindAgent},
		{"codex exec", hdr("Originator", "codex_exec", "User-Agent", "codex_exec/0.46.0 (Ubuntu 24.4.0; x86_64)"),
			"codex", "0.46.0", KindAgent},
		{"opencode", hdr("User-Agent", "opencode/0.15.2 ai-sdk/provider-utils/3.0.9 runtime/bun/1.2.21"),
			"opencode", "0.15.2", KindAgent},
		{"openai python", hdr("User-Agent", "OpenAI/Python 1.109.1", "X-Stainless-Lang", "python",
			"X-Stainless-Package-Version", "1.109.1", "X-Stainless-Runtime", "CPython"), "openai-python", "1.109.1", KindSDK},
		{"openai node", hdr("User-Agent", "OpenAI/JS 5.23.0", "X-Stainless-Lang", "js", "X-Stainless-Package-Version", "5.23.0"),
			"openai-node", "5.23.0", KindSDK},
		{"anthropic python", hdr("User-Agent", "Anthropic/Python 0.69.0", "X-Stainless-Lang", "python",
			"X-Stainless-Package-Version", "0.69.0", "Anthropic-Version", "2023-06-01"), "anthropic-python", "0.69.0", KindSDK},
		{"anthropic node", hdr("User-Agent", "Anthropic/JS 0.65.0", "X-Stainless-Lang", "js", "Anthropic-Version", "2023-06-01"),
			"anthropic-node", "", KindSDK},
		{"openai go", hdr("User-Agent", "OpenAI/Go 2.7.0", "X-Stainless-Lang", "go"), "openai-go", "", KindSDK},
		{"litellm", hdr("User-Agent", "litellm/1.77.5"), "litellm", "1.77.5", KindSDK},
		{"langchain", hdr("User-Agent", "langchain-openai/0.3.33"), "langchain", "", KindSDK},
		{"curl", hdr("User-Agent", "curl/8.7.1"), "curl", "8.7.1", KindCLI},
		{"chrome", hdr("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"),
			"browser", "", KindBrowser},
		{"nothing", hdr(), "unknown", "", KindUnknown},
	}
	for _, c := range cases {
		got := Detect(c.h)
		if got.ID != c.id || got.Kind != c.kind || (c.ver != "" && got.Version != c.ver) {
			t.Errorf("%s: got %+v, want id %s version %q kind %s", c.name, got, c.id, c.ver, c.kind)
		}
		if got.Name == "" {
			t.Errorf("%s: no display name", c.name)
		}
	}
	if Detect(cases[0].h).Name != "Claude Code" || Detect(cases[4].h).Name != "OpenAI Python SDK" {
		t.Error("display names")
	}
}
