# RLCD Gateway

An LLM gateway for coding agents (Claude Code, Codex, OpenCode) and for any app
that uses an OpenAI or Anthropic SDK. Point the client's base URL at it: the
gateway routes each call to the provider you choose (Anthropic, OpenRouter,
OpenAI, Groq, Together, DeepSeek, Mistral, Ollama, vLLM, LM Studio…), prunes
the context the model no longer needs, and returns the provider's response
untouched, streaming included. The dashboard shows exactly what every call
sent, who sent it, where it went and what it cost.

```
Claude Code ─────┐                        ┌─► api.anthropic.com  (your own login, forwarded as-is)
Codex, OpenCode ─┼─► RLCD Gateway :4777 ──┼─► openrouter.ai      (a key the gateway holds)
your chatbot ────┘    │                   └─► groq, ollama, …    (any OpenAI-compatible API)
 (OpenAI SDK)         └─ dashboard http://127.0.0.1:4777/ui/
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

Every model call runs the same pipeline whatever its protocol: Anthropic
Messages (`POST /v1/messages`), OpenAI Chat Completions
(`POST /v1/chat/completions`) and OpenAI Responses (`POST /v1/responses`, also
under `/openai/v1`). A model alias or a rule picks the route, pruning runs,
then the route's shaping, and the call is forwarded in its own protocol. The
gateway never translates between protocols: a route of kind `anthropic` or
`openrouter` serves Anthropic Messages, a route of kind `openai` serves the two
OpenAI formats, and a configuration that would cross them is refused with a
clear error. (OpenRouter serves Claude models over the OpenAI format too, at
`https://openrouter.ai/api/v1`, so an OpenAI SDK can reach Claude through an
`openai` route.)

## Use it from any app

Set the SDK's base URL to the gateway and use a model alias (see Routing) or
any model name the route's provider knows:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:4777/v1", api_key="rlcd-…")
reply = client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "Hello"}],
)
print(reply.choices[0].message.content)
```

```js
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://127.0.0.1:4777/v1", apiKey: "rlcd-…" });
const reply = await client.chat.completions.create({
  model: "smart",
  messages: [{ role: "user", content: "Hello" }],
});
```

```bash
curl http://127.0.0.1:4777/v1/chat/completions \
  -H "Authorization: Bearer rlcd-…" -H "Content-Type: application/json" \
  -d '{"model": "smart", "messages": [{"role": "user", "content": "Hello"}]}'
curl http://127.0.0.1:4777/v1/models -H "Authorization: Bearer rlcd-…"   # the aliases
```

```python
import anthropic

client = anthropic.Anthropic(base_url="http://127.0.0.1:4777", api_key="rlcd-…")
message = client.messages.create(model="claude-sonnet-4-5", max_tokens=1024,
                                 messages=[{"role": "user", "content": "Hello"}])
```

Most tools also read `OPENAI_BASE_URL=http://127.0.0.1:4777/v1` or
`ANTHROPIC_BASE_URL=http://127.0.0.1:4777`. The dashboard's Apps tab fills
these snippets in with your address, aliases and key.

`rlcd-…` is a gateway key (see Gateway keys): the app never holds a provider
key. On loopback a key is optional, and an app can send its own provider key
instead, which a `passthrough` route forwards. An OpenAI-format call that no
alias or rule claims goes to `default_openai_route`, or, when that is unset,
to OpenAI or the ChatGPT backend depending on the client's own login (as
Codex needs). Other OpenAI endpoints under `/openai/v1` (and
`POST /v1/embeddings`) are passed through to that same default without
routing or pruning.

