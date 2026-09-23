package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// decodeAny parses JSON keeping numbers exact, for semantic comparison.
func decodeAny(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

func big(tag string, n int) string {
	return strings.Repeat(tag+" lorem ipsum dolor sit amet <b>&amp;</b> ", n/len(tag)+1)[:n]
}

const (
	anthropicBody = `{
  "model": "claude-sonnet-4-5", "max_tokens": 32000, "stream": true,
  "system": [{"type": "text", "text": %q}, {"type": "text", "text": "short", "cache_control": {"type": "ephemeral"}}],
  "tools": [{"name": "Read", "description": %q, "input_schema": {"type": "object"}}, {"name": "Bash", "description": "run", "input_schema": {}}],
  "messages": [
    {"role": "user", "content": "hello"},
    {"role": "assistant", "content": [{"type": "thinking", "thinking": "hm", "signature": "sig"}, {"type": "tool_use", "id": "toolu_1", "name": "Read", "input": {"path": "a.go"}}]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": %q, "is_error": false}]},
    {"role": "user", "content": []}
  ],
  "temperature": 0.7000000000000001, "big_int": 12345678901234567890, "metadata": {"user_id": "u"}
}`
	chatBody = `{"model":"gpt-4.1","stream":true,"messages":[{"role":"system","content":%q},{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"p\":1}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":%q}],"tools":[{"type":"function","function":{"name":"read","description":%q}}],"n":1}`
	responsesBody = `{"model":"gpt-5","instructions":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},` +
		`{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":%q},` +
		`{"type":"reasoning","encrypted_content":"xyz","summary":[]}],"tools":[{"type":"function","name":"shell","parameters":{}}],"store":false}`
)

func protocolBodies() map[string]string {
	return map[string]string{
		"anthropic-messages": fmt.Sprintf(anthropicBody, big("sys", 3000), big("tool", 2000), big("result", 5000)),
		"openai-chat":        fmt.Sprintf(chatBody, big("sys", 3000), big("out", 4000), big("desc", 1500)),
		"openai-responses":   fmt.Sprintf(responsesBody, big("inst", 2500), big("out", 6000)),
	}
}

// The reassembled body parses equal to the original for every protocol,
// through Save and Get, and the Detail keeps its shape.
func TestRoundTripAllProtocols(t *testing.T) {
	s := openTemp(t)
	for proto, body := range protocolBodies() {
		t.Run(proto, func(t *testing.T) {
			sent := strings.Replace(body, `"stream":true`, `"stream":false`, 1)
			d := &Detail{Record: Record{ID: "20260923T010203-" + strings.ReplaceAll(proto, "-", ""), Protocol: proto, Status: 200,
				Stages: map[string]json.RawMessage{"prune": json.RawMessage(`{"mode":"shadow"}`)}},
				RequestHeaders: map[string]string{"Content-Type": "application/json"},
				StageDetails:   map[string]json.RawMessage{"prune": json.RawMessage(`{"blocks":[1,2,3]}`)},
				RequestBody:    body, SentBody: sent, ResponseBody: "event: x\ndata: {}\n\n"}
			x, err := ir.ParseFor(proto, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			d.XRay = x
			if err := s.Save(d); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeAny(t, got.RequestBody), decodeAny(t, body)) {
				t.Fatalf("request body differs:\n%s\n%s", got.RequestBody, body)
			}
			if !reflect.DeepEqual(decodeAny(t, got.SentBody), decodeAny(t, sent)) {
				t.Fatal("sent body differs")
			}
			// Compacted, not byte-exact; but key order and escaping are kept.
			var want bytes.Buffer
			_ = json.Compact(&want, []byte(body))
			if got.RequestBody != want.String() {
				t.Errorf("reassembled body is not the compacted original:\n got %.200s\nwant %.200s", got.RequestBody, want.String())
			}
			// The X-ray reads back the same, whether it was stored or
			// derived again from the body (the pretty-printed Anthropic
			// body's tool sizes count whitespace, so that one is stored).
			if !reflect.DeepEqual(got.XRay, x) {
				t.Errorf("x-ray differs:\n%+v\n%+v", got.XRay, x)
			}
			if got.ResponseBody != d.ResponseBody || got.RequestHeaders["Content-Type"] != "application/json" ||
				string(got.StageDetails["prune"]) != `{"blocks":[1,2,3]}` || string(got.Stages["prune"]) != `{"mode":"shadow"}` ||
				got.Protocol != proto || got.Status != 200 {
				t.Errorf("detail fields changed: %+v", got)
			}
			if len(got.RequestBody) >= inlineMax && s.blobCount(t) == 0 {
				t.Error("no blob written")
			}
		})
	}
}

func (s *Store) blobCount(t *testing.T) int {
	t.Helper()
	return len(s.scanBlobs())
}

// Bodies that are not a JSON object are kept whole and byte for byte.
func TestRawBodiesKeptExactly(t *testing.T) {
	s := openTemp(t)
	for i, body := range []string{"not json at all \x00\xff", `[1, 2, 3]`, `"just a string"`, `{"a":1} trailing`, `{}`, big("x", 5000)} {
		id := fmt.Sprintf("raw%d", i)
		if err := s.Save(&Detail{Record: Record{ID: id}, RequestBody: body}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.RequestBody != body {
			t.Errorf("%d: got %q want %q", i, got.RequestBody, body)
		}
	}
}

// growingTurn is turn n of an agent session: the same system prompt and
// tools, and one more pair of messages than the turn before.
func growingTurn(n int) string {
	var msgs []string
	for i := 0; i < n; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":%q}]}`, i, big(fmt.Sprintf("file%d", i), 4000)),
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":"ok %d"},{"type":"tool_use","id":"toolu_%d","name":"Read","input":{"i":%d}}]}`, i, i+1, i))
	}
	msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":"turn %d","cache_control":{"type":"ephemeral"}}]}`, n))
	return fmt.Sprintf(`{"model":"m","system":[{"type":"text","text":%q}],"tools":[{"name":"Read","description":%q}],"messages":[%s]}`,
		big("system", 12000), big("tools", 8000), strings.Join(msgs, ","))
}

// N turns of a growing conversation store about one copy of the shared
// prefix, not N.
func TestDedupAcrossTurns(t *testing.T) {
	s := openTemp(t)
	const turns = 20
	var logical int
	for n := 1; n <= turns; n++ {
		body := growingTurn(n)
		logical += len(body)
		if err := s.Save(&Detail{Record: Record{ID: fmt.Sprintf("20260923T0100%02d-aa", n)}, RequestBody: body}); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.DiskUsage()
	if err != nil {
		t.Fatal(err)
	}
	last := len(growingTurn(turns))
	// Unique content is about the last turn; allow skeletons and gzip overhead.
	var raw int64
	for h := range s.scanBlobs() {
		b, err := s.getBlob(h)
		if err != nil {
			t.Fatal(err)
		}
		raw += int64(len(b))
	}
	if raw > int64(last)*11/10 {
		t.Errorf("unique blob bytes %d for a final turn of %d: the prefix was stored more than once", raw, last)
	}
	if u.StoredBytes > int64(logical)/8 {
		t.Errorf("stored %d bytes for %d logical: dedup did not work", u.StoredBytes, logical)
	}
	if u.DedupRatio < 5 {
		t.Errorf("dedup ratio %.2f, want >= 5", u.DedupRatio)
	}
	// Every turn still reads back.
	for n := 1; n <= turns; n++ {
		d, err := s.Get(fmt.Sprintf("20260923T0100%02d-aa", n))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decodeAny(t, d.RequestBody), decodeAny(t, growingTurn(n))) {
			t.Fatalf("turn %d differs", n)
		}
	}
	t.Logf("%d turns: logical %d bytes, stored %d (blobs %d, records %d), dedup %.1fx, compression %.1fx",
		turns, logical, u.StoredBytes, u.Bytes.Blobs, u.Bytes.Records, u.DedupRatio, u.CompressionRatio)
}

// Legacy requests/<id>.json files still read, and compact rewrites them
// into the new format with the same content.
func TestLegacyReadAndCompact(t *testing.T) {
	s := openTemp(t)
	body := protocolBodies()["anthropic-messages"]
	legacy := &Detail{Record: Record{ID: "20260101T000000-legacy", Status: 200, ConversationID: "c1", Usage: &Usage{InputTokens: 5}},
		RequestHeaders: map[string]string{"User-Agent": "claude-cli/2"}, RequestBody: body,
		StageDetails: map[string]json.RawMessage{"prune": json.RawMessage(`{"x":1}`)}, ResponseBody: "resp"}
	b, _ := json.Marshal(legacy)
	path := filepath.Join(s.dir, "requests", legacy.ID+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(path, old, old)
	check := func(label string) {
		t.Helper()
		got, err := s.Get(legacy.ID)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !reflect.DeepEqual(decodeAny(t, got.RequestBody), decodeAny(t, body)) || got.ResponseBody != "resp" ||
			got.Usage == nil || got.Usage.InputTokens != 5 || string(got.StageDetails["prune"]) != `{"x":1}` ||
			got.RequestHeaders["User-Agent"] != "claude-cli/2" {
			t.Fatalf("%s: detail differs: %+v", label, got)
		}
	}
	check("legacy")
	u, _ := s.DiskUsage()
	if u.Counts.LegacyRecords != 1 {
		t.Fatalf("legacy records = %d", u.Counts.LegacyRecords)
	}

	rep, err := s.Compact(true)
	if err != nil || rep.Converted != 1 {
		t.Fatalf("dry compact: %+v %v", rep, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dry run removed the legacy file")
	}
	rep, err = s.Compact(false)
	if err != nil || rep.Converted != 1 || rep.BytesAfter == 0 {
		t.Fatalf("compact: %+v %v", rep, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy file still there")
	}
	info, err := os.Stat(s.recordPath(legacy.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("compact did not keep the modification time: %v", info.ModTime())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v", info.Mode().Perm())
	}
	check("compacted")
	if rep, _ := s.Compact(false); rep.Converted != 0 {
		t.Error("second compact converted again")
	}
}

// The index rotates per month and loads across files, the legacy
// index.jsonl first.
func TestIndexRotationAndLoading(t *testing.T) {
	dir := t.TempDir()
	legacy, _ := json.Marshal(Record{ID: "old", Time: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), ConversationID: "c"})
	if err := os.WriteFile(filepath.Join(dir, "index.jsonl"), append(legacy, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{
		time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC),
		time.Date(2026, 9, 23, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600)),
	}
	for i, tm := range times {
		if err := s.Save(&Detail{Record: Record{ID: fmt.Sprintf("r%d", i), Time: tm, ConversationID: "c"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index-2026-08.jsonl", "index-2026-09.jsonl"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", name, info.Mode().Perm())
		}
	}
	re, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range re.Recent() {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "r2,r1,r0,old" {
		t.Fatalf("recent after reload = %v", ids)
	}
	if got := re.lastSeen["c"]; !got.Equal(times[2]) {
		t.Errorf("last seen = %v", got)
	}
}

// An X-ray that parsing the stored body reproduces is not stored twice.
func TestXRayDerivedFromBody(t *testing.T) {
	s := openTemp(t)
	body := growingTurn(3)
	x, _ := ir.ParseFor("", []byte(body))
	d := &Detail{Record: Record{ID: "20260923T000000-x"}, XRay: x, RequestBody: body}
	rec, _, err := encodeRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gunzip(t, rec), []byte(`"xray_from_body":true`)) {
		t.Fatal("x-ray stored although the body reproduces it")
	}
	// Inline leaves are written back as sent, without HTML escaping.
	amp := &Detail{Record: Record{ID: "20260923T000000-amp"}, RequestBody: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a && b <c>"}]}]}`}
	if err := s.Save(amp); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(amp.ID); got.RequestBody != amp.RequestBody {
		t.Fatalf("escaping changed: %s", got.RequestBody)
	}
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(d.ID)
	if err != nil || !reflect.DeepEqual(got.XRay, x) {
		t.Fatalf("x-ray: %v", err)
	}
	// Without a body, the X-ray is kept as it is.
	d2 := &Detail{Record: Record{ID: "20260923T000000-y"}, XRay: x}
	_ = s.Save(d2)
	if got, _ := s.Get(d2.ID); !reflect.DeepEqual(got.XRay, x) {
		t.Fatal("x-ray without a body lost")
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.gz")
	_ = os.WriteFile(p, b, 0o600)
	out, err := gunzipFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
