import { useEffect, useMemo, useState } from 'react';
import { copyText, type GatewayConfig } from '../api';
import { routerApi, type AliasView, type RouterRoute } from '../routing/types';

type Lang = 'python' | 'node' | 'curl' | 'anthropic' | 'env';

const TABS: [Lang, string][] = [
  ['python', 'OpenAI Python'],
  ['node', 'OpenAI Node'],
  ['curl', 'curl'],
  ['anthropic', 'Anthropic Python'],
  ['env', 'Environment'],
];

// Quick start for any app: point an SDK's base URL at the gateway.
export function AppsPanel({ config }: { config: GatewayConfig }) {
  const [aliases, setAliases] = useState<AliasView[]>([]);
  const [routes, setRoutes] = useState<RouterRoute[]>([]);
  const [lang, setLang] = useState<Lang>('python');
  const [model, setModel] = useState('');
  const [key, setKey] = useState('');

  useEffect(() => {
    routerApi.aliases().then((a) => {
      setAliases(a);
      setModel((m) => m || a.find((x) => x.protocols.includes('openai-chat'))?.name || '');
    }).catch(() => {});
    routerApi.routes().then(setRoutes).catch(() => {});
  }, []);

  // Beyond loopback, the address this page was opened on is the one other
  // machines use; on loopback, the bound address (the dev server differs).
  const base = config.exposed ? window.location.origin : `http://${config.listen}`;
  const apiKey = key.trim() || 'rlcd-YOUR-GATEWAY-KEY';
  const oaiModel = model || 'gpt-4.1';
  const anthModel = aliases.find((a) => a.protocols.includes('anthropic-messages'))?.name || 'claude-sonnet-4-5';
  const active = routes.find((r) => r.active);
  const openaiDefault = routes.find((r) => r.openai_default);

  const snippet = useMemo(() => {
    switch (lang) {
      case 'python':
        return `from openai import OpenAI

client = OpenAI(base_url="${base}/v1", api_key="${apiKey}")
reply = client.chat.completions.create(
    model="${oaiModel}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(reply.choices[0].message.content)`;
      case 'node':
        return `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}/v1", apiKey: "${apiKey}" });
const reply = await client.chat.completions.create({
  model: "${oaiModel}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(reply.choices[0].message.content);`;
      case 'curl':
        return `curl ${base}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${oaiModel}", "messages": [{"role": "user", "content": "Hello"}]}'

# The models the gateway serves (its aliases):
curl ${base}/v1/models -H "Authorization: Bearer ${apiKey}"`;
      case 'anthropic':
        return `import anthropic

client = anthropic.Anthropic(base_url="${base}", api_key="${apiKey}")
message = client.messages.create(
    model="${anthModel}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello"}],
)
print(message.content[0].text)`;
      case 'env':
        return `# Most SDKs and tools read these:
export OPENAI_BASE_URL=${base}/v1
export OPENAI_API_KEY=${apiKey}

export ANTHROPIC_BASE_URL=${base}
export ANTHROPIC_API_KEY=${apiKey}`;
    }
  }, [lang, base, apiKey, oaiModel, anthModel]);

  const [copied, setCopied] = useState(false);
  const copy = async () => {
    setCopied(await copyText(snippet));
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <main className="apps">
      <section className="panel rt-section">
        <h2>Use it from any app</h2>
        <p className="muted">
          Point an SDK's base URL at the gateway. It routes the call (a model alias picks the provider and the real model),
          prunes the context, and returns the provider's response untouched, streaming included.
        </p>
        <div className="apps-facts">
          <div className="fact"><span className="label">OpenAI base URL</span><span className="value mono">{base}/v1</span></div>
          <div className="fact"><span className="label">Anthropic base URL</span><span className="value mono">{base}</span></div>
          <div className="fact"><span className="label">Unclaimed OpenAI calls go to</span>
            <span className="value">{openaiDefault ? openaiDefault.name : 'OpenAI / ChatGPT, with the client’s login'}</span></div>
          <div className="fact"><span className="label">Unclaimed Anthropic calls go to</span><span className="value">{active?.name ?? config.active_route}</span></div>
        </div>

        <div className="form apps-inputs">
          <label>
            Model
            <select value={model} onChange={(e) => setModel(e.target.value)}>
              <option value="">gpt-4.1 (no alias: sent as-is)</option>
              {aliases.map((a) => <option key={a.name} value={a.name}>{a.name} → {a.route}{a.model ? ` · ${a.model}` : ''}</option>)}
            </select>
          </label>
          <label>
            API key <span className="muted">(paste a gateway key to fill the snippet; it stays in this page)</span>
            <input type="password" autoComplete="off" value={key} placeholder="rlcd-…" onChange={(e) => setKey(e.target.value)} />
          </label>
        </div>

        <nav className="tabs apps-tabs">
          {TABS.map(([l, label]) => (
            <button key={l} className={lang === l ? 'active' : ''} onClick={() => setLang(l)}>{label}</button>
          ))}
        </nav>
        <div className="apps-snippet">
          <pre className="mono">{snippet}</pre>
          <button onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
        </div>

        <ul className="muted small apps-notes">
          <li>
            With a gateway key the app holds no provider key: the gateway removes it and the route injects its own. That needs a
            route that holds a key. A route that forwards the client's own login (like a Claude subscription) needs the app to send
            that login, and the gateway key in an <span className="mono">X-Rlcd-Key</span> header.
          </li>
          <li>
            Formats are never translated. OpenAI SDKs reach OpenAI-compatible routes; the Anthropic SDK reaches Anthropic routes.
            For Claude models from an OpenAI SDK, use an OpenRouter route of kind <span className="mono">openai</span>.
          </li>
          <li>
            {config.exposed
              ? 'The gateway listens beyond this machine, so every call needs a gateway key.'
              : config.require_keys
                ? 'Keys are required on this machine too (Keys tab).'
                : 'On this machine a key is optional: without one the call goes through with the client’s own login.'}
          </li>
        </ul>
      </section>
    </main>
  );
}
