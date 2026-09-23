package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// MirrorResult is one mirrored call, side by side with the call the client
// got its answer from. It is kept in <home>/decisions/mirror.jsonl.
type MirrorResult struct {
	RequestID string    `json:"request_id"`
	Time      time.Time `json:"time"`
	Backend   string    `json:"backend"`
	Model     string    `json:"model"`
	Status    int       `json:"status"`
	Error     string    `json:"error,omitempty"`
	// DurationMs, ForwardMs and TotalMs are the mirror's; the Primary*
	// fields the client's call, copied for a side-by-side view.
	DurationMs        int64        `json:"duration_ms"`
	ForwardMs         *float64     `json:"forward_ms,omitempty"`
	TotalMs           *float64     `json:"total_ms,omitempty"`
	PrimaryBackend    string       `json:"primary_backend"`
	PrimaryModel      string       `json:"primary_model"`
	PrimaryDurationMs int64        `json:"primary_duration_ms"`
	PrimaryForwardMs  *float64     `json:"primary_forward_ms,omitempty"`
	Usage             *store.Usage `json:"usage,omitempty"`
	CostUSD           float64      `json:"est_cost_usd,omitempty"`
	// Questions compares each answer. Compared counts the questions both
	// sides answered, Agreed those whose answers match.
	Questions []MirrorQuestion `json:"questions"`
	Compared  int              `json:"compared"`
	Agreed    int              `json:"agreed"`
}

// MirrorQuestion is one question answered by both backends. Answers agree
// when their answer strings match: the same choice, the same side of 0.5
// for noul, the same rounded score.
type MirrorQuestion struct {
	ID                string   `json:"id"`
	Answer            string   `json:"answer"`
	Confidence        *float64 `json:"confidence,omitempty"`
	PrimaryAnswer     string   `json:"primary_answer"`
	PrimaryConfidence *float64 `json:"primary_confidence,omitempty"`
	Agree             bool     `json:"agree"`
}

// maybeMirror starts a mirror call in the background when the settings
// ask for one and the sample says so. It returns at once: the client's
// response is already written and the handler returns right after, so the
// mirror never adds latency. With every slot busy the copy is skipped.
func (d *Decisions) maybeMirror(cfg config.Config, s Settings, primary string, raw []byte, h http.Header, rec *store.Detail) {
	mr := s.Mirror
	if mr == nil || mr.Backend == "" || mr.SampleRate <= 0 || mr.Backend == primary || !d.sample(mr.SampleRate) {
		return
	}
	be, ok := s.backend(cfg, mr.Backend)
	if !ok || be.BaseURL == "" {
		return
	}
	select {
	case d.mirrorSem <- struct{}{}:
	default:
		d.mirrorSkipped.Add(1)
		return
	}
	body := raw
	if mr.Model != "" {
		body = withModel(raw, mr.Model)
	}
	hdr := http.Header{"Content-Type": {"application/json"}, "User-Agent": {"rlcd-gateway-mirror"}}
	if be.Auth == AuthPassthrough {
		for _, k := range []string{"Authorization", "X-Api-Key", "Api-Key"} {
			if v := h.Get(k); v != "" {
				hdr.Set(k, v)
			}
		}
	} else {
		setToken(hdr, be.ResolvedToken())
	}
	primarySum := *rec.Decisions
	res := MirrorResult{RequestID: rec.ID, Backend: mr.Backend, PrimaryBackend: primary, PrimaryModel: rec.Model,
		PrimaryDurationMs: rec.DurationMs, PrimaryForwardMs: primarySum.ForwardMs, Questions: []MirrorQuestion{}}
	head, _ := parseRequest(body)
	res.Model = head.Model
	go func() {
		defer func() { <-d.mirrorSem }()
		defer func() {
			if p := recover(); p != nil {
				log.Printf("decisions: mirror: %v", p)
			}
		}()
		d.runMirror(cfg, be, body, hdr, head, &primarySum, &res)
		if err := d.mirrors.Append(res); err != nil {
			d.mirrorSaveFail.Add(1)
		}
	}()
}

func (d *Decisions) runMirror(cfg config.Config, be Backend, body []byte, hdr http.Header, head requestHead,
	primary *store.Decisions, res *MirrorResult) {
	ctx, cancel := context.WithTimeout(context.Background(), d.MirrorTimeout)
	defer cancel()
	start := time.Now()
	res.Time = start
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(be.BaseURL, "/")+Endpoint, bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return
	}
	req.Header = hdr
	resp, err := d.Client.Do(req)
	if err != nil {
		res.DurationMs, res.Error = time.Since(start).Milliseconds(), err.Error()
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, relay.MaxCapturedResponse))
	res.DurationMs, res.Status = time.Since(start).Milliseconds(), resp.StatusCode
	if err != nil {
		res.Error = err.Error()
		return
	}
	if resp.StatusCode >= 300 {
		res.Error = relay.ErrorMessage(b)
		return
	}
	out, ok := parseResponse(b)
	if !ok {
		res.Error = "the mirror's response is not JSON"
		return
	}
	if out.Model != "" {
		res.Model = out.Model
	}
	if res.Usage = out.usage(); res.Usage != nil {
		res.CostUSD = pricing.Cost(pricing.Table(cfg), res.Model, "systemone", res.Usage)
	}
	sum := summarize(head, out, resp.Header)
	res.ForwardMs, res.TotalMs = sum.ForwardMs, sum.TotalMs
	res.Questions = compare(primary, sum)
	for _, q := range res.Questions {
		res.Compared++
		if q.Agree {
			res.Agreed++
		}
	}
}

// compare lines up the answers both sides gave.
func compare(primary, mirror *store.Decisions) []MirrorQuestion {
	theirs := map[string]store.DecisionQuestion{}
	for _, q := range mirror.Questions {
		theirs[q.ID] = q
	}
	out := []MirrorQuestion{}
	for _, p := range primary.Questions {
		m, ok := theirs[p.ID]
		if !ok || p.Answer == "" || m.Answer == "" {
			continue
		}
		out = append(out, MirrorQuestion{ID: p.ID, Answer: m.Answer, Confidence: m.Confidence,
			PrimaryAnswer: p.Answer, PrimaryConfidence: p.Confidence, Agree: p.Answer == m.Answer})
	}
	return out
}

// withModel returns body with its "model" replaced, keeping every other
// member as it was.
func withModel(body []byte, model string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	b, _ := json.Marshal(model)
	m["model"] = b
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
