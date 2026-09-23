package decisions

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// seed stores n synthetic decision calls, one a minute from t0. Call i asks
// "spam" (noul) and "topic" (choice); odd calls go to jev from key "k1",
// even ones to rlcd; every fifth failed.
func seed(t *testing.T, e *env, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%016x", t0.Add(time.Duration(i)*time.Minute).Format("20060102T150405"), i)
		backend, model, key := "rlcd", "Open-RLCD-text", ""
		if i%2 == 1 {
			backend, model, key = "jev", "jev-latest", "k1"
		}
		noul := float64(i%10) / 10 // confidence max(n, 1-n)
		topic := []string{"billing", "shipping"}[i%2]
		conf := 0.55 + float64(i%5)/10
		rec := store.Record{ID: id, Time: t0.Add(time.Duration(i) * time.Minute), Protocol: ir.ProtocolSystemOne,
			Route: backend, Model: model, ClientModel: model, KeyName: key, Status: 200, DurationMs: int64(100 + i),
			Client: &store.Client{ID: "python-requests", Name: "Python requests", Kind: "sdk"},
			Usage:  &store.Usage{InputTokens: 100, OutputTokens: 2},
			Decisions: &store.Decisions{Backend: backend, Source: SourceClient, StateBytes: 50, Questions: []store.DecisionQuestion{
				{ID: "spam", Type: "noul", Noul: f64(noul), Answer: map[bool]string{true: "yes", false: "no"}[noul >= 0.5],
					Confidence: f64(math.Max(noul, 1-noul)), TopProb: f64(math.Max(noul, 1-noul))},
				{ID: "topic", Type: "choice", Labels: []string{"billing", "shipping"}, Choice: topic, Answer: topic,
					Confidence: f64(conf), TopProb: f64(conf)},
			}}}
		if i%5 == 4 {
			rec.Status, rec.Error = 500, "backend exploded"
		}
		must(t, e.st.Save(&store.Detail{Record: rec, RequestHeaders: map[string]string{}}))
		ids = append(ids, id)
	}
	// A non-decision record never shows up.
	must(t, e.st.Save(&store.Detail{Record: store.Record{ID: t0.Format("20060102T150405") + "-ffffffffffffffff", Time: t0,
		Protocol: ir.ProtocolAnthropic, Status: 200}, RequestHeaders: map[string]string{}}))
	return ids
}

func (e *env) list(q string) ListResponse {
	e.t.Helper()
	resp, out := e.do("GET", "/api/decisions?"+q, "", nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("list %s: %d %s", q, resp.StatusCode, out)
	}
	var l ListResponse
	must(e.t, json.Unmarshal([]byte(out), &l))
	return l
}

