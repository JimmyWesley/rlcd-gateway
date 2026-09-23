# RLCD Gateway

A local gateway between coding agents (Claude Code today; Codex and OpenCode
next) and their model providers. It shows exactly what your agent sends on
every turn and routes each request wherever you choose. Next up, it decides
what actually needs to stay in the context.

```
Claude Code ──► RLCD Gateway :4777 ──┬─► api.anthropic.com   (your own login, forwarded as-is)
                 │                   └─► openrouter.ai        (a key the gateway holds)
                 └─ dashboard http://127.0.0.1:4777/ui/
```

## How it intercepts

It uses no system proxy and no certificates. You point the agent's **base URL** at the
gateway, and the agent keeps its own login, whether that is a subscription or an API key.
For every request the gateway picks the active route:

- **passthrough** routes forward the client's credentials untouched, so a Claude
  subscription is still what serves the turn.
- **key** routes drop the client's credentials and use a key the gateway holds,
  optionally swapping the model (e.g. OpenRouter).

You can switch routes from the dashboard mid-session; the agent never notices.
When a turn crosses providers, signed `thinking` blocks are removed because
they only validate on the model that wrote them.

## Economy model

Pruning is decided by a System One model that answers per block
key and never rewrites text. Jev and open-rlcd share the same API
(`POST /v1/systemone`), so the backend is just configuration:

| Backend | Base URL | Token |
|---|---|---|
| `open-rlcd-local` | wherever you run it | optional |
| `open-rlcd-cloud` | hosted open-rlcd | required |
| `jev` | `https://api.typesafe.ai` | TypeSafe token |

## Pruning

Before each `POST /v1/messages` is forwarded, the gateway looks at the old blocks of the
context and replaces the ones the current goal no longer needs with a marker:

```
[rlcd: ~1027 tokens omitted · key toolu_01Ab… · req 20260923T041241-… · call rlcd_recall to restore]
```

The marker names the request whose logged body still holds the original, so the model can
get it back through `rlcd_recall`. A dropped `tool_result` keeps its `tool_use_id` and
`is_error`; only its content changes. The latest turns, signed thinking blocks, tool calls,
and recall results are never touched.

It starts in **shadow** mode: every decision is computed and shown in the request view
("would have saved…"), but the agent's request is forwarded untouched. Switch to
**enforce** in the Pruning tab once the decisions look right. Enforcing needs
`log_bodies`, because recall reads the logged bodies.

Decisions come from free deterministic rules first (keep errors and edit results, drop a
file read that is read again later, skip small blocks), then from one batched selector
call that answers a keep probability per block key. If the selector fails or takes longer
than `selector_timeout_ms` (3 s), everything is kept.

**Prompt caching.** Anthropic bills cached prefix reads at about 0.1× input and cache
writes at 1.25×, and any change invalidates everything after it. A pruner that edits
the prefix every turn therefore costs more than it saves. To avoid that:

- Decisions are sticky per conversation and survive restarts (`<home>/prune/`). A dropped
  block is replaced by the byte-identical marker on every later turn. A kept block is
  not asked about again.
- New decisions are only taken in **epochs**: when the context has grown by
  `epoch_tokens` since the last epoch, and is above `floor_tokens`. An epoch only
  considers blocks that appeared since the previous one. The prefix before them stays
  cached, and only the tail from the first new drop is written again, once.
- Blocks are identified by `tool_use_id` or a content hash, not by position, so they
  are recognised again after the agent compacts.

The trade-off is that a block kept at one epoch is never reconsidered, even if it later
becomes stale. Dropping it would rewrite the cache from that point on: for a 1k-token
block with 50k tokens after it, the rewrite costs as much as hundreds of turns of
savings. Pruning tool definitions or system sections (`prune_tools`, `prune_system`,
both off by default) changes the very start of the prompt, so the dashboard warns about it.

Every report prices the turn with and without pruning, cache reads and writes included,
using a per-model price table you can override (list prices as of 2026-06, labelled as
estimates). Token counts are chars/4 estimates. Epoch turns can cost more than they save,
so savings can be negative early in a session.

Mark any decision as "should have kept" or "should have dropped" in the request view.
The Pruning tab replays those cases against the current criteria and threshold, which
works as a regression suite for criteria changes.

API, under `/api/prune`: `GET|PUT config`, `GET presets`, `GET|POST feedback`,
`DELETE feedback/{id}`, `POST replay`, `GET stats`.

## Run

```bash
make build                       # frontend -> embedded -> bin/rlcd-gateway
./bin/rlcd-gateway               # proxy + dashboard on 127.0.0.1:4777
ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude   # try it without changing settings
```

To make it permanent, `rlcd-gateway setup claude` writes `ANTHROPIC_BASE_URL` into
`~/.claude/settings.json` and leaves a backup. `rlcd-gateway undo claude` reverts it.

Development uses two terminals: `make dev-gateway`, and `make dev-ui` (Vite on :5177,
with `/api` forwarded to the gateway).

Config and logs live in `~/.rlcd-gateway/`, or in `$RLCD_GATEWAY_HOME` if set. Logs
contain full prompts; set `"log_bodies": false` to keep summaries only.
Credentials are masked in logs and are never returned by the dashboard API.
The dashboard only accepts requests addressed to the loopback host it is bound to.

## Layout

```
gateway/    Go, stdlib only
  cmd/rlcd-gateway   CLI: serve, setup/undo claude
  internal/proxy     request path, routing, SSE passthrough, usage capture
  internal/ir        request -> keyed blocks (foundation for pruning)
  internal/selector  System One client (Jev / open-rlcd)
  internal/api       dashboard JSON API + live event stream
  internal/web       embedded dashboard
frontend/   React + Vite dashboard, built into gateway/internal/web/dist
```

## Roadmap

- **F0** ✅ transparent proxy, routes, context X-ray, live dashboard
- **F1** ✅ pruning with sticky decisions, cache-aware epochs, shadow mode,
  and a kept/dropped diff view with per-block feedback and replay
- **F2** model routing
- **F3** Codex and OpenCode adapters
- **F4** `rlcd_recall(key)` so the model can ask for pruned content back

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
