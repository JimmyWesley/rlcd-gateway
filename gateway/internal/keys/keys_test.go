package keys

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeysAreHashedAndShownOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, v, err := s.Create("app", Limits{Aliases: []string{"smart", " smart ", ""}, RPM: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, Prefix) || len(key) != len(Prefix)+64 || !strings.HasPrefix(key, strings.TrimSuffix(v.Hint, "…")) {
		t.Fatalf("key %q hint %q", key, v.Hint)
	}
	if len(v.Aliases) != 1 {
		t.Errorf("aliases not cleaned: %v", v.Aliases)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if strings.Contains(string(b), key) || !strings.Contains(string(b), hash(key)) {
		t.Fatal("keys.json must hold the hash, never the key")
	}
	for _, view := range s.List() {
		if strings.Contains(strings.Join([]string{view.ID, view.Name, view.Hint}, " "), key[len(Prefix)+6:]) {
			t.Fatal("a view leaks the key")
		}
	}

	// Another process (the CLI) creates a key; the running store sees it.
	cli, _ := Open(dir)
	time.Sleep(10 * time.Millisecond)
	key2, _, err := cli.Create("cli", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+key2)
	id, found, err := s.Authenticate(h)
	if err != nil || !found || id.Name != "cli" || !id.Stripped || h.Get("Authorization") != "" {
		t.Fatalf("CLI key not picked up: %+v %v %v %v", id, found, err, h)
	}
	// X-Rlcd-Key leaves the client's own login in place.
	h = http.Header{}
	h.Set(Header, key)
	h.Set("Authorization", "Bearer sk-ant-oat01-own")
	id, _, err = s.Authenticate(h)
	if err != nil || id.Stripped || h.Get("Authorization") == "" || h.Get(Header) != "" {
		t.Fatalf("X-Rlcd-Key: %+v %v %v", id, err, h)
	}
	// A wrong key is stripped too, and refused.
	h = http.Header{}
	h.Set("X-Api-Key", "rlcd-nope")
	if _, found, err := s.Authenticate(h); !found || err != ErrInvalid || h.Get("X-Api-Key") != "" {
		t.Fatalf("wrong key: %v %v", found, err)
	}
	// Provider keys are not ours to judge.
	h = http.Header{}
	h.Set("Authorization", "Bearer sk-proj-123")
	if _, found, _ := s.Authenticate(h); found || h.Get("Authorization") == "" {
		t.Fatal("a provider key was taken for a gateway key")
	}
}

func TestDailyQuotaResets(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Date(2026, 9, 23, 23, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	_, v, _ := s.Create("q", Limits{TokensPerDay: 10})
	if s.CheckQuota(v.ID) != nil {
		t.Fatal("fresh key over quota")
	}
	s.Record(v.ID, nil, 0, false)
	s.mu.Lock()
	s.findLocked(v.ID).DayTokens = 50
	s.mu.Unlock()
	if s.CheckQuota(v.ID) != ErrQuota {
		t.Fatal("quota not enforced")
	}
	now = now.Add(2 * time.Hour) // the next UTC day
	if s.CheckQuota(v.ID) != nil {
		t.Fatal("quota did not reset at midnight UTC")
	}
}

func TestViewAlwaysListsAliasesAndRoutes(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, v, _ := s.Create("plain", Limits{})
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"aliases":[]`) || !strings.Contains(string(b), `"routes":[]`) {
		t.Fatalf("the dashboard expects both lists: %s", b)
	}
}
