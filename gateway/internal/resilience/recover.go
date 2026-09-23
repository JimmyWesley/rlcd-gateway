package resilience

import (
	"fmt"
	"math"
	"time"
)

// Actions: what the gateway does after a failed attempt.
const (
	ActionClamp          = "clamp_max_tokens"
	ActionEmergencyPrune = "emergency_prune"
	ActionIgnoreProvider = "ignore_provider"
	ActionBackoff        = "backoff"
	ActionFallback       = "fallback"
	// ActionNone: the failure is the client's to see (auth, bad request):
	// passed through untouched.
	ActionNone = "none"
	// ActionGiveUp: recovery was possible in principle but nothing is left
	// (attempts, time, fallbacks); the last failure goes to the client.
	ActionGiveUp = "give_up"
)

// Actions lists every action, for the stats.
var Actions = []string{ActionClamp, ActionEmergencyPrune, ActionIgnoreProvider, ActionBackoff, ActionFallback,
	ActionNone, ActionGiveUp}

// State is what the decision needs to know about the request so far.
type State struct {
	// Attempt is the number of the attempt that just failed (from 1).
	Attempt int
	Elapsed time.Duration
	// OpenRouter: the current route is OpenRouter (provider routing works).
	OpenRouter bool
	// Ignored are providers already excluded on this route.
	Ignored []string
	// BackoffsUsed counts backoffs taken on the current route.
	BackoffsUsed int
	// FallbacksLeft counts usable fallbacks not tried yet.
	FallbacksLeft int
	// CanClamp: a smaller output limit than the one sent is known.
	CanClamp bool
	// EmergencyTried: the emergency prune already ran for this request.
	EmergencyTried bool
}

// Decision is the next step.
type Decision struct {
	Action string
	Detail string
	// Wait is the backoff before the next attempt.
	Wait time.Duration
}

// Retries reports whether the action is followed by another attempt.
func (d Decision) Retries() bool {
	return d.Action != ActionNone && d.Action != ActionGiveUp
}

// Decide picks the recovery for a failure. rnd (0-1) jitters the backoff.
func Decide(p Policy, f Failure, s State, rnd float64) Decision {
	switch f.Class {
	case ClassAuth, ClassBadRequest, ClassUnknown:
		return Decision{Action: ActionNone, Detail: f.Class + " is never retried: passed through untouched"}
	}
	budget := time.Duration(p.TimeBudgetMs) * time.Millisecond
	switch {
	case !p.Enabled:
		return Decision{Action: ActionGiveUp, Detail: "recovery is disabled"}
	case s.Attempt >= p.MaxAttempts:
		return Decision{Action: ActionGiveUp, Detail: fmt.Sprintf("max_attempts (%d) reached", p.MaxAttempts)}
	case s.Elapsed >= budget:
		return Decision{Action: ActionGiveUp, Detail: fmt.Sprintf("time budget (%d ms) spent", p.TimeBudgetMs)}
	}
	switch f.Class {
	case ClassOutputTooLarge:
		if s.CanClamp {
			return Decision{Action: ActionClamp}
		}
		return Decision{Action: ActionGiveUp, Detail: "no smaller output limit to try"}
	case ClassContextOverflow:
		switch {
		case s.EmergencyTried:
			return Decision{Action: ActionGiveUp, Detail: "the context still does not fit after the emergency prune"}
		case !p.EmergencyPrune.Enabled:
			return Decision{Action: ActionGiveUp, Detail: "emergency_prune is disabled"}
		}
		return Decision{Action: ActionEmergencyPrune}
	case ClassModelUnavailable:
		if s.FallbacksLeft > 0 {
			return Decision{Action: ActionFallback}
		}
		return Decision{Action: ActionGiveUp, Detail: "the model is unavailable and no fallback route is left"}
	}
	// rate_limited, overloaded, provider_error.
	if p.IgnoreProvider && s.OpenRouter && f.UpstreamProvider != "" && !contains(s.Ignored, f.UpstreamProvider) {
		return Decision{Action: ActionIgnoreProvider, Detail: "retry without provider " + f.UpstreamProvider}
	}
	why := ""
	if s.BackoffsUsed < p.Backoff.Retries {
		wait := BackoffWait(p.Backoff, s.BackoffsUsed, f.RetryAfter, rnd)
		if s.Elapsed+wait <= budget {
			d := Decision{Action: ActionBackoff, Wait: wait, Detail: fmt.Sprintf("wait %d ms", wait.Milliseconds())}
			if f.RetryAfter > 0 {
				d.Detail += fmt.Sprintf(" (Retry-After %d ms)", f.RetryAfter.Milliseconds())
			}
			return d
		}
		why = fmt.Sprintf("a %d ms wait would exceed the time budget", wait.Milliseconds())
	} else {
		why = fmt.Sprintf("%d backoff retries used", s.BackoffsUsed)
	}
	if s.FallbacksLeft > 0 {
		return Decision{Action: ActionFallback, Detail: why}
	}
	return Decision{Action: ActionGiveUp, Detail: why + "; no fallback route is left"}
}

// BackoffWait is the wait before retry n (from 0): base·2ⁿ capped at max,
// jittered by ±jitter, and never shorter than what the provider asked.
func BackoffWait(b Backoff, n int, retryAfter time.Duration, rnd float64) time.Duration {
	w := float64(b.BaseMs) * math.Pow(2, float64(n))
	if w > float64(b.MaxMs) {
		w = float64(b.MaxMs)
	}
	w *= 1 + b.Jitter*(2*rnd-1)
	if w < 0 {
		w = 0
	}
	d := time.Duration(w * float64(time.Millisecond))
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

// ClampTarget is the output limit to retry an output_too_large failure
// with, from the provider's numbers first, then the known limits.
// current is the limit sent (0 when none was). It returns 0 when no
// smaller useful limit is known.
func ClampTarget(f Failure, l Limits, estInput, margin, current int) int {
	target := 0
	if f.MaxOutput > 0 {
		target = f.MaxOutput
	}
	if f.Window > 0 && f.Input > 0 {
		// The provider counted the input: only a small margin is needed.
		target = minPos(target, f.Window-f.Input-f.Input/100-minUsefulOutput)
	}
	if target <= 0 {
		target = minPos(l.MaxOutputTokens, fit(l, estInput, margin))
	}
	if target <= 0 && current > 0 {
		target = current / 2
	}
	if target < minUsefulOutput || (current > 0 && target >= current) {
		return 0
	}
	return target
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
