package pipeline

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

func TestMarkerRoundTrip(t *testing.T) {
	m := Marker("20260923T010203-abcd", "m4.b0", 9818)
	id, key, ok := ParseMarker("prefix " + m + " suffix")
	if !ok || id != "20260923T010203-abcd" || key != "m4.b0" {
		t.Fatalf("got %q %q %v from %q", id, key, ok, m)
	}
}

func TestConversationID(t *testing.T) {
	cc := `{"metadata":{"user_id":"user_x_account_y_session_1b2c3d4e-aaaa-bbbb-cccc-123456789abc"},"messages":[]}`
	if got := ConversationID(nil, []byte(cc)); got != "cc-1b2c3d4e-aaaa-bbbb-cccc-123456789abc" {
		t.Errorf("claude code session: %s", got)
	}
	a := `{"system":"s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`
	b := `{"system":"s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	if ConversationID(nil, []byte(a)) != ConversationID(nil, []byte(b)) {
		t.Error("later turns of the same conversation must keep the id")
	}
	c := `{"system":"s","messages":[{"role":"user","content":"other"}]}`
	if ConversationID(nil, []byte(a)) == ConversationID(nil, []byte(c)) {
		t.Error("different conversations must differ")
	}
}

// Real Claude Code 2.1.280 shapes, ids replaced with fake values.
const ccBilling = `x-anthropic-billing-header: cc_version=2.1.280.%s; cc_entrypoint=cli`

func ccBody(suffix, userID, prompt string) []byte {
	b := `{"model":"claude-opus-5","metadata":{"user_id":` + userID + `},
	 "system":[{"type":"text","text":"` + fmt.Sprintf(ccBilling, suffix) + `"},{"type":"text","text":"You are Claude Code."}],
	 "messages":[{"role":"user","content":"` + prompt + `"}]}`
	return []byte(b)
}

func TestConversationIDClaudeCode2280(t *testing.T) {
	jsonUID := `"{\"device_id\":\"dev-0000\",\"account_uuid\":\"acct-0000\",\"session_id\":\"11111111-2222-3333-4444-555555555555\"}"`
	noSession := `"{\"device_id\":\"dev-0000\",\"account_uuid\":\"acct-0000\"}"`

	// 1. The header wins over everything.
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "aaaaaaaa-0000-0000-0000-000000000001")
	if got := ConversationID(h, ccBody("afb", jsonUID, "hi")); got != "cc-aaaaaaaa-0000-0000-0000-000000000001" {
		t.Errorf("header: %s", got)
	}
	// 2. The session id inside a JSON user_id.
	if got := ConversationID(nil, ccBody("afb", jsonUID, "hi")); got != "cc-11111111-2222-3333-4444-555555555555" {
		t.Errorf("json user_id: %s", got)
	}
	// 3. Two sessions with the same prompt must not merge.
	h2 := http.Header{}
	h2.Set("X-Claude-Code-Session-Id", "bbbbbbbb-0000-0000-0000-000000000002")
	if ConversationID(h, ccBody("afb", noSession, "hi")) == ConversationID(h2, ccBody("afb", noSession, "hi")) {
		t.Error("sessions with the same prompt merged")
	}
	// 4. Hash fallback: the billing line's suffix must not split a session.
	main, bg := ConversationID(nil, ccBody("afb", noSession, "hi")), ConversationID(nil, ccBody("0e4", noSession, "hi"))
	if main != bg || main[:2] != "h-" {
		t.Errorf("billing suffix changed the id: %s vs %s", main, bg)
	}
	if ConversationID(nil, ccBody("afb", noSession, "other")) == main {
		t.Error("different first prompts must differ")
	}
}

func TestStableSystemKeepsLegacyBytes(t *testing.T) {
	raw := []byte(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	if string(StableSystem(raw)) != string(raw) {
		t.Error("a system prompt without a billing line must hash as before")
	}
}

func TestOpenAIConversationIDs(t *testing.T) {
	chat := func(user, first string, more bool) []byte {
		b := `{"model":"gpt-4.1","user":"` + user + `","messages":[{"role":"system","content":"s"},{"role":"user","content":"` + first + `"}`
		if more {
			b += `,{"role":"assistant","content":"a"},{"role":"user","content":"b"}`
		}
		return []byte(b + `]}`)
	}
	id := func(b []byte) string { return ConversationIDFor(ir.ProtocolOpenAIChat, nil, b) }
	if id(chat("u1", "hi", false)) != id(chat("u1", "hi", true)) {
		t.Error("later turns must keep the id")
	}
	if id(chat("u1", "hi", false)) == id(chat("u2", "hi", false)) {
		t.Error("two end users opening with the same words must not share an id")
	}
	h := http.Header{}
	h.Set("Conversation-Id", "c-42")
	if got := ConversationIDFor(ir.ProtocolOpenAIChat, h, chat("u1", "hi", false)); got != "cx-c-42" {
		t.Errorf("header: %s", got)
	}
	if got := ConversationIDFor(ir.ProtocolOpenAIResponses, nil, []byte(`{"prompt_cache_key":"sess-1","input":"x"}`)); got != "cx-sess-1" {
		t.Errorf("prompt_cache_key: %s", got)
	}
}