func TestListFiltersAndPagination(t *testing.T) {
	e := newEnv(t, false)
	ids := seed(t, e, 20)

	all := e.list("")
	if all.Total != 20 || len(all.Items) != 20 || all.Items[0].RequestID != ids[19] || all.NextCursor != "" {
		t.Fatalf("all: total %d, %d items, first %s", all.Total, len(all.Items), all.Items[0].RequestID)
	}
	// Pages of 7, newest first, with no gap and no repeat.
	var got []string
	cursor := ""
	for page := 0; page < 5; page++ {
		l := e.list("limit=7&cursor=" + url.QueryEscape(cursor))
		for _, it := range l.Items {
			got = append(got, it.RequestID)
		}
		if l.Total != 20 {
			t.Fatalf("page %d total %d", page, l.Total)
		}
		if cursor = l.NextCursor; cursor == "" {
			break
		}
	}
	if len(got) != 20 || got[0] != ids[19] || got[19] != ids[0] {
		t.Fatalf("pagination: %d items %v", len(got), got)
	}

	for q, want := range map[string]int{
		"backend=jev":                         10,
		"model=jev-latest":                    10,
		"key=k1":                              10,
		"client=python-requests":              20,
		"client=codex":                        0,
		"status=error":                        4,
		"status=ok":                           16,
		"source=client":                       20,
		"question=topic&answer=shipping":      10,
		"answer=yes":                          10, // spam noul >= 0.5
		"question=spam&confidence_below=0.65": 6,  // noul 0.4, 0.5 and 0.6, twice
		"from=" + t0.Add(5*time.Minute).Format(time.RFC3339):                                                        15,
		"from=" + t0.Add(5*time.Minute).Format(time.RFC3339) + "&to=" + t0.Add(10*time.Minute).Format(time.RFC3339): 5,
		"backend=jev&question=topic&answer=billing":                                                                 0,
		"question=nope": 0,
	} {
		if l := e.list(q); l.Total != want {
			t.Errorf("%s: %d, want %d", q, l.Total, want)
		}
	}
	// The matched questions are flagged.
	l := e.list("question=spam&confidence_below=0.65&limit=1")
	if q := l.Items[0].Questions; !q[0].Matched || q[1].Matched {
		t.Errorf("matched flags: %+v", q)
	}
	for _, bad := range []string{"limit=0", "limit=9999", "from=yesterday", "confidence_below=2", "status=meh", "since=-1h"} {
		if resp, _ := e.do("GET", "/api/decisions?"+bad, "", nil); resp.StatusCode != 400 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

func TestExportCSVAndJSONL(t *testing.T) {
	e := newEnv(t, false)
	ids := seed(t, e, 6)
	resp, out := e.do("GET", "/api/decisions/export?format=csv&backend=rlcd", "", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv: %d %v", resp.StatusCode, resp.Header)
	}
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	must(t, err)
	if len(rows) != 1+3*2 || strings.Join(rows[0], ",") != strings.Join(csvHeader, ",") {
		t.Fatalf("csv rows: %d\n%s", len(rows), out)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	// Oldest first, one row per question.
	if r := rows[1]; r[col["request_id"]] != ids[0] || r[col["question"]] != "spam" || r[col["answer"]] != "no" ||
		r[col["backend"]] != "rlcd" || r[col["input_tokens"]] != "100" || r[col["confidence"]] != "1" {
		t.Errorf("first row: %v", r)
	}
	// A question filter narrows the rows too.
	_, out = e.do("GET", "/api/decisions/export?format=csv&question=topic", "", nil)
	if rows, _ := csv.NewReader(strings.NewReader(out)).ReadAll(); len(rows) != 1+6 {
		t.Errorf("question-filtered csv: %d rows", len(rows))
	}

	resp, out = e.do("GET", "/api/decisions/export?format=jsonl", "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("jsonl: %d %v", resp.StatusCode, resp.Header)
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	n := 0
	for sc.Scan() {
		var it Item
		must(t, json.Unmarshal(sc.Bytes(), &it))
		if it.RequestID != ids[n] || len(it.Questions) != 2 {
			t.Errorf("line %d: %+v", n, it)
		}
		n++
	}
	if n != 6 {
		t.Errorf("jsonl lines: %d", n)
	}
	if resp, _ := e.do("GET", "/api/decisions/export?format=xml", "", nil); resp.StatusCode != 400 {
		t.Errorf("unknown format: %d", resp.StatusCode)
	}
}

func (e *env) outcome(id, question string, outcome any) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"question": question, "outcome": outcome})
	resp, out := e.do("POST", "/api/decisions/"+id+"/outcome", string(b), nil)
	return resp.StatusCode, out
}

func (e *env) stats(q string) StatsResponse {
	e.t.Helper()
	resp, out := e.do("GET", "/api/decisions/stats?"+q, "", nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("stats: %d %s", resp.StatusCode, out)
	}
	var s StatsResponse
	must(e.t, json.Unmarshal([]byte(out), &s))
	return s
}

func TestOutcomesAccuracyAndECE(t *testing.T) {
	e := newEnv(t, false)
	ids := seed(t, e, 10)

	// topic: confidence 0.55 + (i%5)/10, choice billing (even) / shipping (odd).
	// Ground truth: billing for every call, so the even calls are right.
	var sumAbs float64
	type bin struct {
		n, right int
		conf     float64
	}
	bins := map[int]*bin{}
	for i, id := range ids {
		if code, out := e.outcome(id, "topic", "billing"); code != 200 {
			t.Fatalf("outcome %d: %d %s", i, code, out)
		}
		c := 0.55 + float64(i%5)/10
		b := bins[binOf(c)]
		if b == nil {
			b = &bin{}
			bins[binOf(c)] = b
		}
		b.n++
		b.conf += c
		if i%2 == 0 {
			b.right++
		}
	}
	for _, b := range bins {
		sumAbs += float64(b.n) / 10 * math.Abs(float64(b.right)/float64(b.n)-b.conf/float64(b.n))
	}
	// spam: noul i/10, truth "spam" (true) for the first five only.
	for i, id := range ids {
		if code, out := e.outcome(id, "spam", i < 5); code != 200 {
			t.Fatalf("spam outcome: %d %s", code, out)
		}
	}

	s := e.stats("")
	topic := s.Questions["topic"]
	if topic == nil || topic.Outcomes != 10 || topic.Correct != 5 || topic.Accuracy == nil || *topic.Accuracy != 0.5 {
		t.Fatalf("topic: %+v", topic)
	}
	if topic.ECE == nil || math.Abs(*topic.ECE-round4(sumAbs)) > 1e-9 {
		t.Errorf("topic ECE %v, want %v", topic.ECE, sumAbs)
	}
	total := 0
	for i, b := range topic.Bins {
		total += b.N
		if want := bins[i]; want != nil && b.N != want.n {
			t.Errorf("bin %d: n %d, want %d", i, b.N, want.n)
		}
	}
	if total != 10 || len(topic.Bins) != Bins || topic.Answers["billing"] != 5 || topic.Types["choice"] != 10 {
		t.Errorf("bins/answers: %+v", topic)
	}
	// spam: predicted yes for noul >= 0.5 (i >= 5), truth yes for i < 5:
	// every answer is wrong except none; accuracy 0.
	spam := s.Questions["spam"]
	if spam.Outcomes != 10 || spam.Accuracy == nil || *spam.Accuracy != 0 {
		t.Errorf("spam: %+v", spam)
	}
	var hist int
	for _, n := range s.ConfidenceHistogram {
		hist += n
	}
	if hist != 20 || s.Total.Requests != 10 || s.Total.Errors != 2 || s.Total.ErrorRate != 0.2 ||
		s.ByBackend["jev"].Requests != 5 || s.ByModel["Open-RLCD-text"].Requests != 5 || s.Total.P50Ms == nil {
		t.Errorf("totals: %+v hist %d", s.Total, hist)
	}
	// p50/p95 (nearest rank) over the successful calls' durations
	// (100+i for i%5 != 4: 100-103, 105-108).
	if *s.Total.P50Ms != 103 || *s.Total.P95Ms != 108 {
		t.Errorf("latency p50 %d p95 %d", *s.Total.P50Ms, *s.Total.P95Ms)
	}

	// The list shows the outcome and whether the answer was right.
	l := e.list("outcome=with&limit=1")
	if q := l.Items[0].Questions[1]; string(q.Outcome) != `"billing"` || q.Correct == nil || *q.Correct {
		t.Errorf("list outcome: %+v", q)
	}
	// Bad outcomes are refused; null clears one.
	for _, bad := range []struct {
		q string
		o any
	}{{"topic", "sports"}, {"topic", 3}, {"spam", "maybe"}, {"nope", true}} {
		if code, _ := e.outcome(ids[0], bad.q, bad.o); code != 400 {
			t.Errorf("outcome %v: %d", bad, code)
		}
	}
	if code, _ := e.outcome("20990101T000000-0000000000000000", "topic", "billing"); code != 404 {
		t.Errorf("unknown request: %d", code)
	}
	if code, _ := e.outcome(ids[0], "topic", nil); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if s := e.stats(""); s.Questions["topic"].Outcomes != 9 {
		t.Errorf("after clearing: %d", s.Questions["topic"].Outcomes)
	}
	// Outcomes survive a restart (they are replayed from their file).
	d2 := New(e.cs, e.st)
	n := 0
	d2.outcomes.Each(func(Outcome) { n++ })
	if n != 19 {
		t.Errorf("replayed outcomes: %d", n)
	}
}

func TestCorrectRules(t *testing.T) {
	score := store.DecisionQuestion{Type: "score", Labels: []string{"low", "medium", "high"}, Score: f64(1.52), Answer: "high"}
	for o, want := range map[string]bool{`2`: true, `"high"`: true, `1`: false, `"medium"`: false, `1.1`: true} {
		if got, err := correct(score, json.RawMessage(o)); err != nil || got != want {
			t.Errorf("score %s: %v %v", o, got, err)
		}
	}
	noul := store.DecisionQuestion{Type: "noul", Noul: f64(0.2), Answer: "no"}
	for o, want := range map[string]bool{`false`: true, `"no"`: true, `0`: true, `true`: false, `"yes"`: false} {
		if got, err := correct(noul, json.RawMessage(o)); err != nil || got != want {
			t.Errorf("noul %s: %v %v", o, got, err)
		}
	}
}
