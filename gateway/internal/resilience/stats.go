package resilience

import (
	"sort"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

// Stats is GET /api/resilience/stats.
type Stats struct {
	// Days is the window asked for (0 = everything on disk); Since is the
	// oldest record counted.
	Days  int        `json:"days"`
	Since *time.Time `json:"since"`
	// Requests counts model calls in the window.
	Requests int `json:"requests"`
	// Engaged counts calls the resilience layer touched: a failure, a
	// retry, or a body change (they carry an attempts list).
	Engaged int `json:"engaged"`
	// FirstAttemptFailed counts calls whose first attempt failed.
	FirstAttemptFailed int `json:"first_attempt_failed"`
	Retried            int `json:"retried"`
	// Recovered: retried and the last attempt succeeded; FailedAfterRetry:
	// retried and still failed.
	Recovered        int `json:"recovered"`
	FailedAfterRetry int `json:"failed_after_retry"`
	// RecoveryRate is recovered / retried (0 when nothing was retried).
	RecoveryRate float64 `json:"recovery_rate"`
	// Fallbacks counts calls served by a fallback route.
	Fallbacks int `json:"fallbacks"`
	// Guard counts preventive max_tokens rewrites.
	Guard GuardStats `json:"guard"`
	// ByClass and ByAction list every class and action (zeros included).
	ByClass  map[string]*ClassStats  `json:"by_class"`
	ByAction map[string]*ActionStats `json:"by_action"`
	// TopProviders and TopModels rank failed attempts (at most 10 each).
	// A provider behind OpenRouter is named "openrouter/<provider_name>".
	TopProviders []Top `json:"top_failing_providers"`
	TopModels    []Top `json:"top_failing_models"`
	// FallbackRoutes counts primary → fallback pairs.
	FallbackRoutes []FallbackPair `json:"fallback_routes"`
}

// GuardStats counts preventive max_tokens rewrites by reason.
type GuardStats struct {
	Filled         int `json:"filled_missing"`
	AboveMaxOutput int `json:"above_max_output"`
	ExceedsWindow  int `json:"exceeds_context_window"`
}

// ClassStats: Failures counts failed attempts of the class; Requests the
// calls that saw it at least once, Recovered those of them that ended well.
type ClassStats struct {
	Failures  int `json:"failures"`
	Requests  int `json:"requests"`
	Recovered int `json:"recovered"`
}

// ActionStats: Count is how often the action was taken, Succeeded how
// often the attempt right after it succeeded.
type ActionStats struct {
	Count     int `json:"count"`
	Succeeded int `json:"succeeded"`
}

// Top is one ranked name.
type Top struct {
	Name     string `json:"name"`
	Failures int    `json:"failures"`
	// Recovered counts the failures whose request still ended well.
	Recovered int `json:"recovered"`
}

// FallbackPair is one primary → fallback route pair.
type FallbackPair struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

// ComputeStats summarizes records newer than since (zero = all).
func ComputeStats(scan func(func(store.Record) bool) error, days int, now time.Time) (Stats, error) {
	s := Stats{Days: days, ByClass: map[string]*ClassStats{}, ByAction: map[string]*ActionStats{},
		TopProviders: []Top{}, TopModels: []Top{}, FallbackRoutes: []FallbackPair{}}
	for _, c := range Classes {
		s.ByClass[c] = &ClassStats{}
	}
	for _, a := range Actions {
		s.ByAction[a] = &ActionStats{}
	}
	var since time.Time
	if days > 0 {
		since = now.Add(-time.Duration(days) * 24 * time.Hour)
	}
	providers, models := map[string]*Top{}, map[string]*Top{}
	pairs := map[[2]string]int{}
	err := scan(func(r store.Record) bool {
		if r.Time.Before(since) || r.Protocol == "systemone" || (r.ClientModel == "" && r.Model == "") {
			return true
		}
		if s.Since == nil || r.Time.Before(*s.Since) {
			t := r.Time
			s.Since = &t
		}
		s.Requests++
		if g := r.MaxTokensGuard; g != nil {
			switch g.Reason {
			case ReasonFilled:
				s.Guard.Filled++
			case ReasonAboveMaxOut:
				s.Guard.AboveMaxOutput++
			case ReasonExceedsWindow:
				s.Guard.ExceedsWindow++
			}
		}
		if len(r.Attempts) == 0 {
			return true
		}
		s.Engaged++
		ok := r.Status > 0 && r.Status < 400 && r.ErrorClass == ""
		if r.Attempts[0].Class != "" {
			s.FirstAttemptFailed++
		}
		if r.Retried {
			s.Retried++
			if r.Recovered {
				s.Recovered++
			} else {
				s.FailedAfterRetry++
			}
		}
		if r.FallbackRoute != "" {
			s.Fallbacks++
			pairs[[2]string{r.PrimaryRoute, r.FallbackRoute}]++
		}
		seen := map[string]bool{}
		for i, a := range r.Attempts {
			if a.Action != "" {
				as := s.ByAction[a.Action]
				if as == nil {
					as = &ActionStats{}
					s.ByAction[a.Action] = as
				}
				as.Count++
				if i+1 < len(r.Attempts) && r.Attempts[i+1].Class == "" {
					as.Succeeded++
				}
			}
			if a.Class == "" {
				continue
			}
			cs := s.ByClass[a.Class]
			if cs == nil {
				cs = &ClassStats{}
				s.ByClass[a.Class] = cs
			}
			cs.Failures++
			if !seen[a.Class] {
				seen[a.Class] = true
				cs.Requests++
				if ok {
					cs.Recovered++
				}
			}
			name := a.Provider
			if a.UpstreamProvider != "" {
				name += "/" + a.UpstreamProvider
			}
			bump(providers, name, ok)
			bump(models, a.Model, ok)
		}
		return true
	})
	if s.Retried > 0 {
		s.RecoveryRate = float64(int(float64(s.Recovered)/float64(s.Retried)*1000+0.5)) / 1000
	}
	s.TopProviders, s.TopModels = rank(providers), rank(models)
	for k, n := range pairs {
		s.FallbackRoutes = append(s.FallbackRoutes, FallbackPair{From: k[0], To: k[1], Count: n})
	}
	sort.Slice(s.FallbackRoutes, func(i, j int) bool {
		a, b := s.FallbackRoutes[i], s.FallbackRoutes[j]
		return a.Count > b.Count || a.Count == b.Count && a.From+a.To < b.From+b.To
	})
	return s, err
}

func bump(m map[string]*Top, name string, recovered bool) {
	if name == "" {
		return
	}
	t := m[name]
	if t == nil {
		t = &Top{Name: name}
		m[name] = t
	}
	t.Failures++
	if recovered {
		t.Recovered++
	}
}

func rank(m map[string]*Top) []Top {
	out := make([]Top, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Failures > out[j].Failures || out[i].Failures == out[j].Failures && out[i].Name < out[j].Name
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
