package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

func TestInsights(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return base }
	t.Cleanup(func() { now = time.Now })

	prune := func(s string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"prune": json.RawMessage(s)}
	}
	recs := []store.Record{
		{ID: "a", Time: base.Add(-30 * time.Minute), Route: "sub", Model: "claude-x", Status: 200, DurationMs: 1000,
			ConversationID: "c1", Usage: &store.Usage{InputTokens: 10, CacheReadTokens: 100, CacheCreationTokens: 20, OutputTokens: 5},
			Stages: prune(`{"mode":"enforce","applied":true,"dropped":2,"saved_tokens":500,"est_cost_before":0.01,"est_cost_after":0.004,"epoch_ran":true,"selector_ms":200}`)},
		{ID: "b", Time: base.Add(-90 * time.Minute), Route: "sub", Model: "claude-x", Status: 200, DurationMs: 3000,
			ConversationID: "c1", Stages: prune(`{"mode":"shadow","saved_tokens":300,"est_cost_before":0.01,"est_cost_after":0.012,"cache_invalidating":true}`)},
		{ID: "c", Time: base.Add(-2 * time.Hour), Route: "or", ClientModel: "haiku", Status: 500, DurationMs: 50,
			Stages: prune(`{"mode":"enforce","selector_error":"timeout"}`)},
		{ID: "old", Time: base.Add(-48 * time.Hour), Route: "sub", Status: 200},
	}
	for i := range recs {
		if err := st.Save(&store.Detail{Record: recs[i]}); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	(&API{Store: st}).Register(mux)
	get := func(q string) (*httptest.ResponseRecorder, insightsView) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/insights"+q, nil))
		var v insightsView
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w, v
	}

	w, v := get("?window=24h")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if v.BucketSeconds != 3600 || len(v.Buckets) != 25 {
		t.Errorf("buckets: %ds × %d", v.BucketSeconds, len(v.Buckets))
	}
	tt := v.Totals
	if tt.Requests != 3 || tt.Errors != 1 || tt.Conversations != 1 {
		t.Errorf("totals: %+v", tt)
	}
	if tt.SavedTokens != 500 || tt.SavedUSD != 0.006 || tt.ShadowSavedTokens != 300 || tt.ShadowSavedUSD != -0.002 {
		t.Errorf("savings: %+v", tt)
	}
	if tt.PrunedRequests != 1 || tt.CacheInvalidations != 1 || tt.CacheReadTokens != 100 || tt.CacheWriteTokens != 20 {
		t.Errorf("prune totals: %+v", tt)
	}
	if v.Selector.Epochs != 1 || v.Selector.AvgMs != 200 || v.Selector.Errors != 1 || v.Selector.LastError != "timeout" {
		t.Errorf("selector: %+v", v.Selector)
	}
	if v.Latency.P50Ms != 1000 || v.Latency.P95Ms != 3000 {
		t.Errorf("latency: %+v", v.Latency)
	}
	if len(v.ByRoute) != 2 || v.ByRoute[0].Name != "sub" || v.ByRoute[0].Requests != 2 || v.ByRoute[0].Tokens != 130 {
		t.Errorf("by route: %+v", v.ByRoute)
	}
	if len(v.ByModel) != 2 || v.ByModel[1].Name != "haiku" || v.ByModel[1].Errors != 1 {
		t.Errorf("by model: %+v", v.ByModel)
	}
	var sum int
	for _, b := range v.Buckets {
		sum += b.Requests
	}
	if sum != 3 {
		t.Errorf("bucketed %d requests, want 3", sum)
	}

	if _, v := get("?window=1h"); v.Totals.Requests != 1 || v.BucketSeconds != 120 {
		t.Errorf("1h: %d requests, %ds buckets", v.Totals.Requests, v.BucketSeconds)
	}
	if _, v := get("?window=all"); v.Totals.Requests != 4 || v.BucketSeconds != 7200 {
		t.Errorf("all: %d requests, %ds buckets", v.Totals.Requests, v.BucketSeconds)
	}
	if _, v := get("?window=7d"); v.Totals.Requests != 4 {
		t.Errorf("7d: %d requests", v.Totals.Requests)
	}
	if w, _ := get("?window=soon"); w.Code != 400 {
		t.Errorf("bad window: status %d", w.Code)
	}
}

func TestPercentile(t *testing.T) {
	xs := []int64{5, 1, 4, 2, 3}
	if p := percentile(xs, 50); p != 3 {
		t.Errorf("p50 = %d", p)
	}
	if p := percentile(xs, 100); p != 5 {
		t.Errorf("p100 = %d", p)
	}
	if p := percentile(nil, 95); p != 0 {
		t.Errorf("empty = %d", p)
	}
}
