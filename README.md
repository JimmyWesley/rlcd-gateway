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
- **F2** model routing
- **F3** Codex and OpenCode adapters
- **F4** `rlcd_recall(key)` so the model can ask for pruned content back

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
