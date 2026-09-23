import { useEffect, useState } from 'react';
import { api, type GatewayConfig, type ProbeResult, type SelectorView } from './api';

const BACKENDS: Record<SelectorView['backend'], { title: string; body: string }> = {
  'open-rlcd-local': {
    title: 'open-rlcd · self-hosted',
    body: 'You run open-rlcd yourself and point the gateway at it. Context never leaves your network.',
  },
  'open-rlcd-cloud': {
    title: 'open-rlcd · hosted',
    body: 'The hosted open-rlcd service. Needs a token.',
  },
  jev: {
    title: 'Jev · TypeSafe',
    body: 'TypeSafe’s hosted Jev, same API. Needs a TypeSafe token.',
  },
};

type Props = { config: GatewayConfig; onSaved: (s: SelectorView) => void };

export function SelectorPanel({ config, onSaved }: Props) {
  const [presets, setPresets] = useState<SelectorView[]>([]);
  const [form, setForm] = useState<SelectorView>(config.selector);
  const [token, setToken] = useState('');
  const [saving, setSaving] = useState(false);
  const [probe, setProbe] = useState<ProbeResult | null>(null);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    api.presets().then(setPresets).catch(() => {});
  }, []);

  const pick = (backend: SelectorView['backend']) => {
    // Switching backend loads its defaults, except a base URL you already typed for the same backend.
    const p = presets.find((x) => x.backend === backend);
    setForm(backend === config.selector.backend ? config.selector : { ...(p ?? form), backend, has_token: false });
    setProbe(null);
  };

  const save = async () => {
    setSaving(true);
    try {
      const s = await api.setSelector({ ...form, ...(token ? { token } : {}) });
      setToken('');
      setForm(s);
      onSaved(s);
      return true;
    } catch (e) {
      setProbe({ ok: false, error: String(e) });
      return false;
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setProbe(null);
    if (await save()) setProbe(await api.testSelector().catch((e) => ({ ok: false as const, error: String(e) })));
    setTesting(false);
  };

  const needsToken = form.backend !== 'open-rlcd-local';
  const keep = probe?.ok ? probe.result.answers.keep?.noul : undefined;

  return (
    <main className="panel selector">
      <h2>Economy model</h2>
      <p className="muted">
        The model that decides what stays in the context. It only answers per block key, so it cannot rewrite or invent
        content. Jev and open-rlcd share the same API, so switching is only configuration.
      </p>

      <div className="cards">
        {(Object.keys(BACKENDS) as SelectorView['backend'][]).map((b) => (
          <button key={b} className={`card ${form.backend === b ? 'active' : ''}`} onClick={() => pick(b)}>
            <strong>{BACKENDS[b].title}</strong>
            <span>{BACKENDS[b].body}</span>
          </button>
        ))}
      </div>

      <div className="form">
        <label>
          Base URL
          <input
            value={form.base_url}
            placeholder={form.backend === 'open-rlcd-cloud' ? 'https://…' : 'http://127.0.0.1:8000'}
            onChange={(e) => setForm({ ...form, base_url: e.target.value })}
          />
        </label>
        <label>
          Model
          <input value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} />
        </label>
        <label>
          Token {needsToken ? '' : <span className="muted">(optional)</span>}
          <input
            type="password"
            autoComplete="off"
            value={token}
            placeholder={form.has_token ? 'set — leave empty to keep it' : 'paste token'}
            onChange={(e) => setToken(e.target.value)}
          />
        </label>
        <label>
          …or read it from env var
          <input
            value={form.token_env ?? ''}
            placeholder="TYPESAFE_API_KEY"
            onChange={(e) => setForm({ ...form, token_env: e.target.value })}
          />
        </label>
      </div>
      <p className="muted small">Saved to {config.config_path} (file mode 600). Tokens are never sent back to this page.</p>

      <div className="actions">
        <button className="primary" onClick={save} disabled={saving}>Save</button>
        <button onClick={test} disabled={testing || !form.base_url}>{testing ? 'Testing…' : 'Save & test connection'}</button>
      </div>

      {probe && (
        <div className={`probe ${probe.ok ? 'ok' : 'bad'}`}>
          {probe.ok ? (
            <>
              <strong>Connected.</strong> {probe.result.wall_ms} ms end to end
              {probe.result.forward_ms != null && <> · {probe.result.forward_ms} ms model compute</>}
              {keep != null && (
                <div className="muted">
                  Probe: is “npm install finished: added 812 packages” needed to fix a login bug? keep = {keep.toFixed(3)}
                </div>
              )}
            </>
          ) : (
            <><strong>Failed.</strong> {probe.error}</>
          )}
        </div>
      )}
    </main>
  );
}
