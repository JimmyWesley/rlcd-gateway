package prune

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

type configView struct {
	Settings        Settings         `json:"settings"`
	Effective       Effective        `json:"effective"`
	Presets         []Preset         `json:"presets"`
	DefaultCriteria string           `json:"default_criteria"`
	ChatCriteria    string           `json:"chat_criteria"`
	DefaultPrices   map[string]Price `json:"default_prices"`
	PricesAsOf      string           `json:"prices_as_of"`
	// LogBodies must be on to enforce: recall reads the logged bodies.
	LogBodies bool `json:"log_bodies"`
}

func (p *Pruner) view(c config.Config) (configView, error) {
	s, err := parseSettings(c.Section("prune"))
	if err != nil {
		return configView{}, err
	}
	return configView{Settings: s, Effective: s.resolve(), Presets: presets, DefaultCriteria: DefaultCriteria, ChatCriteria: ChatCriteria,
		DefaultPrices: defaultPrices, PricesAsOf: PricesAsOf, LogBodies: c.LogBodies}, nil
}

func (p *Pruner) getConfig(w http.ResponseWriter, r *http.Request) {
	v, err := p.view(p.cfg.Get())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stored prune settings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putConfig replaces the whole section. Send null (or omit) a field to fall
// back to the preset's value.
func (p *Pruner) putConfig(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if !readJSON(w, r, &s) {
		return
	}
	if err := s.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(s)
	if err := p.cfg.SetSection("prune", raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.getConfig(w, r)
}

func (p *Pruner) getPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, presets)
}

func (p *Pruner) listFeedback(w http.ResponseWriter, r *http.Request) {
	fs, err := p.allFeedback()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fs)
}

func (p *Pruner) addFeedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID string `json:"request_id"`
		Key       string `json:"key"`
		Verdict   string `json:"verdict"`
		Note      string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Verdict != VerdictShouldKeep && in.Verdict != VerdictShouldDrop {
		writeError(w, http.StatusBadRequest, `verdict must be "should_keep" or "should_drop"`)
		return
	}
	d, err := p.st.Get(in.RequestID)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "no such request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var det Detail
	if raw := d.StageDetails["prune"]; raw == nil || json.Unmarshal(raw, &det) != nil {
		writeError(w, http.StatusNotFound, "request has no pruning report")
		return
	}
	var blk *BlockReport
	for i := range det.Blocks {
		if b := &det.Blocks[i]; b.Key == in.Key || b.IRKey == in.Key || b.ID == in.Key {
			blk = b
			break
		}
	}
	if blk == nil {
		writeError(w, http.StatusNotFound, "no block "+in.Key+" in that request")
		return
	}
	f, err := p.feedback.add(Feedback{RequestID: in.RequestID, ConversationID: d.ConversationID, Key: blk.Key,
		Verdict: in.Verdict, Note: in.Note, Kind: blk.Kind, Role: blk.Role, Name: blk.Name, What: blk.What,
		Tokens: blk.Tokens, IsError: blk.IsError, Preview: blk.Preview, Goal: det.Goal, Recent: det.Recent,
		Decision: blk.Decision, Reason: blk.Reason, Score: blk.Score})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (p *Pruner) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.PathValue("id"), "recall-") {
		writeError(w, http.StatusBadRequest, "this case comes from the recall log (recall/events.jsonl) and cannot be deleted here")
		return
	}
	ok, err := p.feedback.delete(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no such feedback")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (p *Pruner) replay(w http.ResponseWriter, r *http.Request) {
	c := p.cfg.Get()
	s, err := parseSettings(c.Section("prune"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fs, err := p.allFeedback()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, replayCases(r.Context(), s.resolve(), p.asker(c.Selector), fs))
}

type modeTotals struct {
	Requests    int     `json:"requests"`
	Pruned      int     `json:"pruned"` // requests with at least one drop
	SavedTokens int     `json:"saved_tokens"`
	SavedUSD    float64 `json:"saved_usd"`
	// Invalidations counts turns whose drops changed the cached prefix.
	Invalidations int `json:"cache_invalidations"`
}

type statsView struct {
	Requests       int        `json:"requests"`
	Epochs         int        `json:"epochs"`
	SelectorErrors int        `json:"selector_errors"`
	AvgSelectorMs  int64      `json:"avg_selector_ms"`
	Enforce        modeTotals `json:"enforce"`
	Shadow         modeTotals `json:"shadow"`
	Feedback       int        `json:"feedback"`
	RecallFeedback int        `json:"recall_feedback"`
	// Window is how many recent requests the totals cover (the in-memory log).
	Window int `json:"window"`
}

// stats sums the per-request summaries of the recent request log. Savings
// are estimates and include the extra cache writes of epoch turns, so they
// can be negative early in a conversation.
func (p *Pruner) stats(w http.ResponseWriter, r *http.Request) {
	var v statsView
	var selMs int64
	recs := p.st.Recent()
	v.Window = len(recs)
	for _, rec := range recs {
		raw := rec.Stages["prune"]
		if raw == nil {
			continue
		}
		var s Summary
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		v.Requests++
		if s.EpochRan {
			v.Epochs++
			selMs += s.SelectorMs
		}
		if s.SelectorError != "" {
			v.SelectorErrors++
		}
		t := &v.Shadow
		if s.Applied || (s.Mode == ModeEnforce && s.Warning == "") {
			t = &v.Enforce
		}
		t.Requests++
		if s.Dropped > 0 {
			t.Pruned++
		}
		if s.CacheInvalidating {
			t.Invalidations++
		}
		t.SavedTokens += s.SavedTokens
		t.SavedUSD += s.EstCostBefore - s.EstCostAfter
	}
	if v.Epochs > 0 {
		v.AvgSelectorMs = selMs / int64(v.Epochs)
	}
	v.Enforce.SavedUSD, v.Shadow.SavedUSD = round6(v.Enforce.SavedUSD), round6(v.Shadow.SavedUSD)
	if fs, err := p.allFeedback(); err == nil {
		v.Feedback = len(fs)
		for _, f := range fs {
			if f.Source == SourceRecall {
				v.RecallFeedback++
			}
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
