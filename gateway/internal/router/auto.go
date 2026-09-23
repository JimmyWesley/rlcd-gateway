package router

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/selector"
)

// autoInstructions is the one question the economy model answers. It only
// picks a key among the route descriptions; it never writes text.
//
// The wording and the state were checked against open-rlcd on a handful of
// prompts: adding the request's shape (context size, tools, client model)
// pushed every answer to the strongest route, because every Claude Code
// turn has tools and a large system prompt, so only the user's words are
// sent. Framings about "the whole conversation" or "how demanding the task
// is" had the same effect.
const autoInstructions = "Which of these models should handle the user's request? " +
	"Choose the option whose description best matches the request."

// maxPromptChars bounds how much of the user's message goes to the selector.
const maxPromptChars = 2000

// autoCandidates returns the routes an auto rule chooses among: its
// candidates (or every route) that exist and have a description.
func autoCandidates(cfg config.Config, s Settings, rule Rule, protocol string) map[string]string {
	names := rule.Candidates
	if len(names) == 0 {
		names = cfg.RouteNames()
	}
	out := map[string]string{}
	for _, n := range names {
		if rt, ok := cfg.Routes[n]; !ok || !rt.Speaks(protocol) {
			continue
		}
		if d := s.Routes[n].Description; d != "" {
			out[n] = d
		}
	}
	return out
}

// auto asks the selector a choice question. Any failure (no selector,
// timeout, unknown key, low confidence) is an error, and the caller falls
// through to the next rule.
func (r *Router) auto(ctx context.Context, cfg config.Config, s Settings, rule Rule, f *Facts) (route, why string, err error) {
	cands := autoCandidates(cfg, s, rule, f.Protocol)
	if len(cands) < 2 {
		return "", "", fmt.Errorf("needs at least two routes with a description (has %d)", len(cands))
	}
	timeout := time.Duration(rule.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultAutoTimeout * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if f.Prompt == "" {
		return "", "", fmt.Errorf("no user text to judge")
	}
	state := map[string]any{"user_request": clip(f.Prompt, maxPromptChars)}
	start := time.Now()
	res, err := selector.New(cfg.Selector).Ask(ctx, state, map[string]selector.Question{
		"route": {Type: "choice", Instructions: autoInstructions, Criteria: cands},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return "", "", fmt.Errorf("selector timed out after %s", timeout)
		}
		return "", "", fmt.Errorf("selector: %v", err)
	}
	ans, ok := res.Answers["route"]
	if !ok || ans.Choice == "" {
		return "", "", fmt.Errorf("selector returned no choice")
	}
	if _, ok := cands[ans.Choice]; !ok {
		return "", "", fmt.Errorf("selector chose unknown key %q (candidates: %v)", ans.Choice, sortedKeys(cands))
	}
	conf := -1.0
	if ans.Confidence != nil {
		conf = *ans.Confidence
	}
	if rule.MinConfidence > 0 && conf < rule.MinConfidence {
		return "", "", fmt.Errorf("selector chose %q with confidence %.2f < %.2f", ans.Choice, conf, rule.MinConfidence)
	}
	why = fmt.Sprintf("in %dms", time.Since(start).Milliseconds())
	if conf >= 0 {
		why = fmt.Sprintf("(confidence %.2f, %dms)", conf, time.Since(start).Milliseconds())
	}
	return ans.Choice, why, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
