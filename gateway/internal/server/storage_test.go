package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bodies policy applies to proxied calls, and the storage API is
// mounted behind the guard.
func TestStorageThroughGateway(t *testing.T) {
	e := newEnv(t, false)
	hdr := map[string]string{"Authorization": "Bearer sk-ant-oat01-subscription", "Anthropic-Version": "2023-06-01"}

	// full (the default): the bodies are kept, and read back equal.
	if resp, _ := e.do("POST", "/v1/messages", anthReq, hdr); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	d := e.lastRecord()
	if d.RequestBody != anthReq || d.ResponseBody == "" {
		t.Fatalf("full: %q", d.RequestBody)
	}
	if _, err := os.Stat(filepath.Join(e.home, "requests", d.ID+".json.gz")); err != nil {
		t.Fatalf("new record format: %v", err)
	}

	resp, out := e.do("PUT", "/api/storage/settings", `{"bodies":"errors_only"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(out, `"effective_bodies":"errors_only"`) {
		t.Fatalf("PUT settings: %d %s", resp.StatusCode, out)
	}
	if resp, _ := e.do("POST", "/v1/messages", anthReq, hdr); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if d := e.lastRecord(); d.RequestBody != "" || d.ResponseBody != "" || d.XRay == nil || d.Usage == nil {
		t.Fatalf("errors_only kept a successful call's bodies, or lost its summary: %+v", d.Record)
	}

	resp, out = e.do("GET", "/api/storage", "", nil)
	var view struct {
		Counts struct {
			Records int `json:"records"`
		} `json:"counts"`
		Settings struct {
			Bodies string `json:"bodies"`
		} `json:"settings"`
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(out), &view) != nil || view.Counts.Records != 2 || view.Settings.Bodies != "errors_only" {
		t.Fatalf("GET /api/storage: %d %s", resp.StatusCode, out)
	}
	resp, out = e.do("POST", "/api/storage/purge", `{"dry_run":true}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("purge: %d %s", resp.StatusCode, out)
	}
	// A state-changing call from another site's page is refused.
	if resp, _ := e.do("POST", "/api/storage/purge", `{}`, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != 403 {
		t.Errorf("foreign origin: %d", resp.StatusCode)
	}
}
