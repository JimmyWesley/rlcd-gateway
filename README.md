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

## Recall

When pruning omits a block, the model sees a marker in its place:

```
[rlcd: ~1200 tokens omitted · key toolu_01A09q90… · req 20260923T010203-0a1b2c3d4e5f6a7b · call rlcd_recall to restore]
```

The gateway serves an MCP server at `http://127.0.0.1:4777/mcp` (Streamable HTTP,
protocol revision 2026-07-28, and the `initialize`-based 2025-11-25, 2025-06-18 and
2025-03-26 revisions for older clients). Its one tool, `rlcd_recall`, takes the `key`
and `req` from a marker (or the whole `marker`) and returns the original content from
the logged request body. Register it once with Claude Code:

```bash
claude mcp add --transport http --scope user rlcd-gateway http://127.0.0.1:4777/mcp
```

The model then sees the tool as `mcp__rlcd-gateway__rlcd_recall`. Recall needs
`"log_bodies": true`. Settings live in the `recall` section of the config
(`enabled`, default true; `max_bytes` per recall, default 100000, larger content is
truncated with a notice).

Every recall is appended to `recall/events.jsonl` in the config directory. A recall
means pruning dropped something the model needed, so events are the pruner's
"should keep" feedback. One JSON object per line; fields are only ever added:

| Field | Meaning |
|---|---|
| `time` | when the tool was called |
| `conversation_id` | conversation of the request the content came from |
| `req`, `key` | what the model asked for |
| `block_key`, `tool_use_id`, `tool`, `kind` | the block it resolved to |
| `tokens`, `bytes`, `truncated` | estimated size of the original; bytes returned |
| `ok`, `error`, `message` | outcome; `error` is `bad_args`, `disabled`, `unknown_request`, `body_not_logged`, `key_not_found` or `store_error` |
| `client` | the MCP client's self-reported name |

The dashboard reads them from `GET /api/recall/events?limit=N` and
`GET /api/recall/stats`; `GET`/`PUT /api/recall/settings` read and change the settings.

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
- **F2** ✅ per-request routing rules, sticky per conversation, background-request
  detection, economy-model auto rule, dry run
- **F3** ✅ Codex (Responses API, ChatGPT subscription or API key) and OpenCode
  passthrough with usage and X-ray; `setup`/`undo` and status for Claude Code, Codex
  and OpenCode. Pruning of OpenAI formats is not done yet
- **F4** ✅ `rlcd_recall` MCP tool so the model can ask for pruned content back,
  with every recall logged as feedback for the pruner

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
