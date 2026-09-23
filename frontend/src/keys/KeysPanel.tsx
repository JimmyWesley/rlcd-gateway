import { useCallback, useEffect, useState } from 'react';
import { api, copyText, fmt, type GatewayConfig, type Stats } from '../api';
import { routerApi, type AliasView, type RouterRoute } from '../routing/types';
import { keysApi, type KeyCreated, type KeyLimits, type KeyView } from './keysApi';

type Draft = KeyLimits & { id?: string; name: string };

const emptyDraft = (): Draft => ({ name: '', aliases: [], routes: [] });

// Gateway keys ("virtual keys"): an app holds an rlcd-… key instead of a
// provider key; the gateway checks it, strips it, and the route injects its own.
export function KeysPanel({ config, onChanged }: { config: GatewayConfig; onChanged: () => void }) {
  const [keys, setKeys] = useState<KeyView[]>([]);
  const [aliases, setAliases] = useState<AliasView[]>([]);
  const [routes, setRoutes] = useState<RouterRoute[]>([]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [created, setCreated] = useState<KeyCreated | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    keysApi.list().then(setKeys).catch((e) => setError(String(e)));
    api.stats().then(setStats).catch(() => {});
  }, []);
  useEffect(() => {
    load();
    routerApi.aliases().then(setAliases).catch(() => {});
    routerApi.routes().then(setRoutes).catch(() => {});
  }, [load]);

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    setError(null);
    const limits: KeyLimits = {
      aliases: draft.aliases, routes: draft.routes,
      rpm: draft.rpm || undefined, tokens_per_day: draft.tokens_per_day || undefined,
    };
    try {
      if (draft.id) {
        await keysApi.update(draft.id, draft.name, limits);
      } else {
        setCreated(await keysApi.create(draft.name, limits));
        setCopied(false);
        onChanged();
      }
      setDraft(null);
      load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (k: KeyView) => {
    if (!confirm(`Revoke "${k.name}"? Apps using it stop working at once. This cannot be undone.`)) return;
    try {
      await keysApi.revoke(k.id);
      load();
      onChanged();
    } catch (e) {
      setError(String(e));
    }
  };

  const toggleRequire = async (on: boolean) => {
    try {
      await api.setRequireKeys(on);
      onChanged();
    } catch (e) {
      setError(String(e));
    }
  };

  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const active = keys.filter((k) => !k.revoked);

  return (
    <main className="keys">
      {error && <div className="banner" onClick={() => setError(null)}>{error}</div>}

      <section className="panel rt-section">
        <div className="rt-head">
          <h2>Gateway keys</h2>
          <span className="spacer" />
          <button className="primary" onClick={() => { setDraft(emptyDraft()); setCreated(null); }}>+ New key</button>
        </div>
        <p className="muted">
          An app sends a gateway key (<span className="mono">rlcd-…</span>) where it would send its OpenAI or Anthropic key. The
          gateway checks it, removes it, and the route injects the provider key it holds, so the app never sees a provider key.
          Keys are stored as hashes: a new key is shown once, here, and never again.
        </p>
        <div className={`keys-mode ${config.exposed ? 'exposed' : ''}`}>
          {config.exposed ? (
            <>
              <strong>Listening beyond this machine ({config.listen}).</strong> Every call to <span className="mono">/v1</span>,{' '}
              <span className="mono">/openai</span> and <span className="mono">/mcp</span> needs a gateway key.
              {active.length === 0 && <span className="note"> There is no active key yet, so every call is refused.</span>}
            </>
          ) : (
            <label className="rt-check">
              <input type="checkbox" checked={config.require_keys} disabled={!config.require_keys && active.length === 0}
                onChange={(e) => toggleRequire(e.target.checked)} />
              <span>
                <strong>Require a gateway key on this machine too</strong>
                <span className="rt-explain">
                  Off: on loopback a call without a key goes through with the client's own login, as before (Claude Code on its
                  subscription needs nothing). Beyond loopback keys are always required.
                  {!config.require_keys && active.length === 0 && ' Create a key first.'}
                </span>
              </span>
            </label>
          )}
        </div>
      </section>

      {created && (
        <section className="panel keys-created">
          <h3>Key created: {created.view.name}</h3>
          <p className="note">{created.note}</p>
          <div className="agents-copy">
            <pre className="mono">{created.key}</pre>
            <button onClick={async () => setCopied(await copyText(created.key))}>{copied ? 'Copied' : 'Copy'}</button>
          </div>
          <p className="muted small">Use it as the API key of any OpenAI or Anthropic SDK; see the Apps tab for snippets.</p>
          <div className="actions"><button onClick={() => setCreated(null)}>I have copied it</button></div>
        </section>
      )}

      {draft && (
        <section className="panel rt-section">
          <h3>{draft.id ? `Edit ${draft.name}` : 'New key'}</h3>
          <div className="form rt-grid">
            <label>
              Name
              <input value={draft.name} placeholder="e.g. support-chatbot" onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </label>
            <label>
              Requests per minute <span className="muted">(empty: no limit)</span>
              <input type="number" min={0} value={draft.rpm ?? ''} onChange={(e) => setDraft({ ...draft, rpm: Number(e.target.value) || undefined })} />
            </label>
            <label>
              Tokens per day <span className="muted">(UTC; input + output + cache)</span>
              <input type="number" min={0} value={draft.tokens_per_day ?? ''}
                onChange={(e) => setDraft({ ...draft, tokens_per_day: Number(e.target.value) || undefined })} />
            </label>
          </div>
          <h4>Allowed models <span className="muted small">none selected: any model name</span></h4>
          <div className="rt-chips">
            {aliases.length === 0 && <span className="muted small">No aliases yet (Routing tab).</span>}
            {aliases.map((a) => (
              <label key={a.name} className={`rt-chip ${draft.aliases.includes(a.name) ? 'on' : ''}`}>
                <input type="checkbox" checked={draft.aliases.includes(a.name)} onChange={() => setDraft({ ...draft, aliases: toggle(draft.aliases, a.name) })} />
                {a.name}
              </label>
            ))}
          </div>
          <h4>Allowed routes <span className="muted small">none selected: any route</span></h4>
          <div className="rt-chips">
            {routes.map((r) => (
              <label key={r.name} className={`rt-chip ${draft.routes.includes(r.name) ? 'on' : ''}`}>
                <input type="checkbox" checked={draft.routes.includes(r.name)} onChange={() => setDraft({ ...draft, routes: toggle(draft.routes, r.name) })} />
                {r.name}
              </label>
            ))}
          </div>
          <p className="muted small">
            The daily limit is checked before each call against what earlier calls used, so the call that crosses it still completes.
          </p>
          <div className="actions">
            <button className="primary" onClick={save} disabled={busy || !draft.name.trim()}>{busy ? 'Saving…' : draft.id ? 'Save' : 'Create key'}</button>
            <button onClick={() => setDraft(null)}>Cancel</button>
          </div>
        </section>
      )}

      <section className="panel rt-section">
        <h3>Keys <span className="muted small">{active.length} active</span></h3>
        {keys.length === 0 ? (
          <p className="muted">No keys yet.</p>
        ) : (
          <div className="scroll-x">
            <table className="rt-table keys-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Key</th>
                  <th>Limits</th>
                  <th className="num">Requests</th>
                  <th className="num">Tokens</th>
                  <th className="num">Today</th>
                  <th className="num">Cost (est.)</th>
                  <th>Last used</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {keys.map((k) => {
                  const u = k.usage;
                  const tokens = u.input_tokens + u.output_tokens + u.cache_read_input_tokens + u.cache_creation_input_tokens;
                  const recent = stats?.by_key?.[k.name];
                  return (
                    <tr key={k.id} className={k.revoked ? 'keys-revoked' : ''}>
                      <td>
                        <strong>{k.name}</strong>
                        {k.revoked && <span className="tag">revoked</span>}
                        <div className="muted small mono">{k.id}</div>
                      </td>
                      <td className="mono small">{k.hint}</td>
                      <td className="small">
                        {k.aliases.length > 0 && <div>models: {k.aliases.join(', ')}</div>}
                        {k.routes.length > 0 && <div>routes: {k.routes.join(', ')}</div>}
                        {k.rpm ? <div>{k.rpm}/min</div> : null}
                        {k.tokens_per_day ? <div>{fmt.n(k.tokens_per_day)} tokens/day</div> : null}
                        {!k.aliases.length && !k.routes.length && !k.rpm && !k.tokens_per_day && <span className="muted">none</span>}
                      </td>
                      <td className="num mono" title={recent ? `${recent.requests} in the recent log` : undefined}>
                        {fmt.n(u.requests)}{u.errors ? <div className="bad small">{u.errors} errors</div> : null}
                      </td>
                      <td className="num mono">{fmt.n(tokens)}</td>
                      <td className="num mono">
                        {fmt.n(k.today_tokens)}
                        {k.tokens_per_day ? <div className="muted small">of {fmt.n(k.tokens_per_day)}</div> : null}
                      </td>
                      <td className="num mono">{fmt.usd(u.est_cost_usd)}</td>
                      <td className="mono small">{k.last_used ? new Date(k.last_used).toLocaleString('en-US', { hour12: false }) : '—'}</td>
                      <td className="rt-row-actions">
                        {!k.revoked && (
                          <>
                            <button onClick={() => setDraft({ id: k.id, name: k.name, aliases: k.aliases, routes: k.routes, rpm: k.rpm, tokens_per_day: k.tokens_per_day })}>Limits</button>
                            <button onClick={() => revoke(k)}>Revoke</button>
                          </>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <p className="muted small">Costs are estimates from the price table (Pruning tab). Every request made with a key shows it in Traffic.</p>
      </section>
    </main>
  );
}
