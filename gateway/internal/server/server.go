// Package server wires the gateway together: the request engine, the
// feature packages plugged into its hooks, the dashboard API, and the guard
// in front of all of it. main and the end-to-end tests build the same
// handler through New.
package server

import (
	"net/http"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/adapters"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/api"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/decisions"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/guard"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/proxy"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/prune"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/recall"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/router"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/web"
)

type Options struct {
	// Listen is the address actually bound; it decides the guard's mode.
	Listen     string
	AdminToken string
	// AllowHosts adds to the config's allowed_hosts for this run.
	AllowHosts []string
	// Home is the config directory (config.Dir() when empty).
	Home string
}

// Gateway is the assembled server.
type Gateway struct {
	Handler  http.Handler
	Guard    *guard.Guard
	Proxy    *proxy.Proxy
	Router   *router.Router
	Pruner   *prune.Pruner
	Recall   *recall.Server
	Keys     *keys.Store
	Adapters *adapters.Adapters
	// Decisions proxies and audits System One decision calls.
	Decisions *decisions.Decisions
	// Janitor applies the storage retention settings. New does not start
	// it; main calls Janitor.Start.
	Janitor *store.Janitor
}

func New(cs *config.Store, st *store.Store, o Options) (*Gateway, error) {
	home := o.Home
	if home == "" {
		home = config.Dir()
	}
	ks, err := keys.Open(home)
	if err != nil {
		return nil, err
	}
	g := &Gateway{Keys: ks}

	// Feature packages plug into the request path through pipeline hooks and
	// mount their own endpoints; see internal/pipeline.
	g.Pruner = prune.New(cs, st)
	g.Router = router.New(cs, st)
	g.Recall = recall.New(cs, st)
	g.Pruner.Recalls = g.Recall
	g.Proxy = proxy.New(cs, st)
	g.Proxy.Keys = ks
	g.Proxy.Hooks = pipeline.Hooks{Router: g.Router, Transformers: []pipeline.Transformer{g.Pruner}, Models: g.Router}
	// Retention must know which request bodies pruning markers point at.
	g.Janitor = store.NewJanitor(cs, st, g.Pruner, g.Router, g.Recall)
	g.Decisions = decisions.New(cs, st)
	g.Decisions.Keys = ks
	// The pipeline's own economy-model calls are audited as decisions.
	g.Proxy.SelectorObserver = g.Decisions.ObserveInternal
	g.Adapters = adapters.New(cs, st)
	g.Adapters.UseEngine(g.Proxy)
	g.Guard = &guard.Guard{Listen: o.Listen, Config: cs, Keys: ks, AdminToken: o.AdminToken, ExtraHosts: o.AllowHosts}
	if err := g.Guard.Check(); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	(&api.API{Config: cs, Store: st, Listen: o.Listen, Keys: ks, Exposed: g.Guard.Exposed()}).Register(mux)
	g.Pruner.Register(mux)
	g.Router.Register(mux)
	g.Recall.Register(mux)
	g.Janitor.Register(mux)
	g.Adapters.Register(mux)
	g.Decisions.Register(mux)
	ks.Register(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/ui/", web.Handler())
	mux.Handle("GET /{$}", http.RedirectHandler("/ui/", http.StatusFound))
	mux.Handle("/", g.Proxy)
	g.Handler = g.Guard.Wrap(mux)
	return g, nil
}
