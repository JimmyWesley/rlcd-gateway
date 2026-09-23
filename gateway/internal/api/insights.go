package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// insights serves GET /api/insights?window=24h: the recent request log
// bucketed over time, for the dashboard's overview charts. It is read-only
// and only looks at the in-memory log (store.Recent), so "all" means the
// last few hundred requests, not the whole history on disk.

// now is replaced in tests.
var now = time.Now

// bucketSteps are the bucket sizes a window can be split into; the first
// one that gives at most maxBuckets buckets wins.
var bucketSteps = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

const maxBuckets = 36

type insightBucket struct {
	Start             time.Time `json:"t"`
	Requests          int       `json:"requests"`
	Errors            int       `json:"errors"`
	InputTokens       int       `json:"input_tokens"`
	OutputTokens      int       `json:"output_tokens"`
	CacheReadTokens   int       `json:"cache_read_input_tokens"`
	CacheWriteTokens  int       `json:"cache_creation_input_tokens"`
	SavedTokens       int       `json:"saved_tokens"`
	SavedUSD          float64   `json:"saved_usd"`
	ShadowSavedTokens int       `json:"shadow_saved_tokens"`
	ShadowSavedUSD    float64   `json:"shadow_saved_usd"`
	CostUSD           float64   `json:"est_cost_usd"`
	LatencyP50Ms      int64     `json:"latency_p50_ms"`
	LatencyP95Ms      int64     `json:"latency_p95_ms"`
	Epochs            int       `json:"epochs"`
	SelectorMs        int64     `json:"selector_avg_ms"`

	durations []int64
	selMs     int64
}

type insightTotals struct {
	Requests           int     `json:"requests"`
	Errors             int     `json:"errors"`
	InputTokens        int     `json:"input_tokens"`
	OutputTokens       int     `json:"output_tokens"`
	CacheReadTokens    int     `json:"cache_read_input_tokens"`
	CacheWriteTokens   int     `json:"cache_creation_input_tokens"`
	SavedTokens        int     `json:"saved_tokens"`
	SavedUSD           float64 `json:"saved_usd"`
	ShadowSavedTokens  int     `json:"shadow_saved_tokens"`
	ShadowSavedUSD     float64 `json:"shadow_saved_usd"`
	CostUSD            float64 `json:"est_cost_usd"`
	PrunedRequests     int     `json:"pruned_requests"`
	CacheInvalidations int     `json:"cache_invalidations"`
	Conversations      int     `json:"conversations"`
}

type insightLatency struct {
	P50Ms     int64 `json:"p50_ms"`
	P95Ms     int64 `json:"p95_ms"`
	P99Ms     int64 `json:"p99_ms"`
	TTFBP50Ms int64 `json:"ttfb_p50_ms"`
}

type insightGroup struct {
	Name string `json:"name"`
	// Label is a display name when Name is a slug (clients).
	Label    string `json:"label,omitempty"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
	// Tokens is the input the provider billed: fresh + cache read + cache write.
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"est_cost_usd"`
}

