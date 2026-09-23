package store

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// Recall safety. A pruning marker names the request where a block was first
// dropped, and recall rebuilds the original from that request's body. So:
//
//   - a conversation is active while any of its state (a request in the
//     log, pruning state, a routing pin, a recall) is younger than
//     conversation_ttl;
//   - every request an active conversation's pruning state points at is
//     pinned: retention never deletes it, whatever its age or the size cap;
//   - an idle conversation expires as a whole: its pruning state, routing
//     pin and recall events go, which releases its pins, and its requests
//     then age out like any other.
//
// The janitor computes the pins from the features' own state on every run
// (nothing is counted incrementally, so nothing can drift), expires idle
// conversations, and runs the store's Purge.

// Conversation is one conversation a feature keeps state for.
type Conversation struct {
	ID         string
	LastActive time.Time
	// Pins are request ids whose bodies the state depends on.
	Pins []string
}

// ConversationSource is a feature that keeps state per conversation
// (pruning, routing, recall, and the log itself).
type ConversationSource interface {
	StorageName() string
	StorageConversations() []Conversation
	// ExpireConversations deletes the state of these conversations, and
	// state not tied to a conversation that is older than before. It must
	// keep a conversation that became active again (activity after before).
	ExpireConversations(ids []string, before time.Time) (int, error)
}

// Grace is how recently a blob must have been written or referenced for
// the janitor to keep it even when no record references it.
const Grace = 10 * time.Minute

// JanitorInterval is how often the janitor runs, after once on startup.
const JanitorInterval = time.Hour

// JanitorReport is one janitor run.
type JanitorReport struct {
	Time       time.Time `json:"time"`
	DurationMs int64     `json:"duration_ms"`
	DryRun     bool      `json:"dry_run"`
	// Trigger is startup | schedule | api | cli.
	Trigger string `json:"trigger"`
	// ConversationsActive are within conversation_ttl; ConversationsExpired
	// were idle longer and are (or, dry, would be) expired.
	ConversationsActive  int      `json:"conversations_active"`
	ConversationsExpired int      `json:"conversations_expired"`
	ExpiredIDs           []string `json:"expired_ids"`
	// PinnedRequests are details kept for recall by active conversations.
	PinnedRequests int `json:"pinned_requests"`
	// ExpiredState counts what each feature deleted, by feature.
	ExpiredState map[string]int `json:"expired_state"`
	Purge        PurgeReport    `json:"purge"`
	Errors       []string       `json:"errors,omitempty"`
}

// Janitor applies the storage settings on startup, hourly, and on demand.
type Janitor struct {
	cfg     *config.Store
	st      *Store
	sources []ConversationSource
	now     func() time.Time
	grace   time.Duration

	// run makes runs exclusive; the store's own lock orders them against Save.
	run  sync.Mutex
	mu   sync.Mutex
	last *JanitorReport
}

// NewJanitor wires the janitor. The store is always a source (its log is
// the primary sign of activity); features add theirs.
func NewJanitor(cfg *config.Store, st *Store, sources ...ConversationSource) *Janitor {
	all := append([]ConversationSource{st}, sources...)
	return &Janitor{cfg: cfg, st: st, sources: all, now: time.Now, grace: Grace}
}

