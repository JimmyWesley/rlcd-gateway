package selector

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// A failure before any response (here, the first connection is dropped) is
// retried once, so a flaky mDNS lookup doesn't make pruning fail open.
func TestAskRetriesOnceOnNetworkError(t *testing.T) {
	var calls int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"keep":{"type":"noul","noul":0.9}}}`))
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew && atomic.AddInt32(&calls, 1) == 1 {
			c.Close()
		}
	}
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := New(config.Selector{BaseURL: srv.URL, Model: "m"}).Ask(ctx, map[string]any{"g": 1},
		map[string]Question{"keep": {Type: "noul", Instructions: "?"}})
	if err != nil {
		t.Fatalf("expected the retry to succeed: %v", err)
	}
	if n := res.Answers["keep"].Noul; n == nil || *n != 0.9 {
		t.Errorf("answer: %+v", res.Answers)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("no retry happened (%d connections)", calls)
	}
}
