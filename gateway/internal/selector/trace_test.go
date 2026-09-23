package selector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// A traced context sees every call, with its source, parent and the raw
// exchange; an observer that panics never breaks the call.
func TestTraceObservesCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rlcd-Forward-Ms", "12.5")
		_, _ = w.Write([]byte(`{"answers":{"keep":{"type":"noul","noul":0.9}}}`))
	}))
	defer srv.Close()
	var got []Call
	ctx := WithTrace(context.Background(), Trace{ParentID: "req-1", ConversationID: "conv-1",
		Observe: func(c Call) { got = append(got, c) }})
	c := New(config.Selector{BaseURL: srv.URL, Model: "m"})
	if _, err := c.Ask(WithSource(ctx, "prune"), "s", map[string]Question{"keep": {Type: "noul"}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "prune" || got[0].ParentID != "req-1" || got[0].ConversationID != "conv-1" ||
		got[0].Status != 200 || string(got[0].Response) != `{"answers":{"keep":{"type":"noul","noul":0.9}}}` ||
		got[0].Header.Get("X-Rlcd-Forward-Ms") != "12.5" || len(got[0].Body) == 0 || got[0].Err != nil {
		t.Fatalf("observed: %+v", got)
	}
	panicky := WithTrace(context.Background(), Trace{Observe: func(Call) { panic("audit bug") }})
	if _, err := c.Ask(panicky, "s", map[string]Question{"keep": {Type: "noul"}}); err != nil {
		t.Fatal(err)
	}
	// Untraced calls cost nothing extra.
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"keep": {Type: "noul"}}); err != nil || len(got) != 1 {
		t.Fatal(err, len(got))
	}
}
