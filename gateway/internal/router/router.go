// Package router picks which route serves each request (F2). Stub: owned by
// the F2 work.
package router

import (
	"context"
	"net/http"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Router struct {
	cfg *config.Store
	st  *store.Store
}

func New(cfg *config.Store, st *store.Store) *Router { return &Router{cfg: cfg, st: st} }

func (r *Router) Route(ctx context.Context, req *pipeline.Request) (pipeline.RouteDecision, bool) {
	return pipeline.RouteDecision{}, false
}

// Register mounts the package's dashboard API under /api/router.
func (r *Router) Register(mux *http.ServeMux) {}
