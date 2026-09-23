package resilience

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Limits are what a model accepts, in tokens. 0 means unknown.
type Limits struct {
	ContextWindow   int `json:"context_window,omitempty"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Source names where the values came from: builtin, openrouter,
	// learned, override:model, override:route, override:alias.
	Source string `json:"source,omitempty"`
}

// Known reports whether anything is known.
func (l Limits) Known() bool { return l.ContextWindow > 0 || l.MaxOutputTokens > 0 }

// Limit sources.
const (
	SourceBuiltin    = "builtin"
	SourceOpenRouter = "openrouter"
	SourceLearned    = "learned"
	SourceModel      = "override:model"
	SourceRoute      = "override:route"
	SourceAlias      = "override:alias"
	SourceError      = "error"
)

// BuiltinAsOf dates the built-in table.
const BuiltinAsOf = "2026-09"

type builtin struct {
	window, maxOut int
	// window1M is the window with Anthropic's context-1m beta header.
	window1M int
}

// builtins are first-party model families. A key matches the model id
// exactly or followed by a date or "-latest" only: a newer family
// ("claude-opus-4-7") is unknown rather than guessed from its prefix, so it
// is never clamped to an older model's limits.
var builtins = map[string]builtin{
	"claude-opus-4-5":   {200000, 64000, 0},
	"claude-opus-4-1":   {200000, 32000, 0},
	"claude-opus-4-0":   {200000, 32000, 0},
	"claude-opus-4":     {200000, 32000, 0},
	"claude-sonnet-4-5": {200000, 64000, 1000000},
	"claude-sonnet-4-0": {200000, 64000, 1000000},
	"claude-sonnet-4":   {200000, 64000, 1000000},
	"claude-haiku-4-5":  {200000, 64000, 0},
	"claude-3-7-sonnet": {200000, 64000, 0},
	"claude-3-5-sonnet": {200000, 8192, 0},
	"claude-3-5-haiku":  {200000, 8192, 0},
	"claude-3-opus":     {200000, 4096, 0},
	"claude-3-haiku":    {200000, 4096, 0},
	"gpt-4.1":           {1047576, 32768, 0},
	"gpt-4.1-mini":      {1047576, 32768, 0},
	"gpt-4.1-nano":      {1047576, 32768, 0},
	"gpt-4o":            {128000, 16384, 0},
	"gpt-4o-mini":       {128000, 16384, 0},
	"o1":                {200000, 100000, 0},
	"o3":                {200000, 100000, 0},
	"o3-mini":           {200000, 100000, 0},
	"o4-mini":           {200000, 100000, 0},
	"gpt-5":             {400000, 128000, 0},
	"gpt-5-mini":        {400000, 128000, 0},
	"gpt-5-nano":        {400000, 128000, 0},
	"gpt-5-codex":       {400000, 128000, 0},
}

var dateSuffix = regexp.MustCompile(`^(-\d{8}|-\d{4}-\d{2}-\d{2}|-latest)?$`)

// builtinFor finds a model in the built-in table. OpenRouter-style ids
// ("anthropic/claude-sonnet-4.5") are normalized first.
func builtinFor(model string, beta1M bool) (Limits, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if org, rest, ok := strings.Cut(m, "/"); ok {
		if org != "anthropic" && org != "openai" {
			return Limits{}, false
		}
		m = rest
		if strings.HasPrefix(m, "claude-") {
			m = strings.ReplaceAll(m, ".", "-")
		}
	}
	best, bestKey := builtin{}, ""
	for k, b := range builtins {
		if strings.HasPrefix(m, k) && dateSuffix.MatchString(m[len(k):]) && len(k) > len(bestKey) {
			best, bestKey = b, k
		}
	}
	if bestKey == "" {
		return Limits{}, false
	}
	l := Limits{ContextWindow: best.window, MaxOutputTokens: best.maxOut, Source: SourceBuiltin}
	if beta1M && best.window1M > 0 {
		l.ContextWindow = best.window1M
	}
	return l, true
}

// Registry knows model limits: the built-in table, OpenRouter's public
// model list (cached on disk, refreshed in the background), and limits
// learned from provider errors. Lookups never block on the network.
type Registry struct {
	dir    string
	url    string
	client *http.Client

	mu        sync.RWMutex
	catalog   map[string]Limits
	fetchedAt time.Time
	lastErr   string
	learned   map[string]learned
	ttl       time.Duration
	fetching  bool
	now       func() time.Time
}

type learned struct {
	Limits
	At time.Time
}

// OpenRouterModelsURL is OpenRouter's public model list (no key needed).
const OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"

// DefaultCatalogTTL is how long the cached OpenRouter list is fresh.
const DefaultCatalogTTL = 24 * time.Hour

// learnedTTL is how long a limit read from a provider error is trusted.
const learnedTTL = 24 * time.Hour

// NewRegistry loads the cached catalog from dir (<home>/resilience) and
// fetches nothing: call Start for the background refresh.
func NewRegistry(dir string) *Registry {
	r := &Registry{dir: dir, url: OpenRouterModelsURL, client: &http.Client{Timeout: 30 * time.Second},
		catalog: map[string]Limits{}, learned: map[string]learned{}, ttl: DefaultCatalogTTL, now: time.Now}
	r.loadCache()
	return r
}

// SetURL points the catalog fetch elsewhere (tests).
func (r *Registry) SetURL(u string) { r.url = u }

// SetTTL changes the catalog's freshness.
func (r *Registry) SetTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.ttl = d
	r.mu.Unlock()
}

func (r *Registry) cachePath() string { return filepath.Join(r.dir, "openrouter-models.json") }

type catalogFile struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Source    string            `json:"source"`
	Models    map[string]Limits `json:"models"`
}

func (r *Registry) loadCache() {
	b, err := os.ReadFile(r.cachePath())
	if err != nil {
		return
	}
	var f catalogFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("resilience: %s: %v (ignored)", r.cachePath(), err)
		return
	}
	r.catalog, r.fetchedAt = f.Models, f.FetchedAt
	if r.catalog == nil {
		r.catalog = map[string]Limits{}
	}
}

// Start refreshes the catalog in the background whenever it is stale,
// until ctx ends.
func (r *Registry) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if r.stale() {
				if err := r.Refresh(ctx); err != nil {
					log.Printf("resilience: OpenRouter model list: %v", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (r *Registry) stale() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.now().Sub(r.fetchedAt) > r.ttl
}

// Refresh fetches the OpenRouter model list now and caches it on disk.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	if r.fetching {
		r.mu.Unlock()
		return nil
	}
	r.fetching = true
	r.mu.Unlock()
	models, err := r.fetch(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetching = false
	if err != nil {
		r.lastErr = err.Error()
		return err
	}
	r.catalog, r.fetchedAt, r.lastErr = models, r.now(), ""
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(catalogFile{FetchedAt: r.fetchedAt, Source: r.url, Models: models})
	tmp := r.cachePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.cachePath())
}

func (r *Registry) fetch(ctx context.Context) (map[string]Limits, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", r.url, resp.Status)
	}
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				ContextLength       int `json:"context_length"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Data) == 0 {
		return nil, fmt.Errorf("GET %s: no models", r.url)
	}
	out := make(map[string]Limits, len(doc.Data))
	for _, m := range doc.Data {
		l := Limits{ContextWindow: m.TopProvider.ContextLength, MaxOutputTokens: m.TopProvider.MaxCompletionTokens,
			Source: SourceOpenRouter}
		if l.ContextWindow == 0 {
			l.ContextWindow = m.ContextLength
		}
		if m.ID != "" && l.Known() {
			out[m.ID] = l
		}
	}
	return out, nil
}

