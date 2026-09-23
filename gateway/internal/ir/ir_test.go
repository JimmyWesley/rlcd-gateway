package ir

import "testing"

func TestParseKeysAndKinds(t *testing.T) {
	body := `{
	  "model": "claude-sonnet-4-5", "stream": true, "max_tokens": 1024,
	  "system": [{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],
	  "tools": [{"name":"Read","description":"Read a file","input_schema":{}}],
	  "messages": [
	    {"role":"user","content":"fix auth.go"},
	    {"role":"assistant","content":[
	      {"type":"thinking","thinking":"look at the file","signature":"x"},
	      {"type":"tool_use","id":"tu_1","name":"Read","input":{"path":"auth.go"}}]},
	    {"role":"user","content":[
	      {"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"package auth"}]}]}
	  ]}`
	req, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ key, kind string }{
		{"sys.0", KindSystem}, {"tool.0", KindTool}, {"m0.b0", KindText},
		{"m1.b0", KindThinking}, {"m1.b1", KindToolUse}, {"m2.b0", KindToolResult},
	}
	if len(req.Blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(req.Blocks), len(want))
	}
	for i, w := range want {
		b := req.Blocks[i]
		if b.Key != w.key || b.Kind != w.kind {
			t.Errorf("block %d = %s/%s, want %s/%s", i, b.Key, b.Kind, w.key, w.kind)
		}
	}
	if !req.Blocks[0].Cached {
		t.Error("system block should be marked cached")
	}
	if req.Blocks[4].ToolUseID != "tu_1" || req.Blocks[5].ToolUseID != "tu_1" {
		t.Error("tool_use and tool_result must share the id")
	}
	if req.Blocks[5].Chars != len("package auth") {
		t.Errorf("tool_result chars = %d", req.Blocks[5].Chars)
	}
}
