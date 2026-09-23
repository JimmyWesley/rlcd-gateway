package decisions

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/guard"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// A System One request and open-rlcd's real answer to it (captured from
// open-rlcd.idie.local), used by every fake backend.
const (
	reqBody = `{"model":"Open-RLCD-text","state":{"ticket":{"subject":"Charged twice","body":"I want a refund for Pro."}},` +
		`"questions":{"intent":{"type":"choice","instructions":"What does the customer want?","criteria":{"billing":"charges and refunds","shipping":"delivery","other":"anything else"}},` +
		`"urgency":{"type":"score","instructions":"How urgent?","criteria":["low","medium","high"]},` +
		`"refund":{"type":"noul","instructions":"Explicitly asks for a refund?"}}}`
	respBody = `{"model":"Open-RLCD-text","answers":{"intent":{"type":"choice","choice":"billing","probabilities":{"billing":0.999906,"shipping":9e-05,"other":4e-06},"confidence":0.999859},` +
		`"urgency":{"type":"score","score":1.522924,"legend":{"0":"low","1":"medium","2":"high"},"probabilities":{"0":0.061431,"1":0.354215,"2":0.584354},"confidence":0.315904},` +
		`"refund":{"type":"noul","noul":0.933137}},"usage":{"input_tokens":245,"output_tokens":3},"thinking":null}`
)

// backend is a fake System One server.
type backend struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []hit
	resp string
	// hold, when set, blocks every answer until it is closed.
	hold chan struct{}
}

type hit struct {
	path string
	h    http.Header
	body []byte
}

func newBackend(t *testing.T, resp string) *backend {
	b := &backend{resp: resp}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.hits = append(b.hits, hit{path: r.URL.RequestURI(), h: r.Header.Clone(), body: body})
		resp, hold := b.resp, b.hold
		b.mu.Unlock()
		if hold != nil {
			<-hold
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Rlcd-Forward-Ms", "82.6")
		w.Header().Set("X-Rlcd-Total-Ms", "83.6")
		w.Header().Set("X-Request-Id", "rlcd_430e7c666a174861a9405e93")
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) last(t *testing.T) hit {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.hits) == 0 {
		t.Fatal("backend was not called")
	}
	return b.hits[len(b.hits)-1]
}

func (b *backend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.hits)
}

type env struct {
	t   *testing.T
	cs  *config.Store
	st  *store.Store
	ks  *keys.Store
	d   *Decisions
	srv *httptest.Server
	// seen is how many records existed before the last request.
	seen int
	// economy is the selector's backend; jev and rlcd are named backends.
	economy, jev, rlcd *backend
}

// newEnv serves the decision endpoints behind the real guard, on loopback
// or (exposed) as if listening on every interface.
func newEnv(t *testing.T, exposed bool) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RLCD_GATEWAY_HOME", home)
	e := &env{t: t, economy: newBackend(t, respBody), jev: newBackend(t, strings.Replace(respBody, "Open-RLCD-text", "jev-latest", 1)),
		rlcd: newBackend(t, respBody)}
	cfg := config.Default()
	cfg.Selector = config.Selector{Backend: config.SelectorOpenRLCDLocal, BaseURL: e.economy.srv.URL, Model: "Open-RLCD-text"}
	e.cs = config.NewStore(cfg)
	var err error
	e.st, err = store.Open(home)
	must(t, err)
	e.ks, err = keys.Open(home)
	must(t, err)
	e.d = New(e.cs, e.st)
	e.d.Keys = e.ks

	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	listen := l.Addr().String()
	if exposed {
		_, port, _ := net.SplitHostPort(listen)
		listen = "0.0.0.0:" + port
	}
	mux := http.NewServeMux()
	e.d.Register(mux)
	g := &guard.Guard{Listen: listen, Config: e.cs, Keys: e.ks}
	e.srv = &httptest.Server{Listener: l, Config: &http.Server{Handler: g.Wrap(mux)}}
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// configure stores a decisions section.
func (e *env) configure(s Settings) {
	e.t.Helper()
	must(e.t, s.Validate())
	raw, _ := json.Marshal(s)
	must(e.t, e.cs.SetSection(sectionName, raw))
}

// twoBackends maps the Jev models to a passthrough jev backend and the
// open-rlcd models to a key backend with a token.
func (e *env) twoBackends() {
	e.configure(Settings{
		Backends: map[string]Backend{
			"jev":  {BaseURL: e.jev.srv.URL, Auth: AuthPassthrough, Provider: "typesafe"},
			"rlcd": {BaseURL: e.rlcd.srv.URL, Auth: AuthKey, Token: "rlcd-backend-token"},
		},
		DefaultBackend: "rlcd",
		Models: map[string]string{"jev-latest": "jev", "jev-preview": "jev",
			"Open-RLCD-text": "rlcd", "Open-RLCD-vision": "rlcd"},
	})
}

func (e *env) do(method, path, body string, h map[string]string) (*http.Response, string) {
	e.t.Helper()
	e.seen = len(e.st.Recent())
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// last returns the record of the last request. The client can have the
// whole response (Content-Length framing) a moment before it is stored.
func (e *env) last() *store.Detail {
	e.t.Helper()
	eventually(e.t, "the record", func() bool { return len(e.st.Recent()) > e.seen })
	recs := e.st.Recent()
	d, err := e.st.Get(recs[0].ID)
	must(e.t, err)
	return d
}

func bodyFor(model string) string {
	return strings.Replace(reqBody, `"model":"Open-RLCD-text"`, `"model":"`+model+`"`, 1)
}

// eventually polls cond for up to 5 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
