package pricing

import (
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

func TestCostIsProtocolAware(t *testing.T) {
	u := &store.Usage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000, OutputTokens: 1_000_000}
	// Anthropic: input 3 + read 0.30 + write 3.75 + output 15.
	if got := Cost(Defaults, "claude-sonnet-4-5", ir.ProtocolAnthropic, u); got != 22.05 {
		t.Errorf("anthropic cost %v", got)
	}
	// OpenAI: no write premium, whatever the row says.
	if got := Cost(Defaults, "gpt-4.1-2025-04-14", ir.ProtocolOpenAIChat, u); got != 2+0.5+2+8 {
		t.Errorf("openai cost %v", got)
	}
	if _, key := For(Defaults, "openai/gpt-4o-mini"); key != "gpt-4o-mini" {
		t.Errorf("routed model: %s", key)
	}
	if _, key := For(Defaults, "meta-llama/Llama-3.3-70B-Instruct-Turbo"); key != "llama-3-3-70b" {
		t.Errorf("case and dots: %s", key)
	}
	if Cost(Defaults, "x", ir.ProtocolAnthropic, nil) != 0 {
		t.Error("no usage, no cost")
	}
}
