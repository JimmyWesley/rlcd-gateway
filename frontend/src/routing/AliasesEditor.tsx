import { useEffect, useState } from 'react';
import { PROTOCOL_LABEL } from '../api';
import { routerApi, type Alias, type AliasView, type RouterRoute } from './types';

type Props = { routes: RouterRoute[]; onError: (e: string) => void; onSaved?: () => void };

// Model aliases: the model name a client asks for picks the route and the
// upstream model. GET /v1/models lists them.
export function AliasesEditor({ routes, onError, onSaved }: Props) {
  const [saved, setSaved] = useState<AliasView[]>([]);
  const [rows, setRows] = useState<Alias[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    routerApi.aliases().then((a) => {
      setSaved(a);
      setRows(a.map(strip));
    }).catch((e) => onError(String(e)));
  }, [onError]);

  const dirty = JSON.stringify(rows) !== JSON.stringify(saved.map(strip));
  const set = (i: number, patch: Partial<Alias>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const add = () => {
    const route = routes.find((r) => r.kind === 'openai')?.name ?? routes[0]?.name ?? '';
    let name = 'smart';
    for (let n = 2; rows.some((r) => r.name === name); n++) name = `smart-${n}`;
    setRows([...rows, { name, route, model: '', description: '' }]);
  };

  const save = async () => {
    setSaving(true);
    try {
      const out = await routerApi.saveAliases(rows.map((r) => ({ ...r, model: r.model?.trim() || undefined, description: r.description?.trim() || undefined })));
      setSaved(out);
      setRows(out.map(strip));
      onSaved?.();
    } catch (e) {
      onError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const routeOf = (name: string) => routes.find((r) => r.name === name);

  return (
    <section className="panel rt-section">
      <div className="rt-head">
        <h3>Model aliases</h3>
        <span className="muted small">
          The model name a client asks for picks the route and the model sent upstream. Checked before the rules, never pinned.
          Listed by <span className="mono">GET /v1/models</span>.
        </span>
        <span className="spacer" />
        <button onClick={add}>+ Alias</button>
      </div>
      {rows.length === 0 ? (
        <p className="muted">
          No aliases. Add one to let an app ask for <span className="mono">smart</span> or <span className="mono">gpt-4o</span> and
          have the gateway pick the provider and the real model.
        </p>
      ) : (
        <div className="scroll-x">
          <table className="rt-table rt-aliases">
            <thead>
              <tr>
                <th>Client asks for</th>
                <th>Route</th>
                <th>Upstream model</th>
                <th>Serves</th>
                <th>Description</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((a, i) => {
                const rt = routeOf(a.route);
                return (
                  <tr key={i}>
                    <td><input className="mono" value={a.name} onChange={(e) => set(i, { name: e.target.value })} /></td>
                    <td>
                      <select value={a.route} onChange={(e) => set(i, { route: e.target.value })}>
                        {routes.map((r) => <option key={r.name} value={r.name}>{r.name} ({r.provider})</option>)}
                      </select>
                    </td>
                    <td>
                      <input className="mono" value={a.model ?? ''} placeholder={rt?.model || 'send the alias as-is'}
                        onChange={(e) => set(i, { model: e.target.value })} />
                    </td>
                    <td className="small muted">{rt ? rt.protocols.map((p) => PROTOCOL_LABEL[p]).join(', ') : <span className="bad">no such route</span>}</td>
                    <td><input value={a.description ?? ''} placeholder="optional" onChange={(e) => set(i, { description: e.target.value })} /></td>
                    <td className="rt-row-actions"><button onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</button></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted small">
        An alias only serves requests in its route's format: an Anthropic route answers <span className="mono">/v1/messages</span>,
        an OpenAI-compatible one answers <span className="mono">/v1/chat/completions</span> and <span className="mono">/v1/responses</span>.
        Asking for an alias in the other format is refused with a clear error, never translated.
      </p>
      {(dirty || rows.length > 0) && (
        <div className="actions">
          <button className="primary" onClick={save} disabled={saving || !dirty}>{saving ? 'Saving…' : 'Save aliases'}</button>
          {dirty && <button onClick={() => setRows(saved.map(strip))}>Discard</button>}
        </div>
      )}
    </section>
  );
}

const strip = (a: AliasView | Alias): Alias => ({ name: a.name, route: a.route, model: a.model ?? '', description: a.description ?? '' });
