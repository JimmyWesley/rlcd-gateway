package prune

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

// A new drop asks the store to keep the request body whatever the bodies
// policy, and pins that request for as long as the conversation lives.
func TestDropsPinTheirRequest(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := baseConv()
	id1, res, s, _ := h.run(c.body(t))
	if !s.Applied || s.NewDrops == 0 || !res.KeepBody {
		t.Fatalf("first epoch should drop and keep its body: %+v keep=%v", s, res.KeepBody)
	}
	// The next turn re-applies the same drops: nothing new to keep.
	h.set(Settings{Mode: ModeEnforce, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(0), EpochTokens: ptr(1 << 30)})
	c.call("toolu_ls", "Bash", map[string]any{"command": "ls"}, big("NOISE ls"), false)
	_, res2, s2, _ := h.run(c.body(t))
	if s2.NewDrops != 0 || res2.KeepBody {
		t.Fatalf("second turn: %+v keep=%v", s2, res2.KeepBody)
	}

	conv := pipeline.ConversationID(nil, c.body(t))
	convs := h.p.StorageConversations()
	if len(convs) != 1 || convs[0].ID != conv || len(convs[0].Pins) != 1 || convs[0].Pins[0] != id1 {
		t.Fatalf("conversations: %+v", convs)
	}
	if time.Since(convs[0].LastActive) > time.Minute {
		t.Errorf("last active %v", convs[0].LastActive)
	}

	// Active since the cutoff: kept.
	if n, err := h.p.ExpireConversations([]string{conv}, time.Now().Add(-time.Hour)); n != 0 || err != nil {
		t.Fatalf("expired an active conversation: %d %v", n, err)
	}
	// Idle: its state goes, and so do its pins.
	if n, err := h.p.ExpireConversations([]string{conv}, time.Now().Add(time.Hour)); n != 1 || err != nil {
		t.Fatalf("expire: %d %v", n, err)
	}
	if _, err := os.Stat(h.p.states.path(conv)); !os.IsNotExist(err) {
		t.Fatal("state file still there")
	}
	if len(h.p.StorageConversations()) != 0 {
		t.Fatal("still listed")
	}
}

// Enforce falls back to shadow when the storage bodies policy keeps none.
func TestEnforceNeedsBodies(t *testing.T) {
	h := newHarness(t, testSettings(ModeEnforce))
	c := h.cfg.Get()
	c.Sections["storage"] = json.RawMessage(`{"bodies":"none"}`)
	h.cfg = config.NewStore(&c)
	h.p.cfg = h.cfg
	_, res, s, _ := h.run(baseConv().body(t))
	if res.Body != nil || s.Warning == "" {
		t.Fatalf("enforce with bodies none must not rewrite: %+v", s)
	}
	// Decisions taken in shadow name this request: its body is asked for,
	// so a later enforce can still be recalled.
	if s.NewDrops == 0 || !res.KeepBody {
		t.Fatalf("shadow drops must keep their body: %+v", s)
	}
	// errors_only still enforces: the bodies markers point at are kept.
	c.Sections["storage"] = json.RawMessage(`{"bodies":"errors_only"}`)
	h.cfg = config.NewStore(&c)
	h.p.cfg = h.cfg
	_, res, s, _ = h.run(baseConv().call("toolu_x", "Bash", map[string]any{"command": "x"}, big("NOISE x"), false).body(t))
	if res.Body == nil || !s.Applied {
		t.Fatalf("errors_only should enforce: %+v", s)
	}
}
