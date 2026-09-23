// Package resilience makes upstream failures the gateway's problem instead
// of the client's.
//
// Three parts:
//
//   - a model limits registry (context window and max output per model):
//     a built-in table for first-party models, OpenRouter's public model
//     list cached on disk and refreshed in the background, limits learned
//     from provider errors, and overrides in config;
//   - a preventive max_tokens guard that fills a missing output limit and
//     clamps one the model cannot honour, before the call is made;
//   - recovery: an upstream failure is classified (Classify) and, while
//     nothing has been written to the client, the proxy retries it with the
//     action Decide picks (clamp max_tokens, emergency prune, exclude an
//     OpenRouter provider, back off, fall back to another route). A
//     failure after the first byte reached the client is never retried.
//
// Every attempt is recorded on the request record (store.Attempt).
// The settings live in the "resilience" config section (Settings).
package resilience

import (
	"context"
	"math/rand"
	"path/filepath"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Engine holds what the proxy needs at request time.
type Engine struct {
	Config   *config.Store
	Store    *store.Store
	Registry *Registry
	// Rand jitters backoffs (tests fix it).
	Rand func() float64
	// Sleep waits d or until ctx ends (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock.
	Now func() time.Time
}

// New builds the engine; the catalog cache lives in <home>/resilience.
func New(cfg *config.Store, st *store.Store, home string) *Engine {
	return &Engine{Config: cfg, Store: st, Registry: NewRegistry(filepath.Join(home, "resilience")),
		Rand: rand.Float64, Sleep: sleep, Now: time.Now}
}

// Start refreshes the OpenRouter model list in the background when the
// settings ask for it. It never blocks.
func (e *Engine) Start(ctx context.Context) {
	s := FromConfig(e.Config.Get())
	if !s.OpenRouterCatalog {
		return
	}
	e.Registry.SetTTL(time.Duration(s.CatalogTTLHours * float64(time.Hour)))
	e.Registry.Start(ctx)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
