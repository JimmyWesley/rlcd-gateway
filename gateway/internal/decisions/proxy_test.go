package decisions

import (
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
)

// With no decisions section the call goes to the economy-model selector,
// and both directions are byte for byte, the backend's headers included.
func TestPassthroughByteExact(t *testing.T) {
	e := newEnv(t, false)
	for _, path := range []string{"/v1/systemone", "/v1/decisions"} {
		resp, out := e.do("POST", path+"?trace=1", reqBody, map[string]string{"User-Agent": "python-requests/2.32.3"})
		if resp.StatusCode != 200 || out != respBody {
			t.Fatalf("%s: response not byte-exact (%d): %s", path, resp.StatusCode, out)
		}
		for k, v := range map[string]string{"X-Rlcd-Forward-Ms": "82.6", "X-Rlcd-Total-Ms": "83.6",
			"X-Request-Id": "rlcd_430e7c666a174861a9405e93", "Content-Type": "application/json", "X-Rlcd-Route": Economy} {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("%s: header %s = %q, want %q", path, k, got, v)
			}
		}
		if resp.ContentLength != int64(len(respBody)) {
			t.Errorf("%s: content length %d", path, resp.ContentLength)
		}
		got := e.economy.last(t)
		if string(got.body) != reqBody || got.path != "/v1/systemone?trace=1" {
			t.Fatalf("%s: request changed on the way: %s %s", path, got.path, got.body)
		}
	}
}

