package ir

import (
	"strings"
	"testing"
)

func TestParseSystemOne(t *testing.T) {
	body := `{"model":"jev-latest","state":{"ticket":"refund please"},"think":true,"questions":{
	  "urgency":{"type":"score","instructions":"How urgent?","criteria":["low","medium","high"]},
	  "intent":{"type":"choice","instructions":"What for?","criteria":{"billing":"money","other":"anything else"}},
	  "refund":{"type":"noul","instructions":"Asks for a refund?"}}}`
	x, err := ParseFor(ProtocolSystemOne, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if x.Model != "jev-latest" || len(x.Blocks) != 4 || x.Blocks[0].Kind != KindState || x.Blocks[0].Chars != len(`{"ticket":"refund please"}`) {
		t.Fatalf("%+v", x)
	}
	// Questions keep the client's order.
	want := []struct{ key, typ, labels string }{{"urgency", "score", "low,medium,high"}, {"intent", "choice", "billing,other"}, {"refund", "noul", ""}}
	for i, w := range want {
		b := x.Blocks[i+1]
		if b.Key != w.key || b.Kind != KindQuestion || b.Name != w.typ || strings.Join(b.Labels, ",") != w.labels || b.Index != i {
			t.Errorf("question %d: %+v", i, b)
		}
	}
	if x.ByKind[KindState] == 0 || x.ByKind[KindQuestion] == 0 || x.Tokens != x.ByKind[KindState]+x.ByKind[KindQuestion] {
		t.Errorf("tokens: %+v", x.ByKind)
	}
	if _, err := ParseSystemOne([]byte("not json")); err == nil {
		t.Error("parsed garbage")
	}
}
