package recall

import (
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Recall of a body retention purged says so, instead of "not found", even
// when the block was recalled (and cached) before.
func TestRecallExpired(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	old := now.Add(-30*store.Day).Format("20060102T150405") + "-0a1b2c3d4e5f6a7b"
	body := fixtureBody(t, "old result")
	if err := e.rs.st.Save(&store.Detail{Record: store.Record{ID: old, ConversationID: "c-old"}, RequestBody: body}); err != nil {
		t.Fatal(err)
	}
	marker := pipeline.Marker(old, "toolu_B", 1500)
	if text, isErr := recallText(t, e, map[string]any{"marker": marker}); isErr || !strings.Contains(text, bigRead) {
		t.Fatalf("before purge: %.200s", text)
	}
	rep, err := e.rs.st.Purge(store.PurgeOptions{Now: now, Settings: store.DefaultSettings(), Grace: store.Grace})
	if err != nil || rep.DetailsDeleted != 1 {
		t.Fatalf("purge: %+v %v", rep, err)
	}
	text, isErr := recallText(t, e, map[string]any{"marker": marker})
	if !isErr || !strings.HasPrefix(text, "expired: the original was purged by retention after 14d (detail_max_age)") {
		t.Fatalf("after purge: isErr=%v %s", isErr, text)
	}
	if !strings.Contains(text, "Re-run the tool or re-read the file") {
		t.Errorf("no advice: %s", text)
	}
	if ev := e.rs.events.recent(1)[0]; ev.OK || ev.Error != ErrExpired {
		t.Errorf("event: %+v", ev)
	}
	// A request that never existed is still "unknown".
	if text, _ := recallText(t, e, map[string]any{"key": "toolu_A", "req": "20260101T000000-ffffffffffffffff"}); !strings.Contains(text, "no request with id") {
		t.Errorf("unknown: %s", text)
	}
}

// Expiring a conversation deletes its recall events, on disk too, and the
// events tied to no conversation that are older than the cutoff.
func TestRecallEventsExpire(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-10 * store.Day)
	for i, conv := range []string{"c1", "c2", "", "c1"} {
		if err := e.rs.events.append(Event{Time: base.Add(time.Duration(i) * time.Hour), ConversationID: conv, Req: "r", Key: "k", OK: conv != ""}); err != nil {
			t.Fatal(err)
		}
	}
	convs := e.rs.StorageConversations()
	if len(convs) != 2 {
		t.Fatalf("conversations: %+v", convs)
	}
	for _, c := range convs {
		if c.ID == "c1" && !c.LastActive.Equal(base.Add(3*time.Hour)) {
			t.Errorf("c1 last active %v", c.LastActive)
		}
	}
	n, err := e.rs.ExpireConversations([]string{"c1"}, time.Now())
	if err != nil || n != 3 {
		t.Fatalf("expired %d %v", n, err)
	}
	left := e.rs.events.recent(0)
	if len(left) != 1 || left[0].ConversationID != "c2" {
		t.Fatalf("in memory: %+v", left)
	}
	again := New(e.cfg, e.rs.st)
	if got := again.events.recent(0); len(got) != 1 || got[0].ConversationID != "c2" {
		t.Fatalf("on disk: %+v", got)
	}
}