// Learn records limits a provider stated in an error. They only ever
// lower what is known, and expire after a day.
func (r *Registry) Learn(model string, l Limits) {
	if model == "" || !l.Known() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.learned[model]
	if ok && r.now().Sub(cur.At) < learnedTTL {
		l.ContextWindow = minPos(l.ContextWindow, cur.ContextWindow)
		l.MaxOutputTokens = minPos(l.MaxOutputTokens, cur.MaxOutputTokens)
	}
	l.Source = SourceLearned
	r.learned[model] = learned{Limits: l, At: r.now()}
}

// Lookup returns what is known about model, without overrides: learned
// limits lower the catalog (OpenRouter routes) or built-in values.
func (r *Registry) Lookup(model string, openRouter, beta1M bool) Limits {
	var base Limits
	if r != nil && openRouter {
		r.mu.RLock()
		l, ok := r.catalog[model]
		if !ok {
			// ":free", ":nitro"... variants share the base model's limits.
			if i := strings.LastIndexByte(model, ':'); i > 0 {
				l, ok = r.catalog[model[:i]]
			}
		}
		r.mu.RUnlock()
		if ok {
			base = l
		}
	}
	if !base.Known() {
		if l, ok := builtinFor(model, beta1M); ok {
			base = l
		}
	}
	if r == nil {
		return base
	}
	r.mu.RLock()
	lr, ok := r.learned[model]
	r.mu.RUnlock()
	if ok && r.now().Sub(lr.At) < learnedTTL {
		if lr.ContextWindow > 0 && (base.ContextWindow == 0 || lr.ContextWindow < base.ContextWindow) {
			base.ContextWindow, base.Source = lr.ContextWindow, SourceLearned
		}
		if lr.MaxOutputTokens > 0 && (base.MaxOutputTokens == 0 || lr.MaxOutputTokens < base.MaxOutputTokens) {
			base.MaxOutputTokens, base.Source = lr.MaxOutputTokens, SourceLearned
		}
	}
	return base
}

// CatalogStatus is the catalog's state, for the API.
type CatalogStatus struct {
	URL       string     `json:"url"`
	Models    int        `json:"models"`
	FetchedAt *time.Time `json:"fetched_at"`
	Stale     bool       `json:"stale"`
	LastError string     `json:"last_error,omitempty"`
	Learned   int        `json:"learned"`
}

// Status describes the catalog.
func (r *Registry) Status() CatalogStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := CatalogStatus{URL: r.url, Models: len(r.catalog), LastError: r.lastErr, Learned: len(r.learned),
		Stale: r.now().Sub(r.fetchedAt) > r.ttl}
	if !r.fetchedAt.IsZero() {
		t := r.fetchedAt
		s.FetchedAt = &t
	}
	return s
}

// minPos is the smaller of two limits, where 0 means unknown.
func minPos(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}
