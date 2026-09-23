package decisions

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// requestHead is what the gateway reads of a decision request.
type requestHead struct {
	Model     string          `json:"model"`
	State     json.RawMessage `json:"state"`
	Questions json.RawMessage `json:"questions"`
	Think     bool            `json:"think"`
}

func parseRequest(body []byte) (requestHead, error) {
	var h requestHead
	err := json.Unmarshal(body, &h)
	return h, err
}

// answer is one answer of a System One response.
type answer struct {
	Type          string             `json:"type"`
	Choice        *string            `json:"choice"`
	Score         *float64           `json:"score"`
	Noul          *float64           `json:"noul"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Legend        map[string]string  `json:"legend"`
}

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func parseResponse(b []byte) (response, bool) {
	var r response
	if json.Unmarshal(b, &r) != nil {
		return response{}, false
	}
	return r, true
}

func (r response) usage() *store.Usage {
	if r.Usage == nil {
		return nil
	}
	return &store.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
}

func f64(v float64) *float64 { return &v }

// summarize builds the record's decisions summary from the request, the
// response and its headers. It never fails: what cannot be read is left
// empty.
func summarize(req requestHead, resp response, h http.Header) *store.Decisions {
	d := &store.Decisions{Think: req.Think, StateBytes: len(req.State), Questions: []store.DecisionQuestion{}}
	if h != nil {
		d.ForwardMs, d.TotalMs = headerMs(h, "X-Rlcd-Forward-Ms"), headerMs(h, "X-Rlcd-Total-Ms")
	}
	seen := map[string]bool{}
	qs, _ := ir.OrderedMembers(req.Questions)
	for _, q := range qs {
		var head struct {
			Type     string          `json:"type"`
			Criteria json.RawMessage `json:"criteria"`
		}
		_ = json.Unmarshal(q.Value, &head)
		dq := store.DecisionQuestion{ID: q.Key, Type: head.Type, Labels: ir.CriteriaLabels(head.Criteria)}
		if a, ok := resp.Answers[q.Key]; ok {
			fill(&dq, a)
		}
		seen[q.Key] = true
		d.Questions = append(d.Questions, dq)
	}
	// Answers to questions the request did not list (it did not parse).
	extra := make([]string, 0)
	for id := range resp.Answers {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		dq := store.DecisionQuestion{ID: id}
		fill(&dq, resp.Answers[id])
		d.Questions = append(d.Questions, dq)
	}
	return d
}

func headerMs(h http.Header, name string) *float64 {
	if v, err := strconv.ParseFloat(h.Get(name), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		return &v
	}
	return nil
}

// fill copies an answer into the summary, deriving the answer string, the
// confidence and the top probability.
func fill(dq *store.DecisionQuestion, a answer) {
	if a.Type != "" {
		dq.Type = a.Type
	}
	dq.Confidence = a.Confidence
	var top *float64
	for _, p := range a.Probabilities {
		if top == nil || p > *top {
			top = f64(p)
		}
	}
	dq.TopProb = top
	switch {
	case dq.Type == "noul" || (a.Noul != nil && a.Choice == nil && a.Score == nil):
		if a.Noul == nil {
			return
		}
		n := *a.Noul
		dq.Noul = f64(n)
		dq.Answer = "no"
		if n >= 0.5 {
			dq.Answer = "yes"
		}
		c := math.Max(n, 1-n)
		if dq.Confidence == nil {
			dq.Confidence = f64(c)
		}
		if dq.TopProb == nil {
			dq.TopProb = f64(c)
		}
	case a.Choice != nil:
		dq.Choice, dq.Answer = *a.Choice, *a.Choice
	case a.Score != nil:
		dq.Score = f64(*a.Score)
		i := int(math.Round(*a.Score))
		switch {
		case a.Legend[strconv.Itoa(i)] != "":
			dq.Answer = a.Legend[strconv.Itoa(i)]
		case i >= 0 && i < len(dq.Labels):
			dq.Answer = dq.Labels[i]
		default:
			dq.Answer = strconv.Itoa(i)
		}
		if len(dq.Labels) == 0 && len(a.Legend) > 0 {
			for j := 0; j < len(a.Legend); j++ {
				dq.Labels = append(dq.Labels, a.Legend[strconv.Itoa(j)])
			}
		}
	}
}
