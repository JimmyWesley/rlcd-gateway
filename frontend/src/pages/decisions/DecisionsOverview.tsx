import { useMemo } from 'react';
import { useI18n } from '../../i18n';
import type { Window } from '../../lib/api';
import { decisionsApi, type DecisionItem, type DecisionStats, type Group, type Source } from '../../lib/decisionsApi';
import { href, navigate } from '../../lib/router';
import { useLiveFetch } from '../../state/gateway';
import { BarList, Bars, TimeChart } from '../../charts';
import { Badge, Button, Card, CopyField, EmptyState, ErrorState, Loading, Stat } from '../../ui';

const WINDOW_MS: Record<Window, number> = { '1h': 3.6e6, '6h': 6 * 3.6e6, '24h': 24 * 3.6e6, '7d': 7 * 24 * 3.6e6, all: 0 };
const SOURCES = ['client', 'prune', 'router'] as const;
const SOURCE_COLOR = { client: 'var(--series-1)', prune: 'var(--series-2)', router: 'var(--series-7)' } as const;

export const pctLabels = (f: ReturnType<typeof useI18n>['f']) => Array.from({ length: 10 }, (_, i) => f.pct(i / 10));

/** Buckets the recent items into ~24 time slots, per source, with latency percentiles. */
function bucketize(items: DecisionItem[], win: Window) {
  if (!items.length) return null;
  const now = Date.now();
  const times = items.map((i) => Date.parse(i.time));
  // From the first decision in the window, so a quiet week does not squash today.
  const first = Math.min(...times);
  const from = WINDOW_MS[win] ? Math.max(now - WINDOW_MS[win], first) : first;
  const n = 24;
  const step = Math.max(60_000, Math.ceil((now - from) / n));
  const start = now - step * n;
  const buckets = Array.from({ length: n }, (_, i) => ({ t: start + i * step, client: 0, prune: 0, router: 0, errors: 0, d: [] as number[], fwd: [] as number[] }));
  items.forEach((it, k) => {
    const i = Math.min(n - 1, Math.floor((times[k] - start) / step));
    if (i < 0 || i >= n) return;
    const b = buckets[i];
    b[it.source] += 1;
    if (it.status >= 400 || it.error) b.errors++;
    b.d.push(it.duration_ms);
    if (it.forward_ms != null) b.fwd.push(it.forward_ms);
  });
  const p = (xs: number[], q: number) => {
    if (!xs.length) return NaN;
    const s = [...xs].sort((a, b) => a - b);
    return s[Math.min(s.length - 1, Math.ceil((q / 100) * s.length) - 1)];
  };
  return { step, buckets: buckets.map((b) => ({ ...b, p50: p(b.d, 50), p95: p(b.d, 95), fwd50: p(b.fwd, 50) })) };
}

