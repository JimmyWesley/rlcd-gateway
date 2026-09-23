import { useCallback, useEffect, useState } from 'react';
import { fmt, type GatewayConfig } from '../api';
import { DryRunPanel } from './DryRunPanel';
import { RoutesEditor } from './RoutesEditor';
import { RulesEditor } from './RulesEditor';
import { routerApi, type Assignment, type RouterRoute, type RulesDoc } from './types';

// Routes and routing rules (F2).
export function RoutingPanel({ config, onChanged }: { config: GatewayConfig; onChanged: () => void }) {
  const [saved, setSaved] = useState<RulesDoc | null>(null);
  const [doc, setDoc] = useState<RulesDoc | null>(null);
  const [routes, setRoutes] = useState<RouterRoute[]>([]);
  const [convs, setConvs] = useState<Assignment[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadConvs = useCallback(() => routerApi.conversations().then(setConvs).catch(() => {}), []);
  useEffect(() => {
    routerApi.rules().then((d) => {
      setSaved(d);
      setDoc(d);
    }).catch((e) => setError(String(e)));
    routerApi.routes().then(setRoutes).catch((e) => setError(String(e)));
    loadConvs();
  }, [loadConvs]);

  // The active route is switched from the top bar; keep the list in step.
  useEffect(() => {
    setRoutes((rs) => rs.map((r) => ({ ...r, active: r.name === config.active_route })));
  }, [config.active_route]);

  const dirty = !!doc && !!saved && JSON.stringify(doc) !== JSON.stringify(saved);

  // keepDraft saves only the stickiness settings and leaves unsaved rule edits in place.
  const save = async (next?: RulesDoc, keepDraft = false) => {
    const d = next ?? doc;
    if (!d) return;
    setSaving(true);
    setError(null);
    try {
      const out = await routerApi.saveRules(d);
      setSaved(out);
      setDoc((cur) =>
        keepDraft && cur ? { ...cur, sticky: out.sticky, ttl_hours: out.ttl_hours, background_bypass: out.background_bypass } : out,
      );
      routerApi.routes().then(setRoutes).catch(() => {});
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  // Stickiness settings save immediately; they are a single switch, not an edit session.
  const setSticky = (patch: Partial<RulesDoc>) => {
    if (!doc || !saved) return;
    setDoc({ ...doc, ...patch });
    save({ ...saved, ...patch }, true);
  };

  const onRoutes = (rs: RouterRoute[]) => {
    setRoutes(rs);
    onChanged();
  };

  const reset = async (id?: string) => {
    try {
      if (id) await routerApi.resetConversation(id);
      else if (confirm('Forget every sticky conversation? Their next turn is routed afresh.')) await routerApi.resetAll();
    } catch (e) {
      setError(String(e));
    }
    loadConvs();
  };

  const routeName = (a: Assignment) => a.route || `${config.active_route} (active route)`;

  return (
    <main className="rt">
      {error && <div className="banner" onClick={() => setError(null)}>{error}</div>}

      <section className="panel rt-section">
        <h2>Routing</h2>
        <p className="muted">
          Every request to <span className="mono">/v1/messages</span> goes through the rules below. The first enabled rule that matches
          picks the route; when none does, the active route from the top bar serves it, as before. Every decision is logged with a
          one-line reason, shown in Traffic.
        </p>
        {doc && (
          <div className="rt-sticky">
            <label className="rt-check">
              <input type="checkbox" checked={doc.sticky} onChange={(e) => setSticky({ sticky: e.target.checked })} />
              <span>
                <strong>Sticky per conversation</strong> <span className="muted small">(recommended)</span>
                <span className="rt-explain">
                  The first decision of a conversation holds for all its turns, including “no rule matched → active route”. Switching
                  model halfway throws away the provider’s prompt cache (the whole context is billed again at full price) and
                  invalidates signed thinking blocks, which the gateway then has to strip. Rules therefore decide at the start of a
                  conversation; only rules marked “override stickiness” can move it later. Pins are kept on disk and survive restarts.
                </span>
              </span>
            </label>
            <label className="rt-check">
              <input
                type="checkbox"
                checked={doc.background_bypass}
                disabled={!doc.sticky}
                onChange={(e) => setSticky({ background_bypass: e.target.checked })}
              />
              <span>
                <strong>Background requests bypass the pin</strong>
                <span className="rt-explain">
                  Claude Code sends independent side requests (session titles, summaries, quota probes) with no tools and a single
                  message. They share no cache with the conversation, so they can go to a cheaper route. They never create a pin, so a
                  title request racing the first turn cannot decide the conversation’s model.
                </span>
              </span>
            </label>
            <label className="rt-inline">
              Forget pins after
              <input
                type="number"
                min={1}
                value={doc.ttl_hours}
                disabled={!doc.sticky}
                onChange={(e) => setDoc({ ...doc, ttl_hours: Math.max(1, Number(e.target.value) || 1) })}
                onBlur={() => doc.ttl_hours !== saved?.ttl_hours && setSticky({ ttl_hours: doc.ttl_hours })}
              />
              hours without traffic
            </label>
          </div>
        )}
      </section>

      {doc && (
        <RulesEditor
          doc={doc}
          routes={routes}
          dirty={dirty}
          saving={saving}
          onChange={setDoc}
          onSave={() => save()}
          onRevert={() => saved && setDoc(saved)}
        />
      )}

      <RoutesEditor routes={routes} onSaved={onRoutes} onError={setError} />

      <DryRunPanel dirty={dirty} />

      <section className="panel rt-section">
        <div className="rt-head">
          <h3>Sticky conversations</h3>
          <span className="muted small">{doc?.sticky ? 'Reset one to route its next turn afresh.' : 'Stickiness is off; existing pins are ignored.'}</span>
          <span className="spacer" />
          <button onClick={loadConvs}>Refresh</button>
          <button onClick={() => reset()} disabled={convs.length === 0}>Reset all</button>
        </div>
        {convs.length === 0 ? (
          <p className="muted">No pinned conversations yet.</p>
        ) : (
          <div className="scroll-x">
            <table className="rt-table">
              <thead>
                <tr>
                  <th>Conversation</th>
                  <th>Route</th>
                  <th>Decided by</th>
                  <th className="num">Turns</th>
                  <th>Last seen</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {convs.map((a) => (
                  <tr key={a.conversation_id}>
                    <td className="mono small" title={a.conversation_id}>{a.conversation_id.slice(0, 20)}…</td>
                    <td>{routeName(a)}</td>
                    <td className="small rt-desc" title={a.reason}>{a.rule ? `rule '${a.rule}'` : <span className="muted">no rule</span>}</td>
                    <td className="num mono">{a.turns}</td>
                    <td className="mono small">{fmt.time(a.last_seen)}</td>
                    <td className="rt-row-actions"><button onClick={() => reset(a.conversation_id)}>Reset</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </main>
  );
}
