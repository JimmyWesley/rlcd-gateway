package prune

import (
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pricing"
)

// dialect is one protocol's view of a request body: what the pruner reads
// (items, goal, thread identity) and how it rewrites and checks the body.
// The decisions themselves (plan, epochs, stickiness) are the same for
// every protocol.
type dialect interface {
	// numMsgs is the number of messages (input items); 0 means nothing to prune.
	numMsgs() int
	fingerprint() string
	items(x *ir.Request, eff Effective) []*item
	goalAndRecent() (goal, recent string)
	apply(items []*item) error
	encode() ([]byte, error)
	// verify checks the rewritten body against the original.
	verify(orig, out []byte) error
	// cached reports whether the provider will serve a prompt prefix from
	// its cache: explicit cache_control on Anthropic, automatic from
	// OpenAIMinCachedPrompt tokens on OpenAI-format providers.
	cached(x *ir.Request) bool
}

func parseDialect(protocol string, body []byte) (dialect, error) {
	if ir.IsOpenAI(protocol) {
		return parseOA(protocol, body)
	}
	return parseDoc(body)
}

// The Anthropic document (body.go) as a dialect.

func (d *doc) numMsgs() int        { return len(d.msgs) }
func (d *doc) fingerprint() string { return d.threadFingerprint() }
func (d *doc) items(x *ir.Request, eff Effective) []*item {
	return buildItems(d, x, eff)
}
func (d *doc) goalAndRecent() (string, string) { return goalAndRecent(d) }
func (d *doc) apply(items []*item) error       { return apply(d, items) }
func (d *doc) verify(orig, out []byte) error   { return verify(orig, out) }
func (d *doc) cached(x *ir.Request) bool       { return d.usesCache(x) }

// oaCached: OpenAI caches prompts from 1024 tokens on, by prefix, with no
// opt-in. Other OpenAI-compatible providers vary; the estimate assumes the
// same rule and labels it as an estimate.
func oaCached(x *ir.Request) bool { return x.Tokens >= pricing.OpenAIMinCachedPrompt }
