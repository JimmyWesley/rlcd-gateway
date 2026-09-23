// Package prune decides which blocks of the context the model actually needs
// and rebuilds the request without the rest (F1). Stub: owned by the F1 work.
package prune

import (
	"context"
	"net/http"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Pruner struct {
	cfg *config.Store
	st  *store.Store
}

func New(cfg *config.Store, st *store.Store) *Pruner { return &Pruner{cfg: cfg, st: st} }

func (p *Pruner) Name() string { return "prune" }

func (p *Pruner) Transform(ctx context.Context, r *pipeline.Request, body []byte) (*pipeline.Result, error) {
	return nil, nil
}

// Register mounts the package's dashboard API under /api/prune.
func (p *Pruner) Register(mux *http.ServeMux) {}
