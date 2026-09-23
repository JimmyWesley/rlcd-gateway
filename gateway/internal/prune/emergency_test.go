package prune

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
)

func (h *harness) emergency(body []byte, threshold float64) (string, *pipeline.EmergencyResult) {
	h.t.Helper()
	h.n++
	id := "em" + string(rune('0'+h.n))
	r := &pipeline.Request{ID: id, Body: body, Config: h.cfg.Get(), ConversationID: pipeline.ConversationID(nil, body)}
	res, err := h.p.EmergencyPrune(context.Background(), r, pipeline.EmergencyOptions{KeepThreshold: threshold})
	if err != nil {
		h.t.Fatalf("emergency: %v", err)
	}
	return id, res
}

// No epoch due, shadow mode: the emergency pass still decides, applies
// its drops within every invariant, and they stick on the next turns.
func TestEmergencyPruneShadowBecomesSticky(t *testing.T) {
	s := Settings{Mode: ModeShadow, KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50), FloorTokens: ptr(1 << 30), EpochTokens: ptr(1 << 30)}
	h := newHarness(t, s)
	c := baseConv()
	body := c.body(t)
	if _, res, sum, _ := h.run(body); res.Body != nil || sum.EpochRan {
		t.Fatalf("no epoch is due: %+v", sum)
	}
	h.sel.reset()

	id, em := h.emergency(body, 0.5)
	if em.Body == nil || em.NewDrops == 0 || em.Skipped != "" || !em.KeepBody {
		t.Fatalf("emergency did nothing: %+v", em)
	}
	if err := verify(body, em.Body); err != nil {
		t.Fatalf("invariants: %v", err)
	}
	var sum Summary
	_ = json.Unmarshal(em.Summary, &sum)
	if !sum.Emergency || !sum.Applied || !sum.CacheInvalidating || sum.SavedTokens <= 0 {
		t.Fatalf("summary: %+v", sum)
	}
	tr := toolResults(t, em.Body)
	for _, key := range []string{"toolu_npm", "toolu_web"} {
		if !strings.Contains(tr[key], "[rlcd:") || !strings.Contains(tr[key], "req "+id) {
			t.Errorf("%s not dropped with a marker naming %s: %s", key, id, tr[key])
		}
	}
	// Errors, edits, recall results and the last turn are never touched.
	for _, key := range []string{"toolu_err", "toolu_edit", "toolu_recall", "toolu_end"} {
		if strings.Contains(tr[key], "[rlcd:") {
			t.Errorf("%s dropped", key)
		}
	}

	// The next turn, still in shadow mode, sends the same drops, byte for byte.
	c.call("toolu_next", "Bash", map[string]any{"command": "ls"}, "small", false)
	_, res, sum2, _ := h.run(c.body(t))
	if res.Body == nil || !sum2.Applied {
		t.Fatalf("emergency drops not applied on the next turn: %+v", sum2)
	}
	tr2 := toolResults(t, res.Body)
	for _, key := range []string{"toolu_npm", "toolu_web"} {
		if tr2[key] != tr[key] {
			t.Errorf("%s changed: %s vs %s", key, tr2[key], tr[key])
		}
	}
	if sum2.CacheInvalidating {
		t.Error("the same drops must not invalidate the cache again")
	}
}

// Enforce mode: a block kept at the configured threshold is re-decided
// from its stored score, without asking the economy model again.
func TestEmergencyPruneRescoresKeeps(t *testing.T) {
	h := newHarness(t, Settings{Mode: ModeEnforce, KeepThreshold: ptr(0.25), KeepLastNTurns: ptr(1), MinBlockTokens: ptr(50),
		FloorTokens: ptr(0), EpochTokens: ptr(0)})
	c := newConv("Fix the failing login test").
		call("toolu_mid", "Bash", map[string]any{"command": "cat notes"}, big("MEDIUM notes"), false).
		call("toolu_noise", "Bash", map[string]any{"command": "npm i"}, big("NOISE npm"), false).
		call("toolu_end", "Bash", map[string]any{"command": "true"}, "ok", false)
	body := c.body(t)
	_, res, _, d := h.run(body)
	if b := blockBy(d, "toolu_mid"); b.Decision != "keep" {
		t.Fatalf("medium block should be kept at 0.25: %+v", b)
	}
	if res.Body == nil || !strings.Contains(toolResults(t, res.Body)["toolu_noise"], "[rlcd:") {
		t.Fatal("noise should be dropped by the normal pass")
	}
	h.sel.reset()
	_, em := h.emergency(body, 0.5)
	asked, _ := h.sel.reset()
	if em.Body == nil || em.NewDrops != 1 {
		t.Fatalf("emergency: %+v", em)
	}
	if !strings.Contains(toolResults(t, em.Body)["toolu_mid"], "[rlcd:") {
		t.Fatal("medium block not dropped by the emergency pass")
	}
	for _, k := range asked {
		if k == "toolu_mid" {
			t.Fatal("the stored score should have been reused")
		}
	}
	// Nothing more to drop: a second pass says so.
	_, em2 := h.emergency(body, 0.5)
	if em2.Body != nil || !strings.Contains(em2.Skipped, "nothing more") {
		t.Fatalf("second pass: %+v", em2)
	}
}

func TestEmergencyPruneSkips(t *testing.T) {
	h := newHarness(t, Settings{Enabled: ptr(false)})
	if _, em := h.emergency(baseConv().body(t), 0.5); em.Body != nil || em.Skipped != "pruning is disabled" {
		t.Fatalf("disabled: %+v", em)
	}
	h = newHarness(t, testSettings(ModeEnforce))
	c := h.cfg.Get()
	c.LogBodies = false
	h.cfg = config.NewStore(&c)
	h.p.cfg = h.cfg
	if _, em := h.emergency(baseConv().body(t), 0.5); em.Body != nil || !strings.Contains(em.Skipped, "not logged") {
		t.Fatalf("no bodies: %+v", em)
	}
}
