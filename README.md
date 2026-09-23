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

## Agents

Every agent is pointed at the gateway through its base URL only; its login is not
touched. Try the one-off command first. `rlcd-gateway setup <agent>` makes it permanent
and `rlcd-gateway undo <agent>` puts back exactly what was there before. The dashboard's
Agents tab shows the same status and buttons (each asks for confirmation).

| Agent | Try it once | `setup` changes |
|---|---|---|
| Claude Code | `ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude` | `env.ANTHROPIC_BASE_URL` in `~/.claude/settings.json` |
| Codex CLI | `codex -c model_provider=rlcd_gateway -c 'model_providers.rlcd_gateway={name="RLCD Gateway", base_url="http://127.0.0.1:4777/openai/v1", wire_api="responses", requires_openai_auth=true, supports_websockets=false}'` | the same provider, in two marked blocks of `~/.codex/config.toml` |
| OpenCode | `OPENCODE_CONFIG_CONTENT='{"provider":{"anthropic":{"options":{"baseURL":"http://127.0.0.1:4777/v1"}},"openai":{"options":{"baseURL":"http://127.0.0.1:4777/openai/v1"}}}}' opencode` | `provider.anthropic` / `provider.openai` `options.baseURL` in `~/.config/opencode/opencode.json` |

Every edit keeps the rest of the file, writes a timestamped backup next to it first, and
refuses a file it cannot parse (for example an OpenCode `.jsonc` with comments). The
previous value of each key is kept in `~/.rlcd-gateway/agents/`, so undo restores it
instead of just deleting the key; an untouched file comes back byte for byte.

**Claude Code.** A subscription stays a subscription: the gateway forwards the login
unchanged. The Agents tab shows which settings level is in effect, because a higher one
wins over `~/.claude/settings.json`: managed settings, then `.claude/settings.local.json`,
then `.claude/settings.json` in the project. An `env` value in any settings file also wins
over the same variable exported in the shell
([settings](https://code.claude.com/docs/en/settings),
[env vars](https://code.claude.com/docs/en/env-vars)). It also shows which credential
Claude Code will send, in its documented order: Bedrock/Vertex/Foundry (which bypass the
base URL), `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, `apiKeyHelper`,
`CLAUDE_CODE_OAUTH_TOKEN`, then the `/login` subscription
([authentication](https://code.claude.com/docs/en/authentication)). Values are never shown.

**Codex CLI.** The built-in `openai` provider can't be redefined and always tries
WebSockets first, so setup adds a custom provider. `requires_openai_auth = true` makes
Codex send its own login, and a custom `base_url` is honoured for a **ChatGPT
subscription** login as well as for an API key
([config reference](https://learn.chatgpt.com/docs/config-file/config-reference),
[provider source](https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs)).
The gateway tells the two apart by the credential. A ChatGPT token plus
`ChatGPT-Account-ID` goes to `https://chatgpt.com/backend-api/codex`. An API key goes to
`https://api.openai.com/v1`. Both upstreams can be changed in the `adapters` section of
the config (`openai_base_url`, `chatgpt_base_url`). Codex's own `chatgpt_base_url` setting
only covers the login flow, not model traffic.

**OpenCode.** Its `anthropic` provider uses the gateway's Anthropic path, so it gets
everything Claude Code gets. OpenAI API keys go through the Responses adapter. OpenCode's
ChatGPT Plus/Pro login is different. Its plugin sends every request to
`chatgpt.com/backend-api/codex/responses` and ignores `baseURL`. That traffic can't be
redirected, and only a TLS-intercepting proxy could see it, which this project does not
build.

Codex and OpenAI-format traffic shows up in the Traffic tab with usage and the Context
X-ray. Pruning and routing only apply to the Anthropic Messages path for now.

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
- **F3** ✅ Codex (Responses API, ChatGPT subscription or API key) and OpenCode
  passthrough with usage and X-ray; `setup`/`undo` and status for Claude Code, Codex
  and OpenCode. Pruning of OpenAI formats is not done yet
- **F4** `rlcd_recall(key)` so the model can ask for pruned content back

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