type insightSelector struct {
	Epochs      int        `json:"epochs"`
	Errors      int        `json:"errors"`
	AvgMs       int64      `json:"avg_ms"`
	P95Ms       int64      `json:"p95_ms"`
	LastEpoch   *time.Time `json:"last_epoch,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

type insightsView struct {
	Window        string          `json:"window"`
	From          time.Time       `json:"from"`
	To            time.Time       `json:"to"`
	BucketSeconds int64           `json:"bucket_seconds"`
	Buckets       []insightBucket `json:"buckets"`
	Totals        insightTotals   `json:"totals"`
	Latency       insightLatency  `json:"latency"`
	ByRoute       []insightGroup  `json:"by_route"`
	ByModel       []insightGroup  `json:"by_model"`
	ByClient      []insightGroup  `json:"by_client"`
	ByProtocol    []insightGroup  `json:"by_protocol"`
	ByKey         []insightGroup  `json:"by_key"`
	Selector      insightSelector `json:"selector"`
}

// pruneSummary is the part of the prune stage's per-request summary that the
// overview needs (see prune.Summary).
type pruneSummary struct {
	Mode              string  `json:"mode"`
	Applied           bool    `json:"applied"`
	EpochRan          bool    `json:"epoch_ran"`
	Dropped           int     `json:"dropped"`
	SavedTokens       int     `json:"saved_tokens"`
	EstCostBefore     float64 `json:"est_cost_before"`
	EstCostAfter      float64 `json:"est_cost_after"`
	CacheInvalidating bool    `json:"cache_invalidating"`
	SelectorMs        int64   `json:"selector_ms"`
	SelectorError     string  `json:"selector_error"`
	Warning           string  `json:"warning"`
}

// parseWindow accepts 1h, 6h, 24h, 7d, all, or any Go duration.
func parseWindow(s string) (time.Duration, bool) {
	switch s {
	case "", "24h":
		return 24 * time.Hour, true
	case "all":
		return 0, true
	}
	if strings.HasSuffix(s, "d") {
		if d, err := time.ParseDuration(strings.TrimSuffix(s, "d") + "h"); err == nil && d > 0 {
			return d * 24, true
		}
		return 0, false
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

func (a *API) insights(w http.ResponseWriter, r *http.Request) {
	win := r.URL.Query().Get("window")
	span, ok := parseWindow(win)
	if !ok {
		writeError(w, http.StatusBadRequest, "window must be like 1h, 24h, 7d or all")
		return
	}
	if win == "" {
		win = "24h"
	}
	writeJSON(w, http.StatusOK, buildInsights(a.Store.Recent(), win, span, now()))
}

func buildInsights(recs []store.Record, win string, span time.Duration, to time.Time) insightsView {
	from := to.Add(-span)
	if span == 0 { // "all": from the oldest record in memory
		from = to.Add(-time.Hour)
		for _, rec := range recs {
			if rec.Time.Before(from) {
				from = rec.Time
			}
		}
	}
	step := bucketSteps[len(bucketSteps)-1]
	for _, s := range bucketSteps {
		if to.Sub(from) <= s*maxBuckets {
			step = s
			break
		}
	}
	start := from.Truncate(step)
	n := int(to.Sub(start)/step) + 1
	v := insightsView{Window: win, From: from, To: to, BucketSeconds: int64(step / time.Second),
		Buckets: make([]insightBucket, n)}
	for i := range v.Buckets {
		v.Buckets[i].Start = start.Add(time.Duration(i) * step)
	}

	var durations, ttfbs, selMs []int64
	routes, models := map[string]*insightGroup{}, map[string]*insightGroup{}
	clients, protocols, keys := map[string]*insightGroup{}, map[string]*insightGroup{}, map[string]*insightGroup{}
	convs := map[string]bool{}
	t := &v.Totals
	for _, rec := range recs {
		if rec.Time.Before(from) || rec.Time.After(to) {
			continue
		}
		b := &v.Buckets[int(rec.Time.Sub(start)/step)]
		b.CostUSD += rec.CostUSD
		failed := rec.Status >= 400 || rec.Error != ""
		b.Requests++
		t.Requests++
		if failed {
			b.Errors++
			t.Errors++
		}
		if rec.ConversationID != "" {
			convs[rec.ConversationID] = true
		}
		billed := 0
		if u := rec.Usage; u != nil {
			b.InputTokens += u.InputTokens
			b.OutputTokens += u.OutputTokens
			b.CacheReadTokens += u.CacheReadTokens
			b.CacheWriteTokens += u.CacheCreationTokens
			billed = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
		}
		if rec.Status > 0 {
			b.durations = append(b.durations, rec.DurationMs)
			durations = append(durations, rec.DurationMs)
			ttfbs = append(ttfbs, rec.TTFBMs)
		}
		model := rec.Model
		if model == "" {
			model = rec.ClientModel
		}
		if model == "" {
			model = rec.Path
		}
		client, clientName := "unknown", ""
		if c := rec.Client; c != nil && c.ID != "" {
			client, clientName = c.ID, c.Name
		}
		protocol := rec.Protocol
		if protocol == "" {
			protocol = "anthropic-messages" // records from before protocols existed
		}
		type group struct {
			m           map[string]*insightGroup
			name, label string
		}
		groups := []group{{routes, rec.Route, ""}, {models, model, ""}, {clients, client, clientName}, {protocols, protocol, ""}}
		if rec.KeyName != "" {
			groups = append(groups, group{keys, rec.KeyName, ""})
		}
		for _, g := range groups {
			x := g.m[g.name]
			if x == nil {
				x = &insightGroup{Name: g.name, Label: g.label}
				g.m[g.name] = x
			}
			x.Requests++
			x.Tokens += billed
			x.CostUSD += rec.CostUSD
			if failed {
				x.Errors++
			}
		}

		var p pruneSummary
		if raw := rec.Stages["prune"]; raw == nil || json.Unmarshal(raw, &p) != nil {
			continue
		}
		usd := p.EstCostBefore - p.EstCostAfter
		// Same split as /api/prune/stats: a turn counts as enforced when the
		// body was pruned or enforce ran without falling back to shadow.
		if p.Applied || (p.Mode == "enforce" && p.Warning == "") {
			b.SavedTokens += p.SavedTokens
			b.SavedUSD += usd
		} else {
			b.ShadowSavedTokens += p.SavedTokens
			b.ShadowSavedUSD += usd
		}
		if p.Dropped > 0 {
			t.PrunedRequests++
		}
		if p.CacheInvalidating {
			t.CacheInvalidations++
		}
		if p.EpochRan {
			b.Epochs++
			b.selMs += p.SelectorMs
			selMs = append(selMs, p.SelectorMs)
			v.Selector.Epochs++
			if v.Selector.LastEpoch == nil || rec.Time.After(*v.Selector.LastEpoch) {
				tm := rec.Time
				v.Selector.LastEpoch = &tm
			}
		}
		if p.SelectorError != "" {
			v.Selector.Errors++
			if v.Selector.LastErrorAt == nil || rec.Time.After(*v.Selector.LastErrorAt) {
				tm := rec.Time
				v.Selector.LastErrorAt, v.Selector.LastError = &tm, p.SelectorError
			}
		}
	}

	for i := range v.Buckets {
		b := &v.Buckets[i]
		b.LatencyP50Ms, b.LatencyP95Ms = percentile(b.durations, 50), percentile(b.durations, 95)
		if b.Epochs > 0 {
			b.SelectorMs = b.selMs / int64(b.Epochs)
		}
		b.SavedUSD, b.ShadowSavedUSD, b.CostUSD = round6(b.SavedUSD), round6(b.ShadowSavedUSD), round6(b.CostUSD)
		t.CostUSD += b.CostUSD
		t.InputTokens += b.InputTokens
		t.OutputTokens += b.OutputTokens
		t.CacheReadTokens += b.CacheReadTokens
		t.CacheWriteTokens += b.CacheWriteTokens
		t.SavedTokens += b.SavedTokens
		t.SavedUSD += b.SavedUSD
		t.ShadowSavedTokens += b.ShadowSavedTokens
		t.ShadowSavedUSD += b.ShadowSavedUSD
	}
	t.SavedUSD, t.ShadowSavedUSD, t.CostUSD = round6(t.SavedUSD), round6(t.ShadowSavedUSD), round6(t.CostUSD)
	t.Conversations = len(convs)
	v.Latency = insightLatency{P50Ms: percentile(durations, 50), P95Ms: percentile(durations, 95),
		P99Ms: percentile(durations, 99), TTFBP50Ms: percentile(ttfbs, 50)}
	if len(selMs) > 0 {
		var sum int64
		for _, ms := range selMs {
			sum += ms
		}
		v.Selector.AvgMs = sum / int64(len(selMs))
		v.Selector.P95Ms = percentile(selMs, 95)
	}
	v.ByRoute, v.ByModel = sortedGroups(routes), sortedGroups(models)
	v.ByClient, v.ByProtocol, v.ByKey = sortedGroups(clients), sortedGroups(protocols), sortedGroups(keys)
	return v
}

// percentile uses the nearest-rank method; it sorts xs in place.
func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	i := (p*len(xs)+99)/100 - 1
	if i < 0 {
		i = 0
	}
	return xs[i]
}

func sortedGroups(m map[string]*insightGroup) []insightGroup {
	out := make([]insightGroup, 0, len(m))
	for _, g := range m {
		g.CostUSD = round6(g.CostUSD)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// round6 rounds to 6 decimals, symmetrically: savings can be negative.
func round6(f float64) float64 {
	const k = 1e6
	if f < 0 {
		return -float64(int64(-f*k+0.5)) / k
	}
	return float64(int64(f*k+0.5)) / k
}