// Start runs the janitor now and then every JanitorInterval, in the
// background. The returned function stops it.
func (j *Janitor) Start() (stop func()) {
	done := make(chan struct{})
	go func() {
		j.logRun(j.Run(false, "startup"))
		t := time.NewTicker(JanitorInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				j.logRun(j.Run(false, "schedule"))
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// convPlan is the conversations' state at one moment.
type convPlan struct {
	active  int
	expired []string
	// pinned are the request ids active conversations point at, and
	// pinnedConvs how many conversations pin at least one.
	pinned      map[string]bool
	pinnedConvs int
}

// plan gathers every source's conversations: the newest activity any
// source knows of decides whether a conversation is active.
func (j *Janitor) plan(now time.Time, ttl time.Duration) convPlan {
	last := map[string]time.Time{}
	pins := map[string][]string{}
	for _, src := range j.sources {
		for _, c := range src.StorageConversations() {
			if c.ID == "" {
				continue
			}
			if t, ok := last[c.ID]; !ok || c.LastActive.After(t) {
				last[c.ID] = c.LastActive
			}
			pins[c.ID] = append(pins[c.ID], c.Pins...)
		}
	}
	cutoff := now.Add(-ttl)
	p := convPlan{pinned: map[string]bool{}}
	for id, t := range last {
		if t.Before(cutoff) {
			p.expired = append(p.expired, id)
			continue
		}
		p.active++
		if len(pins[id]) > 0 {
			p.pinnedConvs++
		}
		for _, r := range pins[id] {
			p.pinned[r] = true
		}
	}
	sort.Strings(p.expired)
	return p
}

// Pinned counts the active conversations that pin request bodies, and
// those requests.
func (j *Janitor) Pinned() (conversations, requests int) {
	p := j.plan(j.now(), time.Duration(SettingsFrom(j.cfg.Get()).ConversationTTL))
	return p.pinnedConvs, len(p.pinned)
}

// Run expires idle conversations and purges the store. With dryRun nothing
// is deleted and the report says what would be.
func (j *Janitor) Run(dryRun bool, trigger string) *JanitorReport {
	j.run.Lock()
	defer j.run.Unlock()
	start := time.Now()
	now := j.now()
	set := SettingsFrom(j.cfg.Get())
	rep := &JanitorReport{Time: now.UTC(), DryRun: dryRun, Trigger: trigger, ExpiredState: map[string]int{}, ExpiredIDs: []string{}}

	ttl := time.Duration(set.ConversationTTL)
	cp := j.plan(now, ttl)
	expired, pinned := cp.expired, cp.pinned
	rep.ConversationsActive, rep.ConversationsExpired, rep.PinnedRequests = cp.active, len(expired), len(pinned)
	rep.ExpiredIDs = append(rep.ExpiredIDs, expired...)
	if len(rep.ExpiredIDs) > maxReportedDetails {
		rep.ExpiredIDs = rep.ExpiredIDs[:maxReportedDetails]
	}
	if !dryRun {
		for _, src := range j.sources {
			n, err := src.ExpireConversations(expired, now.Add(-ttl))
			if n > 0 {
				rep.ExpiredState[src.StorageName()] = n
			}
			if err != nil {
				rep.Errors = append(rep.Errors, src.StorageName()+": "+err.Error())
			}
		}
	}

	pr, err := j.st.Purge(PurgeOptions{Now: now, Settings: set, Pinned: pinned, Grace: j.grace, DryRun: dryRun})
	if err != nil {
		rep.Errors = append(rep.Errors, "purge: "+err.Error())
	}
	rep.Purge = pr
	rep.DurationMs = time.Since(start).Milliseconds()
	if !dryRun {
		j.mu.Lock()
		j.last = rep
		j.mu.Unlock()
	}
	return rep
}

// Last is the last run that deleted (not a dry run), or nil.
func (j *Janitor) Last() *JanitorReport {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.last
}

func (j *Janitor) logRun(r *JanitorReport) {
	p := r.Purge
	log.Printf("storage: janitor (%s): deleted %d details (%s; %d by age, pinned kept %d), %d blobs (%s), %d index files; "+
		"expired %d idle conversations %v; %d active conversations pin %d requests; %s -> %s in %d ms",
		r.Trigger, p.DetailsDeleted, FormatBytes(p.DetailBytes), p.DetailsByAge, p.PinnedKept,
		p.BlobsDeleted, FormatBytes(p.BlobBytes), len(p.IndexFiles), r.ConversationsExpired, r.ExpiredState,
		r.ConversationsActive, r.PinnedRequests, FormatBytes(p.TotalBytesBefore), FormatBytes(p.TotalBytesAfter), r.DurationMs)
	for _, w := range p.Warnings {
		log.Printf("storage: janitor: %s", w)
	}
	for _, e := range r.Errors {
		log.Printf("storage: janitor: %s", e)
	}
}

// --- the store as a conversation source ---

func (s *Store) StorageName() string { return "log" }

// StorageConversations lists conversations by their newest request.
func (s *Store) StorageConversations() []Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Conversation, 0, len(s.lastSeen))
	for id, t := range s.lastSeen {
		out = append(out, Conversation{ID: id, LastActive: t})
	}
	return out
}

// ExpireConversations forgets the activity of idle conversations; their
// requests age out through detail_max_age.
func (s *Store) ExpireConversations(ids []string, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if t, ok := s.lastSeen[id]; ok && t.Before(before) {
			delete(s.lastSeen, id)
		}
	}
	return 0, nil
}
