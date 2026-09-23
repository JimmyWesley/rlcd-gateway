package clients

import (
	"net/http"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Backfill fills client, provider and model_vendor on records written
// before they were detected, with the same functions live requests use.
// The client needs the stored request headers (masked credentials do not
// matter to detection); provider and vendor come from the summary alone.
// Fields already set are left alone, so it is idempotent.
func Backfill(c config.Config) store.Filler {
	return func(r *store.Record, headers map[string]string) bool {
		changed := false
		if r.Client == nil && headers != nil {
			h := http.Header{}
			for k, v := range headers {
				h.Set(k, v)
			}
			cl := Detect(h)
			cl.KeyName = r.KeyName
			r.Client, changed = &cl, true
		}
		if r.Provider == "" && r.Upstream != "" {
			p := config.ProviderFromURL(r.Upstream)
			if rt, ok := c.Routes[r.Route]; ok && strings.TrimRight(rt.BaseURL, "/") == strings.TrimRight(r.Upstream, "/") {
				p = rt.ProviderName()
			}
			r.Provider, changed = p, true
		}
		if r.ModelVendor == "" {
			m := r.Model
			if m == "" {
				m = r.ClientModel
			}
			if v := config.ModelVendor(m); v != "" {
				r.ModelVendor, changed = v, true
			}
		}
		return changed
	}
}
