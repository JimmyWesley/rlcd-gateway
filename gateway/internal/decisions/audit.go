package decisions

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Outcome is the ground truth for one question of one call, recorded later
// (POST /api/decisions/{id}/outcome). A null outcome deletes it.
type Outcome struct {
	RequestID string          `json:"request_id"`
	Question  string          `json:"question"`
	Outcome   json.RawMessage `json:"outcome"`
	Time      time.Time       `json:"time"`
}

func (o Outcome) gone() bool { return len(o.Outcome) == 0 || string(o.Outcome) == "null" }

// Filter selects decision calls. Every field is optional.
type Filter struct {
	From, To time.Time
	Question string
	Model    string
	Backend  string
	Client   string
	Key      string
	Answer   string
	Source   string
	Status   string // ok | error
	// Below keeps calls with a question (the Question one, when set) whose
	// confidence is below it: "show me unsure decisions".
	Below *float64
	// Outcome is with | without: calls with or without a recorded outcome.
	Outcome string
}

// parseFilter reads a Filter from the query string.
func parseFilter(q url.Values, now time.Time) (Filter, error) {
	f := Filter{Question: q.Get("question"), Model: q.Get("model"), Backend: q.Get("backend"), Client: q.Get("client"),
		Key: q.Get("key"), Answer: q.Get("answer"), Source: q.Get("source"), Status: q.Get("status"), Outcome: q.Get("outcome")}
	var err error
	if f.From, err = parseTime(q.Get("from")); err != nil {
		return f, fmt.Errorf("from: %w", err)
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		return f, fmt.Errorf("to: %w", err)
	}
	if v := q.Get("since"); v != "" {
		d, err := parseDuration(v)
		if err != nil || d <= 0 {
			return f, fmt.Errorf("since: %q is not a duration like 24h or 7d", v)
		}
		f.From = now.Add(-d)
	}
	for _, k := range []string{"confidence_below", "below"} {
		if v := q.Get(k); v != "" {
			t, err := strconv.ParseFloat(v, 64)
			if err != nil || t < 0 || t > 1 {
				return f, fmt.Errorf("%s must be a number between 0 and 1", k)
			}
			f.Below = &t
		}
	}
	if f.Status != "" && f.Status != "ok" && f.Status != "error" {
		return f, errors.New("status must be ok or error")
	}
	if f.Outcome != "" && f.Outcome != "with" && f.Outcome != "without" {
		return f, errors.New("outcome must be with or without")
	}
	return f, nil
}

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not RFC 3339, a date or unix seconds", v)
}

func parseDuration(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		f, err := strconv.ParseFloat(n, 64)
		return time.Duration(f * float64(24*time.Hour)), err
	}
	return time.ParseDuration(v)
}

func isError(r store.Record) bool { return r.Status >= 400 || r.Error != "" }

// matchRecord applies the record-level filters.
func (f Filter) matchRecord(r store.Record) bool {
	if r.Protocol != ir.ProtocolSystemOne || r.Decisions == nil {
		return false
	}
	d := r.Decisions
	switch {
	case !f.From.IsZero() && r.Time.Before(f.From), !f.To.IsZero() && !r.Time.Before(f.To):
		return false
	case f.Model != "" && !strings.EqualFold(f.Model, r.Model) && !strings.EqualFold(f.Model, r.ClientModel):
		return false
	case f.Backend != "" && f.Backend != d.Backend:
		return false
	case f.Source != "" && f.Source != d.Source:
		return false
	case f.Key != "" && f.Key != r.KeyID && !strings.EqualFold(f.Key, r.KeyName):
		return false
	case f.Status == "ok" && isError(r), f.Status == "error" && !isError(r):
		return false
	case f.Client != "" && (r.Client == nil || (!strings.EqualFold(f.Client, r.Client.ID) && !strings.EqualFold(f.Client, r.Client.Name))):
		return false
	}
	return true
}

// questionFilters reports whether any filter works per question.
func (f Filter) questionFilters() bool { return f.Question != "" || f.Answer != "" || f.Below != nil }

