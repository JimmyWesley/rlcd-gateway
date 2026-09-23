package pipeline

import "testing"

func TestMarkerRoundTrip(t *testing.T) {
	m := Marker("20260923T010203-abcd", "m4.b0", 9818)
	id, key, ok := ParseMarker("prefix " + m + " suffix")
	if !ok || id != "20260923T010203-abcd" || key != "m4.b0" {
		t.Fatalf("got %q %q %v from %q", id, key, ok, m)
	}
}

func TestConversationID(t *testing.T) {
	cc := `{"metadata":{"user_id":"user_x_account_y_session_1b2c3d4e-aaaa-bbbb-cccc-123456789abc"},"messages":[]}`
	if got := ConversationID([]byte(cc)); got != "cc-1b2c3d4e-aaaa-bbbb-cccc-123456789abc" {
		t.Errorf("claude code session: %s", got)
	}
	a := `{"system":"s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`
	b := `{"system":"s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	if ConversationID([]byte(a)) != ConversationID([]byte(b)) {
		t.Error("later turns of the same conversation must keep the id")
	}
	c := `{"system":"s","messages":[{"role":"user","content":"other"}]}`
	if ConversationID([]byte(a)) == ConversationID([]byte(c)) {
		t.Error("different conversations must differ")
	}
}
