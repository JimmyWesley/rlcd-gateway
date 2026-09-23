package config

import "testing"

func TestDeletedRouteStaysDeleted(t *testing.T) {
	t.Setenv("RLCD_GATEWAY_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute("openrouter"); err != nil {
		t.Fatal(err)
	}
	s2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get().Routes["openrouter"]; ok {
		t.Error("deleted route came back from the defaults after a reload")
	}
	if _, ok := s2.Get().Routes["claude-sub"]; !ok {
		t.Error("remaining route lost")
	}
}

func TestProviderAndVendor(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.anthropic.com":                 "anthropic",
		"https://openrouter.ai/api/v1":              "openrouter",
		"https://api.openai.com/v1":                 "openai",
		"https://chatgpt.com/backend-api/codex":     "openai",
		"https://api.groq.com/openai/v1":            "groq",
		"https://api.together.xyz/v1":               "together",
		"https://api.deepseek.com/v1":               "deepseek",
		"https://api.mistral.ai/v1":                 "mistral",
		"https://generativelanguage.googleapis.com": "google",
		"http://127.0.0.1:11434/v1":                 "ollama",
		"http://localhost:1234/v1":                  "lmstudio",
		"http://10.0.0.5:8000/v1":                   "vllm",
		"https://llm.example.com/v1":                "custom",
	} {
		if got := ProviderFromURL(base); got != want {
			t.Errorf("%s: %s, want %s", base, got, want)
		}
	}
	if (Route{BaseURL: "https://llm.example.com", Provider: "groq"}).ProviderName() != "groq" {
		t.Error("explicit provider ignored")
	}
	for model, want := range map[string]string{
		"qwen/qwen3-235b-a22b": "qwen", "anthropic/claude-sonnet-4.5": "anthropic", "gpt-4.1": "openai",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo": "meta", "llama3.1:8b": "meta", "claude-opus-5": "anthropic",
		"mistralai/mistral-large": "mistral", "z-ai/glm-4.6": "zhipu", "o4-mini": "openai", "my-model": "",
		"openrouter/auto/deepseek-chat": "deepseek",
	} {
		if got := ModelVendor(model); got != want {
			t.Errorf("%s: %q, want %q", model, got, want)
		}
	}
}

func TestProtocolGuards(t *testing.T) {
	t.Setenv("RLCD_GATEWAY_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRoute("groq", Route{Kind: KindOpenAI, BaseURL: "https://api.groq.com/openai/v1", Auth: AuthKey,
		Headers: map[string]string{"X-Title": "app"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActiveRoute("groq"); err == nil {
		t.Error("an OpenAI route became the Anthropic active route")
	}
	if err := s.SetDefaultOpenAIRoute("claude-sub"); err == nil {
		t.Error("an Anthropic route became the default OpenAI route")
	}
	if err := s.SetDefaultOpenAIRoute("groq"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute("groq"); err == nil {
		t.Error("deleted the default OpenAI route")
	}
	if err := s.UpsertRoute("bad", Route{Kind: KindOpenAI, BaseURL: "https://x", Auth: AuthKey,
		Headers: map[string]string{"Authorization": "Bearer leak"}}); err == nil {
		t.Error("a route may not set Authorization through headers")
	}
}
