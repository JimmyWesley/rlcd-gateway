import { useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import type { HeaderCond, Match, Rule, RouterRoute, RulesDoc } from '../../lib/routerApi';
import { Badge, Button, Card, Disclosure, EmptyState, Field, IconButton, Loading, RouteLabel, Toggle, cx } from '../../ui';

type Props = {
  doc: RulesDoc | null;
  routes: RouterRoute[];
  dirty: boolean;
  saving: boolean;
  onChange: (d: RulesDoc) => void;
  onSave: () => void;
  onRevert: () => void;
  onSticky: (patch: Partial<RulesDoc>) => void;
};

/** One-line, translated summary of a rule's conditions. */
export function useDescribeMatch() {
  const { t } = useI18n();
  return (m: Match): string => {
    const parts: string[] = [];
    const flag = (v: boolean | undefined, yes: string, no: string) => {
      if (v !== undefined) parts.push(v ? yes : no);
    };
    if (m.model) parts.push(t('match.model', { re: m.model }));
    if (m.min_context_tokens) parts.push(t('match.ctxMin', { n: m.min_context_tokens }));
    if (m.max_context_tokens) parts.push(t('match.ctxMax', { n: m.max_context_tokens }));
    flag(m.has_tools, t('match.tools'), t('match.noTools'));
    flag(m.has_images, t('match.images'), t('match.noImages'));
    flag(m.has_thinking, t('match.thinking'), t('match.noThinking'));
    flag(m.background, t('match.background'), t('match.notBackground'));
    if (m.max_tokens_lte) parts.push(`max_tokens ≤ ${m.max_tokens_lte}`);
    for (const h of m.headers ?? []) {
      parts.push(h.equals ? `${h.name} = ${h.equals}` : h.contains ? `${h.name} ∋ ${h.contains}` : t('match.headerPresent', { name: h.name }));
    }
    if (m.conversation) parts.push(t('match.conversation', { id: `${m.conversation.slice(0, 14)}…` }));
    return parts.length ? parts.join(' · ') : t('match.every');
  };
}

export function RulesView({ doc, routes, dirty, saving, onChange, onSave, onRevert, onSticky }: Props) {
  const { t } = useI18n();
  const describe = useDescribeMatch();
  const [open, setOpen] = useState<number | null>(null);
  if (!doc) return <Card><Loading lines={4} /></Card>;
  const rules = doc.rules;
  const setRules = (rs: Rule[]) => onChange({ ...doc, rules: rs });
  const update = (i: number, r: Rule) => setRules(rules.map((x, j) => (j === i ? r : x)));
  const move = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= rules.length) return;
    const next = [...rules];
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
    setOpen((o) => (o === i ? j : o === j ? i : o));
  };
  const remove = (i: number) => {
    setRules(rules.filter((_, j) => j !== i));
    setOpen(null);
  };
  const uniqueName = (base: string) => {
    const names = new Set(rules.map((r) => r.name));
    if (!names.has(base)) return base;
    let n = 2;
    while (names.has(`${base}-${n}`)) n++;
    return `${base}-${n}`;
  };
  const add = (kind: 'match' | 'auto') => {
    const name = uniqueName(kind === 'auto' ? 'auto' : 'rule');
    const r: Rule = kind === 'auto'
      ? { name, enabled: true, kind: 'auto', timeout_ms: 1500, when: {} }
      : { name, enabled: true, kind: 'match', route: routes[0]?.name ?? '', when: {} };
    setRules([...rules, r]);
    setOpen(rules.length);
  };
  const addBackground = () => {
    const cheap = routes.find((r) => !r.active)?.name ?? routes[0]?.name ?? '';
    setRules([{ name: uniqueName('background'), enabled: true, kind: 'match', route: cheap, when: { background: true } }, ...rules]);
    setOpen(0);
  };
  const routeOf = (name?: string) => routes.find((r) => r.name === name);

  return (
    <>
      <Card
        title={t('rules.title')}
        subtitle={t('rules.sub')}
        actions={
          <div className="btn-row">
            <Button size="sm" icon="plus" onClick={() => add('match')}>{t('rules.add')}</Button>
            <Button size="sm" icon="zap" onClick={() => add('auto')} title={t('rules.addAutoHint')}>{t('rules.addAuto')}</Button>
            {!rules.some((r) => r.when.background) && <Button size="sm" icon="plus" onClick={addBackground}>{t('rules.addBackground')}</Button>}
          </div>
        }
      >
        {rules.length === 0 && <EmptyState compact icon="routing" title={t('rules.empty')}>{t('rules.emptyBody')}</EmptyState>}
        <ol className="rules">
          {rules.map((r, i) => (
            <li key={i} className={cx('rule', !r.enabled && 'off', open === i && 'open')}>
              <div className="rule-row">
                <span className="rule-idx mono">{i + 1}</span>
                <button type="button" role="switch" aria-checked={r.enabled} aria-label={r.enabled ? t('rules.enabled') : t('rules.disabled')}
                  className={cx('switch', 'switch-sm', r.enabled && 'on')} onClick={() => update(i, { ...r, enabled: !r.enabled })}>
                  <span className="switch-knob" />
                </button>
                <button type="button" className="rule-name" onClick={() => setOpen(open === i ? null : i)} aria-expanded={open === i}>
                  <strong>{r.name}</strong>
                  <span className="muted small">
                    {describe(r.when)}
                    {r.override_sticky && ` · ${t('rules.overridesSticky')}`}
                  </span>
                </button>
                <Icon name="arrowRight" size={14} />
                <span className="rule-target">
                  {r.kind === 'auto' ? (
                    <Badge tone="accent" icon="zap">{t('rules.autoTarget', { routes: (r.candidates?.length ? r.candidates : [t('rules.describedRoutes')]).join(' | ') })}</Badge>
                  ) : r.route ? <RouteLabel name={r.route} route={routeOf(r.route)} /> : '—'}
                </span>
                <div className="rule-actions">
                  <IconButton icon="up" label={t('rules.moveUp')} onClick={() => move(i, -1)} disabled={i === 0} />
                  <IconButton icon="down" label={t('rules.moveDown')} onClick={() => move(i, 1)} disabled={i === rules.length - 1} />
                  <IconButton icon="trash" label={t('rules.delete')} onClick={() => remove(i)} />
                </div>
              </div>
              {open === i && <RuleForm rule={r} routes={routes} onChange={(x) => update(i, x)} />}
            </li>
          ))}
        </ol>
        <div className={cx('savebar', 'savebar-inline', dirty && 'show')}>
          <span className={dirty ? 'tone-warn small' : 'muted small'}>{dirty ? t('common.unsaved') : t('rules.allSaved')}</span>
          <span className="toolbar-spacer" />
          <Button onClick={onRevert} disabled={!dirty || saving}>{t('common.revert')}</Button>
          <Button variant="primary" onClick={onSave} loading={saving} disabled={!dirty}>{t('rules.save')}</Button>
        </div>
      </Card>

      <Card title={t('sticky.title')} subtitle={t('sticky.sub')}>
        <div className="toggle-list">
          <Toggle checked={doc.sticky} onChange={(v) => onSticky({ sticky: v })} label={<>{t('sticky.sticky')} <Badge tone="good">{t('common.recommended')}</Badge></>} description={t('sticky.stickyHelp')} />
          <Toggle checked={doc.background_bypass} disabled={!doc.sticky} onChange={(v) => onSticky({ background_bypass: v })} label={t('sticky.bypass')} description={t('sticky.bypassHelp')} />
        </div>
        <div className="inline-field">
          <span>{t('sticky.ttlBefore')}</span>
          <input
            type="number"
            min={1}
            className="input-sm"
            aria-label={t('sticky.ttlAria')}
            value={doc.ttl_hours}
            disabled={!doc.sticky}
            onChange={(e) => onChange({ ...doc, ttl_hours: Math.max(1, Number(e.target.value) || 1) })}
            onBlur={() => onSticky({ ttl_hours: doc.ttl_hours })}
          />
          <span>{t('sticky.ttlAfter')}</span>
        </div>
      </Card>
    </>
  );
}