func TestResolveMapping(t *testing.T) {
	cfg := *config.Default()
	cfg.Selector.BaseURL = "http://selector.example"
	s := Settings{
		Backends: map[string]Backend{
			"jev":  {BaseURL: "https://api.typesafe.ai", Auth: AuthPassthrough},
			"rlcd": {BaseURL: "http://open-rlcd.idie.local", Auth: AuthKey},
			"lab":  {BaseURL: "http://10.0.0.9:8000", Auth: AuthKey},
		},
		Models: map[string]string{"jev-latest": "jev", "jev-preview": "jev", "Open-RLCD-text": "rlcd",
			"Open-RLCD-vision": "rlcd", "Open-RLCD-*": "lab", "Open-*": "economy"},
	}
	must(t, s.Validate())
	for model, want := range map[string]string{"jev-latest": "jev", "jev-preview": "jev", "Open-RLCD-text": "rlcd",
		"Open-RLCD-vision": "rlcd", "Open-RLCD-audio": "lab", "Open-thing": Economy, "mystery": Economy, "": Economy} {
		if got, _, why, err := s.Resolve(cfg, model); err != nil || got != want {
			t.Errorf("%q → %q (%s, %v), want %q", model, got, why, err, want)
		}
	}
	s.DefaultBackend = "rlcd"
	if got, b, _, _ := s.Resolve(cfg, "mystery"); got != "rlcd" || b.ProviderName() != "open-rlcd" {
		t.Errorf("unknown model with a default: %s %s", got, b.ProviderName())
	}
	if _, b, _, _ := s.Resolve(cfg, "jev-latest"); b.ProviderName() != "typesafe" {
		t.Errorf("jev provider: %s", b.ProviderName())
	}

	for _, bad := range []Settings{
		{Backends: map[string]Backend{"economy": {BaseURL: "http://x", Auth: AuthKey}}},
		{Backends: map[string]Backend{"a": {BaseURL: "ftp://x", Auth: AuthKey}}},
		{Backends: map[string]Backend{"a": {BaseURL: "http://x", Auth: "magic"}}},
		{Backends: map[string]Backend{"a": {BaseURL: "http://x", Auth: AuthPassthrough, Token: "t"}}},
		{Backends: map[string]Backend{"a": {BaseURL: "http://x", Auth: AuthKey, TokenEnv: "not a var"}}},
		{Backends: map[string]Backend{"a b": {BaseURL: "http://x", Auth: AuthKey}}},
		{DefaultBackend: "nope"},
		{Models: map[string]string{"m": "nope"}},
		{Models: map[string]string{"*": "economy"}},
		{Models: map[string]string{"a*b": "economy"}},
		{Mirror: &Mirror{Backend: "nope", SampleRate: 0.5}},
		{Mirror: &Mirror{Backend: "economy", SampleRate: 1.5}},
		{Mirror: &Mirror{SampleRate: 0.5}},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// Each model reaches its backend over HTTP; an unknown one the default.
func TestModelBackendMapping(t *testing.T) {
	e := newEnv(t, false)
	e.twoBackends()
	hdr := map[string]string{"Authorization": "Bearer ts-client-typesafe-key"}
	for model, b := range map[string]*backend{"jev-latest": e.jev, "jev-preview": e.jev, "Open-RLCD-text": e.rlcd,
		"Open-RLCD-vision": e.rlcd, "something-else": e.rlcd} {
		before := b.count()
		if resp, _ := e.do("POST", "/v1/systemone", bodyFor(model), hdr); resp.StatusCode != 200 {
			t.Fatalf("%s: %d", model, resp.StatusCode)
		}
		if b.count() != before+1 {
			t.Errorf("%s did not reach its backend", model)
		}
		if d := e.last(); d.ClientModel != model || d.Decisions.Backend != d.Route {
			t.Errorf("%s: record %s %s", model, d.ClientModel, d.Route)
		}
	}
	if e.economy.count() != 0 {
		t.Errorf("the economy backend was called although a default is set")
	}
	// A section that no longer validates refuses calls instead of guessing.
	must(t, e.cs.SetSection(sectionName, []byte(`{"default_backend":"gone"}`)))
	if resp, out := e.do("POST", "/v1/systemone", reqBody, nil); resp.StatusCode != 500 || !strings.Contains(out, "decisions config") {
		t.Errorf("invalid section: %d %s", resp.StatusCode, out)
	}
}

// passthrough forwards the client's own key; key swaps it for the
// backend's token (or none); a gateway key never travels.
func TestPassthroughVersusKeyAuth(t *testing.T) {
	e := newEnv(t, false)
	e.twoBackends()
	gk, _, err := e.ks.Create("app", keys.Limits{})
	must(t, err)

	e.do("POST", "/v1/systemone", bodyFor("jev-latest"), map[string]string{"Authorization": "Bearer ts-client-typesafe-key"})
	if h := e.jev.last(t).h; h.Get("Authorization") != "Bearer ts-client-typesafe-key" {
		t.Errorf("passthrough did not forward the client's key: %v", h)
	}
	if d := e.last(); d.RequestHeaders["Authorization"] == "Bearer ts-client-typesafe-key" {
		t.Errorf("credential logged unmasked")
	}

	e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer ts-client-typesafe-key"})
	if h := e.rlcd.last(t).h; h.Get("Authorization") != "Bearer rlcd-backend-token" {
		t.Errorf("key backend did not swap credentials: %v", h)
	}

	// Gateway key next to the client's own login: both work, the key is stripped.
	resp, _ := e.do("POST", "/v1/systemone", bodyFor("jev-latest"),
		map[string]string{"Authorization": "Bearer ts-client-typesafe-key", keys.Header: gk})
	if h := e.jev.last(t).h; resp.StatusCode != 200 || h.Get(keys.Header) != "" || h.Get("Authorization") != "Bearer ts-client-typesafe-key" {
		t.Errorf("gateway key + passthrough: %d %v", resp.StatusCode, h)
	}
	// Only a gateway key: a passthrough backend has nothing to forward.
	resp, out := e.do("POST", "/v1/systemone", bodyFor("jev-latest"), map[string]string{"Authorization": "Bearer " + gk})
	if resp.StatusCode != 401 || !strings.Contains(out, "X-Rlcd-Key") {
		t.Errorf("gateway key alone on passthrough: %d %s", resp.StatusCode, out)
	}
	// Only a gateway key on a key backend: stripped, the token goes instead.
	resp, _ = e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer " + gk})
	if h := e.rlcd.last(t).h; resp.StatusCode != 200 || h.Get("Authorization") != "Bearer rlcd-backend-token" {
		t.Errorf("gateway key on key backend: %d %v", resp.StatusCode, h)
	}
	if d := e.last(); d.KeyName != "app" || d.AuthMode != "gateway-key" {
		t.Errorf("record key: %s %s", d.KeyName, d.AuthMode)
	}

	// The economy backend with no token sends no credentials at all.
	e.configure(Settings{})
	e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer ts-client-typesafe-key"})
	if h := e.economy.last(t).h; h.Get("Authorization") != "" {
		t.Errorf("token-less backend got credentials: %v", h)
	}
}

func TestGatewayKeyLimits(t *testing.T) {
	e := newEnv(t, true) // exposed: a key is required
	e.twoBackends()
	if resp, _ := e.do("POST", "/v1/systemone", reqBody, nil); resp.StatusCode != 401 {
		t.Fatalf("exposed without a key: %d", resp.StatusCode)
	}
	rpm, _, err := e.ks.Create("rpm", keys.Limits{RPM: 2})
	must(t, err)
	for i := 0; i < 2; i++ {
		if resp, _ := e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer " + rpm}); resp.StatusCode != 200 {
			t.Fatalf("call %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := e.do("POST", "/v1/systemone", reqBody, map[string]string{"Authorization": "Bearer " + rpm}); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("rpm limit: %d", resp.StatusCode)
	}

	daily, dv, err := e.ks.Create("daily", keys.Limits{TokensPerDay: 300})
	must(t, err)
	hdr := map[string]string{"Authorization": "Bearer " + daily}
	if resp, _ := e.do("POST", "/v1/systemone", reqBody, hdr); resp.StatusCode != 200 { // 248 tokens, under the limit
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/v1/systemone", reqBody, hdr); resp.StatusCode != 200 { // crosses it, still completes
		t.Fatal(resp.StatusCode)
	}
	if resp, out := e.do("POST", "/v1/systemone", reqBody, hdr); resp.StatusCode != 429 || !strings.Contains(out, "daily token limit") {
		t.Errorf("daily limit: %d %s", resp.StatusCode, out)
	}
	for _, v := range e.ks.List() {
		if v.ID == dv.ID && (v.Usage.Requests != 3 || v.Usage.InputTokens != 490 || v.Usage.OutputTokens != 6 || v.Usage.Errors != 1) {
			t.Errorf("usage charged to the key: %+v", v.Usage)
		}
	}

	scoped, _, err := e.ks.Create("scoped", keys.Limits{Routes: []string{"rlcd"}, Aliases: []string{"Open-RLCD-text"}})
	must(t, err)
	hdr = map[string]string{"Authorization": "Bearer " + scoped}
	if resp, _ := e.do("POST", "/v1/systemone", reqBody, hdr); resp.StatusCode != 200 {
		t.Errorf("allowed model: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/v1/systemone", bodyFor("Open-RLCD-vision"), hdr); resp.StatusCode != 403 {
		t.Errorf("model outside the key's list: %d", resp.StatusCode)
	}
	e.configure(Settings{Backends: map[string]Backend{"other": {BaseURL: e.rlcd.srv.URL, Auth: AuthKey}}, DefaultBackend: "other"})
	if resp, out := e.do("POST", "/v1/systemone", reqBody, hdr); resp.StatusCode != 403 || !strings.Contains(out, "backend other") {
		t.Errorf("backend outside the key's routes: %d %s", resp.StatusCode, out)
	}
}

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }

func TestAuditRecordSummary(t *testing.T) {
	e := newEnv(t, false)
	e.twoBackends()
	resp, _ := e.do("POST", "/v1/systemone", bodyFor("jev-latest"), map[string]string{
		"Authorization": "Bearer ts-client", "User-Agent": "python-requests/2.32.3", "Conversation-Id": "ticket-42"})
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	d := e.last()
	if d.Protocol != ir.ProtocolSystemOne || d.Client == nil || d.Client.ID != "python-requests" || d.Provider != "typesafe" ||
		d.ModelVendor != "typesafe" || d.ConversationID != "cx-ticket-42" || d.Usage == nil || d.Usage.InputTokens != 245 ||
		d.Route != "jev" || d.RouteReason != `model "jev-latest"` {
		t.Fatalf("record: %+v client %+v", d.Record, d.Client)
	}
	// Jev: $0.042 per million input tokens, output free.
	if want := 245 * 0.042 / 1e6; math.Abs(d.CostUSD-math.Round(want*1e6)/1e6) > 1e-12 {
		t.Errorf("jev cost %v, want %v", d.CostUSD, want)
	}
	s := d.Decisions
	if s.Source != SourceClient || s.Backend != "jev" || !near(s.ForwardMs, 82.6) || !near(s.TotalMs, 83.6) ||
		s.StateBytes != len(`{"ticket":{"subject":"Charged twice","body":"I want a refund for Pro."}}`) || len(s.Questions) != 3 {
		t.Fatalf("summary: %+v", s)
	}
	q := s.Questions
	if q[0].ID != "intent" || q[0].Type != "choice" || q[0].Answer != "billing" || !near(q[0].Confidence, 0.999859) ||
		!near(q[0].TopProb, 0.999906) || strings.Join(q[0].Labels, ",") != "billing,shipping,other" {
		t.Errorf("choice: %+v", q[0])
	}
	if q[1].ID != "urgency" || q[1].Answer != "high" || !near(q[1].Score, 1.522924) || !near(q[1].Confidence, 0.315904) ||
		!near(q[1].TopProb, 0.584354) || strings.Join(q[1].Labels, ",") != "low,medium,high" {
		t.Errorf("score: %+v", q[1])
	}
	if q[2].ID != "refund" || q[2].Answer != "yes" || !near(q[2].Noul, 0.933137) || !near(q[2].Confidence, 0.933137) || !near(q[2].TopProb, 0.933137) {
		t.Errorf("noul: %+v", q[2])
	}
	// X-ray: the state, then one block per question.
	if d.XRay == nil || len(d.XRay.Blocks) != 4 || d.XRay.Blocks[0].Kind != ir.KindState || d.XRay.Blocks[1].Key != "intent" ||
		d.XRay.Blocks[1].Name != "choice" || strings.Join(d.XRay.Blocks[2].Labels, ",") != "low,medium,high" {
		t.Fatalf("xray: %+v", d.XRay)
	}
	if d.RequestBody != bodyFor("jev-latest") || d.ResponseBody == "" {
		t.Errorf("bodies not kept under the full policy")
	}

	// Self-hosted open-rlcd costs nothing; the bodies policy applies.
	must(t, e.cs.SetSection("storage", []byte(`{"bodies":"none"}`)))
	e.do("POST", "/v1/systemone", reqBody, nil)
	d = e.last()
	if d.CostUSD != 0 || d.Provider != "open-rlcd" || d.ModelVendor != "open-rlcd" || d.Usage == nil {
		t.Errorf("open-rlcd record: %+v", d.Record)
	}
	if d.RequestBody != "" || d.ResponseBody != "" || d.Decisions == nil || len(d.Decisions.Questions) != 3 {
		t.Errorf("bodies policy none: %q %q", d.RequestBody, d.ResponseBody)
	}

	// A backend error is recorded with its message.
	e.rlcd.mu.Lock()
	e.rlcd.resp = `{"detail":"score criteria must be a list"}`
	e.rlcd.mu.Unlock()
	resp, out := e.do("POST", "/v1/systemone", reqBody, nil)
	if resp.StatusCode != 200 || out != `{"detail":"score criteria must be a list"}` {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if d := e.last(); d.Decisions.Questions[0].Answer != "" || d.Usage != nil {
		t.Errorf("unanswered call: %+v", d.Decisions.Questions[0])
	}
}

// A backend that is down: the client gets a 502 and the record says why.
func TestBackendDown(t *testing.T) {
	e := newEnv(t, false)
	e.configure(Settings{Backends: map[string]Backend{"dead": {BaseURL: "http://127.0.0.1:1", Auth: AuthKey}}, DefaultBackend: "dead"})
	resp, out := e.do("POST", "/v1/systemone", reqBody, nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(out, "rlcd-gateway: upstream") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if d := e.last(); d.Status != 502 || d.Error == "" || d.Route != "dead" {
		t.Errorf("record: %+v", d.Record)
	}
}