`GET /v1/models` and `GET /openai/v1/models` list the aliases in a shape both
SDK families read (OpenAI's `{object: "list", data: [{id, object, created,
owned_by}]}` with Anthropic's `type`, `display_name`, `created_at`,
`has_more`, `first_id` and `last_id` alongside). They answer from the gateway
for gateway-key clients and, once aliases exist, for other OpenAI clients; an
Anthropic SDK (`anthropic-version` header) or a ChatGPT login still gets its
provider's own list, as before.

Every request record names the client (`client`: `{id, name, version, kind,
key_name}`, detected from the User-Agent, `X-Stainless-*`, `originator` and
Claude Code's headers; ids such as `claude-code`, `codex`, `opencode`,
`openai-python`, `anthropic-node`, `curl`), the `provider` behind the route
(explicit, or from the base URL host: `anthropic`, `openrouter`, `openai`,
`groq`, `together`, `deepseek`, `mistral`, `google`, `ollama`, `vllm`,
`lmstudio`, `custom`), the `model_vendor` of the model sent (`qwen/qwen3-…` →
`qwen`), the gateway key, and an estimated cost.

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

Before each model call is forwarded (any protocol), the gateway looks at the old blocks of the
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
using a per-model price table you can override (Anthropic and OpenAI-compatible list
prices as of 2026-06, labelled as estimates). Token counts are chars/4 estimates. Epoch
turns can cost more than they save, so savings can be negative early in a session.

The cost model follows the protocol. OpenAI-format providers cache prompt prefixes on
their own (from 1024 tokens on OpenAI) with cached input discounted and no write
premium, so an uncached token is priced at plain input rather than 1.25×; the
estimate assumes the same rule for every OpenAI-compatible provider. Local servers are
priced by the `default` row unless you add a zero row for their models.

**OpenAI formats.** Chat Completions and Responses requests are pruned with the same
invariants. The tool results are `role: "tool"` messages (`tool_call_id`) and
`function_call_output` items (`call_id`); only their output becomes the marker (a string
stays a string, a list of parts becomes one text part), and every call keeps its output.
Assistant `tool_calls`, `function_call` and `reasoning` items, images, files and tool
definitions are never touched, and the rewritten body is checked field by field against
the original before it is forwarded: nothing outside the message list may change. Call
ids that repeat within a conversation (some servers number them per response) are keyed
by content and named by block key in their markers. A compaction request
(`/responses/compact`) is routed but never pruned.

Plain chatbot conversations have no tool output. For them `prune_conversation_text`
(off by default) makes long user and assistant messages older than `keep_last_n_turns`
candidates too; `always_keep_user_text` still protects user text. It is off because
dropping old turns changes what the model remembers of the conversation, and a chat app
usually has no recall tool to get them back. Anthropic text blocks are candidates as
before.

Mark any decision as "should have kept" or "should have dropped" in the request view.
The Pruning tab replays those cases against the current criteria and threshold, which
works as a regression suite for criteria changes. Every block the model got back with
`rlcd_recall` is a case too ("recall" in the list, verdict "should keep", with the
snapshot of the request that dropped it), and it is never dropped again in that
conversation. A drop that was already sent stays as its marker: restoring it would
rewrite the cached prefix, and the recall result already holds the content.

API, under `/api/prune`: `GET|PUT config`, `GET presets`, `GET|POST feedback`,
`DELETE feedback/{id}` (not for recall cases), `POST replay`, `GET stats`.

## Run

```bash
make build                       # frontend -> embedded -> bin/rlcd-gateway
./bin/rlcd-gateway               # proxy + dashboard on 127.0.0.1:4777
ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude   # try it without changing settings
OPENAI_BASE_URL=http://127.0.0.1:4777/v1 python my_app.py   # any OpenAI SDK app
./bin/rlcd-gateway help          # every command and option
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
X-ray, and is routed and pruned like the Anthropic path.

Development uses two terminals: `make dev-gateway`, and `make dev-ui` (Vite on :5177,
with `/api` forwarded to the gateway).

Config and logs live in `~/.rlcd-gateway/`, or in `$RLCD_GATEWAY_HOME` if set. Logs
contain full prompts; set `"log_bodies": false` to keep summaries only.
Credentials are masked in logs and are never returned by the dashboard API.
On loopback the gateway only accepts requests addressed to the loopback host and port it
is bound to; see Gateway keys for listening beyond this machine.

## Gateway keys

A gateway key (`rlcd-` followed by 64 hex characters) lets an app call the gateway
without holding any provider key. Create one in the Keys tab or with
`rlcd-gateway keys create <name> [-rpm N] [-tokens-per-day N] [-aliases a,b] [-routes r,s]`.
It is shown once. The gateway stores only its SHA-256 hash (in `keys.json`, mode 600):
the key is 256 random bits, so a slow password hash would add nothing.

An app sends the key where its SDK sends an API key (`Authorization: Bearer`,
`x-api-key` or `api-key`). The gateway checks it, removes it, and the route injects its
own provider key, so neither the provider nor the request log ever sees the gateway key.
A route that forwards the client's own login (a Claude subscription) cannot serve a
request that only carries a gateway key; the client then sends its login as usual and
the gateway key in `X-Rlcd-Key`. Each key can be limited to some aliases and routes, to
requests per minute (checked before forwarding; `429` with `Retry-After`) and to tokens
per UTC day (checked before forwarding against what earlier calls used, so the call that
crosses the limit still completes). Usage and estimated cost are kept per key and shown
in the Keys tab, in `GET /api/stats` (`by_key`) and on every request. Conversation ids,
and with them pruning decisions and routing pins, are scoped to the key, and
`rlcd_recall` called with a key only reaches that key's requests.

**Listening beyond this machine.** The listen address decides the mode:

- *Loopback* (`127.0.0.1:4777`, the default): as before. Requests must be addressed to
  the loopback host and port the gateway is bound to (which stops DNS rebinding). Keys
  are optional: a request carrying one is checked, one without is forwarded with the
  client's own login. `"require_keys": true` (or the switch in the Keys tab) makes keys
  mandatory anyway.
