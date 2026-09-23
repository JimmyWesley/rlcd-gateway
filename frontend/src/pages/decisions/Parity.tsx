// Parity: a sample of decisions is replayed on a mirror backend (a candidate
// model or host), and the answers are compared. Agreement by question,
// latency side by side, and the disagreements to read one by one.
import { useI18n } from '../../i18n';
import { decisionsApi, type MirrorStats, type Source } from '../../lib/decisionsApi';
import { href, navigate } from '../../lib/router';
import { useFetch, useLiveFetch } from '../../state/gateway';
import { BarList } from '../../charts';
import { Badge, Button, Card, EmptyState, ErrorState, Loading, Stat, cx } from '../../ui';

export function Parity({ since, source }: { since: string; source: Source }) {
  const { t, f } = useI18n();
  const stats = useLiveFetch(() => decisionsApi.stats({ since, source }), [since, source], 5000);
  const settings = useFetch(() => decisionsApi.settings(), []);
  // The API has no "disagreed" filter: read the recent mirrored items and keep the splits.
  const recent = useLiveFetch(() => decisionsApi.list({ since, source, limit: '500' }), [since, source], 5000);
  const s = stats.data;
  if (stats.error && !s) return <ErrorState error={stats.error} onRetry={stats.reload} />;
  if (!s) return <Card><Loading lines={6} /></Card>;

  const m = s.mirror;
  const mirrorOn = !!settings.data?.mirror;
  if (m.calls === 0) {
    return (
      <Card>
        <EmptyState icon="layers" title={mirrorOn ? t('parity.waiting.title') : t('parity.off.title')}
          actions={<Button onClick={() => navigate('decisions/settings')}>{t('parity.configure')}</Button>}>
          <p>{mirrorOn ? t('parity.waiting.body', { backend: settings.data!.mirror!.backend, rate: f.pct(settings.data!.mirror!.sample_rate) }) : t('parity.off.body')}</p>
        </EmptyState>
      </Card>
    );
  }

  const disagreements = (recent.data?.items ?? []).filter((it) => it.mirror && it.mirror.questions.some((q) => !q.agree));
  const qs = Object.entries(s.questions).filter(([, q]) => q.mirror && q.mirror.compared > 0);
  const rate = (x: { agreement_rate?: number | null }) => x.agreement_rate ?? 0;
  const tone = (r: number) => (r >= 0.95 ? 'good' : r >= 0.8 ? 'warn' : 'bad');
  const cfg = settings.data?.mirror;
  return (
    <>
      <div className="kpi-row kpi-row-4">
        <Card className="kpi"><Stat icon="layers" label={t('parity.kpi.agreement')} value={m.agreement_rate != null ? f.pct(m.agreement_rate, true) : '—'} tone={m.agreement_rate != null ? tone(m.agreement_rate) === 'good' ? 'good' : 'warn' : undefined} sub={t('parity.kpi.compared', { agreed: f.num(m.agreed), compared: f.num(m.compared) })} /></Card>
        <Card className="kpi"><Stat icon="cpu" label={t('parity.kpi.calls')} value={f.num(m.calls)} sub={cfg ? t('parity.kpi.sample', { backend: cfg.backend, rate: f.pct(cfg.sample_rate) }) : t('parity.kpi.nowOff')} /></Card>
        <Card className="kpi"><Stat icon="zap" label={t('parity.kpi.latency')} value={<LatencyPair a={m.primary_p50_ms} b={m.mirror_p50_ms} />} sub={t('parity.kpi.p95', { a: f.ms(m.primary_p95_ms), b: f.ms(m.mirror_p95_ms) })} /></Card>
        <Card className="kpi"><Stat icon="alert" label={t('parity.kpi.errors')} value={f.num(m.errors)} tone={m.errors ? 'warn' : undefined} sub={t('parity.kpi.skipped', { skipped: f.num(m.skipped), failed: f.num(m.save_failed) })} /></Card>
      </div>

      <div className="grid grid-2">
        <Card title={t('parity.byQuestion')} subtitle={t('parity.byQuestionSub')}>
          <BarList
            label={t('parity.byQuestion')}
            max={1}
            items={qs.sort((a, b) => rate(a[1].mirror!) - rate(b[1].mirror!)).map(([id, q]) => ({
              key: id,
              label: <code>{id}</code>,
              value: rate(q.mirror!),
              display: f.pct(rate(q.mirror!)),
              sub: t('parity.kpi.compared', { agreed: f.num(q.mirror!.agreed), compared: f.num(q.mirror!.compared) }),
              color: `var(--${tone(rate(q.mirror!))})`,
              onClick: () => navigate('decisions/audit', { question: id }),
            }))}
          />
        </Card>
        <Card title={t('parity.latency')} subtitle={t('parity.latencySub')}>
          <LatencyTable rows={Object.keys(m.by_backend ?? {}).length > 1 ? [[t('parity.overall'), m], ...Object.entries(m.by_backend ?? {})] : Object.entries(m.by_backend ?? { [cfg?.backend ?? t('parity.mirror')]: m })} />
        </Card>
      </div>

      <Card title={t('parity.diff.title')} subtitle={t('parity.diff.sub', { count: disagreements.length })}>
        {disagreements.length === 0 ? (
          <EmptyState compact icon="check" title={t('parity.diff.none')} />
        ) : (
          <table className="table parity-table">
            <thead>
              <tr>
                <th>{t('traffic.col.time')}</th>
                <th>{t('dec.question')}</th>
                <th>{t('parity.primary')}</th>
                <th>{t('parity.mirror')}</th>
                <th>{t('parity.truth')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {disagreements.slice(0, 50).flatMap((it) => it.mirror!.questions.filter((q) => !q.agree).map((mq) => {
                const pq = it.questions.find((q) => q.id === mq.id);
                return (
                  <tr key={`${it.request_id}-${mq.id}`}>
                    <td className="mono small">{f.dayTime(it.time)}</td>
                    <td><code>{mq.id}</code></td>
                    <td><Answer v={mq.primary_answer ?? pq?.answer} conf={mq.primary_confidence ?? pq?.confidence} backend={it.mirror!.primary_backend} good={pq?.correct} /></td>
                    <td><Answer v={mq.answer} conf={mq.confidence} backend={it.mirror!.backend} /></td>
                    <td>{pq?.outcome != null ? <span className={cx(pq.correct ? 'tone-good' : 'tone-bad')}>{String(pq.outcome)}</span> : <span className="muted">—</span>}</td>
                    <td className="num"><a className="small" href={href('traffic/' + it.request_id)}>{t('common.details')}</a></td>
                  </tr>
                );
              }))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}

function Answer({ v, conf, backend, good }: { v?: string; conf?: number; backend: string; good?: boolean }) {
  const { f } = useI18n();
  return (
    <span className="parity-answer">
      <strong className={cx(good === true && 'tone-good', good === false && 'tone-bad')}>{v ?? '—'}</strong>
      {conf != null && <span className="muted small"> {f.pct(conf)}</span>}
      <span className="muted tiny mono">{backend}</span>
    </span>
  );
}

function LatencyPair({ a, b }: { a: number; b: number }) {
  const { f } = useI18n();
  return <span className="lat-pair">{f.ms(a)}<span className="muted"> / </span>{f.ms(b)}</span>;
}

function LatencyTable({ rows }: { rows: [string, MirrorStats][] }) {
  const { t, f } = useI18n();
  const max = Math.max(1, ...rows.flatMap(([, m]) => [m.primary_p95_ms, m.mirror_p95_ms]));
  const bar = (v: number, cls: string) => <span className="lat-track"><span className={cls} style={{ width: `${(v / max) * 100}%` }} /></span>;
  return (
    <div className="lat-table">
      <div className="legend">
        <span className="legend-item static"><i className="legend-key" style={{ ['--c' as string]: 'var(--series-1)' }} />{t('parity.primary')}</span>
        <span className="legend-item static"><i className="legend-key" style={{ ['--c' as string]: 'var(--series-7)' }} />{t('parity.mirror')}</span>
        <span className="muted small">{t('parity.latencyLegend')}</span>
      </div>
      {rows.map(([name, m]) => (
        <div key={name} className="lat-row">
          <div className="lat-name"><span className="mono small">{name}</span> <Badge>{t('parity.kpi.compared', { agreed: f.num(m.agreed), compared: f.num(m.compared) })}</Badge></div>
          <div className="lat-bars">
            <div>{bar(m.primary_p50_ms, 'lat-p')}<span className="mono small">{f.ms(m.primary_p50_ms)} · p95 {f.ms(m.primary_p95_ms)}</span></div>
            <div>{bar(m.mirror_p50_ms, 'lat-m')}<span className="mono small">{f.ms(m.mirror_p50_ms)} · p95 {f.ms(m.mirror_p95_ms)}</span></div>
          </div>
        </div>
      ))}
    </div>
  );
}
