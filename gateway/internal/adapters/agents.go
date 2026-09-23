package adapters

// Dashboard API for agent setup:
//
//	GET  /api/agents[?project=/abs/path]   setup.Status for claude, codex, opencode
//	POST /api/agents/{name}/setup          body {"confirm": true}
//	POST /api/agents/{name}/undo           body {"confirm": true}
//
// Setup and undo edit the user's own config files, so they need an explicit
// JSON body. A cross-site page can send a simple form POST to 127.0.0.1
// (the Host check in main.go does not stop that), but not an
// application/json one without a CORS preflight, which is never granted.

import (
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/setup"
)

func (a *Adapters) registerAgents(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", a.listAgents)
	mux.HandleFunc("POST /api/agents/{name}/setup", func(w http.ResponseWriter, r *http.Request) { a.runAgent(w, r, false) })
	mux.HandleFunc("POST /api/agents/{name}/undo", func(w http.ResponseWriter, r *http.Request) { a.runAgent(w, r, true) })
}

// gatewayURL is the address agents should use: the loopback host the
// dashboard was reached on (which reflects -listen), else the configured one.
func (a *Adapters) gatewayURL(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return "http://" + r.Host
		}
	}
	return "http://" + a.cfg.Get().Listen
}

func (a *Adapters) listAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, setup.Status(a.gatewayURL(r), r.URL.Query().Get("project")))
}

type agentResult struct {
	OK      bool                `json:"ok"`
	Message string              `json:"message,omitempty"`
	Error   string              `json:"error,omitempty"`
	Agents  []setup.AgentStatus `json:"agents"`
}

func (a *Adapters) runAgent(w http.ResponseWriter, r *http.Request, undo bool) {
	name := r.PathValue("name")
	if !slices.Contains(setup.Agents(), name) {
		writeJSONError(w, http.StatusNotFound, "unknown agent "+name)
		return
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "send a JSON body {\"confirm\": true}")
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			writeJSONError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
	}
	var body struct {
		Confirm bool   `json:"confirm"`
		Project string `json:"project"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil || !body.Confirm {
		writeJSONError(w, http.StatusBadRequest, "this edits your agent's config file; send {\"confirm\": true}")
		return
	}
	gw := a.gatewayURL(r)
	msg, err := setup.Run(name, undo, gw)
	res := agentResult{OK: err == nil, Message: msg, Agents: setup.Status(gw, body.Project)}
	status := http.StatusOK
	if err != nil {
		res.Error, status = err.Error(), http.StatusConflict
	}
	writeJSON(w, status, res)
}