// matchQuestion applies the question-level filters.
func (f Filter) matchQuestion(q store.DecisionQuestion) bool {
	switch {
	case f.Question != "" && q.ID != f.Question:
		return false
	case f.Answer != "" && !strings.EqualFold(f.Answer, q.Answer):
		return false
	case f.Below != nil && (q.Confidence == nil || *q.Confidence >= *f.Below):
		return false
	}
	return true
}

// ItemQuestion is a question of a listed call, with its outcome.
type ItemQuestion struct {
	store.DecisionQuestion
	Outcome     json.RawMessage `json:"outcome,omitempty"`
	OutcomeTime *time.Time      `json:"outcome_time,omitempty"`
	// Correct compares the answer with the outcome (score: within 0.5).
	Correct *bool `json:"correct,omitempty"`
	// Matched is true for the questions the filters selected.
	Matched bool `json:"matched"`
}

// Item is one decision call in the audit list and the JSONL export.
type Item struct {
	RequestID      string         `json:"request_id"`
	Time           time.Time      `json:"time"`
	Source         string         `json:"source"`
	ParentID       string         `json:"parent_id,omitempty"`
	Backend        string         `json:"backend"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	ClientModel    string         `json:"client_model,omitempty"`
	Client         *store.Client  `json:"client,omitempty"`
	KeyID          string         `json:"key_id,omitempty"`
	KeyName        string         `json:"key_name,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	Status         int            `json:"status"`
	Error          string         `json:"error,omitempty"`
	DurationMs     int64          `json:"duration_ms"`
	TTFBMs         int64          `json:"ttfb_ms"`
	ForwardMs      *float64       `json:"forward_ms,omitempty"`
	TotalMs        *float64       `json:"total_ms,omitempty"`
	Think          bool           `json:"think,omitempty"`
	StateBytes     int            `json:"state_bytes"`
	Usage          *store.Usage   `json:"usage,omitempty"`
	CostUSD        float64        `json:"est_cost_usd"`
	Questions      []ItemQuestion `json:"questions"`
	Mirror         *MirrorResult  `json:"mirror,omitempty"`
}

// audit is one pass over the log: the outcomes and mirror results joined
// to the records the filter selects.
type audit struct {
	d        *Decisions
	f        Filter
	outcomes map[string]map[string]Outcome // request id → question → outcome
}

func (d *Decisions) newAudit(f Filter) *audit {
	a := &audit{d: d, f: f, outcomes: map[string]map[string]Outcome{}}
	d.outcomes.Each(func(o Outcome) {
		if a.outcomes[o.RequestID] == nil {
			a.outcomes[o.RequestID] = map[string]Outcome{}
		}
		a.outcomes[o.RequestID][o.Question] = o
	})
	return a
}

// item joins a record to its outcomes and mirror result, or returns false
// when the filters reject it.
func (a *audit) item(r store.Record) (Item, bool) {
	if !a.f.matchRecord(r) {
		return Item{}, false
	}
	dd := r.Decisions
	it := Item{RequestID: r.ID, Time: r.Time, Source: dd.Source, ParentID: dd.ParentID, Backend: dd.Backend,
		Provider: r.Provider, Model: r.Model, ClientModel: r.ClientModel, Client: r.Client, KeyID: r.KeyID,
		KeyName: r.KeyName, ConversationID: r.ConversationID, Status: r.Status, Error: r.Error,
		DurationMs: r.DurationMs, TTFBMs: r.TTFBMs, ForwardMs: dd.ForwardMs, TotalMs: dd.TotalMs, Think: dd.Think,
		StateBytes: dd.StateBytes, Usage: r.Usage, CostUSD: r.CostUSD, Questions: make([]ItemQuestion, 0, len(dd.Questions))}
	matched := !a.f.questionFilters()
	outs := a.outcomes[r.ID]
	for _, q := range dd.Questions {
		iq := ItemQuestion{DecisionQuestion: q, Matched: !a.f.questionFilters() || a.f.matchQuestion(q)}
		if o, ok := outs[q.ID]; ok {
			t := o.Time
			iq.Outcome, iq.OutcomeTime = o.Outcome, &t
			if c, err := correct(q, o.Outcome); err == nil {
				iq.Correct = &c
			}
		}
		matched = matched || iq.Matched
		it.Questions = append(it.Questions, iq)
	}
	if !matched {
		return Item{}, false
	}
	switch a.f.Outcome {
	case "with":
		if len(outs) == 0 {
			return Item{}, false
		}
	case "without":
		if len(outs) > 0 {
			return Item{}, false
		}
	}
	if m, ok := a.d.mirrors.Get(r.ID); ok {
		it.Mirror = &m
	}
	return it, true
}

