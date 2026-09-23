package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/router"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const (
	s1Req  = `{"model":"Open-RLCD-text","state":"hi","questions":{"q":{"type":"noul","instructions":"?"}}}`
	s1Resp = `{"model":"Open-RLCD-text","answers":{"q":{"type":"noul","noul":0.8}},"usage":{"input_tokens":20,"output_tokens":1}}`
	// The router's auto rule asks a choice question named "route".
	autoResp = `{"model":"Open-RLCD-text","answers":{"route":{"type":"choice","choice":"cheap","confidence":0.9,` +
		`"probabilities":{"cheap":0.9,"claude-sub":0.1}}},"usage":{"input_tokens":60,"output_tokens":1}}`
)

// /v1/systemone is served by the decision proxy (not passed through to the
// Anthropic login), and the router's own economy-model call is logged as a
// decision of client rlcd-gateway, pointing at the turn it routed.
func TestDecisionsThroughGateway(t *testing.T) {
	e := newEnv(t, false)
	sel := newUpstream(t, s1Resp, false)
	must(t, e.cs.SetSelector(config.Selector{Backend: config.SelectorOpenRLCDLocal, BaseURL: sel.srv.URL, Model: "Open-RLCD-text"}))

	resp, out := e.do("POST", "/v1/systemone", s1Req, map[string]string{"User-Agent": "curl/8.7.1"})
	if resp.StatusCode != 200 || out != s1Resp || sel.count() != 1 || e.anth.count() != 0 {
		t.Fatalf("decision call: %d %s (selector %d, anthropic %d)", resp.StatusCode, out, sel.count(), e.anth.count())
	}
	waitRecords(t, e.st, 1)
	if d := e.lastRecord(); d.Protocol != ir.ProtocolSystemOne || d.Client.ID != "curl" || d.Decisions == nil ||
		d.Decisions.Questions[0].Answer != "yes" {
		t.Fatalf("record: %+v", d.Record)
	}

	// An auto rule: the router asks the selector which route serves the turn.
	sel.mu.Lock()
	sel.resp = autoResp
	sel.mu.Unlock()
	must(t, e.cs.UpsertRoute("cheap", config.Route{Kind: config.KindAnthropic, BaseURL: e.anth.srv.URL, Auth: config.AuthPassthrough}))
	rs, _ := json.Marshal(router.Settings{Sticky: true, TTLHours: 1,
		Routes: map[string]router.RouteMeta{"cheap": {Description: "fast cheap model"}, "claude-sub": {Description: "strongest model"}},
		Rules:  []router.Rule{{Name: "auto", Enabled: true, Kind: router.KindAuto, TimeoutMs: 3000}}})
	must(t, e.cs.SetSection("router", rs))
	hdr := map[string]string{"Authorization": "Bearer sk-ant-oat01-subscription", "Anthropic-Version": "2023-06-01",
		"User-Agent": "claude-cli/2.1.280 (external, cli)"}
	if resp, _ := e.do("POST", "/v1/messages", anthReq, hdr); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	waitRecords(t, e.st, 3)
	var turn, internal *store.Record
	for _, r := range e.st.Recent() {
		r := r
		switch {
		case r.Protocol == ir.ProtocolAnthropic && turn == nil:
			turn = &r
		case r.Decisions != nil && r.Decisions.Source == "router":
			internal = &r
		}
	}
	if turn == nil || turn.Route != "cheap" || !strings.Contains(turn.RouteReason, "auto rule") {
		t.Fatalf("turn: %+v", turn)
	}
	if internal == nil || internal.Client == nil || internal.Client.ID != "rlcd-gateway" || internal.Decisions.ParentID != turn.ID ||
		internal.Decisions.Questions[0].Answer != "cheap" || internal.Route != "economy" {
		t.Fatalf("internal decision: %+v", internal)
	}
	// The counter moves right after the record is stored.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, out = e.do("GET", "/api/decisions/stats?source=router", "", nil)
		if resp.StatusCode == 200 && strings.Contains(out, `"logged":1`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats: %d %s", resp.StatusCode, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRecords(t *testing.T, st *store.Store, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(st.Recent()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d records, have %d", n, len(st.Recent()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
