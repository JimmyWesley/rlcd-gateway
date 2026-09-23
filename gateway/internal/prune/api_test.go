package prune

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

func TestAPI(t *testing.T) {
	h := newHarness(t, testSettings(ModeShadow))
	t.Setenv("RLCD_GATEWAY_HOME", h.home) // SetSection writes config.json there
	mux := http.NewServeMux()
	h.p.Register(mux)
	do := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, rd))
		return w.Code, w.Body.Bytes()
	}

	// Config: presets resolve, explicit fields override, bad input is rejected.
	code, b := do("PUT", "/api/prune/config", map[string]any{"preset": "aggressive", "keep_errors": true, "mode": "enforce"})
	if code != 200 {
		t.Fatalf("put config: %d %s", code, b)
	}
	var v configView
	_ = json.Unmarshal(b, &v)
	if v.Effective.Preset != PresetAggressive || !v.Effective.KeepErrors || v.Effective.KeepThreshold != 0.40 ||
		v.Effective.Mode != ModeEnforce || v.Effective.PruneTools {
		t.Fatalf("effective: %+v", v.Effective)
	}
	if code, _ := do("PUT", "/api/prune/config", map[string]any{"mode": "yolo"}); code != 400 {
		t.Fatal("bad mode accepted")
	}
	if code, b := do("GET", "/api/prune/presets", nil); code != 200 || !bytes.Contains(b, []byte("conservative")) {
		t.Fatalf("presets: %d %s", code, b)
	}

	// A request as the proxy would have stored it, with its pruning report.
	h.set(testSettings(ModeShadow))
	body := baseConv().body(t)
	id, res, _, _ := h.run(body)
	d := &store.Detail{Record: store.Record{ID: id, Time: time.Now(), Stages: map[string]json.RawMessage{"prune": res.Summary}}}
	d.StageDetails = map[string]json.RawMessage{"prune": res.Detail}
	d.RequestBody = string(body)
	if err := h.p.st.Save(d); err != nil {
		t.Fatal(err)
	}

	code, b = do("POST", "/api/prune/feedback", map[string]any{"request_id": id, "key": "toolu_web", "verdict": "should_keep", "note": "needed"})
	if code != 201 {
		t.Fatalf("feedback: %d %s", code, b)
	}
	var f Feedback
	_ = json.Unmarshal(b, &f)
	if f.Decision != "drop" || f.Goal == "" || f.Preview == "" || f.What == "" {
		t.Fatalf("snapshot incomplete: %+v", f)
	}
	if code, _ := do("POST", "/api/prune/feedback", map[string]any{"request_id": id, "key": "nope", "verdict": "should_keep"}); code != 404 {
		t.Fatal("unknown key accepted")
	}
	if code, _ := do("POST", "/api/prune/feedback", map[string]any{"request_id": id, "key": "toolu_web", "verdict": "maybe"}); code != 400 {
		t.Fatal("bad verdict accepted")
	}

	code, b = do("POST", "/api/prune/replay", nil)
	var rep ReplayReport
	_ = json.Unmarshal(b, &rep)
	if code != 200 || len(rep.Cases) != 1 || rep.Cases[0].Agrees {
		t.Fatalf("replay: %d %s", code, b)
	}

	code, b = do("GET", "/api/prune/stats", nil)
	var sv statsView
	_ = json.Unmarshal(b, &sv)
	if code != 200 || sv.Requests != 1 || sv.Shadow.SavedTokens <= 0 || sv.Feedback != 1 || sv.Epochs != 1 {
		t.Fatalf("stats: %s", b)
	}

	if code, _ := do("DELETE", "/api/prune/feedback/"+f.ID, nil); code != 200 {
		t.Fatal("delete")
	}
	if code, _ := do("DELETE", "/api/prune/feedback/"+f.ID, nil); code != 404 {
		t.Fatal("double delete")
	}
	if _, b := do("GET", "/api/prune/feedback", nil); string(bytes.TrimSpace(b)) != "[]" {
		t.Fatalf("feedback list: %s", b)
	}
}