- *Exposed* (`-listen 0.0.0.0:4777`, or any non-loopback address): every request to
  `/v1`, `/openai` and `/mcp` needs a valid gateway key. The `Host` must be an IP
  address, `localhost`, or a name in `allowed_hosts` (config) or `-allow-host`; any
  other name is refused, which is what a DNS-rebinding page would send. The dashboard
  and `/api` are only served to a browser on the gateway's own machine (loopback peer
  and loopback `Host`), or to requests carrying the admin token from
  `RLCD_GATEWAY_ADMIN_TOKEN` (24 characters or more), as `Authorization: Bearer` or as
  the HttpOnly, SameSite=Strict session cookie the dashboard's login form gets from
  `POST /auth/admin` (the cookie holds a hash of the token). Without an admin token
  there is no remote dashboard at all. A gateway key never opens the dashboard.

In both modes a state-changing dashboard request from another site's page (a foreign
`Origin`) is refused. The gateway speaks plain HTTP: beyond a trusted network, put it
behind a TLS-terminating reverse proxy on another host, since a proxy on the same host
would make every request look local to the dashboard check.

API: `GET|POST /api/keys`, `PUT /api/keys/{id}`, `POST /api/keys/{id}/revoke`,
`PUT /api/require-keys`.

## Routing

Routes have a kind: `anthropic` (api.anthropic.com), `openrouter` (OpenRouter's
Anthropic-compatible endpoint), or `openai` for any OpenAI-compatible API, whose base URL
is the one an OpenAI SDK takes (`https://openrouter.ai/api/v1`, `https://api.openai.com/v1`,
`https://api.groq.com/openai/v1`, `http://127.0.0.1:11434/v1` for Ollama, …). A route can
add headers to every upstream call (OpenRouter's `HTTP-Referer` and `X-Title`); they are
shown in the dashboard, so keys belong in `api_key` or `api_key_env`. The active route
(top bar) serves Anthropic requests; `default_openai_route` serves OpenAI ones.

**Model aliases** map the model name a client asks for (`smart`, `gpt-4o`) to a route and
an upstream model. An alias match is the simplest rule: it is checked before the rules
and never pins a conversation, since the client names it on every turn. An alias only
serves requests in its route's protocol; asking for it in the other one is a clear `400`.
`GET /v1/models` lists the aliases.

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

Rules can be limited to a protocol (`when.protocol`: `anthropic-messages`, `openai-chat`,
`openai-responses`, or `openai` for both). A rule that can only meet a protocol its route
does not speak is refused when saved; at run time a rule (or a sticky pin) whose route
speaks another protocol is skipped with that reason. Background detection only applies to
Anthropic requests: a plain chat completion without tools is an ordinary turn.

Every decision is logged with a one-line reason (`alias 'smart' → route 'groq'`,
`rule 'background' matched: …`, `sticky: conversation started on 'claude-sub'`). The dry
run replays any logged request, of any protocol, through the same code and shows why
each rule matched or not.

API, under `/api/router`: `GET|PUT rules`, `GET routes`, `PUT|DELETE routes/{name}`,
`GET|PUT aliases`, `PUT openai-default`, `POST dryrun`, `GET|DELETE conversations`.

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
`"log_bodies": true`, and resolves markers in Anthropic and OpenAI-format bodies alike.
An app without an MCP client cannot recall, so enforced pruning is lossy for it. Settings live in the `recall` section of the config
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
  cmd/rlcd-gateway   CLI: serve, setup/undo <agent>, keys list/create/revoke
  internal/server    assembles the gateway (engine, hooks, APIs, guard)
  internal/guard     front door: Host check, gateway keys, dashboard access
  internal/proxy     the request engine for every protocol, route shaping, /v1/models
  internal/adapters  OpenAI endpoints (Codex, OpenCode, SDKs), fallback upstreams, agents API
  internal/relay     shared forwarding: headers, masking, capture, SSE/JSON usage
  internal/pipeline  hook interfaces (router, transformers, models), markers, conversation ids
  internal/ir        request -> keyed blocks, per protocol
  internal/router    aliases, rules, stickiness, auto rule, dry run
  internal/prune     pruning, per-protocol dialects, feedback and replay
  internal/recall    rlcd_recall MCP server and its event log
  internal/keys      gateway keys: hashed store, limits, usage
  internal/pricing   price table and protocol-aware cost
  internal/clients   who made a call, from its headers
  internal/setup     setup/undo for Claude Code, Codex, OpenCode
  internal/config    config file, routes and providers
  internal/store     request log
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
- **F5** ✅ a general LLM gateway for any app: one pipeline for Anthropic Messages,
  OpenAI Chat Completions and Responses; OpenAI-compatible routes; model aliases and
  `/v1/models`; pruning of the OpenAI formats with a protocol-aware cost model; gateway
  keys with limits and per-key usage; a guarded non-loopback mode; recalls as pruning
  feedback; client, provider and model-vendor on every request

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
