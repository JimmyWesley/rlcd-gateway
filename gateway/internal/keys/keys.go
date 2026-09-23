// Package keys manages gateway API keys ("virtual keys").
//
// A gateway key lets an app call the gateway without ever holding a
// provider key: the app sends "rlcd-…" where it would send an OpenAI or
// Anthropic key, the gateway checks it, removes it, and the route injects
// its own provider key upstream.
//
// Keys are 32 random bytes, so they are stored as a SHA-256 hash (a slow
// password hash buys nothing against a key that cannot be guessed). The
// plaintext is returned once, by Create, and never again. keys.json holds
// hashes, limits and usage counters; it is re-read when it changes on disk,
// so `rlcd-gateway keys create` works while the gateway runs.
//
// Each key can be limited to some aliases and routes, to a number of
// requests per minute (enforced before forwarding) and to a number of tokens
// per UTC day (checked before forwarding against what earlier calls used,
// so the call that crosses the limit still completes).
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Prefix starts every gateway key.
const Prefix = "rlcd-"

// Header carries a gateway key next to the client's own provider login, so
// a passthrough route (a Claude subscription, say) can still be used.
const Header = "X-Rlcd-Key"

// Totals is a key's usage.
type Totals struct {
	Requests            int     `json:"requests"`
	Errors              int     `json:"errors"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationTokens int     `json:"cache_creation_input_tokens"`
	CostUSD             float64 `json:"est_cost_usd"`
}

// Key is one stored key. Hash never leaves the package: see View.
type Key struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Hint    string     `json:"hint"` // "rlcd-3f9a…", enough to recognise a key
	Hash    string     `json:"hash"`
	Created time.Time  `json:"created"`
	Revoked *time.Time `json:"revoked,omitempty"`
	Limits
	LastUsed *time.Time `json:"last_used,omitempty"`
	Usage    Totals     `json:"usage"`
	// Day is the UTC date DayTokens counts.
	Day       string `json:"day,omitempty"`
	DayTokens int    `json:"day_tokens,omitempty"`
}

// Limits restrict what a key may do. Zero values mean "no limit".
type Limits struct {
	// Aliases the key may ask for; empty allows any model name.
	Aliases []string `json:"aliases,omitempty"`
	// Routes the key's requests may be served by; empty allows any.
	Routes []string `json:"routes,omitempty"`
	// RPM is requests per minute.
	RPM int `json:"rpm,omitempty"`
	// TokensPerDay caps input + output + cache tokens per UTC day.
	TokensPerDay int `json:"tokens_per_day,omitempty"`
}

// View is what the dashboard sees of a key.
type View struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Hint     string     `json:"hint"`
	Created  time.Time  `json:"created"`
	Revoked  *time.Time `json:"revoked,omitempty"`
	LastUsed *time.Time `json:"last_used,omitempty"`
	Limits
	Usage     Totals `json:"usage"`
	TodayUsed int    `json:"today_tokens"`
}

func (k *Key) view(today string) View {
	v := View{ID: k.ID, Name: k.Name, Hint: k.Hint, Created: k.Created, Revoked: k.Revoked,
		LastUsed: k.LastUsed, Limits: k.Limits, Usage: k.Usage}
	if k.Day == today {
		v.TodayUsed = k.DayTokens
	}
	if v.Aliases == nil {
		v.Aliases = []string{}
	}
	if v.Routes == nil {
		v.Routes = []string{}
	}
	return v
}

// Identity is an authenticated caller, attached to the request context.
type Identity struct {
	ID   string
	Name string
	Limits
	// Stripped is true when the key came in the provider credential header
	// (Authorization, x-api-key, api-key): the client then sent no login of
	// its own, so a passthrough route has nothing to forward.
	Stripped bool
}

// AllowsAlias reports whether the key may use alias ("" = no alias matched).
func (id *Identity) AllowsAlias(alias string) bool {
	return len(id.Aliases) == 0 || (alias != "" && contains(id.Aliases, alias))
}

// AllowsRoute reports whether the key's requests may go to route.
func (id *Identity) AllowsRoute(route string) bool {
	return len(id.Routes) == 0 || contains(id.Routes, route)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the caller's identity, or nil for a request made
// without a gateway key.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKey{}).(*Identity)
	return id
}

// Errors returned by Authenticate and Admit. Their text is shown to the
// client, so it never contains the key.
var (
	ErrMissing     = errors.New("a gateway key is required: send it as the API key (Authorization: Bearer rlcd-…, x-api-key or X-Rlcd-Key)")
	ErrInvalid     = errors.New("invalid gateway key")
	ErrRevoked     = errors.New("this gateway key has been revoked")
	ErrRateLimited = errors.New("gateway key rate limit reached (requests per minute)")
	ErrQuota       = errors.New("gateway key daily token limit reached")
)

// Store keeps the keys in <dir>/keys.json.
type Store struct {
	path string
	mu   sync.Mutex
	keys []*Key
	mod  time.Time
	// recent request times per key id, for the per-minute limit.
	hits map[string][]time.Time
	now  func() time.Time
}

// Open loads the keys (an absent file means no keys).
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "keys.json"), hits: map[string][]time.Time{}, now: time.Now}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s, s.reloadLocked(true)
}

// reloadLocked re-reads the file when it changed on disk.
func (s *Store) reloadLocked(force bool) error {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if force || s.mod != (time.Time{}) {
			s.keys, s.mod = nil, time.Time{}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !force && fi.ModTime().Equal(s.mod) {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var keys []*Key
	if err := json.Unmarshal(b, &keys); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.keys, s.mod = keys, fi.ModTime()
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.mod = fi.ModTime()
	}
	return nil
}

// sync picks up edits made by another process (the keys CLI). On a read
// error the keys already loaded stay in force.
func (s *Store) sync() { _ = s.reloadLocked(false) }

// hash is the stored form of a key.
func hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("keys: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Create stores a new key and returns it in plaintext, the only time it is
// ever available.
func (s *Store) Create(name string, lim Limits) (string, View, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", View{}, errors.New("a key needs a name (at most 64 characters)")
	}
	if err := lim.validate(); err != nil {
		return "", View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	secret := Prefix + randomHex(32)
	k := &Key{ID: "key_" + randomHex(6), Name: name, Hint: secret[:len(Prefix)+6] + "…",
		Hash: hash(secret), Created: s.now().UTC(), Limits: lim.clean()}
	s.keys = append(s.keys, k)
	if err := s.saveLocked(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		return "", View{}, err
	}
	return secret, k.view(s.today()), nil
}

func (l Limits) validate() error {
	if l.RPM < 0 || l.TokensPerDay < 0 {
		return errors.New("limits cannot be negative")
	}
	return nil
}

func (l Limits) clean() Limits {
	trim := func(in []string) []string {
		var out []string
		for _, v := range in {
			if v = strings.TrimSpace(v); v != "" && !contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	l.Aliases, l.Routes = trim(l.Aliases), trim(l.Routes)
	return l
}

// Update replaces a key's name and limits.
func (s *Store) Update(id, name string, lim Limits) (View, error) {
	if err := lim.validate(); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	k := s.findLocked(id)
	if k == nil {
		return View{}, os.ErrNotExist
	}
	if n := strings.TrimSpace(name); n != "" {
		k.Name = n
	}
	k.Limits = lim.clean()
	return k.view(s.today()), s.saveLocked()
}

// Revoke disables a key for good. Its usage history stays.
func (s *Store) Revoke(id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	k := s.findLocked(id)
	if k == nil {
		return View{}, os.ErrNotExist
	}
	if k.Revoked == nil {
		t := s.now().UTC()
		k.Revoked = &t
	}
	return k.view(s.today()), s.saveLocked()
}

// List returns every key, newest first.
func (s *Store) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	out := make([]View, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.view(s.today()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Active reports how many keys are not revoked.
func (s *Store) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	n := 0
	for _, k := range s.keys {
		if k.Revoked == nil {
			n++
		}
	}
	return n
}

func (s *Store) findLocked(id string) *Key {
	for _, k := range s.keys {
		if k.ID == id {
			return k
		}
	}
	return nil
}

func (s *Store) today() string { return s.now().UTC().Format("2006-01-02") }

// credentialHeaders are where a client may put a gateway key, in order.
// X-Rlcd-Key leaves the client's own login in place; the others are the
// provider credential headers the SDKs fill from their api_key setting.
var credentialHeaders = []string{Header, "Authorization", "X-Api-Key", "Api-Key"}

// Extract finds a gateway key in the request headers. header names where it
// was found; ok is false when the request carries none.
func Extract(h http.Header) (key, header string, ok bool) {
	for _, name := range credentialHeaders {
		v := strings.TrimSpace(h.Get(name))
		if name == "Authorization" {
			scheme, rest, found := strings.Cut(v, " ")
			if !found || !strings.EqualFold(scheme, "Bearer") {
				continue
			}
			v = strings.TrimSpace(rest)
		}
		if strings.HasPrefix(v, Prefix) {
			return v, name, true
		}
	}
	return "", "", false
}

// Authenticate checks the gateway key a request carries. On success it
// removes the key from h, so it is never forwarded or logged, and returns
// the caller's identity. found is false when the request has no key at all.
// It also applies the per-minute limit.
func (s *Store) Authenticate(h http.Header) (id *Identity, found bool, err error) {
	key, header, ok := Extract(h)
	if !ok {
		return nil, false, nil
	}
	// Whatever happens next, the key must not travel further.
	h.Del(header)
	sum := hash(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	var k *Key
	for _, c := range s.keys {
		if c.Hash == sum {
			k = c
			break
		}
	}
	switch {
	case k == nil:
		return nil, true, ErrInvalid
	case k.Revoked != nil:
		return nil, true, ErrRevoked
	}
	if k.RPM > 0 {
		now := s.now()
		cut := now.Add(-time.Minute)
		hits := s.hits[k.ID][:0:0]
		for _, t := range s.hits[k.ID] {
			if t.After(cut) {
				hits = append(hits, t)
			}
		}
		if len(hits) >= k.RPM {
			s.hits[k.ID] = hits
			return &Identity{ID: k.ID, Name: k.Name}, true, ErrRateLimited
		}
		s.hits[k.ID] = append(hits, now)
	}
	return &Identity{ID: k.ID, Name: k.Name, Limits: k.Limits, Stripped: header != Header}, true, nil
}

// RetryAfter is how many seconds until key id may send again under its
// per-minute limit.
func (s *Store) RetryAfter(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	hits := s.hits[id]
	if len(hits) == 0 {
		return 1
	}
	if secs := int(hits[0].Add(time.Minute).Sub(s.now()).Seconds()) + 1; secs > 1 {
		return secs
	}
	return 1
}

// CheckQuota returns ErrQuota when key id has used its tokens for today.
func (s *Store) CheckQuota(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	k := s.findLocked(id)
	if k == nil || k.TokensPerDay <= 0 {
		return nil
	}
	if k.Day == s.today() && k.DayTokens >= k.TokensPerDay {
		return ErrQuota
	}
	return nil
}

// Record adds one finished call to key id's usage.
func (s *Store) Record(id string, u *store.Usage, costUSD float64, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	k := s.findLocked(id)
	if k == nil {
		return
	}
	now := s.now().UTC()
	k.LastUsed = &now
	k.Usage.Requests++
	if failed {
		k.Usage.Errors++
	}
	if day := s.today(); k.Day != day {
		k.Day, k.DayTokens = day, 0
	}
	if u != nil {
		k.Usage.InputTokens += u.InputTokens
		k.Usage.OutputTokens += u.OutputTokens
		k.Usage.CacheReadTokens += u.CacheReadTokens
		k.Usage.CacheCreationTokens += u.CacheCreationTokens
		k.DayTokens += u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens
	}
	k.Usage.CostUSD = float64(int64((k.Usage.CostUSD+costUSD)*1e6+0.5)) / 1e6
	_ = s.saveLocked()
}
