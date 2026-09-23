package recall

import (
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const chatBody = `{"model":"gpt-4.1","messages":[
 {"role":"system","content":"Be brief."},
 {"role":"user","content":[{"type":"text","text":"fix it"}]},
 {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},
 {"role":"tool","tool_call_id":"call_1","content":"FULL FILE BODY"}]}`

const responsesBody = `{"model":"gpt-5-codex","instructions":"You are Codex.","input":[
 {"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},
 {"type":"function_call","name":"shell","arguments":"{}","call_id":"call_9"},
 {"type":"function_call_output","call_id":"call_9","output":[{"type":"input_text","text":"LS OUTPUT"}]}]}`

func TestRecallFromOpenAIBodies(t *testing.T) {
	e := newEnv(t)
	for _, d := range []*store.Detail{
		{Record: store.Record{ID: "chat1", Protocol: ir.ProtocolOpenAIChat, ConversationID: "h-chat"}, RequestBody: chatBody},
		{Record: store.Record{ID: "resp1", Protocol: ir.ProtocolOpenAIResponses, ConversationID: "cx-s"}, RequestBody: responsesBody},
	} {
		if err := e.rs.st.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ req, key, want string }{
		{"chat1", "call_1", "FULL FILE BODY"},
		{"chat1", "m1.b0", "fix it"},
		{"resp1", "call_9", "LS OUTPUT"},
		{"resp1", "sys.0", "You are Codex."},
	} {
		res := e.rs.Recall(Args{Req: c.req, Key: c.key}, "test", "")
		if res.Error || !strings.HasSuffix(res.Text, c.want) {
			t.Errorf("%s/%s: %q", c.req, c.key, res.Text)
		}
	}
	if ev := e.rs.Recall(Args{Req: "chat1", Key: "call_1"}, "test", "").Event; ev.Kind != ir.KindToolResult || ev.Tool != "read" {
		t.Errorf("event %+v", ev)
	}
}

func TestRecallIsScopedToTheKey(t *testing.T) {
	e := newEnv(t)
	if err := e.rs.st.Save(&store.Detail{Record: store.Record{ID: "k1req", Protocol: ir.ProtocolOpenAIChat, KeyID: "key_a"},
		RequestBody: chatBody}); err != nil {
		t.Fatal(err)
	}
	if res := e.rs.Recall(Args{Req: "k1req", Key: "call_1"}, "x", "key_a"); res.Error {
		t.Fatalf("own request: %s", res.Text)
	}
	res := e.rs.Recall(Args{Req: "k1req", Key: "call_1"}, "x", "key_b")
	if !res.Error || res.Event.Error != ErrUnknownReq || strings.Contains(res.Text, "FULL FILE") {
		t.Fatalf("another key's request must look absent: %+v", res)
	}
	// A key cannot reach requests made without a key either.
	if res := e.rs.Recall(Args{Req: "req1", Key: "toolu_A"}, "x", "key_b"); !res.Error {
		t.Fatal("keyless request leaked to a key")
	}
}
