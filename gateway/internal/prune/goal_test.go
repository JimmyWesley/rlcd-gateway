package prune

import (
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// A chatbot's answered questions are history: with goal_turns 1 only the
// current question is the goal, so the earlier topic's documents can go.
func TestGoalTurns(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
	  {"role":"user","content":"How long does shipping take?"},
	  {"role":"assistant","content":"2-6 days."},
	  {"role":"user","content":"Can I refund opened software?"}]}`)
	d, err := parseOA(ir.ProtocolOpenAIChat, body)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := d.goalAndRecent(1); g != "Can I refund opened software?" {
		t.Errorf("goal_turns 1: %q", g)
	}
	if g, _ := d.goalAndRecent(2); !strings.Contains(g, "shipping") || !strings.HasSuffix(g, "Can I refund opened software?") {
		t.Errorf("goal_turns 2 must keep the earlier request first: %q", g)
	}
	if g, _ := d.goalAndRecent(0); !strings.Contains(g, "shipping") {
		t.Errorf("zero falls back to the default of 2: %q", g)
	}
	one := 1
	if e := (Settings{GoalTurns: &one}).resolve(); e.GoalTurns != 1 {
		t.Errorf("override not applied: %d", e.GoalTurns)
	}
	if e := (Settings{}).resolve(); e.GoalTurns != defaultGoalTurns {
		t.Errorf("default: %d", e.GoalTurns)
	}
	six := 6
	if err := (Settings{GoalTurns: &six}).validate(); err == nil {
		t.Error("goal_turns 6 must be rejected")
	}
}

func TestProfiles(t *testing.T) {
	base := (Settings{}).resolve()
	if e := (Settings{}).forRequest(base, "agent", ir.ProtocolOpenAIChat); e.Profile != ProfileAgent || e.Criteria != DefaultCriteria || e.GoalTurns != 2 {
		t.Errorf("an agent keeps the coding defaults: %+v", e.Profile)
	}
	if e := (Settings{}).forRequest(base, "unknown", ir.ProtocolAnthropic); e.Profile != ProfileAgent {
		t.Errorf("Anthropic calls stay on the agent profile: %s", e.Profile)
	}
	e := (Settings{}).forRequest(base, "sdk", ir.ProtocolOpenAIChat)
	if e.Profile != ProfileChat || e.Criteria != ChatCriteria || e.GoalTurns != 1 {
		t.Errorf("an SDK chat app gets the chat defaults: %s %d", e.Profile, e.GoalTurns)
	}
	two := 2
	s := Settings{Criteria: "mine", GoalTurns: &two}
	if e := s.forRequest(s.resolve(), "sdk", ir.ProtocolOpenAIChat); e.Criteria != "mine" || e.GoalTurns != 2 {
		t.Errorf("explicit criteria and goal_turns win over the profile: %q %d", e.Criteria, e.GoalTurns)
	}
	s = Settings{Profile: ProfileChat}
	if e := s.forRequest(s.resolve(), "agent", ir.ProtocolAnthropic); e.Profile != ProfileChat {
		t.Errorf("a forced profile wins over detection: %s", e.Profile)
	}
	if err := (Settings{Profile: "robot"}).validate(); err == nil {
		t.Error("unknown profile must be rejected")
	}
}