type Tri = 'any' | 'yes' | 'no';
const toTri = (v: boolean | undefined): Tri => (v === undefined ? 'any' : v ? 'yes' : 'no');
const fromTri = (x: Tri): boolean | undefined => (x === 'any' ? undefined : x === 'yes');
const num = (s: string): number | undefined => {
  const n = parseInt(s, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

function RuleForm({ rule, routes, onChange }: { rule: Rule; routes: RouterRoute[]; onChange: (r: Rule) => void }) {
  const { t } = useI18n();
  const m = rule.when;
  const setWhen = (patch: Partial<Match>) => {
    const next: Match = { ...m, ...patch };
    // Drop unset keys so the stored JSON stays readable.
    for (const k of Object.keys(next) as (keyof Match)[]) {
      const v = next[k];
      if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) delete next[k];
    }
    onChange({ ...rule, when: next });
  };
  const headers = m.headers ?? [];
  const setHeader = (i: number, h: HeaderCond) => setWhen({ headers: headers.map((x, j) => (j === i ? h : x)) });
  const auto = rule.kind === 'auto';
  const described = routes.filter((r) => r.description);

  const tri = (label: string, key: 'has_tools' | 'has_images' | 'has_thinking' | 'background', hint?: string) => (
    <Field label={label} hint={hint}>
      <select value={toTri(m[key])} onChange={(e) => setWhen({ [key]: fromTri(e.target.value as Tri) })}>
        <option value="any">{t('rules.form.any')}</option>
        <option value="yes">{t('common.yes')}</option>
        <option value="no">{t('common.no')}</option>
      </select>
    </Field>
  );

  return (
    <div className="rule-form">
      <div className="form-grid">
        <Field label={t('rules.form.name')}>
          <input value={rule.name} onChange={(e) => onChange({ ...rule, name: e.target.value })} />
        </Field>
        {auto ? (
          <Field label={t('rules.form.timeout')}>
            <input type="number" min={100} max={10000} value={rule.timeout_ms ?? ''} placeholder="1500" onChange={(e) => onChange({ ...rule, timeout_ms: num(e.target.value) })} />
          </Field>
        ) : (
          <Field label={t('rules.form.route')}>
            <select value={rule.route ?? ''} onChange={(e) => onChange({ ...rule, route: e.target.value })}>
              {!routes.some((r) => r.name === rule.route) && <option value={rule.route ?? ''}>{rule.route || t('rules.form.pickRoute')}</option>}
              {routes.map((r) => <option key={r.name} value={r.name}>{r.name}{r.model ? ` · ${r.model}` : ''}</option>)}
            </select>
          </Field>
        )}
      </div>

      {auto && (
        <div className="auto-box">
          <p className="fine">{t('rules.form.autoHelp')}</p>
          <div className="chips">
            {described.length < 2 && <Badge tone="warn">{t('rules.form.needDescriptions')}</Badge>}
            {described.map((r) => {
              const on = !rule.candidates?.length || rule.candidates.includes(r.name);
              return (
                <label key={r.name} className={cx('chip', 'chip-check', on && 'on')} title={r.description}>
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={(e) => {
                      const cur = rule.candidates?.length ? rule.candidates : described.map((d) => d.name);
                      const next = e.target.checked ? [...cur, r.name] : cur.filter((c) => c !== r.name);
                      onChange({ ...rule, candidates: next.length === described.length ? undefined : next });
                    }}
                  />
                  {r.name}
                </label>
              );
            })}
          </div>
          <div className="inline-field">
            <span>{t('rules.form.minConfidence')}</span>
            <input type="number" className="input-sm" step={0.05} min={0} max={1} value={rule.min_confidence ?? 0}
              onChange={(e) => onChange({ ...rule, min_confidence: parseFloat(e.target.value) || undefined })} />
          </div>
        </div>
      )}

      <h4 className="form-section">{t('rules.form.conditions')} <span className="muted small">{t('rules.form.conditionsHint')}</span></h4>
      <div className="form-grid form-grid-4">
        <Field label={t('rules.form.model')}>
          <input className="mono" value={m.model ?? ''} placeholder="haiku|sonnet" onChange={(e) => setWhen({ model: e.target.value })} />
        </Field>
        <Field label="max_tokens ≤">
          <input type="number" min={0} value={m.max_tokens_lte ?? ''} placeholder={t('rules.form.any')} onChange={(e) => setWhen({ max_tokens_lte: num(e.target.value) })} />
        </Field>
        <Field label={t('rules.form.ctxMin')}>
          <input type="number" min={0} value={m.min_context_tokens ?? ''} placeholder={t('rules.form.any')} onChange={(e) => setWhen({ min_context_tokens: num(e.target.value) })} />
        </Field>
        <Field label={t('rules.form.ctxMax')}>
          <input type="number" min={0} value={m.max_context_tokens ?? ''} placeholder={t('rules.form.any')} onChange={(e) => setWhen({ max_context_tokens: num(e.target.value) })} />
        </Field>
        {tri(t('rules.form.hasTools'), 'has_tools')}
        {tri(t('rules.form.hasImages'), 'has_images')}
        {tri(t('rules.form.hasThinking'), 'has_thinking', t('rules.form.hasThinkingHint'))}
        {tri(t('rules.form.background'), 'background', t('rules.form.backgroundHint'))}
      </div>
      <Disclosure title={t('rules.form.more')} defaultOpen={!!(m.conversation || headers.length)}>
        <Field label={t('rules.form.conversation')} wide>
          <input className="mono" value={m.conversation ?? ''} placeholder={t('rules.form.conversationPh')} onChange={(e) => setWhen({ conversation: e.target.value.trim() })} />
        </Field>
        <div className="headers">
          <span className="field-label">{t('rules.form.headers')}</span>
          {headers.map((h, i) => {
            const op = h.equals !== undefined ? 'equals' : h.contains !== undefined ? 'contains' : 'present';
            return (
              <div key={i} className="header-row">
                <input className="mono" value={h.name} placeholder={t('rules.form.headerName')} aria-label={t('rules.form.headerName')} onChange={(e) => setHeader(i, { ...h, name: e.target.value })} />
                <select value={op} aria-label={t('rules.form.headerOp')} onChange={(e) => {
                  const v = h.equals ?? h.contains ?? '';
                  setHeader(i, e.target.value === 'equals' ? { name: h.name, equals: v } : e.target.value === 'contains' ? { name: h.name, contains: v } : { name: h.name });
                }}>
                  <option value="present">{t('rules.form.present')}</option>
                  <option value="equals">{t('rules.form.equals')}</option>
                  <option value="contains">{t('rules.form.contains')}</option>
                </select>
                {op !== 'present' && (
                  <input className="mono" aria-label={t('rules.form.headerValue')} value={(op === 'equals' ? h.equals : h.contains) ?? ''}
                    onChange={(e) => setHeader(i, op === 'equals' ? { name: h.name, equals: e.target.value } : { name: h.name, contains: e.target.value })} />
                )}
                <IconButton icon="x" label={t('rules.form.removeHeader')} onClick={() => setWhen({ headers: headers.filter((_, j) => j !== i) })} />
              </div>
            );
          })}
          <Button size="sm" variant="ghost" icon="plus" onClick={() => setWhen({ headers: [...headers, { name: '' }] })}>{t('rules.form.addHeader')}</Button>
        </div>
      </Disclosure>
      {!auto && (
        <Toggle
          checked={!!rule.override_sticky}
          onChange={(v) => onChange({ ...rule, override_sticky: v || undefined })}
          label={t('rules.form.override')}
          description={t('rules.form.overrideHelp')}
        />
      )}
    </div>
  );
}
