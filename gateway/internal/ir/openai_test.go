package ir

import (
	"strings"
	"testing"
)

const responsesReq = `{"model":"gpt-5-codex","stream":true,"instructions":"You are Codex.","prompt_cache_key":"sess-123",
 "tools":[{"type":"function","name":"shell","description":"Run a command","parameters":{}},{"type":"web_search"}],
 "input":[
  {"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},
  {"type":"reasoning","summary":[],"encrypted_content":"gAAAAB..."},
  {"type":"function_call","name":"shell","arguments":"{\"cmd\":[\"ls\"]}","call_id":"call_1"},
  {"type":"function_call_output","call_id":"call_1","output":"README.md\nmain.go"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Found it."}]}
 ]}`

const chatReq = `{"model":"gpt-4.1","stream":true,"stream_options":{"include_usage":true},
 "tools":[{"type":"function","function":{"name":"read","description":"Read a file"}}],
 "messages":[
  {"role":"system","content":"Be brief."},
  {"role":"user","content":[{"type":"text","text":"hello"}]},
  {"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]},
  {"role":"tool","tool_call_id":"t1","content":"file body"}
 ]}`

func TestParseResponsesXRay(t *testing.T) {
	x, err := ParseResponses([]byte(responsesReq))
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ key, kind, role, name, id string }
	var got []row
	for _, b := range x.Blocks {
		got = append(got, row{b.Key, b.Kind, b.Role, b.Name, b.ToolUseID})
	}
	want := []row{
		{"sys.0", KindSystem, "", "", ""},
		{"tool.0", KindTool, "", "shell", ""},
		{"tool.1", KindTool, "", "web_search", ""},
		{"m0.b0", KindSystem, "developer", "", ""},
		{"m1.b0", KindText, "user", "", ""},
		{"m1.b1", KindImage, "user", "", ""},
		{"m2.b0", KindThinking, "assistant", "", ""},
		{"m3.b0", KindToolUse, "assistant", "shell", "call_1"},
		{"m4.b0", KindToolResult, "user", "", "call_1"},
		{"m5.b0", KindText, "assistant", "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if x.Messages != 6 || x.Model != "gpt-5-codex" || !x.Stream {
		t.Errorf("header %+v", x)
	}
	if b := x.Blocks[8]; b.Preview != "README.md\nmain.go" || b.Chars != len("README.md\nmain.go") {
		t.Errorf("tool result %+v", b)
	}
	if x.Blocks[6].Preview != "[encrypted reasoning]" {
		t.Errorf("reasoning preview %q", x.Blocks[6].Preview)
	}
	sum := 0
	for _, b := range x.Blocks {
		sum += b.Tokens
	}
	if sum != x.Tokens || x.ByKind[KindImage] != EstimateTokens(imageChars) {
		t.Errorf("token totals: sum %d total %d by kind %+v", sum, x.Tokens, x.ByKind)
	}
}

func TestParseChatXRay(t *testing.T) {
	x, err := ParseChat([]byte(chatReq))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, b := range x.Blocks {
		keys = append(keys, b.Key+":"+b.Kind+":"+b.ToolUseID)
	}
	want := "tool.0:tool: m0.b0:system: m1.b0:text: m2.b0:tool_use:t1 m3.b0:tool_result:t1"
	if strings.Join(keys, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(keys, " "), want)
	}
	if x.Blocks[0].Name != "read" || x.Blocks[3].Name != "read" {
		t.Errorf("names: %+v", x.Blocks)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := ParseResponses([]byte(`{"input": 5}`)); err == nil {
		t.Error("numeric input accepted")
	}
	if _, err := ParseChat([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
}
