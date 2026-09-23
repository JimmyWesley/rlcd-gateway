// The audit log: every System One call, filterable, with its state, its
// answers and the outcome control that feeds calibration.
import { useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { BrandIcon } from '../../icons/BrandIcon';
import { api, purgedInfo } from '../../lib/api';
import { recordClient } from '../../lib/brands';
import {
  decisionsApi, requestQuestions, requestState, responseProbabilities,
  type DecisionFilters, type DecisionItem, type DecisionList, type DecisionQuestion, type Source,
} from '../../lib/decisionsApi';
import { href, navigate, useLocation } from '../../lib/router';
import { useFetch } from '../../state/gateway';
import { Badge, Button, Callout, EmptyState, ErrorState, Field, Loading, cx } from '../../ui';
import { DecisionCard } from './DecisionCard';

const FILTERS = ['question', 'answer', 'status', 'outcome', 'backend', 'model', 'key', 'confidence_below'] as const;
type FilterKey = (typeof FILTERS)[number];
const UNSURE = '0.6';
const PAGE = 50;

export function Audit({ since, source }: { since: string; source: Source }) {
  const { t, f } = useI18n();
  const loc = useLocation();
  const q = (k: FilterKey) => loc.query.get(k) ?? '';
  const filters: DecisionFilters = { since, source, ...Object.fromEntries(FILTERS.map((k) => [k, q(k)])) };
  const fkey = JSON.stringify(filters);
  const setFilter = (k: FilterKey, v: string) => {
    const next: Record<string, string> = {};
    loc.query.forEach((val, key) => (next[key] = val));
    if (v) next[k] = v;
    else delete next[k];
    navigate('decisions/audit', next, true);
  };
  const [panel, setPanel] = useState(false);

  // Options for the selects come from the stats of the same window.
  const stats = useFetch(() => decisionsApi.stats({ since, source }), [since, source]);
  const first = useFetch(() => decisionsApi.list({ ...filters, limit: String(PAGE) }), [fkey]);
  const [pages, setPages] = useState<DecisionList[]>([]);
  const [more, setMore] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => setPages([]), [fkey]);

  const items = useMemo(() => [first.data, ...pages].flatMap((p) => p?.items ?? []), [first.data, pages]);
  const last = pages.length ? pages[pages.length - 1] : first.data;
  const loadMore = async () => {
    if (!last?.next_cursor) return;
    setMore(true);
    try {
      const p = await decisionsApi.list({ ...filters, limit: String(PAGE), cursor: last.next_cursor });
      setPages((ps) => [...ps, p]);
    } finally {
      setMore(false);
    }
  };
  // An outcome saved in a detail updates the row in place.
  const patch = (id: string, qq: DecisionQuestion) => {
    const upd = (l: DecisionList | null) => l && { ...l, items: l.items.map((it) => it.request_id !== id ? it : { ...it, questions: it.questions.map((x) => (x.id === qq.id ? { ...x, ...qq } : x)) }) };
    first.setData((d) => upd(d));
    setPages((ps) => ps.map((p) => upd(p)!));
  };

  const s = stats.data;
  const questions = s ? Object.keys(s.questions).sort() : [];
  const backends = s ? Object.keys(s.by_backend).sort() : [];
  const models = s ? Object.keys(s.by_model).sort() : [];
  const answers = s && q('question') ? Object.keys(s.questions[q('question')]?.answers ?? {}).sort() : [];
  const keys = useMemo(() => [...new Set(items.map((i) => i.key_name ?? '').filter(Boolean))].sort(), [items]);
  const unsure = q('confidence_below') !== '';
  const active = FILTERS.filter((k) => q(k) && k !== 'confidence_below');
  const dims = FILTERS.filter((k) => q(k) && !['confidence_below', 'status'].includes(k)).length;

  return (
    <section className="card card-flush audit" aria-label={t('audit.label')}>
      <div className="list-toolbar">
        <Button size="sm" icon="alert" aria-pressed={unsure} className={cx(unsure && 'is-on')} onClick={() => setFilter('confidence_below', unsure ? '' : UNSURE)}>
          {t('audit.unsure', { pct: f.pct(Number(UNSURE)) })}
        </Button>
        <select className="select-sm" aria-label={t('audit.f.status')} value={q('status')} onChange={(e) => setFilter('status', e.target.value)}>
          <option value="">{t('audit.status.all')}</option>
          <option value="ok">{t('audit.status.ok')}</option>
          <option value="error">{t('audit.status.error')}</option>
        </select>
        <Button size="sm" icon="filter" aria-expanded={panel} onClick={() => setPanel(!panel)} className={cx(dims > 0 && 'is-on')}>
          {t('traffic.filters')}{dims > 0 && ` · ${dims}`}
        </Button>
        <span className="toolbar-spacer" />
        <span className="muted small">{first.data ? t('audit.count', { count: first.data.total }) : ''}</span>
        <Button size="sm" variant="ghost" icon="refresh" onClick={first.reload} aria-label={t('common.refresh')} />
        <a className="btn btn-sm btn-secondary" href={decisionsApi.exportURL(filters, 'csv')} download>{t('audit.export', { format: 'CSV' })}</a>
        <a className="btn btn-sm btn-secondary" href={decisionsApi.exportURL(filters, 'jsonl')} download>{t('audit.export', { format: 'JSONL' })}</a>
      </div>
      {panel && (
        <div className="filter-panel">
          <Field label={t('audit.f.question')}>
            <select value={q('question')} onChange={(e) => setFilter('question', e.target.value)}>
              <option value="">{t('audit.all')}</option>
              {questions.map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
          </Field>
          <Field label={t('audit.f.answer')}>
            {answers.length ? (
              <select value={q('answer')} onChange={(e) => setFilter('answer', e.target.value)}>
                <option value="">{t('audit.all')}</option>
                {answers.map((x) => <option key={x} value={x}>{x}</option>)}
              </select>
            ) : (
              <input value={q('answer')} placeholder={t('audit.answerPh')} onChange={(e) => setFilter('answer', e.target.value)} />
            )}
          </Field>
          <Field label={t('audit.f.outcome')}>
            <select value={q('outcome')} onChange={(e) => setFilter('outcome', e.target.value)}>
              <option value="">{t('audit.all')}</option>
              <option value="with">{t('audit.outcome.with')}</option>
              <option value="without">{t('audit.outcome.without')}</option>
            </select>
          </Field>
          <Field label={t('audit.f.backend')}>
            <select value={q('backend')} onChange={(e) => setFilter('backend', e.target.value)}>
              <option value="">{t('audit.all')}</option>
              {backends.map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
          </Field>
          <Field label={t('audit.f.model')}>
            <select value={q('model')} onChange={(e) => setFilter('model', e.target.value)}>
              <option value="">{t('audit.all')}</option>
              {models.map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
          </Field>
          <Field label={t('audit.f.key')}>
            <select value={q('key')} onChange={(e) => setFilter('key', e.target.value)} disabled={!keys.length && !q('key')}>
              <option value="">{t('audit.all')}</option>
              {[...new Set([...keys, q('key')].filter(Boolean))].map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
          </Field>
        </div>
      )}
      {(active.length > 0 || unsure) && (
        <div className="filter-chips">
          {unsure && (
            <button type="button" className="chip" onClick={() => setFilter('confidence_below', '')}>
              <span className="muted">{t('audit.f.confidence')}</span> &lt; {f.pct(Number(q('confidence_below')))} <Icon name="x" size={12} />
            </button>
          )}
          {active.map((k) => (
            <button key={k} type="button" className="chip" onClick={() => setFilter(k, '')} aria-label={t('traffic.removeFilter', { name: t(`audit.f.${k}`) })}>
              <span className="muted">{t(`audit.f.${k}`)}</span> {q(k)} <Icon name="x" size={12} />
            </button>
          ))}
          <button type="button" className="linkish small" onClick={() => navigate('decisions/audit', {}, true)}>{t('traffic.clearFilters')}</button>
        </div>
      )}
      <div className="audit-head" aria-hidden>
        <span>{t('traffic.col.time')}</span>
        <span>{t('audit.col.from')}</span>
        <span>{t('audit.col.answers')}</span>
        <span>{t('audit.col.backend')}</span>
        <span className="num">{t('traffic.col.status')}</span>
        <span className="num">{t('traffic.col.latency')}</span>
      </div>
      <div className="audit-rows" role="list">
        {first.error && !first.data && <div className="pad"><ErrorState error={first.error} onRetry={first.reload} /></div>}
        {!first.data && !first.error && <div className="pad"><Loading lines={6} /></div>}
        {first.data && items.length === 0 && (
          <EmptyState compact icon="filter" title={t('audit.empty')} actions={active.length || unsure ? <Button size="sm" onClick={() => navigate('decisions/audit', {}, true)}>{t('traffic.clearFilters')}</Button> : undefined} />
        )}
        {items.map((it) => (
          <AuditRow key={it.request_id} it={it} open={open === it.request_id} onToggle={() => setOpen(open === it.request_id ? null : it.request_id)} onChange={(qq) => patch(it.request_id, qq)} />
        ))}
      </div>
      {last?.next_cursor && (
        <div className="pad center">
          <Button size="sm" loading={more} onClick={loadMore}>{t('audit.more', { n: items.length, total: first.data?.total ?? 0 })}</Button>
        </div>
      )}
    </section>
  );
}

function AuditRow({ it, open, onToggle, onChange }: { it: DecisionItem; open: boolean; onToggle: () => void; onChange: (q: DecisionQuestion) => void }) {
  const { t, f } = useI18n();
  const failed = it.status >= 400 || !!it.error;
  const client = it.source === 'client' ? recordClient({ client: it.client, key_name: it.key_name } as Parameters<typeof recordClient>[0]) : null;
  const mirrorDiff = it.mirror && it.mirror.compared > it.mirror.agreed;
  return (
    <div role="listitem" className={cx('audit-row', open && 'is-open', failed && 'is-failed')}>
      <button type="button" className="audit-sum" aria-expanded={open} onClick={onToggle}>
        <span className="mono small audit-time" title={f.ago(it.time)}>
          <span className={cx('audit-caret', open && 'on')} aria-hidden>›</span> {new Date(it.time).toDateString() === new Date().toDateString() ? f.time(it.time) : f.dayTime(it.time)}
        </span>
        <span className="audit-from">
          {it.source === 'client' ? (
            <>
              {client && <BrandIcon id={client.icon} label={client.name} size={14} />}
              <span className="truncate">{it.key_name ?? client?.name}</span>
            </>
          ) : <Badge tone="shadow" icon="cpu">{t(`decision.source.${it.source}`)}</Badge>}
        </span>
        <span className="audit-answers">
          {it.questions.length === 0 && <span className="muted small">{it.error ?? '—'}</span>}
          {it.questions.map((q) => (
            <span key={q.id} className={cx('qa', q.correct === true && 'is-good', q.correct === false && 'is-bad', (q.confidence ?? 1) < Number(UNSURE) && 'is-unsure')}>
              <span className="qa-id">{q.id}</span>
              <span className="qa-ans">{q.answer ?? '—'}</span>
              {q.confidence != null && <span className="qa-conf">{f.pct(q.confidence)}</span>}
              {q.correct != null && <Icon name={q.correct ? 'check' : 'x'} size={11} />}
            </span>
          ))}
          {mirrorDiff && <Badge tone="warn" icon="alert">{t('audit.mirrorDiff')}</Badge>}
        </span>
        <span className="audit-backend mono small truncate">{it.backend}<span className="muted"> · {it.model}</span></span>
        <span className={cx('num mono small', failed && 'tone-bad')}>{it.status || '—'}</span>
        <span className="num mono small" title={it.forward_ms != null ? t('decision.forward', { ms: f.ms(it.forward_ms) }) : undefined}>
          {f.ms(it.duration_ms)}
        </span>
      </button>
      {open && <AuditDetail it={it} onChange={onChange} />}
    </div>
  );
}

function AuditDetail({ it, onChange }: { it: DecisionItem; onChange: (q: DecisionQuestion) => void }) {
  const { t, f } = useI18n();
  const d = useFetch(() => api.request(it.request_id), [it.request_id]);
  const probs = useMemo(() => responseProbabilities(d.data?.response_body), [d.data]);
  const defs = useMemo(() => requestQuestions(d.data?.request_body), [d.data]);
  const state = useMemo(() => requestState(d.data?.request_body), [d.data]);
  const purged = purgedInfo(d.cause);
  return (
    <div className="audit-detail">
      <div className="audit-facts">
        <span><span className="fact-label">{t('audit.col.backend')}</span> <span className="mono">{it.backend}</span></span>
        <span><span className="fact-label">{t('traffic.col.model')}</span> <span className="mono">{it.model}</span></span>
        {it.forward_ms != null && <span><span className="fact-label">{t('dec.kpi.forward')}</span> <span className="mono">{f.ms(it.forward_ms)}</span></span>}
        {it.usage && <span><span className="fact-label">{t('dec.tokensIn')}</span> <span className="mono">{f.num(it.usage.input_tokens ?? 0)}</span></span>}
        {it.est_cost_usd != null && <span><span className="fact-label">{t('dec.kpi.cost')}</span> <span className="mono">{f.usd(it.est_cost_usd)}</span></span>}
        {it.think && <Badge>{t('decision.think')}</Badge>}
        <span className="toolbar-spacer" />
        {it.parent_id && <a className="small" href={href(`traffic/${it.parent_id}`)}>{t('decision.openParent')}</a>}
        <a className="small" href={href(`traffic/${it.request_id}`)}>{t('audit.openTraffic')} <Icon name="external" size={11} /></a>
      </div>
      {it.error && <Callout tone="bad" title={t('chat.error.title', { status: it.status || t('traffic.err') })}>{it.error}</Callout>}
      {purged && <p className="fine">{t('audit.purged')}</p>}
      {d.loading && !d.data && !purged ? <Loading lines={3} /> : (
        <DecisionCard questions={it.questions} probs={probs} defs={defs} state={state} mirror={it.mirror} requestId={it.request_id} editable={it.status < 400 && !it.error} onChange={onChange} />
      )}
    </div>
  );
}