export function DecisionsOverview({ since, win, source, hidden }: { since: string; win: Window; source: Source; hidden: number }) {
  const { t, f } = useI18n();
  const stats = useLiveFetch(() => decisionsApi.stats({ since, source }), [since, source], 4000);
  const recent = useLiveFetch(() => decisionsApi.list({ since, source, limit: '500' }), [since, source], 4000);
  const series = useMemo(() => (recent.data ? bucketize(recent.data.items, win) : null), [recent.data, win]);
  const s = stats.data;

  if (stats.error && !s) return <ErrorState error={stats.error} onRetry={stats.reload} />;
  if (!s) return <Card><Loading lines={6} /></Card>;
  if (s.total.requests === 0) return <DecisionsEmpty />;

  const tot = s.total;
  const xFmt = (x: number) => (series && series.step >= 3 * 3.6e6 ? f.day(x) : f.hm(x));
  return (
    <>
      <div className="kpi-row">
        <Card className="kpi"><Stat icon="cpu" label={t('dec.kpi.decisions')} value={f.compact(tot.requests)} sub={t('dec.kpi.questions', { n: f.num(Object.values(s.questions).reduce((a, q) => a + q.count, 0)) })} /></Card>
        <Card className="kpi"><Stat icon="zap" label={t('dec.kpi.p50')} value={f.ms(tot.p50_ms)} sub={t('dec.kpi.p95', { v: f.ms(tot.p95_ms) })} /></Card>
        <Card className="kpi"><Stat icon="cpu" label={t('dec.kpi.forward')} hint={t('dec.kpi.forwardHint')} value={f.ms(tot.forward_p50_ms)} sub={t('dec.kpi.p95', { v: f.ms(tot.forward_p95_ms) })} /></Card>
        <Card className="kpi"><Stat icon="alert" label={t('dec.kpi.errors')} value={f.pct(tot.error_rate, true)} tone={tot.errors ? 'warn' : undefined} sub={t('overview.kpi.errors', { count: tot.errors })} /></Card>
        <Card className="kpi"><Stat icon="dollar" label={t('dec.kpi.cost')} value={f.usd(tot.est_cost_usd)} sub={t('dec.kpi.tokens', { n: f.compact(tot.input_tokens) })} /></Card>
      </div>

      <div className="grid grid-2">
        <Card title={t('dec.volume.title')} subtitle={t('dec.volume.sub')}>
          {series ? (
            <TimeChart
              kind="columns"
              stacked
              times={series.buckets.map((b) => b.t)}
              series={SOURCES.map((src) => ({ key: src, label: t(`decision.source.${src}`), color: SOURCE_COLOR[src], values: series.buckets.map((b) => b[src]) }))}
              yFormat={(v) => f.compact(v)}
              xFormat={xFmt}
              tipTitle={(x) => f.dayTime(x)}
              label={t('dec.volume.title')}
            />
          ) : <Loading />}
          {recent.data && recent.data.total > recent.data.items.length && <p className="fine">{t('dec.volume.recent', { n: recent.data.items.length, total: recent.data.total })}</p>}
        </Card>
        <Card title={t('dec.latency.title')} subtitle={t('dec.latency.sub', { p50: f.ms(tot.p50_ms), fwd: f.ms(tot.forward_p50_ms) })}>
          {series ? (
            <TimeChart
              kind="line"
              times={series.buckets.map((b) => b.t)}
              series={[
                { key: 'p50', label: 'p50', color: 'var(--series-1)', values: series.buckets.map((b) => b.p50) },
                { key: 'p95', label: 'p95', color: 'var(--series-7)', values: series.buckets.map((b) => b.p95) },
                { key: 'fwd', label: t('dec.latency.forward'), color: 'var(--series-3)', values: series.buckets.map((b) => b.fwd50) },
              ]}
              yFormat={(v) => f.ms(v)}
              xFormat={xFmt}
              tipTitle={(x) => f.dayTime(x)}
              label={t('dec.latency.title')}
            />
          ) : <Loading />}
        </Card>
      </div>

      <div className="grid grid-2">
        <Card title={t('dec.conf.title')} subtitle={t('dec.conf.sub')}>
          <Bars labels={pctLabels(f)} values={s.confidence_histogram} label={t('dec.conf.title')} format={(v) => t('dec.nQuestions', { count: v })} highlight={(i) => i < 6} />
          <p className="fine">{t('dec.conf.hint')} <a href={href('decisions/audit', { confidence_below: '0.6' })}>{t('dec.unsureLink')}</a></p>
        </Card>
        <Card title={t('dec.breakdown')}>
          <div className="stack-lg">
            <GroupList title={t('dec.byBackend')} groups={s.by_backend} onPick={(k) => navigate('decisions/audit', { backend: k })} />
            <GroupList title={t('dec.byModel')} groups={s.by_model} onPick={(k) => navigate('decisions/audit', { model: k })} />
          </div>
        </Card>
      </div>

      <div className="section-head"><h3>{t('dec.questions.title')}</h3><span className="muted small">{t('dec.questions.sub')}</span></div>
      <div className="q-grid">
        {Object.entries(s.questions).sort((a, b) => b[1].count - a[1].count).map(([id, q]) => (
          <QuestionCard key={id} id={id} q={q} />
        ))}
      </div>
      {hidden > 0 && (
        <p className="fine">{t('dec.hiddenInternal', { count: hidden })} <a href={href('decisions', { src: 'prune' })}>{t('dec.showInternal')}</a></p>
      )}
    </>
  );
}