// correct compares an answer with an outcome, the way the Open-RLCD labs
// do: choice by equality, noul by the side of 0.5, score within 0.5.
func correct(q store.DecisionQuestion, raw json.RawMessage) (bool, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, err
	}
	switch q.Type {
	case "choice":
		s, ok := v.(string)
		if !ok {
			return false, errors.New("a choice outcome is one of the criteria labels, as a string")
		}
		if len(q.Labels) > 0 && !contains(q.Labels, s) {
			return false, fmt.Errorf("%q is not one of the criteria (%s)", s, strings.Join(q.Labels, ", "))
		}
		if q.Answer == "" {
			return false, errors.New("the call has no answer to compare")
		}
		return q.Choice == s, nil
	case "noul":
		b, err := truth(v)
		if err != nil {
			return false, err
		}
		if q.Noul == nil {
			return false, errors.New("the call has no answer to compare")
		}
		return (*q.Noul >= 0.5) == b, nil
	case "score":
		x, err := scoreValue(v, q.Labels)
		if err != nil {
			return false, err
		}
		if q.Score == nil {
			return false, errors.New("the call has no answer to compare")
		}
		return math.Abs(*q.Score-x) <= 0.5, nil
	}
	return false, fmt.Errorf("question type %q has no outcome rule", q.Type)
}

func truth(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case float64:
		if t == 0 || t == 1 {
			return t == 1, nil
		}
	case string:
		switch strings.ToLower(t) {
		case "yes", "true", "1":
			return true, nil
		case "no", "false", "0":
			return false, nil
		}
	}
	return false, errors.New("a noul outcome is true/false (or yes/no, 1/0)")
}

func scoreValue(v any, labels []string) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case string:
		for i, l := range labels {
			if l == t {
				return float64(i), nil
			}
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f, nil
		}
	}
	return 0, errors.New("a score outcome is the legend index (a number) or one of its labels")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ListResponse is GET /api/decisions.
type ListResponse struct {
	Items []Item `json:"items"`
	// Total counts every call the filters match; NextCursor continues the
	// list (empty on the last page).
	Total      int    `json:"total"`
	NextCursor string `json:"next_cursor,omitempty"`
}

const (
	defaultLimit = 50
	maxLimit     = 500
)

// list is GET /api/decisions: newest first, paginated by cursor (the
// previous page's next_cursor).
func (d *Decisions) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := parseFilter(q, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := defaultLimit
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be 1 to %d", maxLimit))
			return
		}
	}
	cursor := q.Get("cursor")
	a := d.newAudit(f)
	var all []Item
	if err := d.st.Scan(func(rec store.Record) bool {
		if it, ok := a.item(rec); ok {
			all = append(all, it)
		}
		return true
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Newest first by time, then id: ids only carry whole seconds.
	sort.Slice(all, func(i, j int) bool { return after(all[i], all[j]) })
	out := ListResponse{Total: len(all), Items: []Item{}}
	i := 0
	if cursor != "" {
		ct, cid, ok := parseCursor(cursor)
		if !ok {
			writeError(w, http.StatusBadRequest, "cursor is not one this API returned")
			return
		}
		i = sort.Search(len(all), func(k int) bool { return after(Item{Time: ct, RequestID: cid}, all[k]) })
	}
	end := i + limit
	if end > len(all) {
		end = len(all)
	}
	out.Items = append(out.Items, all[i:end]...)
	if end < len(all) && end > i {
		out.NextCursor = makeCursor(all[end-1])
	}
	writeJSON(w, http.StatusOK, out)
}

// after orders items newest first: by time, then by id.
func after(a, b Item) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.After(b.Time)
	}
	return a.RequestID > b.RequestID
}

