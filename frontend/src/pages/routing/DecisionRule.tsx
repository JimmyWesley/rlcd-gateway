// Decision rules: a router rule whose branch a System One model picks, like
// an n8n Switch node. The editor, the "Try it" box and the trace view are
// shared by Routing → Rules, the dry run, the inspector and the Flow editor.
import { useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { decisionsApi } from '../../lib/decisionsApi';
import { isModelCall } from '../../lib/brands';
import {
  FACTS, parseDecisionReason, routerApi, targetString,
  type Branch, type BranchWhen, type DecisionTrace, type QuestionType, type Rule, type RouterRoute, type RuleTestResult, type Target,
} from '../../lib/routerApi';
import { useFetch, useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Field, IconButton, Segmented, cx } from '../../ui';

type T = ReturnType<typeof useI18n>['t'];
type F = ReturnType<typeof useI18n>['f'];

/** The labels a question can answer: choice labels, or the score legend's heads. */
export function answerLabels(r: Rule): string[] {
  const c = r.question?.criteria;
  if (r.question?.type === 'choice' && c && !Array.isArray(c)) return Object.keys(c);
  if (r.question?.type === 'score' && Array.isArray(c)) return c.map((l) => l.split(/[:—(-]/)[0].trim());
  return [];
}

/** "code", "≥ 2 hard", "p ≥ 0.70": a branch's condition in a few characters. */
export function whenText(r: Rule, w: BranchWhen, f: F): string {
  const legend = answerLabels(r);
  const idx = (n?: number) => (n == null ? '?' : legend[n] ? `${n} ${legend[n]}` : String(n));
  switch (r.question?.type) {
    case 'choice': return w.in?.length ? w.in.join(' | ') : w.equals ?? '?';
    case 'score':
      if (w.op === 'between') return `${w.min ?? '?'}–${w.max ?? '?'}`;
      return `${w.op === '>=' ? '≥' : w.op === '<=' ? '≤' : '='} ${idx(w.value)}`;
    case 'noul': return `p ${w.op === '<' ? '<' : '≥'} ${f.dec(w.value ?? 0.5)}`;
  }
  return '?';
}

export const targetLabel = (t: Target | null | undefined, next: string) => (!t || (!t.route && !t.alias) ? next : targetString(t));

const defaultWhen = (type: QuestionType, labels: string[]): BranchWhen =>
  type === 'choice' ? { equals: labels[0] ?? '' } : type === 'score' ? { op: '>=', value: 1 } : { op: '>=', value: 0.7 };

/** A route, a route with a model, or an alias. */
export function TargetPicker({ value, onChange, routes, aliases, allowNext, label }: {
  value: Target | null | undefined; onChange: (t: Target) => void; routes: RouterRoute[]; aliases: string[]; allowNext?: boolean; label: string;
}) {
  const { t } = useI18n();
  const v = value ?? {};
  const key = v.alias ? `alias:${v.alias}` : v.route ? `route:${v.route}` : '';
  return (
    <div className="target-picker">
      <select aria-label={label} value={key} onChange={(e) => {
        const x = e.target.value;
        onChange(x.startsWith('alias:') ? { alias: x.slice(6) } : x.startsWith('route:') ? { route: x.slice(6) } : allowNext ? { next_rule: true } : {});
      }}>
        <option value="">{allowNext ? t('drule.nextRule') : t('drule.pickTarget')}</option>
        <optgroup label={t('drule.routes')}>{routes.map((r) => <option key={r.name} value={`route:${r.name}`}>{r.name}</option>)}</optgroup>
        {aliases.length > 0 && <optgroup label={t('drule.aliases')}>{aliases.map((a) => <option key={a} value={`alias:${a}`}>{a}</option>)}</optgroup>}
      </select>
      {v.route && (
        <input className="mono" aria-label={t('drule.model')} value={v.model ?? ''} placeholder={t('drule.modelPh')} onChange={(e) => onChange({ route: v.route, model: e.target.value || undefined })} />
      )}
    </div>
  );
}

function WhenEditor({ rule, when, onChange }: { rule: Rule; when: BranchWhen; onChange: (w: BranchWhen) => void }) {
  const { t } = useI18n();
  const labels = answerLabels(rule);
  const type = rule.question?.type;
  if (type === 'choice') {
    const on = when.in?.length ? when.in : when.equals ? [when.equals] : [];
    const toggle = (l: string) => {
      const next = on.includes(l) ? on.filter((x) => x !== l) : [...on, l];
      onChange(next.length === 1 ? { equals: next[0] } : { in: next });
    };
    return (
      <div className="chips">
        {labels.length === 0 && <span className="muted small">{t('drule.addLabelsFirst')}</span>}
        {labels.map((l) => (
          <label key={l} className={cx('chip', 'chip-check', on.includes(l) && 'on')}>
            <input type="checkbox" checked={on.includes(l)} onChange={() => toggle(l)} />{l}
          </label>
        ))}
      </div>
    );
  }
  if (type === 'score') {
    const idx = (k: 'value' | 'min' | 'max') => (
      <select aria-label={t(`drule.when.${k}`)} value={when[k] ?? ''} onChange={(e) => onChange({ ...when, [k]: Number(e.target.value) })}>
        {labels.map((l, i) => <option key={i} value={i}>{i} · {l}</option>)}
      </select>
    );
    return (
      <div className="when-row">
        <select aria-label={t('drule.when.op')} value={when.op ?? '>='} onChange={(e) => {
          const op = e.target.value as BranchWhen['op'];
          onChange(op === 'between' ? { op, min: when.value ?? 0, max: labels.length - 1 } : { op, value: when.value ?? when.min ?? 0 });
        }}>
          <option value=">=">≥</option><option value="<=">≤</option><option value="==">=</option><option value="between">{t('drule.when.between')}</option>
        </select>
        {when.op === 'between' ? <>{idx('min')}<span className="muted">–</span>{idx('max')}</> : idx('value')}
      </div>
    );
  }
  return (
    <div className="when-row">
      <span className="muted small">p(yes)</span>
      <select aria-label={t('drule.when.op')} value={when.op === '<' ? '<' : '>='} onChange={(e) => onChange({ op: e.target.value as '>=' | '<', value: when.value ?? 0.7 })}>
        <option value=">=">≥</option><option value="<">&lt;</option>
      </select>
      <input type="number" min={0} max={1} step={0.05} className="input-sm" aria-label={t('drule.when.value')} value={when.value ?? 0.7} onChange={(e) => onChange({ ...when, value: Number(e.target.value) })} />
    </div>
  );
}

/** Everything a decision rule has besides its preconditions. */
export function DecisionRuleForm({ rule, onChange, routes }: { rule: Rule; onChange: (r: Rule) => void; routes: RouterRoute[] }) {
  const { t, f } = useI18n();
  const dec = useFetch(() => decisionsApi.settings(), []);
  const al = useFetch(() => routerApi.aliases(), []);
  const aliases = (al.data ?? []).map((a) => a.name);
  const q = rule.question ?? { type: 'choice' as const, instructions: '' };
  const setQ = (patch: Partial<typeof q>) => onChange({ ...rule, question: { ...q, ...patch } });
  const branches = rule.branches ?? [];
  const setBranch = (i: number, b: Branch) => onChange({ ...rule, branches: branches.map((x, j) => (j === i ? b : x)) });
  const moveBranch = (i: number, d: -1 | 1) => {
    const next = [...branches];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    onChange({ ...rule, branches: next });
  };
  const facts = rule.inputs?.facts?.length ? rule.inputs.facts : ['latest_user_text', 'context_tokens', 'client'];
  const setFacts = (list: string[]) => onChange({ ...rule, inputs: { ...rule.inputs, facts: list } });
  const choices = q.type === 'choice' && q.criteria && !Array.isArray(q.criteria) ? Object.entries(q.criteria) : [];
  const legend = q.type === 'score' && Array.isArray(q.criteria) ? q.criteria : [];
  const setChoices = (rows: [string, string][]) => setQ({ criteria: Object.fromEntries(rows) });

  const switchType = (type: QuestionType) => {
    const criteria = type === 'choice' ? { yes: '', no: '' } : type === 'score' ? ['low', 'medium', 'high'] : undefined;
    const labels = type === 'choice' ? ['yes', 'no'] : [];
    onChange({ ...rule, question: { ...q, type, criteria }, branches: branches.map((b) => ({ ...b, when: defaultWhen(type, labels) })) });
  };

  return (
    <div className="stack-lg drule">
      <section className="stack">
        <h4 className="form-section">{t('drule.question')}</h4>
        <Segmented label={t('drule.type')} value={q.type} onChange={switchType}
          options={(['choice', 'score', 'noul'] as const).map((id) => ({ id, label: t(`drule.type.${id}`) }))} />
        <p className="fine">{t(`drule.type.${q.type}.hint`)}</p>
        <Field label={t('drule.instructions')}>
          <textarea className="textarea" rows={3} value={q.instructions} placeholder={t('drule.instructionsPh')} onChange={(e) => setQ({ instructions: e.target.value })} />
        </Field>
        {q.type === 'choice' && (
          <div className="stack">
            <span className="field-label">{t('drule.labels')}</span>
            {choices.map(([label, desc], i) => (
              <div key={i} className="crit-row">
                <input className="mono" aria-label={t('drule.label')} value={label} onChange={(e) => setChoices(choices.map((c, j) => (j === i ? [e.target.value, c[1]] : c)))} />
                <input aria-label={t('drule.description')} value={desc} placeholder={t('drule.descriptionPh')} onChange={(e) => setChoices(choices.map((c, j) => (j === i ? [c[0], e.target.value] : c)))} />
                <IconButton icon="x" label={t('drule.removeLabel')} onClick={() => setChoices(choices.filter((_, j) => j !== i))} />
              </div>
            ))}
            <Button size="sm" variant="ghost" icon="plus" onClick={() => setChoices([...choices, [`label${choices.length + 1}`, '']])}>{t('drule.addLabel')}</Button>
          </div>
        )}
        {q.type === 'score' && (
          <div className="stack">
            <span className="field-label">{t('drule.legend')} <span className="muted small">{t('drule.legendHint')}</span></span>
            {legend.map((l, i) => (
              <div key={i} className="crit-row crit-row-2">
                <span className="cnode-n">{i}</span>
                <input value={l} aria-label={t('drule.legendEntry', { n: i })} onChange={(e) => setQ({ criteria: legend.map((x, j) => (j === i ? e.target.value : x)) })} />
                <IconButton icon="x" label={t('drule.removeLabel')} onClick={() => setQ({ criteria: legend.filter((_, j) => j !== i) })} />
              </div>
            ))}
            <Button size="sm" variant="ghost" icon="plus" onClick={() => setQ({ criteria: [...legend, ''] })}>{t('drule.addLegend')}</Button>
          </div>
        )}
      </section>

      <section className="stack">
        <h4 className="form-section">{t('drule.inputs')} <span className="muted small">{t('drule.inputsHint')}</span></h4>
        <div className="chips">
          {FACTS.map((x) => (
            <label key={x} className={cx('chip', 'chip-check', facts.includes(x) && 'on')}>
              <input type="checkbox" checked={facts.includes(x)} onChange={() => setFacts(facts.includes(x) ? facts.filter((y) => y !== x) : [...facts, x])} />
              <span className="mono">{x}</span>
            </label>
          ))}
        </div>
        {facts.includes('goal') && (
          <div className="inline-field"><span>{t('drule.goalTurns')}</span>
            <input type="number" className="input-sm" min={1} max={20} value={rule.inputs?.goal_turns ?? 3} onChange={(e) => onChange({ ...rule, inputs: { ...rule.inputs, facts, goal_turns: Number(e.target.value) || undefined } })} />
          </div>
        )}
        <Field label={t('drule.headers')} hint={t('drule.headersHint')}>
          <input className="mono" value={(rule.inputs?.headers ?? []).join(', ')} placeholder="X-Customer-Tier" onChange={(e) => onChange({ ...rule, inputs: { ...rule.inputs, facts, headers: e.target.value.split(',').map((h) => h.trim()).filter(Boolean) } })} />
        </Field>
      </section>

      <section className="stack">
        <h4 className="form-section">{t('drule.backend')}</h4>
        <div className="form-grid form-grid-4">
          <Field label={t('drule.backend')}>
            <select value={rule.backend || 'economy'} onChange={(e) => onChange({ ...rule, backend: e.target.value === 'economy' ? undefined : e.target.value })}>
              <option value="economy">{t('drule.economy')}</option>
              {(dec.data?.backends ?? []).filter((b) => !b.implicit).map((b) => (
                <option key={b.name} value={b.name} disabled={b.auth === 'passthrough'}>{b.name}{b.auth === 'passthrough' ? ` — ${t('drule.passthrough')}` : ''}</option>
              ))}
            </select>
          </Field>
          <Field label={t('drule.model')}>
            <input className="mono" value={rule.backend_model ?? ''} placeholder={t('drule.backendModelPh')} onChange={(e) => onChange({ ...rule, backend_model: e.target.value || undefined })} />
          </Field>
          <Field label={t('drule.timeout')}>
            <div className="input-suffix"><input type="number" min={100} max={20000} step={100} value={rule.timeout_ms ?? 2000} onChange={(e) => onChange({ ...rule, timeout_ms: Number(e.target.value) || undefined })} /><span className="muted small">ms</span></div>
          </Field>
          <Field label={t('drule.minConfidence')}>
            <input type="number" min={0} max={1} step={0.05} value={rule.min_confidence ?? 0} onChange={(e) => onChange({ ...rule, min_confidence: Number(e.target.value) || undefined })} />
          </Field>
        </div>
      </section>

      <section className="stack">
        <h4 className="form-section">{t('drule.branches')} <span className="muted small">{t('drule.branchesHint')}</span></h4>
        <ol className="branch-list">
          {branches.map((b, i) => (
            <li key={i} className="branch-row">
              <span className="cnode-n">{i + 1}</span>
              <input className="branch-label" aria-label={t('drule.branchLabel')} value={b.label ?? ''} placeholder={t('drule.branchLabelPh')} onChange={(e) => setBranch(i, { ...b, label: e.target.value || undefined })} />
              <WhenEditor rule={rule} when={b.when} onChange={(w) => setBranch(i, { ...b, when: w })} />
              <Icon name="arrowRight" size={13} />
              <TargetPicker value={b.then} onChange={(x) => setBranch(i, { ...b, then: x })} routes={routes} aliases={aliases} label={t('drule.target')} />
              <span className="branch-actions">
                <IconButton icon="up" label={t('rules.moveUp')} disabled={i === 0} onClick={() => moveBranch(i, -1)} />
                <IconButton icon="down" label={t('rules.moveDown')} disabled={i === branches.length - 1} onClick={() => moveBranch(i, 1)} />
                <IconButton icon="trash" label={t('drule.removeBranch')} onClick={() => onChange({ ...rule, branches: branches.filter((_, j) => j !== i) })} />
              </span>
            </li>
          ))}
          <li className="branch-row branch-else">
            <span className="cnode-n">—</span>
            <strong className="branch-label">{t('drule.else')}</strong>
            <span className="muted small">{t('drule.elseHint')}</span>
            <Icon name="arrowRight" size={13} />
            <TargetPicker value={rule.else} allowNext onChange={(x) => onChange({ ...rule, else: x.route || x.alias ? x : { next_rule: true } })} routes={routes} aliases={aliases} label={t('drule.else')} />
          </li>
        </ol>
        <div className="btn-row">
          <Button size="sm" icon="plus" onClick={() => onChange({ ...rule, branches: [...branches, { when: defaultWhen(q.type, answerLabels(rule)), then: {} }] })}>{t('drule.addBranch')}</Button>
        </div>
      </section>

      <section className="stack">
        <h4 className="form-section">{t('drule.evaluate')}</h4>
        <div className="form-grid">
          <Field label={t('drule.evaluate')}>
            <select value={rule.evaluate ?? 'conversation_start'} onChange={(e) => onChange({ ...rule, evaluate: e.target.value as Rule['evaluate'] })}>
              {(['conversation_start', 'every_request', 'when_context_over'] as const).map((m) => <option key={m} value={m}>{t(`drule.eval.${m}`)}</option>)}
            </select>
          </Field>
          {rule.evaluate === 'when_context_over' && (
            <Field label={t('drule.contextOver')}>
              <div className="input-suffix"><input type="number" min={1000} step={1000} value={rule.context_over_tokens ?? 100000} onChange={(e) => onChange({ ...rule, context_over_tokens: Number(e.target.value) || undefined })} /><span className="muted small">{t('drule.tokens')}</span></div>
            </Field>
          )}
        </div>
        <p className="fine">{t(`drule.eval.${rule.evaluate ?? 'conversation_start'}.hint`, { n: f.compact(rule.context_over_tokens ?? 100000) })}</p>
        {rule.evaluate === 'every_request' && <Callout tone="warn" title={t('drule.cacheTitle')}>{t('drule.cacheBody')}</Callout>}
      </section>
    </div>
  );
}

function ConfBar({ v, min }: { v?: number; min?: number }) {
  const { f } = useI18n();
  if (v == null) return <span className="muted small">—</span>;
  const low = min != null && v < min;
  return (
    <span className="conf conf-wide">
      <span className="conf-track">
        <span className={cx('conf-fill', low && 'low', !low && v >= 0.9 && 'high')} style={{ width: `${v * 100}%` }} />
        {min ? <span className="conf-min" style={{ left: `${min * 100}%` }} /> : null}
      </span>
      <span className="mono small">{f.pct(v)}</span>
    </span>
  );
}

/** The question, the answer and its confidence, the probabilities, and where it sent the request. */
export function DecisionTraceView({ d, rule }: { d: DecisionTrace; rule?: Rule }) {
  const { t, f } = useI18n();
  const answer = d.question_type === 'choice' ? d.choice ?? d.answer
    : d.question_type === 'score' ? (d.index != null ? `${d.index}/${d.max_index ?? '?'}${d.label ? ` · ${d.label}` : ''}` : d.answer)
      : d.noul != null ? t('drule.pYes', { p: f.dec(d.noul) }) : d.answer;
  const probs = Object.entries(d.probabilities ?? {}).sort((a, b) => b[1] - a[1]);
  const legend = rule ? answerLabels(rule) : [];
  const top = probs.length ? probs[0][1] : 0;
  return (
    <div className="dtrace">
      <div className="dtrace-head">
        <Badge tone="accent">{t(`drule.type.${d.question_type}`)}</Badge>
        <strong className="dtrace-answer">{answer ?? '—'}</strong>
        <ConfBar v={d.confidence} min={d.min_confidence} />
        {d.dry_run && <Badge>{t('drule.dryRun')}</Badge>}
      </div>
      {d.min_confidence ? <p className="fine">{t('drule.minLine', { min: f.pct(d.min_confidence) })}</p> : null}
      {probs.length > 0 && (
        <ul className="dq-probs">
          {probs.map(([k, p]) => (
            <li key={k} className={cx(p === top && 'is-top')}>
              <span className="dq-label">{d.question_type === 'score' && legend[Number(k)] ? `${k} · ${legend[Number(k)]}` : k}</span>
              <span className="dq-bar"><span style={{ width: `${Math.max(0.5, p * 100)}%` }} /></span>
              <span className="mono small">{f.pct(p, true)}</span>
            </li>
          ))}
        </ul>
      )}
      <div className={cx('dtrace-outcome', d.outcome === 'branch' ? 'is-branch' : d.outcome === 'else' ? 'is-else' : 'is-fall')}>
        <Icon name={d.outcome === 'fall_through' ? 'down' : 'arrowRight'} size={13} />
        {d.outcome === 'branch'
          ? t('drule.outcome.branch', { n: d.branch + 1, label: d.branch_label || whenLabel(d, rule, f), target: targetLabel(d.target, '') })
          : d.outcome === 'else'
            ? t('drule.outcome.else', { target: targetLabel(d.target, '') })
            : t('drule.outcome.fall')}
        {(d.route || d.upstream_model) && d.outcome !== 'fall_through' && <span className="mono muted small"> · {d.route}{d.upstream_model ? `:${d.upstream_model}` : ''}</span>}
      </div>
      {d.error && <p className="dtrace-why"><Icon name="alert" size={12} /> {d.error}</p>}
      <p className="fine">
        {t('drule.meta', { backend: d.backend, model: d.model ?? '—', ms: f.ms(d.latency_ms) })}
        {d.forward_ms != null && ` · ${t('decision.forward', { ms: f.ms(d.forward_ms) })}`}
      </p>
    </div>
  );
}

const whenLabel = (d: DecisionTrace, rule: Rule | undefined, f: F) =>
  rule?.branches?.[d.branch] ? whenText(rule, rule.branches[d.branch].when, f) : `#${d.branch + 1}`;

/** A decision rule's route reason as a compact card (the record keeps the reason, not the whole trace). */
export function DecisionReasonView({ reason }: { reason: string }) {
  const { t, f } = useI18n();
  const r = parseDecisionReason(reason);
  if (!r) return null;
  return (
    <div className="dtrace dtrace-compact">
      <div className="dtrace-head">
        <Badge tone="accent" icon="routing">{t('drule.ruleNamed', { name: r.rule })}</Badge>
        {r.answer && <strong className="dtrace-answer">{r.answer}</strong>}
        {r.confidence != null && <ConfBar v={r.confidence} />}
        {r.redecided && <Badge>{t('drule.redecided')}</Badge>}
      </div>
      <div className={cx('dtrace-outcome', r.outcome === 'branch' ? 'is-branch' : r.outcome === 'else' ? 'is-else' : 'is-fall')}>
        <Icon name={r.outcome === 'fall_through' ? 'down' : 'arrowRight'} size={13} />
        {r.outcome === 'branch' ? t('drule.outcome.to', { target: r.target }) : r.outcome === 'else' ? t('drule.outcome.else', { target: r.target }) : t('drule.outcome.fall')}
      </div>
      {r.error && <p className="dtrace-why"><Icon name="alert" size={12} /> {r.error}</p>}
      {r.backend && <p className="fine">{t('drule.metaShort', { backend: r.backend, ms: f.ms(r.ms ?? 0) })}</p>}
    </div>
  );
}

/** Paste a text or pick a recent request, and see what the rule would do. Never pins. */
export function TryIt({ rule }: { rule: Rule }) {
  const { t } = useI18n();
  const { requests } = useGateway();
  const [mode, setMode] = useState<'text' | 'request'>('text');
  const [text, setText] = useState('');
  const [req, setReq] = useState('');
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<RuleTestResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const recent = requests.filter(isModelCall).slice(0, 25);
  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      setRes(await routerApi.testRule(mode === 'text' ? { rule, text } : { rule, request_id: req || recent[0]?.id }));
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
      setRes(null);
    } finally {
      setBusy(false);
    }
  };
  const failed = res?.conditions.filter((c) => !c.ok) ?? [];
  return (
    <section className="tryit stack" aria-label={t('drule.try')}>
      <div className="section-head"><h4>{t('drule.try')}</h4><span className="muted small">{t('drule.tryHint')}</span></div>
      <Segmented size="sm" label={t('drule.tryInput')} value={mode} onChange={setMode}
        options={[{ id: 'text', label: t('drule.tryText') }, { id: 'request', label: t('drule.tryRequest') }]} />
      {mode === 'text' ? (
        <textarea className="textarea" rows={3} value={text} placeholder={t('drule.tryPh')} onChange={(e) => setText(e.target.value)} />
      ) : (
        <select value={req} onChange={(e) => setReq(e.target.value)} aria-label={t('drule.tryRequest')}>
          {recent.length === 0 && <option value="">{t('drule.noRecent')}</option>}
          {recent.map((r) => <option key={r.id} value={r.id}>{r.time.slice(11, 19)} · {r.model || r.path} · {r.route}</option>)}
        </select>
      )}
      <div className="btn-row">
        <Button variant="primary" icon="play" loading={busy} disabled={mode === 'text' ? !text.trim() : recent.length === 0} onClick={run}>{t('drule.tryRun')}</Button>
      </div>
      {err && <Callout tone="bad">{err}</Callout>}
      {res && (
        <div className="tryit-result stack">
          {!res.conditions_ok && (
            <Callout tone="warn" title={t('drule.condsFailed')}>{failed.map((c) => `${c.condition}: ${c.detail}`).join(' · ')}</Callout>
          )}
          {res.decision && <DecisionTraceView d={res.decision} rule={rule} />}
          <p className="tryit-final">
            <Icon name="routing" size={13} />
            {res.route ? t('drule.wouldRoute', { route: res.route + (res.model ? `:${res.model}` : '') }) : t('drule.wouldFall')}
          </p>
          {res.notes?.map((n) => <p key={n} className="fine">{n}</p>)}
        </div>
      )}
    </section>
  );
}

export function useTemplates() {
  return useFetch(() => routerApi.templates(), []);
}

export function ruleSummary(t: T, rule: Rule): string {
  return `${rule.backend || t('drule.economyShort')} · ${t(`drule.type.${rule.question?.type ?? 'choice'}`)} · ${rule.name}`;
}
