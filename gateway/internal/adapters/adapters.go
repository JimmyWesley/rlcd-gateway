// Package adapters serves agents that do not speak the Anthropic Messages
// API, such as Codex (OpenAI Responses) (F3). Stub: owned by the F3 work.
package adapters

import (
	"net/http"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Adapters struct {
	cfg *config.Store
	st  *store.Store
}

func New(cfg *config.Store, st *store.Store) *Adapters { return &Adapters{cfg: cfg, st: st} }

// Register mounts the adapter endpoints (e.g. POST /v1/responses) and their
// dashboard API under /api/adapters.
func (a *Adapters) Register(mux *http.ServeMux) {}