// A cursor is the last item's time (unix nanoseconds) and id.
func makeCursor(it Item) string {
	return strconv.FormatInt(it.Time.UnixNano(), 10) + "_" + it.RequestID
}

func parseCursor(c string) (time.Time, string, bool) {
	ns, id, ok := strings.Cut(c, "_")
	n, err := strconv.ParseInt(ns, 10, 64)
	if !ok || err != nil || id == "" {
		return time.Time{}, "", false
	}
	return time.Unix(0, n), id, true
}

var csvHeader = []string{"request_id", "time", "source", "parent_id", "backend", "provider", "model", "client", "key",
	"conversation_id", "status", "error", "duration_ms", "forward_ms", "total_ms", "input_tokens", "output_tokens",
	"est_cost_usd", "question", "type", "answer", "choice", "score", "noul", "confidence", "top_prob",
	"outcome", "correct", "mirror_backend", "mirror_answer", "mirror_agree"}

// export streams every matching call, oldest first: CSV has one row per
// (selected) question, JSONL one Item per line.
func (d *Decisions) export(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := parseFilter(q, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	format := q.Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "jsonl" {
		writeError(w, http.StatusBadRequest, "format must be csv or jsonl")
		return
	}
	a := d.newAudit(f)
	flusher, _ := w.(http.Flusher)
	stamp := time.Now().UTC().Format("20060102T150405")
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="decisions-%s.%s"`, stamp, format))
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	enc := json.NewEncoder(w)
	if format == "csv" {
		_ = cw.Write(csvHeader)
	}
	n := 0
	_ = d.st.Scan(func(rec store.Record) bool {
		it, ok := a.item(rec)
		if !ok {
			return true
		}
		if format == "jsonl" {
			if enc.Encode(it) != nil {
				return false
			}
		} else {
			for _, row := range csvRows(it) {
				_ = cw.Write(row)
			}
			cw.Flush()
			if cw.Error() != nil {
				return false
			}
		}
		if n++; n%100 == 0 && flusher != nil {
			flusher.Flush()
		}
		return true
	})
	cw.Flush()
}

func num(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func csvRows(it Item) [][]string {
	client, in, out := "", "", ""
	if it.Client != nil {
		client = it.Client.ID
	}
	if it.Usage != nil {
		in, out = strconv.Itoa(it.Usage.InputTokens), strconv.Itoa(it.Usage.OutputTokens)
	}
	key := it.KeyName
	if key == "" {
		key = it.KeyID
	}
	base := []string{it.RequestID, it.Time.UTC().Format(time.RFC3339Nano), it.Source, it.ParentID, it.Backend, it.Provider,
		it.Model, client, key, it.ConversationID, strconv.Itoa(it.Status), it.Error, strconv.FormatInt(it.DurationMs, 10),
		num(it.ForwardMs), num(it.TotalMs), in, out, strconv.FormatFloat(it.CostUSD, 'f', -1, 64)}
	mirror := map[string]MirrorQuestion{}
	mb := ""
	if it.Mirror != nil {
		mb = it.Mirror.Backend
		for _, m := range it.Mirror.Questions {
			mirror[m.ID] = m
		}
	}
	var rows [][]string
	for _, q := range it.Questions {
		if !q.Matched {
			continue
		}
		correct, ma, agree := "", "", ""
		if q.Correct != nil {
			correct = strconv.FormatBool(*q.Correct)
		}
		if m, ok := mirror[q.ID]; ok {
			ma, agree = m.Answer, strconv.FormatBool(m.Agree)
		}
		row := append(append([]string{}, base...), q.ID, q.Type, q.Answer, q.Choice, num(q.Score), num(q.Noul),
			num(q.Confidence), num(q.TopProb), string(q.Outcome), correct, mb, ma, agree)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		// A call with no question (it failed before any answer) still shows.
		rows = append(rows, append(append([]string{}, base...), make([]string, len(csvHeader)-len(base))...))
	}
	return rows
}

// postOutcome is POST /api/decisions/{id}/outcome {question, outcome}.
func (d *Decisions) postOutcome(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Question string          `json:"question"`
		Outcome  json.RawMessage `json:"outcome"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if in.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	var rec *store.Record
	_ = d.st.Scan(func(x store.Record) bool {
		if x.ID == id {
			rec = &x
			return false
		}
		return true
	})
	if rec == nil || rec.Protocol != ir.ProtocolSystemOne || rec.Decisions == nil {
		writeError(w, http.StatusNotFound, "no decision call with id "+id)
		return
	}
	var q *store.DecisionQuestion
	for i := range rec.Decisions.Questions {
		if rec.Decisions.Questions[i].ID == in.Question {
			q = &rec.Decisions.Questions[i]
		}
	}
	if q == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("call %s has no question %q", id, in.Question))
		return
	}
	o := Outcome{RequestID: id, Question: in.Question, Outcome: in.Outcome, Time: time.Now().UTC()}
	if !o.gone() {
		if _, err := correct(*q, in.Outcome); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var compact json.RawMessage
		if b, err := json.Marshal(in.Outcome); err == nil {
			compact = b
		}
		o.Outcome = compact
	} else {
		o.Outcome = json.RawMessage("null")
	}
	if err := d.outcomes.Append(o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	it, _ := d.newAudit(Filter{}).item(*rec)
	writeJSON(w, http.StatusOK, it)
}

