package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

const decisionRuleJSON = `{"name":"task","enabled":true,"kind":"decision","backend":"jev","backend_model":"jev-latest",
  "question":{"type":"choice","instructions":"What kind of task is this?",
    "criteria":{"code":"programming","chat":"small talk"}},
  "inputs":{"facts":["latest_user_text"]},
  "timeout_ms":1000,"min_confidence":0.5,
  "branches":[{"label":"code","when":{"equals":"code"},"then":{"route":"openrouter"}},
              {"label":"chat","when":{"in":["chat"]},"then":{"route":"cheap","model":"qwen/qwen3"}}],
  "else":{"next_rule":true},"evaluate":"conversation_start","when":{}}`

func TestDecisionRulesAPI(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, fixed(`{"type":"choice","choice":"code","confidence":0.9}`))
	e.useBackend(s1.srv.URL)
	srv := e.server()

	code, out := do(t, "PUT", srv.URL+"/api/router/rules", `{"sticky":true,"ttl_hours":72,"background_bypass":true,"rules":[`+decisionRuleJSON+`]}`)
	if code != 200 {
		t.Fatalf("put: %d %s", code, out)
	}
	var doc rulesDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	r := doc.Rules[0]
	if r.Kind != KindDecision || r.Question.Criteria.Choices["code"] != "programming" || r.Branches[1].Then.Model != "qwen/qwen3" ||
		r.Else == nil || !r.Else.NextRule || r.Evaluate != EvalConversationStart {
		t.Fatalf("round trip: %+v", r)
	}
	// Mixed protocols are refused with the reason.
	mixed := strings.Replace(decisionRuleJSON, `"route":"openrouter"`, `"route":"oa-fast"`, 1)
	e.addRoutes()
	code, out = do(t, "PUT", srv.URL+"/api/router/rules", `{"sticky":true,"rules":[`+mixed+`]}`)
	if code != 400 || !strings.Contains(out, "An Anthropic-format request can't go to an OpenAI-format route") {
		t.Fatalf("mixed: %d %s", code, out)
	}
	// Deleting a route a decision rule uses is refused.
	code, out = do(t, "DELETE", srv.URL+"/api/router/routes/cheap", "")
	if code != 409 || !strings.Contains(out, "task") {
		t.Fatalf("delete: %d %s", code, out)
	}
}

func TestRuleTestEndpoint(t *testing.T) {
	e := newEnv(t)
	s1 := newSystemOne(t, func(state map[string]any) string {
		if strings.Contains(state["user_message"].(string), "python") {
			return `{"type":"choice","choice":"code","confidence":0.93,"probabilities":{"code":0.93,"chat":0.07}}`
		}
		return `{"type":"choice","choice":"chat","confidence":0.88}`
	})
	e.useBackend(s1.srv.URL)
	srv := e.server()

	// An unsaved rule against an ad-hoc text.
	code, out := do(t, "POST", srv.URL+"/api/router/rules/test", `{"rule":`+decisionRuleJSON+`,"text":"write a python script"}`)
	if code != 200 {
		t.Fatalf("text: %d %s", code, out)
	}
	var res RuleTestResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.Input != "text" || res.Route != "openrouter" || res.Decision == nil || !res.Decision.DryRun ||
		res.Decision.Choice != "code" || res.Decision.State["user_message"] != "write a python script" ||
		!strings.HasPrefix(res.Reason, "decision rule 'task': [dry run] choice 'code' (conf 0.93, jev ") || !res.ConditionsOK {
		t.Fatalf("text result: %s", out)
	}
	// Against a logged request.
	b, _ := json.Marshal(chatBody("tell me a joke"))
	id := "20260923T000000-00000e"
	if err := e.st.Save(&store.Detail{Record: store.Record{ID: id, ConversationID: pipeline.ConversationID(nil, b)},
		RequestHeaders: map[string]string{"Content-Type": "application/json"}, RequestBody: string(b)}); err != nil {
		t.Fatal(err)
	}
	code, out = do(t, "POST", srv.URL+"/api/router/rules/test", `{"rule":`+decisionRuleJSON+`,"request_id":"`+id+`"}`)
	_ = json.Unmarshal([]byte(out), &res)
	if code != 200 || res.Input != "request" || res.Route != "cheap" || res.Model != "qwen/qwen3" || res.Facts.Tools != 1 {
		t.Fatalf("request: %d %s", code, out)
	}
	if n := len(e.r.sticky.list(0)); n != 0 {
		t.Fatalf("a test pinned: %d", n)
	}
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"rule":` + decisionRuleJSON + `}`, 400, "request_id or text is required"},
		{`{"rule":` + decisionRuleJSON + `,"request_id":"nope"}`, 404, "no such request"},
		{`{"rule":{"name":"m","enabled":true,"route":"cheap"},"text":"x"}`, 400, "only decision rules"},
		{`{"rule":` + strings.Replace(decisionRuleJSON, `"equals":"code"`, `"equals":"poetry"`, 1) + `,"text":"x"}`, 400, `label \"poetry\"`},
		{`{"rule":` + decisionRuleJSON + `,"text":"x","protocol":"smtp"}`, 400, "protocol must be"},
	} {
		code, out := do(t, "POST", srv.URL+"/api/router/rules/test", c.body)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: %d %s", c.want, code, out)
		}
	}
}

func TestRuleTemplatesEndpoint(t *testing.T) {
	e := newEnv(t)
	e.addRoutes()
	s1 := newSystemOne(t, fixed(`{}`))
	e.useSelector(s1.srv.URL)
	srv := e.server()
	code, out := do(t, "GET", srv.URL+"/api/router/rule-templates", "")
	var ts []RuleTemplate
	if err := json.Unmarshal([]byte(out), &ts); code != 200 || err != nil || len(ts) != 3 {
		t.Fatalf("templates: %d %v %s", code, err, out)
	}
	ids := []string{}
	for _, tpl := range ts {
		ids = append(ids, tpl.ID)
		// Unfilled, a template does not validate; filled, it does.
		if err := (Settings{Rules: []Rule{tpl.Rule}}).validate(e.cfg.Get()); err == nil {
			t.Errorf("%s validates without targets", tpl.ID)
		}
		r := tpl.Rule
		for _, sl := range tpl.Slots {
			target := Target{Route: "oa-fast"}
			if sl.Branch < 0 {
				r.Else = &target
			} else {
				r.Branches[sl.Branch].Then = target
			}
		}
		if err := (Settings{Rules: []Rule{r}}).validate(e.cfg.Get()); err != nil {
			t.Errorf("%s filled: %v", tpl.ID, err)
		}
		if tpl.Title == "" || tpl.Description == "" || r.Question.Instructions == "" {
			t.Errorf("%s: incomplete", tpl.ID)
		}
	}
	if strings.Join(ids, ",") != "task-type,complexity,sensitive-local" {
		t.Fatalf("ids: %v", ids)
	}
	if !strings.Contains(out, `"criteria":["trivial`) || !strings.Contains(out, `"else":{}`) {
		t.Fatalf("shapes: %s", out)
	}
}
