import { useState } from 'react';
import { routerApi, type RouteInput, type RouterRoute } from './types';

type Props = { routes: RouterRoute[]; onSaved: (rs: RouterRoute[]) => void; onError: (e: string) => void };

type Draft = RouteInput & { name: string; isNew: boolean; has_key: boolean; original_base_url: string; headerRows: [string, string][] };

type Preset = { label: string; name: string; draft: Partial<Draft>; group: 'anthropic' | 'openai' };

// Anthropic-format routes serve /v1/messages; OpenAI-compatible ones serve
// /v1/chat/completions and /v1/responses. The gateway never translates.
const PRESETS: Preset[] = [
  { group: 'anthropic', label: 'Your Claude login', name: 'claude-login',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'passthrough', api_key_env: '' } },
  { group: 'anthropic', label: 'Anthropic API key', name: 'anthropic-key',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'key', api_key_env: 'ANTHROPIC_API_KEY' } },
  { group: 'anthropic', label: 'OpenRouter (Anthropic format)', name: 'openrouter-anthropic',
    draft: { kind: 'openrouter', base_url: 'https://openrouter.ai/api', auth: 'key', api_key_env: 'OPENROUTER_API_KEY' } },
  { group: 'openai', label: 'OpenRouter', name: 'openrouter-openai',
    draft: { kind: 'openai', base_url: 'https://openrouter.ai/api/v1', auth: 'key', api_key_env: 'OPENROUTER_API_KEY',
      headerRows: [['HTTP-Referer', 'https://github.com/JimmyWesley/rlcd-gateway'], ['X-Title', 'RLCD Gateway']] } },
  { group: 'openai', label: 'OpenAI', name: 'openai-key',
    draft: { kind: 'openai', base_url: 'https://api.openai.com/v1', auth: 'key', api_key_env: 'OPENAI_API_KEY' } },
  { group: 'openai', label: 'Groq', name: 'groq',
    draft: { kind: 'openai', base_url: 'https://api.groq.com/openai/v1', auth: 'key', api_key_env: 'GROQ_API_KEY' } },
  { group: 'openai', label: 'Together', name: 'together',
    draft: { kind: 'openai', base_url: 'https://api.together.xyz/v1', auth: 'key', api_key_env: 'TOGETHER_API_KEY' } },
  { group: 'openai', label: 'DeepSeek', name: 'deepseek',
    draft: { kind: 'openai', base_url: 'https://api.deepseek.com/v1', auth: 'key', api_key_env: 'DEEPSEEK_API_KEY' } },
  { group: 'openai', label: 'Mistral', name: 'mistral',
    draft: { kind: 'openai', base_url: 'https://api.mistral.ai/v1', auth: 'key', api_key_env: 'MISTRAL_API_KEY' } },
  { group: 'openai', label: 'Ollama', name: 'ollama',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:11434/v1', auth: 'passthrough', api_key_env: '' } },
  { group: 'openai', label: 'vLLM', name: 'vllm',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:8000/v1', auth: 'passthrough', api_key_env: '' } },
  { group: 'openai', label: 'LM Studio', name: 'lmstudio',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:1234/v1', auth: 'passthrough', api_key_env: '' } },
];

const KIND_LABEL: Record<string, string> = {
  anthropic: 'Anthropic Messages',
  openrouter: 'Anthropic Messages (OpenRouter)',
  openai: 'OpenAI-compatible',
};

function fromRoute(r: RouterRoute): Draft {
  return {
    name: r.name, isNew: false, has_key: r.has_key, original_base_url: r.base_url,
    kind: r.kind, base_url: r.base_url, auth: r.auth, model: r.model ?? '',
    api_key_env: r.api_key_env ?? '', description: r.description ?? '', api_key: '',
    headers: r.headers ?? {}, headerRows: Object.entries(r.headers ?? {}), provider: '',
  };
}

export function RoutesEditor({ routes, onSaved, onError }: Props) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [adding, setAdding] = useState(false);

  const startNew = (p: Preset) => {
    const taken = new Set(routes.map((r) => r.name));
    let name = p.name;
    for (let n = 2; taken.has(name); n++) name = `${p.name}-${n}`;
    setDraft({
      name, isNew: true, has_key: false, original_base_url: '', description: '', api_key: '',
      kind: 'openai', base_url: '', auth: 'key', model: '', api_key_env: '', headers: {}, headerRows: [], provider: '',
      ...p.draft,
    });
    setAdding(false);
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const { name, kind, base_url, auth, model, api_key, api_key_env, clear_key, description, headerRows, provider } = draft;
      const headers: Record<string, string> = {};
      for (const [k, v] of headerRows) if (k.trim()) headers[k.trim()] = v.trim();
      const body: RouteInput = {
        kind, base_url, auth, model, api_key_env, description, headers, provider,
        ...(api_key ? { api_key } : {}), ...(clear_key ? { clear_key } : {}),
      };
      onSaved(await routerApi.saveRoute(name.trim(), body));
      setDraft(null);
    } catch (e) {
      onError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (name: string) => {
    if (!confirm(`Delete route "${name}"? Its stored key is removed with it.`)) return;
    try {
      onSaved(await routerApi.deleteRoute(name));
      if (draft?.name === name) setDraft(null);
    } catch (e) {
      onError(String(e));
    }
  };

  const setDefault = async (name: string) => {
    try {
      onSaved(await routerApi.setOpenAIDefault(name));
    } catch (e) {
      onError(String(e));
    }
  };

  const hostChanged = !!draft && !draft.isNew && draft.base_url.replace(/\/+$/, '') !== draft.original_base_url.replace(/\/+$/, '');
  const openaiRoutes = routes.filter((r) => r.kind === 'openai');
  const openaiDefault = routes.find((r) => r.openai_default)?.name ?? '';
  const setRow = (i: number, row: [string, string]) =>
    draft && setDraft({ ...draft, headerRows: draft.headerRows.map((x, j) => (j === i ? row : x)) });

  return (
    <section className="panel rt-section">
      <div className="rt-head">
        <h3>Routes</h3>
        <span className="muted small">Where a request can go. A description lets the auto rule consider the route.</span>
        <span className="spacer" />
        <button onClick={() => setAdding(!adding)}>{adding ? 'Close' : '+ Route'}</button>
      </div>

      {adding && (
        <div className="rt-presets">
          {(['anthropic', 'openai'] as const).map((g) => (
            <div key={g} className="rt-preset-group">
              <span className="label">{g === 'anthropic' ? 'Anthropic Messages (Claude Code, Anthropic SDK)' : 'OpenAI-compatible (OpenAI SDKs, Codex, any chatbot)'}</span>
              <div className="rt-chips">
                {PRESETS.filter((p) => p.group === g).map((p) => (
                  <button key={p.name} className="rt-chip" onClick={() => startNew(p)}>{p.label}</button>
                ))}
              </div>
            </div>
          ))}
          <p className="muted small">
            The gateway never translates between the two formats. To use Claude models from an OpenAI SDK, add the OpenRouter
            route and an alias such as <span className="mono">anthropic/claude-sonnet-4.5</span>: OpenRouter serves them over the OpenAI format.
          </p>
        </div>
      )}

      <div className="scroll-x">
        <table className="rt-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Target</th>
              <th>Credentials</th>
              <th>Description</th>
              <th>Used by</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {routes.map((r) => (
              <tr key={r.name} className={draft?.name === r.name && !draft.isNew ? 'selected' : ''}>
                <td>
                  <strong>{r.name}</strong>
                  {r.active && <span className="tag" title="Serves Anthropic Messages requests no rule claims">active</span>}
                  {r.openai_default && <span className="tag" title="Serves OpenAI-format requests no alias or rule claims">OpenAI default</span>}
                </td>
                <td className="mono small">
                  <span className={`rt-proto ${r.kind === 'openai' ? 'oai' : 'anth'}`}>{r.kind === 'openai' ? 'OpenAI' : 'Anthropic'}</span>
                  {' '}{r.provider} · {r.base_url.replace(/^https?:\/\//, '')}
                  {r.model && <div>model {r.model}</div>}
                  {Object.keys(r.headers ?? {}).length > 0 && <div className="muted">+ {Object.keys(r.headers).join(', ')}</div>}
                </td>
                <td className="small">
                  {r.auth === 'passthrough' ? (
                    <span className="muted">client's own login</span>
                  ) : r.has_key ? (
                    <span>gateway holds a key{r.api_key_env ? ` · $${r.api_key_env}` : ''}</span>
                  ) : (
                    <span className="note">no key{r.api_key_env ? ` · $${r.api_key_env} unset` : ''}</span>
                  )}
                </td>
                <td className="small rt-desc">{r.description || <span className="muted">—</span>}</td>
                <td className="small">{r.used_by.length ? r.used_by.join(', ') : <span className="muted">—</span>}</td>
                <td className="rt-row-actions">
                  <button onClick={() => setDraft(fromRoute(r))}>Edit</button>
                  <button
                    onClick={() => remove(r.name)}
                    disabled={r.active || r.openai_default || r.used_by.length > 0}
                    title={r.active || r.openai_default ? 'A default route cannot be deleted' : r.used_by.length ? 'Used by rules or aliases' : 'Delete route'}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <label className="rt-inline rt-openai-default">
        OpenAI-format requests that no alias or rule claims go to
        <select value={openaiDefault} onChange={(e) => setDefault(e.target.value)}>
          <option value="">OpenAI or ChatGPT, by the client's own login (as before)</option>
          {openaiRoutes.map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
        </select>
      </label>

      {draft && (
        <div className="rt-route-form">
          <h4>{draft.isNew ? 'New route' : `Edit ${draft.name}`}</h4>
          <div className="form">
            <label>
              Name
              <input value={draft.name} disabled={!draft.isNew} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </label>
            <label>
              Kind
              <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value })}>
                {Object.entries(KIND_LABEL).map(([k, l]) => <option key={k} value={k}>{k}: {l}</option>)}
              </select>
            </label>
            <label>
              Base URL {draft.kind === 'openai' && <span className="muted">(the one an OpenAI SDK takes, usually ending in /v1)</span>}
              <input value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
            </label>
            <label>
              Model <span className="muted">(replaces the client's model; empty keeps it; an alias can set its own)</span>
              <input
                value={draft.model}
                placeholder={draft.kind === 'anthropic' ? 'keep the client model' : 'e.g. anthropic/claude-sonnet-4.5'}
                onChange={(e) => setDraft({ ...draft, model: e.target.value })}
              />
            </label>
            <label>
              Credentials
              <select value={draft.auth} onChange={(e) => setDraft({ ...draft, auth: e.target.value })}>
                <option value="passthrough">passthrough: forward the client's own login</option>
                <option value="key">key: the gateway holds a key</option>
              </select>
            </label>
            <label>
              Description <span className="muted">(for the auto rule)</span>
              <input
                value={draft.description}
                placeholder="e.g. fast cheap model for simple questions"
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
              />
            </label>
            {draft.auth === 'key' && (
              <>
                <label>
                  API key
                  <input
                    type="password"
                    autoComplete="off"
                    value={draft.api_key ?? ''}
                    placeholder={draft.has_key && !hostChanged ? 'set — leave empty to keep it' : 'paste key'}
                    onChange={(e) => setDraft({ ...draft, api_key: e.target.value })}
                  />
                </label>
                <label>
                  …or read it from env var
                  <input
                    value={draft.api_key_env}
                    placeholder="OPENROUTER_API_KEY"
                    onChange={(e) => setDraft({ ...draft, api_key_env: e.target.value })}
                  />
                </label>
              </>
            )}
          </div>
          <div className="rt-headers">
            <span className="label">Extra headers <span className="muted small">sent upstream on every request, e.g. OpenRouter's HTTP-Referer and X-Title. Shown here, so not for secrets.</span></span>
            {draft.headerRows.map(([k, v], i) => (
              <div key={i} className="rt-kv-row">
                <input value={k} placeholder="Header-Name" onChange={(e) => setRow(i, [e.target.value, v])} />
                <input value={v} placeholder="value" onChange={(e) => setRow(i, [k, e.target.value])} />
                <button onClick={() => setDraft({ ...draft, headerRows: draft.headerRows.filter((_, j) => j !== i) })}>Remove</button>
              </div>
            ))}
            <button className="rt-link" onClick={() => setDraft({ ...draft, headerRows: [...draft.headerRows, ['', '']] })}>+ header</button>
          </div>
          {hostChanged && draft.auth === 'key' && (
            <p className="note small">The base URL changed: the stored key is not carried to a different host. Paste a key or use an env var.</p>
          )}
          {!draft.isNew && draft.auth === 'key' && draft.has_key && !hostChanged && (
            <label className="rt-check">
              <input type="checkbox" checked={!!draft.clear_key} onChange={(e) => setDraft({ ...draft, clear_key: e.target.checked })} />
              <span>Remove the stored key</span>
            </label>
          )}
          <p className="muted small">Keys are write-only: they are saved to the config file (mode 600) and never sent back to this page.</p>
          <div className="actions">
            <button className="primary" onClick={save} disabled={saving || !draft.name.trim() || !draft.base_url.trim()}>
              {saving ? 'Saving…' : 'Save route'}
            </button>
            <button onClick={() => setDraft(null)}>Cancel</button>
          </div>
        </div>
      )}
    </section>
  );
}
