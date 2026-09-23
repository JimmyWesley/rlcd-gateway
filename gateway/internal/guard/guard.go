// Package guard is the gateway's front door: which requests reach which
// part of the gateway, and with what identity. It is a security boundary.
//
// The gateway serves two very different things on one port:
//
//   - the proxy paths (/v1/..., /openai/..., /mcp), which forward model
//     calls and serve recall. These carry provider credentials or gateway
//     keys and are what apps talk to;
//   - the dashboard (/ui/, /api/..., /), which has full control: it can read
//     every logged prompt, change routes and keys, and edit agent configs.
//
// Two modes follow from the listen address.
//
// Loopback (127.0.0.1, ::1, localhost), the default. Behavior is as it was
// before gateway keys existed: every request must be addressed to the
// loopback host and port the gateway is bound to, which stops DNS
// rebinding (a web page resolving its own name to 127.0.0.1). Gateway keys
// are optional: a request carrying one is checked and the key is stripped;
// a request without one is forwarded with the client's own login, so
// Claude Code on its subscription keeps working. require_keys makes keys
// mandatory on the proxy paths anyway.
//
// Exposed (any other address, 0.0.0.0 included). Then:
//
//   - the Host must be an IP address, localhost, or a name listed in
//     allowed_hosts. Any other name is refused: that is what a DNS
//     rebinding attack sends;
//   - every proxy path requires a valid gateway key;
//   - the dashboard and /api are served only to a browser on the gateway's
//     own machine (loopback peer address and loopback Host), or to a
//     request that carries the admin token (RLCD_GATEWAY_ADMIN_TOKEN) as
//     "Authorization: Bearer <token>" or as the session cookie set by
//     POST /auth/admin. Without an admin token there is no remote dashboard.
//
// In both modes a state-changing dashboard request whose Origin is another
// site is refused, and the admin cookie is SameSite=Strict and HttpOnly.
package guard

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
)

// AdminCookie holds a hash of the admin token, never the token itself.
const AdminCookie = "rlcd_admin"

// MinAdminToken is the shortest admin token accepted.
const MinAdminToken = 24

type Guard struct {
	// Listen is the address actually bound.
	Listen string
	Config *config.Store
	Keys   *keys.Store
	// AdminToken unlocks the dashboard for remote browsers; "" means the
	// dashboard is only served to this machine.
	AdminToken string
	// ExtraHosts are accepted like the config's allowed_hosts (-allow-host).
	ExtraHosts []string
}

// Exposed reports whether the gateway listens beyond loopback.
func (g *Guard) Exposed() bool {
	host, _, err := net.SplitHostPort(g.Listen)
	return err != nil || !isLoopback(host)
}

// Check validates the configuration a mode needs.
func (g *Guard) Check() error {
	if g.AdminToken != "" && len(g.AdminToken) < MinAdminToken {
		return errors.New("RLCD_GATEWAY_ADMIN_TOKEN must be at least " + strconv.Itoa(MinAdminToken) + " characters")
	}
	return nil
}

// Wrap puts the guard in front of next.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(g.Listen)
	exposed := g.Exposed()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.hostAllowed(r.Host, port, exposed) {
			http.Error(w, "rlcd-gateway only accepts requests addressed to itself", http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/auth/admin":
			g.login(w, r)
		case IsProxyPath(r.URL.Path):
			g.proxy(w, r, next, exposed)
		default:
			g.admin(w, r, next, exposed)
		}
	})
}

// IsProxyPath reports whether a path belongs to the proxy side.
func IsProxyPath(p string) bool {
	return p == "/v1" || strings.HasPrefix(p, "/v1/") || p == "/openai" || strings.HasPrefix(p, "/openai/") || p == "/mcp"
}

// hostAllowed is the DNS-rebinding defence.
func (g *Guard) hostAllowed(hostHeader, port string, exposed bool) bool {
	if !exposed {
		// As before keys existed: loopback host, and the port we bound.
		h, p, err := net.SplitHostPort(hostHeader)
		return err == nil && p == port && isLoopback(h)
	}
	h := hostHeader
	if hh, _, err := net.SplitHostPort(hostHeader); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil || strings.EqualFold(h, "localhost") {
		return true
	}
	for _, a := range append(g.Config.Get().AllowedHosts, g.ExtraHosts...) {
		if a = strings.TrimSpace(a); a != "" {
			if ah, _, err := net.SplitHostPort(a); err == nil {
				a = ah
			}
			if strings.EqualFold(a, h) {
				return true
			}
		}
	}
	return false
}

