// Settings → Resilience: what the gateway does when an upstream call fails,
// the max_tokens guard that prevents one class of failure, per-route and
// per-alias overrides with their fallbacks, and the model limits it relies on.
import { useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import type { Protocol, RouteView } from '../../lib/api';
import { useLocation, navigate } from '../../lib/router';
import { routerApi, type AliasView } from '../../lib/routerApi';
import {
  parseFallback, resilienceApi,
  type CatalogStatus, type EffectivePolicy, type LimitsView, type Override, type Policy, type SettingsView,
} from '../../lib/resilienceApi';
import { useFetch, useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, Drawer, ErrorState, Field, IconButton, Loading, Segmented, Toggle, cx } from '../../ui';
import { sourceLabel } from '../traffic/Attempts';

type Msg = { ok: boolean; text: string } | null;
const errText = (e: unknown) => String(e instanceof Error ? e.message : e);

export function ResilienceSettings() {
  const { t } = useI18n();
  const view = useFetch(() => resilienceApi.settings(), []);
  const aliases = useFetch(() => routerApi.aliases(), []);
  const loc = useLocation();
  const { config } = useGateway();
  // ?route=x or ?alias=x opens that override (from the Routes page).
  const [editing, setEditing] = useState<{ kind: 'route' | 'alias'; name: string } | null>(null);
  useEffect(() => {
    const r = loc.query.get('route'), a = loc.query.get('alias');
    if (r) setEditing({ kind: 'route', name: r });
    else if (a) setEditing({ kind: 'alias', name: a });
  }, [loc.query]);

  if (view.error && !view.data) return <ErrorState error={view.error} onRetry={view.reload} />;
  if (!view.data || !config) return <Card><Loading lines={8} /></Card>;
  const v = view.data;
  const close = () => { setEditing(null); navigate('settings/resilience', {}, true); };

  return (
    <div className="stack-lg">
      <Callout tone="info" icon="shield" title={t('rset.intro.title')}>{t('rset.intro.body')}</Callout>
      <GlobalPolicy v={v} onSaved={view.setData} />
      <Overrides v={v} routes={config.routes} aliases={aliases.data ?? []} onEdit={setEditing} />
      <LimitsCard v={v} routes={config.routes} aliases={aliases.data ?? []} onSaved={view.setData} />
      {editing && (
        <OverrideDrawer
          key={`${editing.kind}:${editing.name}`}
          kind={editing.kind}
          name={editing.name}
          v={v}
          routes={config.routes}
          aliases={aliases.data ?? []}
          onClose={close}
          onSaved={(nv) => { view.setData(nv); close(); }}
        />
      )}
    </div>
  );
}

/* ---------- Global policy ---------- */

type Form = Policy & { openrouter_catalog: boolean; catalog_ttl_hours: number };
const formOf = (v: SettingsView): Form => {
  const { enabled, max_attempts, time_budget_ms, backoff, max_tokens_guard, emergency_prune, ignore_provider, openrouter_catalog, catalog_ttl_hours } = v.settings;
  return { enabled, max_attempts, time_budget_ms, backoff, max_tokens_guard, emergency_prune, ignore_provider, openrouter_catalog, catalog_ttl_hours };
};

function Num({ value, onChange, min, max, step, suffix, label }: { value: number; onChange: (n: number) => void; min?: number; max?: number; step?: number; suffix?: string; label: string }) {
  return (
    <div className="input-suffix">
      <input type="number" aria-label={label} value={Number.isFinite(value) ? value : ''} min={min} max={max} step={step} onChange={(e) => onChange(e.target.value === '' ? NaN : Number(e.target.value))} />
      {suffix && <span className="muted small">{suffix}</span>}
    </div>
  );
}

function GlobalPolicy({ v, onSaved }: { v: SettingsView; onSaved: (v: SettingsView) => void }) {
  const { t, f } = useI18n();
  const [form, setForm] = useState<Form>(() => formOf(v));
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<Msg>(null);
  const base = JSON.stringify(formOf(v));
  const dirty = JSON.stringify(form) !== base;
  const d = v.defaults;
  const set = (p: Partial<Form>) => { setForm({ ...form, ...p }); setMsg(null); };
  const g = form.max_tokens_guard;
  const setG = (p: Partial<Form['max_tokens_guard']>) => set({ max_tokens_guard: { ...g, ...p } });
  const b = form.backoff;
  const setB = (p: Partial<Form['backoff']>) => set({ backoff: { ...b, ...p } });
  const e = form.emergency_prune;

  const problems: string[] = [];
  if (!(form.max_attempts >= 1 && form.max_attempts <= 10)) problems.push(t('rset.p.attempts'));
  if (!(form.time_budget_ms >= 1000)) problems.push(t('rset.p.budget'));
  if (!(b.base_ms >= 0 && b.max_ms >= b.base_ms)) problems.push(t('rset.p.backoff'));
  if (!(e.keep_threshold > 0 && e.keep_threshold < 1)) problems.push(t('rset.p.threshold'));
  if (!(g.default_max_tokens > 0)) problems.push(t('rset.p.default'));

  const save = async () => {
    setSaving(true);
    try {
      const nv = await resilienceApi.save(form);
      onSaved(nv);
      setForm(formOf(nv));
      setMsg({ ok: true, text: t('rset.saved') });
    } catch (err) {
      setMsg({ ok: false, text: errText(err) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <div className="grid grid-2">
        <Card title={t('rset.retry.title')} subtitle={t('rset.retry.sub')}>
          <Toggle checked={form.enabled} onChange={(x) => set({ enabled: x })} label={t('rset.enabled')} description={t('rset.enabledHint')} />
          <div className={cx('form-grid', !form.enabled && 'is-off')}>
            <Field label={t('rset.attempts')} hint={t('rset.attemptsHint', { n: d.max_attempts })}>
              <Num label={t('rset.attempts')} value={form.max_attempts} min={1} max={10} onChange={(n) => set({ max_attempts: n })} />
            </Field>
            <Field label={t('rset.budget')} hint={t('rset.budgetHint')}>
              <Num label={t('rset.budget')} value={form.time_budget_ms / 1000} min={1} step={1} suffix="s" onChange={(n) => set({ time_budget_ms: Math.round(n * 1000) })} />
            </Field>
          </div>
          <div className="sub-section">
            <div className="fact-label">{t('rset.backoff')}</div>
            <p className="fine">{t('rset.backoffHint', { base: f.ms(b.base_ms), max: f.ms(b.max_ms), retries: b.retries })}</p>
            <div className="form-grid form-grid-4">
              <Field label={t('rset.backoff.base')}><Num label={t('rset.backoff.base')} value={b.base_ms} min={0} step={100} suffix="ms" onChange={(n) => setB({ base_ms: n })} /></Field>
              <Field label={t('rset.backoff.max')}><Num label={t('rset.backoff.max')} value={b.max_ms} min={0} step={500} suffix="ms" onChange={(n) => setB({ max_ms: n })} /></Field>
              <Field label={t('rset.backoff.jitter')}><Num label={t('rset.backoff.jitter')} value={Math.round(b.jitter * 100)} min={0} max={100} suffix="%" onChange={(n) => setB({ jitter: n / 100 })} /></Field>
              <Field label={t('rset.backoff.retries')}><Num label={t('rset.backoff.retries')} value={b.retries} min={0} max={10} onChange={(n) => setB({ retries: n })} /></Field>
            </div>
          </div>
          <Toggle checked={form.ignore_provider} onChange={(x) => set({ ignore_provider: x })} label={t('rset.ignore')} description={t('rset.ignoreHint')} />
        </Card>

        <Card title={t('rset.guard.title')} subtitle={t('rset.guard.sub')}>
          <Toggle checked={g.enabled} onChange={(x) => setG({ enabled: x })} label={t('rset.guard.enabled')} description={t('rset.guard.enabledHint')} />
          <div className={cx('stack', !g.enabled && 'is-off')}>
            <Field label={t('rset.guard.fill')}>
              <Segmented
                label={t('rset.guard.fill')}
                value={g.fill_missing}
                onChange={(x) => setG({ fill_missing: x })}
                options={(['auto', 'always', 'never'] as const).map((x) => ({ id: x, label: t(`rset.fill.${x}`) }))}
              />
            </Field>
            <p className="fine">{t(`rset.fill.${g.fill_missing}.hint`)}</p>
            <div className="form-grid">
              <Field label={t('rset.guard.default')} hint={t('rset.guard.defaultHint')}>
                <Num label={t('rset.guard.default')} value={g.default_max_tokens} min={1} step={256} onChange={(n) => setG({ default_max_tokens: n })} />
              </Field>
              <Field label={t('rset.guard.margin')} hint={t('rset.guard.marginHint')}>
                <Num label={t('rset.guard.margin')} value={g.safety_margin_tokens} min={0} step={64} onChange={(n) => setG({ safety_margin_tokens: n })} />
              </Field>
            </div>
            <Toggle checked={g.clamp} onChange={(x) => setG({ clamp: x })} label={t('rset.guard.clamp')} description={t('rset.guard.clampHint')} />
          </div>
        </Card>
      </div>

      <Card title={t('rset.em.title')} subtitle={t('rset.em.sub')}>
        <div className="grid grid-2-1">
          <div className="stack">
            <Toggle checked={e.enabled} onChange={(x) => set({ emergency_prune: { ...e, enabled: x } })} label={t('rset.em.enabled')} description={t('rset.em.enabledHint')} />
            <Field label={t('rset.em.threshold')} hint={t('rset.em.thresholdHint', { def: d.emergency_prune.keep_threshold })}>
              <Num label={t('rset.em.threshold')} value={e.keep_threshold} min={0.05} max={0.95} step={0.05} onChange={(n) => set({ emergency_prune: { ...e, keep_threshold: n } })} />
            </Field>
          </div>
          <Callout tone="warn" title={t('rset.em.cacheTitle')}>{t('rset.em.cacheBody')}</Callout>
        </div>
      </Card>

      {problems.map((p) => <Callout key={p} tone="warn">{p}</Callout>)}
      <div className={cx('savebar', 'savebar-inline', (dirty || msg) && 'show')}>
        <span className={cx('small', msg ? (msg.ok ? 'tone-good' : 'tone-bad') : dirty ? 'tone-warn' : 'muted')} role="status">
          {msg ? msg.text : dirty ? t('common.unsaved') : t('rset.allSaved')}
        </span>
        <span className="toolbar-spacer" />
        <Button onClick={() => setForm(formOf(v))} disabled={!dirty || saving}>{t('common.discard')}</Button>
        <Button onClick={() => { setForm({ ...formOf({ ...v, settings: { ...v.settings, ...v.defaults } }) }); setMsg(null); }} disabled={saving}>{t('rset.defaults')}</Button>
        <Button variant="primary" onClick={save} loading={saving} disabled={!dirty || problems.length > 0}>{t('common.save')}</Button>
      </div>
    </>
  );
}

/* ---------- Overrides ---------- */

/** Which settings an override changes, in words. */
function overrideSummary(t: ReturnType<typeof useI18n>['t'], o: Override | undefined): string[] {
  if (!o) return [];
  const out: string[] = [];
  if (o.enabled === false) out.push(t('rset.ov.s.off'));
  if (o.max_attempts != null) out.push(t('rset.ov.s.attempts', { n: o.max_attempts }));
  if (o.max_tokens_guard?.enabled === false) out.push(t('rset.ov.s.guardOff'));
  if (o.max_tokens_guard?.fill_missing) out.push(t('rset.ov.s.fill', { mode: t(`rset.fill.${o.max_tokens_guard.fill_missing}`) }));
  if (o.max_tokens_guard?.default_max_tokens) out.push(t('rset.ov.s.default', { n: o.max_tokens_guard.default_max_tokens }));
  if (o.emergency_prune?.enabled === false) out.push(t('rset.ov.s.emOff'));
  if (o.context_window || o.max_output_tokens) out.push(t('rset.ov.s.limits'));
  if (o.backoff || o.time_budget_ms != null || o.ignore_provider != null || o.max_tokens_guard?.clamp != null || o.max_tokens_guard?.safety_margin_tokens != null || o.emergency_prune?.keep_threshold != null) out.push(t('rset.ov.s.other'));
  return out;
}

function FallbackChips({ list }: { list: string[] }) {
  const { t } = useI18n();
  if (!list.length) return <span className="muted small">{t('rset.ov.noFallbacks')}</span>;
  return (
    <span className="fb-chain">
      {list.map((fb, i) => {
        const p = parseFallback(fb);
        return (
          <span key={fb + i} className="fb-chip">
            <span className="fb-n">{i + 1}</span>
            <strong>{p.route}</strong>{p.model && <span className="mono muted small">:{p.model}</span>}
          </span>
        );
      })}
    </span>
  );
}

function Overrides({ v, routes, aliases, onEdit }: { v: SettingsView; routes: RouteView[]; aliases: AliasView[]; onEdit: (e: { kind: 'route' | 'alias'; name: string }) => void }) {
  const { t } = useI18n();
  const rows: { kind: 'route' | 'alias'; name: string; eff?: EffectivePolicy; ov?: Override; sub: string }[] = [
    ...routes.map((r) => ({ kind: 'route' as const, name: r.name, eff: v.effective.routes[r.name], ov: v.settings.routes[r.name], sub: r.protocols.map((p) => t(`protocol.short.${p}`)).join(' · ') })),
    ...aliases.map((a) => ({ kind: 'alias' as const, name: a.name, eff: v.effective.aliases[a.name], ov: v.settings.aliases[a.name], sub: `→ ${a.route}${a.model ? ` · ${a.model}` : ''}` })),
  ];
  return (
    <Card flush title={t('rset.ov.title')} subtitle={t('rset.ov.sub')}>
      <table className="table ov-table">
        <thead>
          <tr><th>{t('rset.ov.col.name')}</th><th>{t('rset.ov.col.fallbacks')}</th><th>{t('rset.ov.col.changes')}</th><th /></tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const summary = overrideSummary(t, r.ov);
            return (
              <tr key={`${r.kind}:${r.name}`}>
                <td>
                  <div className="ov-name">
                    <Badge tone={r.kind === 'route' ? 'neutral' : 'accent'}>{t(`rset.ov.kind.${r.kind}`)}</Badge>
                    <strong>{r.name}</strong>
                  </div>
                  <div className="muted small">{r.sub}</div>
                </td>
                <td><FallbackChips list={r.eff?.fallbacks ?? []} /></td>
                <td className="small">{summary.length ? summary.join(' · ') : <span className="muted">{t('rset.ov.inherits')}</span>}</td>
                <td className="num"><Button size="sm" icon="edit" onClick={() => onEdit({ kind: r.kind, name: r.name })}>{t('common.edit')}</Button></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Card>
  );
}

type Tri = '' | 'on' | 'off';
const tri = (b: boolean | undefined): Tri => (b == null ? '' : b ? 'on' : 'off');
const fromTri = (x: Tri): boolean | undefined => (x === '' ? undefined : x === 'on');
const numOrU = (s: string) => (s.trim() === '' ? undefined : Number(s));

function OverrideDrawer({ kind, name, v, routes, aliases, onClose, onSaved }: {
  kind: 'route' | 'alias'; name: string; v: SettingsView; routes: RouteView[]; aliases: AliasView[];
  onClose: () => void; onSaved: (v: SettingsView) => void;
}) {
  const { t } = useI18n();
  return (
    <Drawer open wide onClose={onClose} title={<span className="brand-label"><Badge tone={kind === 'route' ? 'neutral' : 'accent'}>{t(`rset.ov.kind.${kind}`)}</Badge>{name}</span>}>
      <OverrideEditor kind={kind} name={name} v={v} routes={routes} aliases={aliases} onCancel={onClose} onSaved={onSaved} />
    </Drawer>
  );
}

/** A route's or alias's resilience override: ordered fallbacks, policy, limits. */
export function OverrideEditor({ kind, name, v, routes, aliases, onCancel, onSaved }: {
  kind: 'route' | 'alias'; name: string; v: SettingsView; routes: RouteView[]; aliases: AliasView[];
  onCancel?: () => void; onSaved: (v: SettingsView) => void;
}) {
  const { t } = useI18n();
  const cur: Override = (kind === 'route' ? v.settings.routes[name] : v.settings.aliases[name]) ?? {};
  const alias = kind === 'alias' ? aliases.find((a) => a.name === name) : undefined;
  const primary = routes.find((r) => r.name === (kind === 'route' ? name : alias?.route));
  const protocols: Protocol[] = kind === 'alias' ? alias?.protocols ?? primary?.protocols ?? [] : primary?.protocols ?? [];
  const eff = kind === 'route' ? v.effective.routes[name] : v.effective.aliases[name];

  const [fallbacks, setFallbacks] = useState<string[]>(cur.fallbacks ?? []);
  const [enabled, setEnabled] = useState<Tri>(tri(cur.enabled));
  const [attempts, setAttempts] = useState(cur.max_attempts != null ? String(cur.max_attempts) : '');
  const [guard, setGuard] = useState<Tri>(tri(cur.max_tokens_guard?.enabled));
  const [fill, setFill] = useState<string>(cur.max_tokens_guard?.fill_missing ?? '');
  const [defTokens, setDefTokens] = useState(cur.max_tokens_guard?.default_max_tokens != null ? String(cur.max_tokens_guard.default_max_tokens) : '');
  const [em, setEm] = useState<Tri>(tri(cur.emergency_prune?.enabled));
  const [win, setWin] = useState(cur.context_window ? String(cur.context_window) : '');
  const [maxOut, setMaxOut] = useState(cur.max_output_tokens ? String(cur.max_output_tokens) : '');
  const [addRoute, setAddRoute] = useState('');
  const [addModel, setAddModel] = useState('');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // A fallback must speak the request's protocol: share one with the primary.
  const compatible = (r: RouteView) => r.protocols.some((p) => protocols.includes(p));
  const candidates = routes.filter((r) => r.name !== primary?.name);
  const addProblem = addRoute && !compatible(routes.find((r) => r.name === addRoute)!) ? t('rset.ov.protocolMismatch', { route: addRoute, protocols: protocols.map((p) => t(`protocol.short.${p}`)).join(', ') }) : null;
  const bad = fallbacks.filter((fb) => { const r = routes.find((x) => x.name === parseFallback(fb).route); return !r || !compatible(r); });

  const build = (): Override | null => {
    // Keep fields this editor does not show (backoff, time budget, ...).
    const o: Override = { ...cur };
    const put = <K extends keyof Override>(k: K, val: Override[K] | undefined) => { if (val === undefined) delete o[k]; else o[k] = val; };
    put('fallbacks', fallbacks.length ? fallbacks : undefined);
    put('enabled', fromTri(enabled));
    put('max_attempts', numOrU(attempts));
    const gp = { ...(cur.max_tokens_guard ?? {}) };
    if (fromTri(guard) === undefined) delete gp.enabled; else gp.enabled = fromTri(guard);
    if (!fill) delete gp.fill_missing; else gp.fill_missing = fill as 'auto';
    if (numOrU(defTokens) === undefined) delete gp.default_max_tokens; else gp.default_max_tokens = numOrU(defTokens);
    put('max_tokens_guard', Object.keys(gp).length ? gp : undefined);
    const ep = { ...(cur.emergency_prune ?? {}) };
    if (fromTri(em) === undefined) delete ep.enabled; else ep.enabled = fromTri(em);
    put('emergency_prune', Object.keys(ep).length ? ep : undefined);
    put('context_window', numOrU(win));
    put('max_output_tokens', numOrU(maxOut));
    return Object.keys(o).length ? o : null;
  };

  const save = async (remove = false) => {
    setSaving(true);
    setErr(null);
    try {
      const o = remove ? null : build();
      const nv = await resilienceApi.save(kind === 'route' ? { routes: { [name]: o } } : { aliases: { [name]: o } });
      onSaved(nv);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setSaving(false);
    }
  };
  const move = (i: number, d: -1 | 1) => {
    const next = [...fallbacks];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    setFallbacks(next);
  };
  const inherit = (on: boolean) => t('rset.ov.inheritValue', { value: on ? t('rset.on') : t('rset.off') });
  return (
    <>
      <div className="stack-lg">
        {kind === 'alias' && alias && <p className="fine">{t('rset.ov.aliasOf', { route: alias.route, model: alias.model ?? '' })}</p>}
        <section className="stack">
          <div className="section-head"><h3>{t('rset.ov.fallbacks')}</h3><span className="muted small">{t('rset.ov.fallbacksHint')}</span></div>
          <ol className="fb-list">
            <li className="fb-primary">
              <span className="fb-n">0</span>
              <strong>{primary?.name ?? '—'}</strong>
              {kind === 'alias' && alias?.model && <span className="mono muted small">:{alias.model}</span>}
              <Badge>{t('rset.ov.primary')}</Badge>
            </li>
            {fallbacks.map((fb, i) => {
              const p = parseFallback(fb);
              const r = routes.find((x) => x.name === p.route);
              const problem = !r ? t('rset.ov.unknownRoute') : !compatible(r) ? t('rset.ov.protocolMismatch', { route: p.route, protocols: protocols.map((x) => t(`protocol.short.${x}`)).join(', ') }) : null;
              return (
                <li key={fb + i} className={cx(problem && 'is-bad')}>
                  <span className="fb-n">{i + 1}</span>
                  <strong>{p.route}</strong>
                  {p.model && <span className="mono muted small">:{p.model}</span>}
                  {problem && <span className="tone-bad small">{problem}</span>}
                  <span className="toolbar-spacer" />
                  <IconButton icon="up" label={t('rset.ov.up')} disabled={i === 0} onClick={() => move(i, -1)} />
                  <IconButton icon="down" label={t('rset.ov.down')} disabled={i === fallbacks.length - 1} onClick={() => move(i, 1)} />
                  <IconButton icon="trash" label={t('rset.ov.remove', { name: fb })} onClick={() => setFallbacks(fallbacks.filter((_, j) => j !== i))} />
                </li>
              );
            })}
          </ol>
          {fallbacks.length === 0 && eff && eff.fallbacks.length > 0 && (
            <p className="fine">{t('rset.ov.inheritedFallbacks', { list: eff.fallbacks.join(' → ') })}</p>
          )}
          <div className="fb-add">
            <select aria-label={t('rset.ov.addRoute')} value={addRoute} onChange={(e) => setAddRoute(e.target.value)}>
              <option value="">{t('rset.ov.pickRoute')}</option>
              {candidates.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}{compatible(r) ? '' : ` — ${t('rset.ov.otherProtocol')}`}
                </option>
              ))}
            </select>
            <input className="mono" placeholder={t('rset.ov.modelPh')} aria-label={t('rset.ov.model')} value={addModel} onChange={(e) => setAddModel(e.target.value)} />
            <Button icon="plus" disabled={!addRoute || !!addProblem} onClick={() => { setFallbacks([...fallbacks, addModel.trim() ? `${addRoute}:${addModel.trim()}` : addRoute]); setAddRoute(''); setAddModel(''); }}>
              {t('rset.ov.add')}
            </Button>
          </div>
          {addProblem && <Callout tone="bad">{addProblem}</Callout>}
          <p className="fine">{t('rset.ov.pinned')}</p>
        </section>

        <section className="stack">
          <div className="section-head"><h3>{t('rset.ov.policy')}</h3><span className="muted small">{t('rset.ov.policyHint')}</span></div>
          <div className="form-grid">
            <Field label={t('rset.enabled')}><TriSelect label={t('rset.enabled')} value={enabled} onChange={setEnabled} inherit={inherit(v.settings.enabled)} /></Field>
            <Field label={t('rset.attempts')}><input type="number" min={1} max={10} placeholder={t('rset.ov.inheritN', { n: v.settings.max_attempts })} value={attempts} onChange={(e) => setAttempts(e.target.value)} /></Field>
            <Field label={t('rset.guard.enabled')}><TriSelect label={t('rset.guard.enabled')} value={guard} onChange={setGuard} inherit={inherit(v.settings.max_tokens_guard.enabled)} /></Field>
            <Field label={t('rset.guard.fill')}>
              <select value={fill} onChange={(e) => setFill(e.target.value)}>
                <option value="">{t('rset.ov.inheritValue', { value: t(`rset.fill.${v.settings.max_tokens_guard.fill_missing}`) })}</option>
                {(['auto', 'always', 'never'] as const).map((x) => <option key={x} value={x}>{t(`rset.fill.${x}`)}</option>)}
              </select>
            </Field>
            <Field label={t('rset.guard.default')}><input type="number" min={1} placeholder={t('rset.ov.inheritN', { n: v.settings.max_tokens_guard.default_max_tokens })} value={defTokens} onChange={(e) => setDefTokens(e.target.value)} /></Field>
            <Field label={t('rset.em.enabled')}><TriSelect label={t('rset.em.enabled')} value={em} onChange={setEm} inherit={inherit(v.settings.emergency_prune.enabled)} /></Field>
          </div>
        </section>

        <section className="stack">
          <div className="section-head"><h3>{t('rset.ov.limits')}</h3><span className="muted small">{t('rset.ov.limitsHint')}</span></div>
          <div className="form-grid">
            <Field label={t('rset.limits.window')}><input type="number" min={1} placeholder={t('rset.ov.fromRegistry')} value={win} onChange={(e) => setWin(e.target.value)} /></Field>
            <Field label={t('rset.limits.maxOut')}><input type="number" min={1} placeholder={t('rset.ov.fromRegistry')} value={maxOut} onChange={(e) => setMaxOut(e.target.value)} /></Field>
          </div>
        </section>
        {eff && <p className="fine">{t('rset.ov.effective', { attempts: eff.max_attempts, fill: t(`rset.fill.${eff.max_tokens_guard.fill_missing}`) })}</p>}
        {err && <Callout tone="bad" title={t('rset.ov.rejected')}>{err}</Callout>}
      </div>
      <div className="btn-row drawer-foot">
        {Object.keys(cur).length > 0 && <Button variant="danger" onClick={() => save(true)} disabled={saving}>{t('rset.ov.reset')}</Button>}
        <span className="toolbar-spacer" />
        {onCancel && <Button onClick={onCancel}>{t('common.cancel')}</Button>}
        <Button variant="primary" onClick={() => save()} loading={saving} disabled={bad.length > 0}>{t('common.save')}</Button>
      </div>
    </>
  );
}

function TriSelect({ value, onChange, label, inherit }: { value: Tri; onChange: (x: Tri) => void; label: string; inherit: string }) {
  const { t } = useI18n();
  return (
    <select aria-label={label} value={value} onChange={(e) => onChange(e.target.value as Tri)}>
      <option value="">{inherit}</option>
      <option value="on">{t('rset.on')}</option>
      <option value="off">{t('rset.off')}</option>
    </select>
  );
}

/* ---------- Model limits ---------- */

function LimitsCard({ v, routes, aliases, onSaved }: { v: SettingsView; routes: RouteView[]; aliases: AliasView[]; onSaved: (v: SettingsView) => void }) {
  const { t, f } = useI18n();
  const { requests } = useGateway();
  const [model, setModel] = useState('');
  const [route, setRoute] = useState('');
  const [alias, setAlias] = useState('');
  const [res, setRes] = useState<LimitsView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [cat, setCat] = useState<CatalogStatus>(v.catalog);
  const [refreshing, setRefreshing] = useState(false);
  const [catErr, setCatErr] = useState<string | null>(null);
  useEffect(() => setCat(v.catalog), [v.catalog]);
  const models = useMemo(() => [...new Set(requests.map((r) => r.model).filter((m): m is string => !!m))].sort(), [requests]);

  const look = async () => {
    setBusy(true);
    setErr(null);
    try {
      setRes(await resilienceApi.limits({ model: model.trim(), route, alias }));
    } catch (e) {
      setErr(errText(e));
      setRes(null);
    } finally {
      setBusy(false);
    }
  };
  const refresh = async () => {
    setRefreshing(true);
    setCatErr(null);
    try {
      setCat(await resilienceApi.refreshCatalog());
    } catch (e) {
      setCatErr(errText(e));
    } finally {
      setRefreshing(false);
    }
  };
  const toggleCatalog = async (on: boolean) => {
    try { onSaved(await resilienceApi.save({ openrouter_catalog: on })); } catch (e) { setCatErr(errText(e)); }
  };

  return (
    <div className="grid grid-2">
      <Card title={t('rset.limits.title')} subtitle={t('rset.limits.sub')}>
        <form className="stack" onSubmit={(e) => { e.preventDefault(); if (model.trim()) void look(); }}>
          <Field label={t('rset.limits.model')}>
            <input className="mono" list="rset-models" value={model} placeholder="qwen/qwen3-235b-a22b-2507" onChange={(e) => setModel(e.target.value)} />
            <datalist id="rset-models">{models.map((m) => <option key={m} value={m} />)}</datalist>
          </Field>
          <div className="form-grid">
            <Field label={t('rset.limits.route')}>
              <select value={route} onChange={(e) => { setRoute(e.target.value); setAlias(''); }}>
                <option value="">{t('rset.limits.anyRoute')}</option>
                {routes.map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
              </select>
            </Field>
            <Field label={t('rset.limits.alias')}>
              <select value={alias} onChange={(e) => { setAlias(e.target.value); setRoute(''); }}>
                <option value="">—</option>
                {aliases.map((a) => <option key={a.name} value={a.name}>{a.name}</option>)}
              </select>
            </Field>
          </div>
          <div className="btn-row"><Button type="submit" variant="primary" icon="search" loading={busy} disabled={!model.trim()}>{t('rset.limits.look')}</Button></div>
        </form>
        {err && <Callout tone="bad">{err}</Callout>}
        {res && (
          res.known ? (
            <div className="mini-stats limits-result">
              <div className="stat"><div className="stat-label">{t('rset.limits.window')}</div><div className="stat-value">{res.limits.context_window ? f.num(res.limits.context_window) : '—'}</div></div>
              <div className="stat"><div className="stat-label">{t('rset.limits.maxOut')}</div><div className="stat-value">{res.limits.max_output_tokens ? f.num(res.limits.max_output_tokens) : '—'}</div></div>
              <div className="stat"><div className="stat-label">{t('rset.limits.source')}</div><div className="stat-value small-value">{sourceLabel(t, res.limits.source)}</div></div>
            </div>
          ) : <Callout tone="info">{t('rset.limits.unknown', { model: res.model })}</Callout>
        )}
      </Card>

      <Card title={t('rset.cat.title')} subtitle={t('rset.cat.sub', { date: v.builtin_as_of })}
        actions={<Button size="sm" icon="refresh" loading={refreshing} onClick={refresh}>{t('rset.cat.refresh')}</Button>}>
        <Toggle checked={v.settings.openrouter_catalog} onChange={toggleCatalog} label={t('rset.cat.use')} description={t('rset.cat.useHint')} />
        <dl className="kv-list">
          <div><dt>{t('rset.cat.models')}</dt><dd><strong>{f.num(cat.models)}</strong> {cat.stale && <Badge tone="warn">{t('rset.cat.stale')}</Badge>}</dd></div>
          <div><dt>{t('rset.cat.fetched')}</dt><dd>{cat.fetched_at ? `${f.dayTime(cat.fetched_at)} · ${f.ago(cat.fetched_at)}` : t('rset.cat.never')}</dd></div>
          <div><dt>{t('rset.cat.ttl')}</dt><dd>{t('rset.cat.ttlValue', { n: v.settings.catalog_ttl_hours })}</dd></div>
          <div><dt>{t('rset.cat.learned')}</dt><dd>{t('rset.cat.learnedValue', { count: cat.learned })}</dd></div>
          <div><dt>{t('rset.cat.url')}</dt><dd className="mono small">{cat.url}</dd></div>
        </dl>
        {cat.last_error && <Callout tone="warn" title={t('rset.cat.error')}><span className="mono small">{cat.last_error}</span></Callout>}
        {catErr && <Callout tone="bad">{catErr}</Callout>}
        {Object.keys(v.settings.models).length > 0 && (
          <div className="stack">
            <div className="fact-label">{t('rset.cat.overrides')}</div>
            <ul className="plain-list">
              {Object.entries(v.settings.models).map(([m, l]) => (
                <li key={m} className="small"><span className="mono">{m}</span> <span className="muted">· {[l.context_window && t('res.limits.window', { n: f.num(l.context_window) }), l.max_output_tokens && t('res.limits.maxOut', { n: f.num(l.max_output_tokens) })].filter(Boolean).join(' · ')}</span></li>
              ))}
            </ul>
          </div>
        )}
        <p className="fine"><Icon name="info" size={12} /> {t('rset.cat.order')}</p>
      </Card>
    </div>
  );
}
