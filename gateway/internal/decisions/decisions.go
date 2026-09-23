// Package decisions proxies and audits System One decision calls.
//
// System One models (TypeSafe's Jev, open-rlcd) answer typed questions
// about a state instead of writing text: POST /v1/systemone {model, state,
// questions: {id: {type: choice|score|noul, instructions, criteria?}}} →
// {model, answers: {id: {type, choice|score|noul, confidence?,
// probabilities?, legend?}}, usage}. An app that makes these calls changes
// only its base URL to the gateway, and every call is then:
//
//   - authenticated and limited by its gateway key, like any proxy path;
//   - sent to a backend picked by model (see Settings), with the client's
//     own credentials or the backend's token;
//   - answered byte for byte, headers included;
//   - recorded in the request log (protocol "systemone") with a compact
//     decisions summary, and priced with the price table;
//   - optionally mirrored to a second backend after the client has its
//     answer, to measure agreement.
//
// The gateway's own economy-model calls (pruning, the router's auto rule)
// are logged the same way (internal.go). Outcomes recorded later turn the
// log into a calibration audit: accuracy and ECE per question (audit.go).
//
// Endpoints:
//
//	POST /v1/systemone, POST /v1/decisions      the proxy (same behavior)
//	GET  /api/decisions                         filtered, paginated list
//	GET  /api/decisions/export?format=csv|jsonl streamed export
//	GET  /api/decisions/stats                   counts, calibration, mirror agreement
//	POST /api/decisions/{request_id}/outcome    record the ground truth
//	GET|PUT /api/decisions/settings             the "decisions" config section
package decisions

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Decisions is the decision proxy and its audit log.
type Decisions struct {
	cfg *config.Store
	st  *store.Store
	// Keys charges usage to gateway keys and enforces their daily limit.
	Keys   *keys.Store
	Client *http.Client

	outcomes *jsonlLog[Outcome]
	mirrors  *jsonlLog[MirrorResult]

	// Mirror calls run in the background, at most mirrorSlots at a time.
	mirrorSem chan struct{}
	// MirrorTimeout bounds one mirror call.
	MirrorTimeout time.Duration
	// sample decides whether a call is mirrored (tests replace it).
	sample func(rate float64) bool

	// internal logging of the gateway's own selector calls.
	internalQ    chan selector.Call
	internalOnce sync.Once
	// save stores a record (tests replace it to make storage fail).
	save func(*store.Detail) error
	counters
}

// counters are what failed silently, reported by the stats endpoint.
type counters struct {
	internalLogged  atomic.Int64
	internalDropped atomic.Int64
	internalFailed  atomic.Int64
	mirrorSkipped   atomic.Int64
	mirrorSaveFail  atomic.Int64
}

const (
	mirrorSlots   = 8
	internalQueue = 256
)

// New opens the decision log under the store's directory.
func New(cfg *config.Store, st *store.Store) *Decisions {
	dir := filepath.Join(st.Dir(), "decisions")
	d := &Decisions{cfg: cfg, st: st, Client: relay.NewClient(),
		outcomes:      newJSONLLog(filepath.Join(dir, "outcomes.jsonl"), func(o Outcome) string { return o.RequestID + "\x00" + o.Question }),
		mirrors:       newJSONLLog(filepath.Join(dir, "mirror.jsonl"), func(m MirrorResult) string { return m.RequestID }),
		mirrorSem:     make(chan struct{}, mirrorSlots),
		MirrorTimeout: 60 * time.Second,
		sample:        func(rate float64) bool { return rate >= 1 || rand.Float64() < rate },
		internalQ:     make(chan selector.Call, internalQueue),
	}
	d.save = st.Save
	d.outcomes.gone = Outcome.gone
	return d
}

// Settings returns the section, or the error that makes it unusable.
func (d *Decisions) Settings() (Settings, error) { return settingsFrom(d.cfg.Get()) }

// Register mounts the proxy endpoints and the audit API.
func (d *Decisions) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/systemone", d.serve)
	mux.HandleFunc("POST /v1/decisions", d.serve)

	mux.HandleFunc("GET /api/decisions", d.list)
	mux.HandleFunc("GET /api/decisions/export", d.export)
	mux.HandleFunc("GET /api/decisions/stats", d.stats)
	mux.HandleFunc("POST /api/decisions/{id}/outcome", d.postOutcome)
	mux.HandleFunc("GET /api/decisions/settings", d.getSettings)
	mux.HandleFunc("PUT /api/decisions/settings", d.putSettings)
}

func (d *Decisions) getSettings(w http.ResponseWriter, r *http.Request) {
	c := d.cfg.Get()
	s, err := settingsFrom(c)
	v := view(c, s)
	if err != nil {
		v.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, v)
}

func (d *Decisions) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	cur, _ := settingsFrom(d.cfg.Get())
	next := in.merge(cur)
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(next)
	if err := d.cfg.SetSection(sectionName, raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view(d.cfg.Get(), next))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
