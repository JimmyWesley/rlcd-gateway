import { useState } from 'react';
import { routerApi, type RouteInput, type RouterRoute } from './types';

type Props = { routes: RouterRoute[]; onSaved: (rs: RouterRoute[]) => void; onError: (e: string) => void };

type Draft = RouteInput & { name: string; isNew: boolean; has_key: boolean; original_base_url: string };

const PRESETS: Record<string, { label: string; draft: Partial<Draft> }> = {
  openrouter: {
    label: 'OpenRouter model',
    draft: { kind: 'openrouter', base_url: 'https://openrouter.ai/api', auth: 'key', api_key_env: 'OPENROUTER_API_KEY', model: '' },
  },
  anthropic_key: {
    label: 'Anthropic API key',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'key', api_key_env: 'ANTHROPIC_API_KEY', model: '' },
  },
  anthropic_login: {
    label: 'Your Claude login',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'passthrough', api_key_env: '', model: '' },
  },
};

function fromRoute(r: RouterRoute): Draft {
  return {
    name: r.name, isNew: false, has_key: r.has_key, original_base_url: r.base_url,
    kind: r.kind, base_url: r.base_url, auth: r.auth, model: r.model ?? '',
    api_key_env: r.api_key_env ?? '', description: r.description ?? '', api_key: '',
  };
}

export function RoutesEditor({ routes, onSaved, onError }: Props) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);

  const startNew = (preset: keyof typeof PRESETS) => {
    const taken = new Set(routes.map((r) => r.name));
    let name = preset === 'openrouter' ? 'openrouter-2' : preset === 'anthropic_key' ? 'anthropic-key' : 'claude-login';
    for (let n = 2; taken.has(name); n++) name = `${name.replace(/-\d+$/, '')}-${n}`;
    setDraft({
      name, isNew: true, has_key: false, original_base_url: '', description: '', api_key: '',
      kind: 'openrouter', base_url: '', auth: 'key', model: '', api_key_env: '',
      ...PRESETS[preset].draft,
    });
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const { name, kind, base_url, auth, model, api_key, api_key_env, clear_key, description } = draft;
      const body: RouteInput = { kind, base_url, auth, model, api_key_env, description, ...(api_key ? { api_key } : {}), ...(clear_key ? { clear_key } : {}) };
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

  const hostChanged = !!draft && !draft.isNew && draft.base_url.replace(/\/+$/, '') !== draft.original_base_url.replace(/\/+$/, '');

  return (
    <section className="panel rt-section">
      <div className="rt-head">
        <h3>Routes</h3>
        <span className="muted small">Where a request can go. A description lets the auto rule consider the route.</span>
        <span className="spacer" />
        {Object.entries(PRESETS).map(([k, p]) => (
          <button key={k} onClick={() => startNew(k as keyof typeof PRESETS)}>+ {p.label}</button>
        ))}
      </div>

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
                  {r.active && <span className="tag">active</span>}
                </td>
                <td className="mono small">
                  {r.kind} · {r.base_url.replace(/^https?:\/\//, '')}
                  {r.model && <div>model {r.model}</div>}
                </td>
                <td className="small">
                  {r.auth === 'passthrough' ? (
                    <span className="muted">your login</span>
                  ) : r.has_key ? (
                    <span>gateway key{r.api_key_env ? ` · $${r.api_key_env}` : ''}</span>
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
                    disabled={r.active || r.used_by.length > 0}
                    title={r.active ? 'The active route cannot be deleted' : r.used_by.length ? 'Used by rules' : 'Delete route'}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

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
                <option value="anthropic">anthropic</option>
                <option value="openrouter">openrouter</option>
              </select>
            </label>
            <label>
              Base URL
              <input value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
            </label>
            <label>
              Model <span className="muted">(replaces the client's model; empty keeps it)</span>
              <input
                value={draft.model}
                placeholder={draft.kind === 'openrouter' ? 'anthropic/claude-sonnet-4.5' : 'keep the client model'}
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
            <label className="rt-wide">
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
