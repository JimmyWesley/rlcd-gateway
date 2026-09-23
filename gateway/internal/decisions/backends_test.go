package decisions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

func TestSelectorFor(t *testing.T) {
	c := *config.Default()
	c.Selector = config.Selector{Backend: config.SelectorOpenRLCDLocal, BaseURL: "http://127.0.0.1:8000", Model: "Open-RLCD-text"}
	t.Setenv("TS_TEST_KEY", "tok-env")
	sec, _ := json.Marshal(Settings{Backends: map[string]Backend{
		"jev":       {BaseURL: "https://api.typesafe.ai", Auth: AuthKey, TokenEnv: "TS_TEST_KEY"},
		"open-rlcd": {BaseURL: "http://open-rlcd.lan", Auth: AuthKey},
		"mapped":    {BaseURL: "http://m.lan", Auth: AuthKey},
		"pass":      {BaseURL: "https://api.typesafe.ai", Auth: AuthPassthrough},
	}, Models: map[string]string{"custom-b": "mapped", "custom-a": "mapped", "m-*": "mapped"}})
	c.Sections = map[string]json.RawMessage{sectionName: sec}

	for _, tc := range []struct {
		backend, model, wantModel, wantURL, wantTok, wantProvider string
	}{
		{"", "", "Open-RLCD-text", "http://127.0.0.1:8000", "", "open-rlcd"},
		{"economy", "other", "other", "http://127.0.0.1:8000", "", "open-rlcd"},
		{"jev", "", "jev-latest", "https://api.typesafe.ai", "tok-env", "typesafe"},
		{"open-rlcd", "", "Open-RLCD-text", "http://open-rlcd.lan", "", "open-rlcd"},
		{"mapped", "", "custom-a", "http://m.lan", "", "open-rlcd"},
		{"jev", "jev-preview", "jev-preview", "https://api.typesafe.ai", "tok-env", "typesafe"},
	} {
		sel, prov, err := SelectorFor(c, tc.backend, tc.model)
		if err != nil || sel.Model != tc.wantModel || sel.BaseURL != tc.wantURL || sel.ResolvedToken() != tc.wantTok || prov != tc.wantProvider {
			t.Errorf("%s/%s: %+v %s %v", tc.backend, tc.model, sel, prov, err)
		}
	}
	if _, _, err := SelectorFor(c, "pass", ""); err == nil || !strings.Contains(err.Error(), "passthrough") {
		t.Errorf("passthrough: %v", err)
	}
	if err := CheckBackend(c, "ghost"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("ghost: %v", err)
	}
	c.Selector.BaseURL = ""
	if CheckBackend(c, "economy") != nil {
		t.Error("an unconfigured economy model must still save")
	}
	if _, _, err := SelectorFor(c, "", ""); err == nil {
		t.Error("an unconfigured economy model cannot be called")
	}
	if got := strings.Join(BackendNames(c), ","); got != "economy,jev,mapped,open-rlcd,pass" {
		t.Errorf("names: %s", got)
	}
}
