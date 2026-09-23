package clients

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Records saved before client detection get their client, provider and
// model vendor back from their stored headers, in the current format, and
// a second run changes nothing.
func TestBackfill(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	body := `{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`

	// 1. Current format, no client: a Claude Code call.
	old := &store.Detail{Record: store.Record{ID: "20260803T100000-0000000000000001", Time: when, Route: "claude-sub",
		Upstream: "https://api.anthropic.com", Model: "claude-sonnet-4-5", Status: 200},
		RequestHeaders: map[string]string{"User-Agent": "claude-cli/2.1.280 (external, cli)", "Authorization": "Bearer sk-ant-oat01-…(108 chars)"},
		RequestBody:    body}
	must(t, st.Save(old))

	// 2. The legacy format (requests/<id>.json and index.jsonl): an OpenAI SDK.
	legacy := store.Detail{Record: store.Record{ID: "20260803T100100-0000000000000002", Time: when.Add(time.Minute),
		Route: "groq", Upstream: "https://api.groq.com/openai/v1", Model: "llama-3.3-70b-versatile", Protocol: "openai-chat", Status: 200},
		RequestHeaders: map[string]string{"User-Agent": "OpenAI/Python 1.51.0", "X-Stainless-Lang": "python",
			"X-Stainless-Package-Version": "1.51.0"}}
	b, _ := json.Marshal(legacy)
	legacyPath := filepath.Join(home, "requests", legacy.ID+".json")
	must(t, os.WriteFile(legacyPath, b, 0o600))
	past := when.Add(time.Hour)
	must(t, os.Chtimes(legacyPath, past, past))
	line, _ := json.Marshal(legacy.Record)
	must(t, os.WriteFile(filepath.Join(home, "index.jsonl"), append(line, '\n'), 0o600))

	// 3. A summary whose detail retention deleted: no headers, no client.
	gone := store.Record{ID: "20260803T100200-0000000000000003", Time: when.Add(2 * time.Minute), Route: "or",
		Upstream: "https://openrouter.ai/api/v1", Model: "qwen/qwen3-coder", Status: 200}
	line, _ = json.Marshal(gone)
	f, err := os.OpenFile(filepath.Join(home, "index-2026-08.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	_, _ = f.Write(append(line, '\n'))
	f.Close()

	// 4. Already complete: untouched.
	done := &store.Detail{Record: store.Record{ID: "20260803T100300-0000000000000004", Time: when.Add(3 * time.Minute),
		Client: &store.Client{ID: "codex", Name: "Codex", Kind: KindAgent}, Provider: "openai", ModelVendor: "openai",
		Upstream: "https://api.openai.com/v1", Model: "gpt-5", Status: 200}, RequestHeaders: map[string]string{}}
	must(t, st.Save(done))

	cfg := *config.Default()
	st, err = store.Open(home) // as the CLI does, from disk
	must(t, err)
	dry, err := st.Backfill(Backfill(cfg), true)
	must(t, err)
	if dry.DetailsUpdated != 2 || dry.SummariesUpdated != 3 {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatal("the dry run changed files")
	}
	rep, err := st.Backfill(Backfill(cfg), false)
	must(t, err)
	if rep.DetailsScanned != 3 || rep.DetailsUpdated != 2 || rep.SummariesUpdated != 3 || len(rep.IndexFiles) != 2 {
		t.Fatalf("report: %+v", rep)
	}

	d, err := st.Get(old.ID)
	must(t, err)
	if d.Client == nil || d.Client.ID != "claude-code" || d.Provider != "anthropic" || d.ModelVendor != "anthropic" || d.RequestBody != body {
		t.Errorf("record 1: %+v %+v", d.Record, d.Client)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy file kept: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, "requests", legacy.ID+".json.gz"))
	must(t, err)
	if !info.ModTime().Equal(past) {
		t.Errorf("modification time not kept: %s", info.ModTime())
	}
	d, err = st.Get(legacy.ID)
	must(t, err)
	if d.Client == nil || d.Client.ID != "openai-python" || d.Provider != "groq" || d.ModelVendor != "meta" {
		t.Errorf("record 2: %+v %+v", d.Record, d.Client)
	}

	// The summaries, read back from the index files.
	st, err = store.Open(home)
	must(t, err)
	byID := map[string]store.Record{}
	for _, r := range st.Recent() {
		byID[r.ID] = r
	}
	if r := byID[old.ID]; r.Client == nil || r.Client.ID != "claude-code" || r.Provider != "anthropic" {
		t.Errorf("summary 1: %+v", r)
	}
	if r := byID[legacy.ID]; r.Client == nil || r.Client.ID != "openai-python" || r.Provider != "groq" {
		t.Errorf("summary 2: %+v", r)
	}
	if r := byID[gone.ID]; r.Client != nil || r.Provider != "openrouter" || r.ModelVendor != "qwen" {
		t.Errorf("summary 3: %+v", r)
	}
	if r := byID[done.ID]; r.Client.ID != "codex" || r.Provider != "openai" {
		t.Errorf("summary 4 changed: %+v", r)
	}
	if len(byID) != 4 {
		t.Errorf("summaries: %d", len(byID))
	}

	// Idempotent.
	again, err := st.Backfill(Backfill(cfg), false)
	must(t, err)
	if again.DetailsUpdated != 0 || again.SummariesUpdated != 0 || len(again.IndexFiles) != 0 {
		t.Errorf("second run: %+v", again)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
