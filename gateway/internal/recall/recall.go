// Package recall lets the model fetch pruned content back (F4). Stub: owned
// by the F4 work.
package recall

import (
	"net/http"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

type Server struct {
	cfg *config.Store
	st  *store.Store
}

func New(cfg *config.Store, st *store.Store) *Server { return &Server{cfg: cfg, st: st} }

// Register mounts the recall endpoints (MCP at /mcp, dashboard API under /api/recall).
func (s *Server) Register(mux *http.ServeMux) {}