function GroupList({ title, groups, label, onPick }: { title: string; groups: Record<string, Group>; label?: (k: string) => string; onPick: (k: string) => void }) {
  const { t, f } = useI18n();
  const entries = Object.entries(groups).sort((a, b) => b[1].requests - a[1].requests);
  return (
    <div className="group-list">
      <div className="fact-label">{title}</div>
      <BarList
        label={title}
        items={entries.map(([k, g], i) => ({
          key: k,
          label: <span className="mono">{label ? label(k) : k}</span>,
          value: g.requests,
          display: f.compact(g.requests),
          sub: t('dec.groupRow', { p50: f.ms(g.p50_ms), fwd: f.ms(g.forward_p50_ms), err: f.pct(g.error_rate, true) }),
          color: ['var(--series-1)', 'var(--series-2)', 'var(--series-3)', 'var(--series-7)'][i % 4],
          onClick: () => onPick(k),
        }))}
      />
    </div>
  );
}

function QuestionCard({ id, q }: { id: string; q: DecisionStats['questions'][string] }) {
  const { t, f } = useI18n();
  const answers = Object.entries(q.answers).sort((a, b) => b[1] - a[1]);
  const total = answers.reduce((s, a) => s + a[1], 0) || 1;
  return (
    <Card className="q-card" title={<code>{id}</code>} actions={<Button size="sm" variant="ghost" onClick={() => navigate('decisions/audit', { question: id })}>{t('common.details')}</Button>}>
      <ul className="q-answers">
        {answers.slice(0, 6).map(([a, n]) => (
          <li key={a}>
            <span className="q-ans">{a}</span>
            <span className="dq-bar"><span style={{ width: `${(n / total) * 100}%` }} /></span>
            <span className="mono small">{f.num(n)}</span>
          </li>
        ))}
        {!answers.length && <li className="muted small">{t('dec.noAnswers')}</li>}
      </ul>
      {answers.length > 0 && <div className="mini-stats">
        <Stat label={t('dec.meanConf')} value={f.pct(q.mean_confidence)} />
        <Stat label={t('dec.accuracy')} value={q.accuracy != null ? f.pct(q.accuracy) : '—'} sub={t('dec.outcomes', { count: q.outcomes })} tone={q.accuracy != null ? (q.accuracy >= 0.9 ? 'good' : q.accuracy < 0.7 ? 'warn' : undefined) : undefined} />
        <Stat label="ECE" value={q.ece != null ? f.score(q.ece) : '—'} />
      </div>}
      {q.mirror && q.mirror.compared > 0 && <Badge className="self-start" tone={q.mirror.agreement_rate === 1 ? 'good' : 'warn'}>{t('dec.mirrorAgree', { rate: f.pct(q.mirror.agreement_rate ?? 0) })}</Badge>}
    </Card>
  );
}

export function DecisionsEmpty() {
  const { t } = useI18n();
  return (
    <Card>
      <EmptyState icon="cpu" title={t('dec.empty.title')} actions={<Button onClick={() => navigate('integrations/apps')}>{t('dec.empty.cta')}</Button>}>
        <p>{t('dec.empty.body')}</p>
        <CopyField multiline label="curl" text={`curl ${window.location.origin}/v1/systemone -H "Content-Type: application/json" -d '{"model": "Open-RLCD-text", "state": "I was charged twice", "questions": {"refund": {"type": "noul", "instructions": "Asks for a refund?"}}}'`} />
      </EmptyState>
    </Card>
  );
}
