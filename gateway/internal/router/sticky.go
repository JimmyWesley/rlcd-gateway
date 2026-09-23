package router

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Assignment pins a conversation to the decision made on its first turn.
type Assignment struct {
	ConversationID string `json:"conversation_id"`
	// Route is empty when no rule matched at the start: the conversation
	// then follows the active route, and later turns do not start matching
	// rules mid-conversation.
	Route    string    `json:"route"`
	Rule     string    `json:"rule,omitempty"`
	Reason   string    `json:"reason"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Turns    int       `json:"turns"`
}

// stickyStore keeps assignments in memory and in one JSON file. It is small
// (one entry per live conversation) and rewritten atomically on change.
type stickyStore struct {
	mu      sync.Mutex
	path    string
	m       map[string]*Assignment
	savedAt time.Time
	now     func() time.Time
}

// touchPersistEvery bounds how often a mere LastSeen update hits the disk.
// Losing a few minutes of LastSeen on a crash only shortens an expiry.
const touchPersistEvery = 5 * time.Minute

func openSticky(dir string) *stickyStore {
	s := &stickyStore{path: filepath.Join(dir, "sticky.json"), m: map[string]*Assignment{}, now: time.Now}
	b, err := os.ReadFile(s.path)
	if err == nil {
		var list []*Assignment
		if err := json.Unmarshal(b, &list); err != nil {
			log.Printf("router: %s: %v (starting with no sticky assignments)", s.path, err)
		}
		for _, a := range list {
			if a != nil && a.ConversationID != "" {
				s.m[a.ConversationID] = a
			}
		}
	}
	return s
}

func (s *stickyStore) expired(a *Assignment, ttl time.Duration) bool {
	return ttl > 0 && s.now().Sub(a.LastSeen) > ttl
}

// get returns a copy of the live assignment, dropping it when expired.
func (s *stickyStore) get(id string, ttl time.Duration) *Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[id]
	if a == nil {
		return nil
	}
	if s.expired(a, ttl) {
		delete(s.m, id)
		s.saveLocked()
		return nil
	}
	c := *a
	return &c
}

// touch records another turn served by the assignment.
func (s *stickyStore) touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.m[id]; a != nil {
		a.LastSeen = s.now()
		a.Turns++
		if s.now().Sub(s.savedAt) > touchPersistEvery {
			s.saveLocked()
		}
	}
}

// put stores a new assignment. With replace=false an existing one wins, so
// two first turns racing each other settle on whichever landed first.
func (s *stickyStore) put(a Assignment, replace bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.m[a.ConversationID]; cur != nil && !replace {
		return
	}
	now := s.now()
	if a.Created.IsZero() {
		a.Created = now
	}
	a.LastSeen, a.Turns = now, 1
	s.m[a.ConversationID] = &a
	s.saveLocked()
}

func (s *stickyStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return false
	}
	delete(s.m, id)
	s.saveLocked()
	return true
}

func (s *stickyStore) clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.m)
	s.m = map[string]*Assignment{}
	s.saveLocked()
	return n
}

// dropRoute forgets assignments to a deleted route.
func (s *stickyStore) dropRoute(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, a := range s.m {
		if a.Route == route {
			delete(s.m, id)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

// list returns live assignments, most recently seen first.
func (s *stickyStore) list(ttl time.Duration) []Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Assignment{}
	expired := false
	for id, a := range s.m {
		if s.expired(a, ttl) {
			delete(s.m, id)
			expired = true
			continue
		}
		out = append(out, *a)
	}
	if expired {
		s.saveLocked()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (s *stickyStore) saveLocked() {
	list := make([]*Assignment, 0, len(s.m))
	for _, a := range s.m {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ConversationID < list[j].ConversationID })
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		log.Printf("router: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("router: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("router: %v", err)
		return
	}
	s.savedAt = s.now()
}

// expire deletes the pins in ids that were last seen before before.
func (s *stickyStore) expire(ids []string, before time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range ids {
		if a := s.m[id]; a != nil && a.LastSeen.Before(before) {
			delete(s.m, id)
			n++
		}
	}
	if n > 0 {
		s.saveLocked()
	}
	return n
}

// all returns every pin, expired or not.
func (s *stickyStore) all() []Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Assignment, 0, len(s.m))
	for _, a := range s.m {
		out = append(out, *a)
	}
	return out
}

// --- storage ---

// StorageName names the router in the janitor's reports.
func (r *Router) StorageName() string { return "router" }

// StorageConversations lists pinned conversations by when they were last
// seen. Pins hold no request bodies.
func (r *Router) StorageConversations() []store.Conversation {
	list := r.sticky.all()
	out := make([]store.Conversation, 0, len(list))
	for _, a := range list {
		out = append(out, store.Conversation{ID: a.ConversationID, LastActive: a.LastSeen})
	}
	return out
}

// ExpireConversations drops the pins of idle conversations.
func (r *Router) ExpireConversations(ids []string, before time.Time) (int, error) {
	return r.sticky.expire(ids, before), nil
}
