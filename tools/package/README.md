# RLCD Gateway

**A self-hosted gateway for LLMs and decision models.** Claude Code, Codex,
OpenCode and any OpenAI or Anthropic SDK app route every call to the provider
you choose, with the context the model no longer needs pruned to cut token
costs. Apps that call Jev or open-rlcd System One decision models change only
their base URL and get every decision audited and calibrated. Every request,
client and cost shows in a live dashboard. One binary, no system proxy, no
certificates.

![RLCD Gateway dashboard](https://raw.githubusercontent.com/JimmyWesley/rlcd-gateway/main/frontend/docs/screenshots/overview-dark-en.png)

![Flow: every client, route, rule and provider on one live graph](https://raw.githubusercontent.com/JimmyWesley/rlcd-gateway/main/frontend/docs/screenshots/flow-dark-en.png)

## Quick start

```bash
@INSTALL@
rlcd-gateway                  # proxy + dashboard on http://127.0.0.1:4777/ui/
```

Then point any client at it through its base URL. Its own login is kept:

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude             # Claude Code
OPENAI_BASE_URL=http://127.0.0.1:4777/v1 python my_app.py   # any OpenAI SDK app
rlcd-gateway setup claude                                   # make it permanent (codex, opencode too; undo: rlcd-gateway undo claude)
```

This package ships the prebuilt `rlcd-gateway` binary for macOS (arm64, x64),
Linux (x64, arm64) and Windows (x64), with the dashboard embedded.

## Why

- **One base URL for every model.** Anthropic, OpenRouter, OpenAI, Groq, Together,
  DeepSeek, Mistral, Ollama, vLLM, LM Studio… behind model aliases, per-request
  rules and fallbacks. Switch provider mid-session; the agent never notices.
- **Keep your subscription.** Claude Code and Codex logins are forwarded untouched,
  so a Claude or ChatGPT subscription still serves the turn.
- **Pay for fewer tokens.** Stale tool output is pruned in a prompt-cache-aware way,
  starting in shadow mode, and the model can ask for anything back with `rlcd_recall` (MCP).
- **See everything.** Every call's body, client, route, tokens and estimated cost,
  with a context X-ray of what the model actually received.
- **Survive provider errors.** Errors are classified and recovered (clamping,
  emergency pruning, backoff, route fallbacks) before the client sees them.
- **Audit your decision models.** A drop-in, Jev-compatible proxy for System One
  decisions (Jev and open-rlcd, `POST /v1/systemone`): answers byte for byte, every
  call logged, outcomes recorded for accuracy and calibration (ECE), a mirror for
  parity between backends, and routing rules driven by a decision's answer.
- **Share it safely.** Gateway keys with rate and token limits, so apps never hold
  a provider key.

## Use it from any app

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:4777/v1", api_key="rlcd-…")  # a gateway key
reply = client.chat.completions.create(
    model="smart",  # a model alias, or any model the route's provider knows
    messages=[{"role": "user", "content": "Hello"}],
)
```

The Anthropic SDKs work the same way with `base_url="http://127.0.0.1:4777"`, and
System One clients (Jev, open-rlcd) with the gateway as their base URL for
`POST /v1/systemone`.

## Links

- Documentation, screenshots and source: https://github.com/JimmyWesley/rlcd-gateway
- Releases and standalone downloads: https://github.com/JimmyWesley/rlcd-gateway/releases
- Issues: https://github.com/JimmyWesley/rlcd-gateway/issues

Apache License 2.0.
