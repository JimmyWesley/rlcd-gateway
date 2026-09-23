package prune

import (
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// ConversationGoal reads the latest human text in every protocol, without
// the harness's injected blocks.
func TestConversationGoal(t *testing.T) {
	anth := []byte(`{"model":"m","messages":[
	  {"role":"user","content":[{"type":"text","text":"<system-reminder>CLAUDE.md</system-reminder>"},{"type":"text","text":"fix the login bug"}]},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"auth.go"}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package auth"},{"type":"text","text":"<command-name>/clear</command-name>"}]}]}`)
	g, recent, err := ConversationGoal(ir.ProtocolAnthropic, anth, 1)
	if err != nil || g != "fix the login bug" || recent == "" {
		t.Fatalf("anthropic: %q %q %v", g, recent, err)
	}
	chat := []byte(`{"model":"m","messages":[{"role":"system","content":"be nice"},{"role":"user","content":"first"},
	  {"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"second"}]}]}`)
	if g, _, _ := ConversationGoal(ir.ProtocolOpenAIChat, chat, 1); g != "second" {
		t.Fatalf("chat: %q", g)
	}
	if g, _, _ := ConversationGoal(ir.ProtocolOpenAIChat, chat, 2); g != "first\n---\nsecond" {
		t.Fatalf("chat, 2 turns: %q", g)
	}
	if g, _, _ := ConversationGoal(ir.ProtocolOpenAIResponses, []byte(`{"model":"m","input":"just a string"}`), 1); g != "just a string" {
		t.Fatalf("responses string: %q", g)
	}
	resp := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	if g, _, _ := ConversationGoal(ir.ProtocolOpenAIResponses, resp, 1); g != "hello" {
		t.Fatalf("responses items: %q", g)
	}
	if _, _, err := ConversationGoal(ir.ProtocolAnthropic, []byte(`not json`), 1); err == nil {
		t.Fatal("expected an error")
	}
}