func (g *Guard) proxy(w http.ResponseWriter, r *http.Request, next http.Handler, exposed bool) {
	var (
		id    *keys.Identity
		found bool
		err   error
	)
	if g.Keys != nil {
		id, found, err = g.Keys.Authenticate(r.Header)
	} else if _, _, has := keys.Extract(r.Header); has {
		found, err = true, keys.ErrInvalid
	}
	switch {
	case errors.Is(err, keys.ErrRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(g.Keys.RetryAfter(id.ID)))
		writeProxyError(w, r, http.StatusTooManyRequests, "rate_limit_error", err.Error())
		return
	case err != nil:
		writeProxyError(w, r, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	case found:
		r = r.WithContext(keys.WithIdentity(r.Context(), id))
	case exposed || g.Config.Get().RequireKeys:
		writeProxyError(w, r, http.StatusUnauthorized, "authentication_error", keys.ErrMissing.Error())
		return
	}
	next.ServeHTTP(w, r)
}

// writeProxyError answers in the shape the client's SDK expects.
func writeProxyError(w http.ResponseWriter, r *http.Request, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	msg = "rlcd-gateway: " + msg
	if openAIShaped(r) {
		code := "invalid_api_key"
		if status == http.StatusTooManyRequests {
			code = "rate_limit_exceeded"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"type": "invalid_request_error", "code": code, "message": msg}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
}

func openAIShaped(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/openai/"), p == "/v1/chat/completions", p == "/v1/responses", p == "/v1/embeddings":
		return true
	case p == "/v1/models":
		return r.Header.Get("Anthropic-Version") == ""
	}
	return false
}

func (g *Guard) admin(w http.ResponseWriter, r *http.Request, next http.Handler, exposed bool) {
	allowed := !exposed || (isLoopbackAddr(r.RemoteAddr) && isLoopbackHostHeader(r.Host)) || g.adminOK(r)
	if !allowed {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":       "the dashboard API needs the admin token (RLCD_GATEWAY_ADMIN_TOKEN)",
				"admin_login": g.AdminToken != "",
			})
			return
		case g.AdminToken != "" && (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/ui/")) && r.Method == http.MethodGet:
			// The dashboard's static files hold no data; the page asks for
			// the token before it can read anything from /api.
		default:
			http.Error(w, "rlcd-gateway: the dashboard is only served to this machine. "+
				"Set RLCD_GATEWAY_ADMIN_TOKEN to open it from elsewhere.", http.StatusForbidden)
			return
		}
	}
	if !originAllowed(r, exposed) {
		http.Error(w, "rlcd-gateway: cross-origin request refused", http.StatusForbidden)
		return
	}
	next.ServeHTTP(w, r)
}

// originAllowed refuses state-changing requests sent by another site's
// page. Reads are safe: the browser does not let that page see the answer.
func originAllowed(r *http.Request, exposed bool) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser, or a same-origin request of an old browser
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	// On loopback, a page served from another local port (the Vite dev
	// server) is this machine's own dashboard.
	return !exposed && isLoopback(u.Hostname())
}

func sessionValue(token string) string {
	sum := sha256.Sum256([]byte("rlcd-admin-session:" + token))
	return hex.EncodeToString(sum[:])
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (g *Guard) adminOK(r *http.Request) bool {
	if g.AdminToken == "" {
		return false
	}
	if scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		if equal(strings.TrimSpace(tok), g.AdminToken) {
			return true
		}
	}
	if c, err := r.Cookie(AdminCookie); err == nil && equal(c.Value, sessionValue(g.AdminToken)) {
		return true
	}
	return false
}

// login exchanges the admin token for a session cookie (POST) or clears it
// (DELETE).
func (g *Guard) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cookie := &http.Cookie{Name: AdminCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil}
	switch r.Method {
	case http.MethodDelete:
		cookie.MaxAge = -1
		http.SetCookie(w, cookie)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil || g.AdminToken == "" ||
		!equal(strings.TrimSpace(in.Token), g.AdminToken) {
		time.Sleep(300 * time.Millisecond) // slows guessing; the token is long anyway
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "wrong admin token"})
		return
	}
	cookie.Value = sessionValue(g.AdminToken)
	cookie.MaxAge = int((30 * 24 * time.Hour).Seconds())
	http.SetCookie(w, cookie)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackAddr(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	return err == nil && isLoopback(h)
}

func isLoopbackHostHeader(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	return isLoopback(strings.Trim(h, "[]"))
}
