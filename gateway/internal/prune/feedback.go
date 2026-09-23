package prune

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
)

// Verdicts a person can give on one decision.
const (
	VerdictShouldKeep = "should_keep"
	VerdictShouldDrop = "should_drop"
)

// Feedback is one human judgement on one block. It snapshots what the
// selector saw (goal, activity, description, preview) so a replay does not
// depend on request logs that may have rotated away.
type Feedback struct {
	ID             string    `json:"id"`
	Time           time.Time `json:"time"`
	RequestID      string    `json:"request_id"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Key            string    `json:"key"`
	Verdict        string    `json:"verdict"`
	Note           string    `json:"note,omitempty"`

	Kind    string `json:"kind"`
	Role    string `json:"role,omitempty"`
	Name    string `json:"name,omitempty"`
	What    string `json:"what,omitempty"`
	Tokens  int    `json:"tokens"`
	IsError bool   `json:"is_error,omitempty"`
	Preview string `json:"preview"`
	Goal    string `json:"goal"`
	Recent  string `json:"recent_activity"`

	// What the pruner decided when the feedback was given.
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
	Score    *float64 `json:"score,omitempty"`
}

func (f Feedback) wantDecision() string {
	if f.Verdict == VerdictShouldDrop {
		return "drop"
	}
	return "keep"
}

type feedbackStore struct {
	path string
	mu   sync.Mutex
}

func newFeedbackStore(dir string) *feedbackStore {
	return &feedbackStore{path: filepath.Join(dir, "feedback.json")}
}

func (s *feedbackStore) list() ([]Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *feedbackStore) loadLocked() ([]Feedback, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Feedback{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Feedback
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Feedback{}
	}
	return out, nil
}

func (s *feedbackStore) saveLocked(fs []Feedback) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// add stores f, replacing an earlier verdict on the same block of the same request.
func (s *feedbackStore) add(f Feedback) (Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, err := s.loadLocked()
	if err != nil {
		return f, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	f.ID, f.Time = hex.EncodeToString(b), time.Now()
	kept := fs[:0]
	for _, x := range fs {
		if !(x.RequestID == f.RequestID && x.Key == f.Key) {
			kept = append(kept, x)
		}
	}
	return f, s.saveLocked(append(kept, f))
}

func (s *feedbackStore) delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	kept := fs[:0]
	for _, x := range fs {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	if len(kept) == len(fs) {
		return false, nil
	}
	return true, s.saveLocked(kept)
}

// ReplayCase is one feedback case judged again under the current settings.
type ReplayCase struct {
	ID        string   `json:"id"`
	RequestID string   `json:"request_id"`
	Key       string   `json:"key"`
	Verdict   string   `json:"verdict"`
	Decision  string   `json:"decision"`
	Reason    string   `json:"reason"`
	Score     *float64 `json:"score,omitempty"`
	Agrees    bool     `json:"agrees"`
	Error     string   `json:"error,omitempty"`
	// Then is the decision at the time of the feedback, for "fixed" / "broke".
	Then       string `json:"then"`
	ThenAgreed bool   `json:"then_agreed"`
}

type ReplayReport struct {
	Cases      []ReplayCase `json:"cases"`
	Agree      int          `json:"agree"`
	Disagree   int          `json:"disagree"`
	Errors     int          `json:"errors"`
	Fixed      int          `json:"fixed"`  // disagreed then, agree now
	Broken     int          `json:"broken"` // agreed then, disagree now
	SelectorMs int64        `json:"selector_ms"`
}

// replayCases is the regression suite for criteria and threshold changes:
// the deterministic rules are applied from the snapshot, and everything
// the selector decided is asked again, one batched call per original request.
func replayCases(ctx context.Context, eff Effective, ask askFunc, cases []Feedback) ReplayReport {
	rep := ReplayReport{Cases: make([]ReplayCase, len(cases))}
	type group struct {
		state map[string]any
		qs    map[string]selector.Question
		idx   map[string]int
	}
	groups := map[string]*group{}
	var order []string
	for i, f := range cases {
		c := ReplayCase{ID: f.ID, RequestID: f.RequestID, Key: f.Key, Verdict: f.Verdict,
			Then: f.Decision, ThenAgreed: f.Decision == f.wantDecision()}
		c.Decision = "keep"
		switch {
		case f.Reason == ReasonProtected:
			c.Reason = ReasonProtected
		case eff.KeepErrors && f.IsError:
			c.Reason = "keep_errors"
		case eff.KeepEdits && f.Kind == ir.KindToolResult && isEditTool(eff, f.Name):
			c.Reason = "keep_edits"
		case eff.AlwaysKeepUserText && f.Kind == ir.KindText && f.Role == "user":
			c.Reason = "always_keep_user_text"
		case f.Tokens < eff.MinBlockTokens:
			c.Reason = "min_block_tokens"
		case f.Reason == ReasonSuperseded && eff.DropSupersededReads:
			c.Decision, c.Reason = "drop", ReasonSuperseded
		default:
			c.Reason = ReasonSelector
			g := groups[f.RequestID]
			if g == nil {
				g = &group{state: map[string]any{"goal": f.Goal, "recent_activity": f.Recent},
					qs: map[string]selector.Question{}, idx: map[string]int{}}
				groups[f.RequestID] = g
				order = append(order, f.RequestID)
			}
			g.qs[f.Key] = selector.Question{Type: "noul",
				Instructions: eff.Criteria + "\n\n" + question(f.What, f.Tokens, f.Preview)}
			g.idx[f.Key] = i
		}
		rep.Cases[i] = c
	}

	start := time.Now()
	for _, id := range order {
		g := groups[id]
		cctx, cancel := context.WithTimeout(ctx, time.Duration(eff.SelectorTimeoutMs)*time.Millisecond*2)
		scores, err := ask(cctx, g.state, g.qs)
		cancel()
		for key, i := range g.idx {
			c := &rep.Cases[i]
			if err != nil {
				c.Error = err.Error()
				continue
			}
			s, ok := scores[key]
			if !ok {
				c.Error = "selector gave no answer"
				continue
			}
			c.Score = &s
			if s < eff.KeepThreshold {
				c.Decision = "drop"
			}
		}
	}
	rep.SelectorMs = time.Since(start).Milliseconds()

	for i := range rep.Cases {
		c := &rep.Cases[i]
		if c.Error != "" {
			rep.Errors++
			continue
		}
		c.Agrees = c.Decision == cases[i].wantDecision()
		if c.Agrees {
			rep.Agree++
			if !c.ThenAgreed {
				rep.Fixed++
			}
		} else {
			rep.Disagree++
			if c.ThenAgreed {
				rep.Broken++
			}
		}
	}
	return rep
}
