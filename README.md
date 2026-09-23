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

Pruning (coming in F1) is decided by a System One model that answers per block
key and never rewrites text. Jev and open-rlcd share the same API
(`POST /v1/systemone`), so the backend is just configuration:

| Backend | Base URL | Token |
|---|---|---|
| `open-rlcd-local` | wherever you run it | optional |
| `open-rlcd-cloud` | hosted open-rlcd | required |
| `jev` | `https://api.typesafe.ai` | TypeSafe token |

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

## Routing

Without rules, the active route serves every request. With rules (dashboard, Routing
tab), each request is routed on its own: the first enabled rule whose conditions all
hold picks the route, and no match means the active route. Conditions cover the client
model (regex), estimated context size, tools, images, thinking, `max_tokens`, request
headers, the conversation id, and **background requests**.

Background requests are the side calls Claude Code makes next to the main loop:
quota probes (`max_tokens: 1`) and one-shot queries such as session titles and
tool-use summaries, which send no tools, no thinking, a single message and no
`cache_control`. Main-loop and subagent turns always carry tools and cache markers,
so they never match.

Decisions are **sticky per conversation** by default. The first decision of a
conversation holds for all its turns, because switching model halfway discards the
provider's prompt cache and invalidates signed thinking blocks. Pins live in
`router/sticky.json`, survive restarts and expire after a quiet period. Background
requests never create a pin and can bypass it. Only rules marked "override stickiness"
can move a pinned conversation.

An **auto rule** asks the economy model to pick among routes that have a description
("fast cheap model for simple questions", "strongest model for complex refactors").
It runs only at conversation start, with a short timeout. On any failure the next rule
decides. The economy model only picks a key; it never writes text.

Every decision is logged with a one-line reason (`rule 'background' matched: …`,
`sticky: conversation started on 'claude-sub'`). The dry run replays any logged request
through the same code and shows why each rule matched or not.

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
- **F1** pruning of old tool results with sticky decisions, shadow mode,
  and a kept/dropped diff view with per-block feedback
- **F2** ✅ per-request routing rules, sticky per conversation, background-request
  detection, economy-model auto rule, dry run
- **F3** Codex and OpenCode adapters
- **F4** `rlcd_recall(key)` so the model can ask for pruned content back

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
