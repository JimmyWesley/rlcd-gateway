package adapters

import (
	"net/http"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/proxy"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/relay"
)

// Upstream labels, stored as the record's route when no configured route
// serves a request.
const (
	RouteOpenAI  = "openai"
	RouteChatGPT = "chatgpt"
)

// endpointOf maps a request path to the part appended to the upstream base
// URL: "/openai/v1/responses" and "/v1/responses" both give "/responses".
func endpointOf(path string) string {
	rest := strings.TrimPrefix(path, Prefix)
	if rest == "/v1" {
		return ""
	}
	return strings.TrimPrefix(rest, "/v1")
}

// Fallback is the upstream for an OpenAI-format request that no alias,
// rule or default_openai_route claims: the ChatGPT backend for a ChatGPT
// subscription login, OpenAI for anything else.
func (a *Adapters) Fallback(h http.Header) proxy.Upstream {
	s := a.Settings()
	if relay.IsChatGPTLogin(h) {
		return proxy.Upstream{Name: RouteChatGPT, BaseURL: s.ChatGPTBaseURL}
	}
	return proxy.Upstream{Name: RouteOpenAI, BaseURL: s.OpenAIBaseURL}
}

// serveModel runs a Chat Completions or Responses call through the pipeline.
func (a *Adapters) serveModel(protocol string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { a.engine().Serve(w, r, protocol) }
}

func (a *Adapters) serveModels(w http.ResponseWriter, r *http.Request) {
	if px := a.engine(); px.ServesModels(r) {
		px.Models(w, r)
		return
	}
	a.passthrough(w, r)
}

// passthrough forwards any other OpenAI endpoint (models, compaction,
// embeddings, files...) without routing or pruning: to the default OpenAI
// route when one is set, else to OpenAI or the ChatGPT backend by login.
func (a *Adapters) passthrough(w http.ResponseWriter, r *http.Request) {
	cfg := a.cfg.Get()
	endpoint := endpointOf(r.URL.Path)
	up := a.Fallback(r.Header)
	plan := proxy.Plan{Protocol: ir.ProtocolOpenAIResponses, Route: up.Name, Kind: config.KindOpenAI,
		Upstream: up.BaseURL, Auth: config.AuthPassthrough}
	ident := keys.FromContext(r.Context())
	if rt, ok := cfg.Routes[cfg.DefaultOpenAIRoute]; ok && !relay.IsChatGPTLogin(r.Header) {
		plan.Route, plan.Upstream, plan.Auth, plan.Headers = cfg.DefaultOpenAIRoute, rt.BaseURL, rt.Auth, rt.Headers
		if rt.Auth == config.AuthKey {
			plan.Key = rt.ResolvedKey()
		}
	}
	if ident != nil && plan.Auth == config.AuthPassthrough && !relay.HasCredentials(r.Header) {
		relay.WriteOpenAIError(w, http.StatusUnauthorized, relay.ErrAuthentication,
			"this request carried only a gateway key, and no default OpenAI route holds a provider key for it")
		return
	}
	if ident != nil && len(ident.Routes) > 0 && !ident.AllowsRoute(plan.Route) {
		relay.WriteOpenAIError(w, http.StatusForbidden, relay.ErrPermission, "this gateway key may not use route "+plan.Route)
		return
	}
	plan.Target = strings.TrimRight(plan.Upstream, "/") + endpoint
	a.engine().Forward(w, r, plan)
}