// --- stats ---

// Group sums the calls of one slice (all, one backend, one model, ...).
type Group struct {
	Requests     int      `json:"requests"`
	Errors       int      `json:"errors"`
	ErrorRate    float64  `json:"error_rate"`
	P50Ms        *int64   `json:"p50_ms"`
	P95Ms        *int64   `json:"p95_ms"`
	ForwardP50Ms *float64 `json:"forward_p50_ms"`
	ForwardP95Ms *float64 `json:"forward_p95_ms"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      float64  `json:"est_cost_usd"`

	lat []int64
	fwd []float64
}

func (g *Group) add(it Item) {
	g.Requests++
	if it.Status >= 400 || it.Error != "" {
		g.Errors++
	} else {
		g.lat = append(g.lat, it.DurationMs)
		if it.ForwardMs != nil {
			g.fwd = append(g.fwd, *it.ForwardMs)
		}
	}
	if it.Usage != nil {
		g.InputTokens += it.Usage.InputTokens
		g.OutputTokens += it.Usage.OutputTokens
	}
	g.CostUSD = pricing.Round6(g.CostUSD + it.CostUSD)
}

func (g *Group) done() {
	if g.Requests > 0 {
		g.ErrorRate = round4(float64(g.Errors) / float64(g.Requests))
	}
	sort.Slice(g.lat, func(i, j int) bool { return g.lat[i] < g.lat[j] })
	sort.Float64s(g.fwd)
	if len(g.lat) > 0 {
		p50, p95 := g.lat[pctIndex(len(g.lat), 50)], g.lat[pctIndex(len(g.lat), 95)]
		g.P50Ms, g.P95Ms = &p50, &p95
	}
	if len(g.fwd) > 0 {
		p50, p95 := g.fwd[pctIndex(len(g.fwd), 50)], g.fwd[pctIndex(len(g.fwd), 95)]
		g.ForwardP50Ms, g.ForwardP95Ms = &p50, &p95
	}
}

// pctIndex is the nearest-rank percentile (the labs' percentile()).
func pctIndex(n, p int) int {
	i := int(math.Ceil(float64(p)/100*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return i
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Bins is the number of calibration and histogram bins.
const Bins = 10

// Bin is one calibration bin: questions whose confidence falls in
// [Lo, Hi) (the last bin includes 1).
type Bin struct {
	Lo             float64  `json:"lo"`
	Hi             float64  `json:"hi"`
	N              int      `json:"n"`
	MeanConfidence *float64 `json:"mean_confidence,omitempty"`
	Accuracy       *float64 `json:"accuracy,omitempty"`

	conf, acc float64
}

func binOf(c float64) int {
	i := int(math.Floor(c * Bins))
	if i < 0 {
		i = 0
	}
	if i >= Bins {
		i = Bins - 1
	}
	return i
}

// Agreement counts mirrored answers.
type Agreement struct {
	Compared int      `json:"compared"`
	Agreed   int      `json:"agreed"`
	Rate     *float64 `json:"agreement_rate"`
}

func (a *Agreement) done() {
	if a.Compared > 0 {
		r := round4(float64(a.Agreed) / float64(a.Compared))
		a.Rate = &r
	}
}

// QuestionStats describes one question id across calls.
type QuestionStats struct {
	Count int `json:"count"`
	// Types counts the question's types (normally one).
	Types   map[string]int `json:"types"`
	Answers map[string]int `json:"answers"`
	// ConfidenceHistogram counts answers per tenth of confidence.
	ConfidenceHistogram [Bins]int `json:"confidence_histogram"`
	MeanConfidence      *float64  `json:"mean_confidence"`
	// Outcomes counts answers with a recorded, comparable outcome;
	// Accuracy and ECE (expected calibration error over 10 equal-width
	// bins, as in the Open-RLCD labs) are computed over them. With few
	// outcomes per bin ECE is noisy: look at Bins[].N.
	Outcomes int       `json:"outcomes"`
	Correct  int       `json:"correct"`
	Accuracy *float64  `json:"accuracy"`
	ECE      *float64  `json:"ece"`
	Bins     []Bin     `json:"bins"`
	Mirror   Agreement `json:"mirror"`

	confSum float64
	confN   int
	bins    [Bins]Bin
}

// MirrorGroup summarizes mirrored calls, overall or per mirror backend.
type MirrorGroup struct {
	Calls  int `json:"calls"`
	Errors int `json:"errors"`
	Agreement
	// Latencies of the pairs that both succeeded, side by side.
	PrimaryP50Ms *int64 `json:"primary_p50_ms"`
	MirrorP50Ms  *int64 `json:"mirror_p50_ms"`
	PrimaryP95Ms *int64 `json:"primary_p95_ms"`
	MirrorP95Ms  *int64 `json:"mirror_p95_ms"`

	prim, mirr []int64
}

func (m *MirrorGroup) add(r MirrorResult) {
	m.Calls++
	if r.Error != "" || r.Status >= 300 {
		m.Errors++
		return
	}
	m.Compared += r.Compared
	m.Agreed += r.Agreed
	m.prim = append(m.prim, r.PrimaryDurationMs)
	m.mirr = append(m.mirr, r.DurationMs)
}

func (m *MirrorGroup) done() {
	m.Agreement.done()
	pct := func(v []int64, p int) *int64 {
		if len(v) == 0 {
			return nil
		}
		s := append([]int64(nil), v...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		x := s[pctIndex(len(s), p)]
		return &x
	}
	m.PrimaryP50Ms, m.PrimaryP95Ms = pct(m.prim, 50), pct(m.prim, 95)
	m.MirrorP50Ms, m.MirrorP95Ms = pct(m.mirr, 50), pct(m.mirr, 95)
}

// StatsResponse is GET /api/decisions/stats (same filters as the list).
type StatsResponse struct {
	Total     Group                     `json:"total"`
	ByBackend map[string]*Group         `json:"by_backend"`
	ByModel   map[string]*Group         `json:"by_model"`
	BySource  map[string]*Group         `json:"by_source"`
	Questions map[string]*QuestionStats `json:"questions"`
	// ConfidenceHistogram counts every selected answer per tenth.
	ConfidenceHistogram [Bins]int `json:"confidence_histogram"`
	Mirror              struct {
		MirrorGroup
		ByBackend map[string]*MirrorGroup `json:"by_backend"`
		// Skipped counts sampled calls not mirrored because every slot was
		// busy; SaveFailed results that could not be written (since start).
		Skipped    int64 `json:"skipped"`
		SaveFailed int64 `json:"save_failed"`
	} `json:"mirror"`
	// Internal counts the gateway's own economy-model calls since start:
	// logged as decisions, dropped (queue full) and failed (not stored).
	Internal struct {
		Logged  int64 `json:"logged"`
		Dropped int64 `json:"dropped"`
		Failed  int64 `json:"failed"`
	} `json:"internal"`
}

func group(m map[string]*Group, k string) *Group {
	g := m[k]
	if g == nil {
		g = &Group{}
		m[k] = g
	}
	return g
}

func (d *Decisions) stats(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := d.newAudit(f)
	out := StatsResponse{ByBackend: map[string]*Group{}, ByModel: map[string]*Group{}, BySource: map[string]*Group{},
		Questions: map[string]*QuestionStats{}}
	out.Mirror.ByBackend = map[string]*MirrorGroup{}
	if err := d.st.Scan(func(rec store.Record) bool {
		it, ok := a.item(rec)
		if !ok {
			return true
		}
		model := it.Model
		if model == "" {
			model = "(none)"
		}
		for _, g := range []*Group{&out.Total, group(out.ByBackend, it.Backend), group(out.ByModel, model), group(out.BySource, it.Source)} {
			g.add(it)
		}
		mq := map[string]MirrorQuestion{}
		if it.Mirror != nil {
			out.Mirror.add(*it.Mirror)
			mg := out.Mirror.ByBackend[it.Mirror.Backend]
			if mg == nil {
				mg = &MirrorGroup{}
				out.Mirror.ByBackend[it.Mirror.Backend] = mg
			}
			mg.add(*it.Mirror)
			for _, m := range it.Mirror.Questions {
				mq[m.ID] = m
			}
		}
		for _, q := range it.Questions {
			if !q.Matched {
				continue
			}
			qs := out.Questions[q.ID]
			if qs == nil {
				qs = &QuestionStats{Types: map[string]int{}, Answers: map[string]int{}}
				out.Questions[q.ID] = qs
			}
			qs.Count++
			if q.Type != "" {
				qs.Types[q.Type]++
			}
			if q.Answer != "" {
				qs.Answers[q.Answer]++
			}
			if m, ok := mq[q.ID]; ok {
				qs.Mirror.Compared++
				if m.Agree {
					qs.Mirror.Agreed++
				}
			}
			if q.Confidence == nil {
				continue
			}
			c := *q.Confidence
			out.ConfidenceHistogram[binOf(c)]++
			qs.ConfidenceHistogram[binOf(c)]++
			qs.confSum += c
			qs.confN++
			if q.Correct != nil {
				qs.Outcomes++
				b := &qs.bins[binOf(c)]
				b.N++
				b.conf += c
				if *q.Correct {
					qs.Correct++
					b.acc++
				}
			}
		}
		return true
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Total.done()
	for _, m := range []map[string]*Group{out.ByBackend, out.ByModel, out.BySource} {
		for _, g := range m {
			g.done()
		}
	}
	out.Mirror.MirrorGroup.done()
	for _, g := range out.Mirror.ByBackend {
		g.done()
	}
	for _, qs := range out.Questions {
		qs.finish()
	}
	out.Mirror.Skipped, out.Mirror.SaveFailed = d.mirrorSkipped.Load(), d.mirrorSaveFail.Load()
	out.Internal.Logged, out.Internal.Dropped, out.Internal.Failed =
		d.internalLogged.Load(), d.internalDropped.Load(), d.internalFailed.Load()
	writeJSON(w, http.StatusOK, out)
}

// finish computes the means, accuracy and ECE.
func (qs *QuestionStats) finish() {
	qs.Mirror.done()
	if qs.confN > 0 {
		m := round4(qs.confSum / float64(qs.confN))
		qs.MeanConfidence = &m
	}
	qs.Bins = make([]Bin, Bins)
	ece := 0.0
	for i := range qs.bins {
		b := qs.bins[i]
		b.Lo, b.Hi = float64(i)/Bins, float64(i+1)/Bins
		if b.N > 0 {
			mc, acc := b.conf/float64(b.N), b.acc/float64(b.N)
			ece += float64(b.N) / float64(qs.Outcomes) * math.Abs(acc-mc)
			mc, acc = round4(mc), round4(acc)
			b.MeanConfidence, b.Accuracy = &mc, &acc
		}
		qs.Bins[i] = b
	}
	if qs.Outcomes > 0 {
		acc, e := round4(float64(qs.Correct)/float64(qs.Outcomes)), round4(ece)
		qs.Accuracy, qs.ECE = &acc, &e
	}
}
