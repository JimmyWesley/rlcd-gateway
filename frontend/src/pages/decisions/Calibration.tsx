// Calibration: when System One says 90%, is it right 90% of the time?
// One reliability diagram per question, built from the recorded outcomes.
import { useI18n } from '../../i18n';
import { decisionsApi, type QuestionStats } from '../../lib/decisionsApi';
import { href } from '../../lib/router';
import { useLiveFetch } from '../../state/gateway';
import { Reliability } from '../../charts';
import { Badge, Callout, Card, ErrorState, Loading, Stat } from '../../ui';
import { DecisionsEmpty } from './DecisionsOverview';

/** Bins with fewer outcomes than this are drawn faded and called out. */
const THIN = 5;

export function Calibration({ since }: { since: string }) {
  const { t, f } = useI18n();
  const stats = useLiveFetch(() => decisionsApi.stats({ since }), [since], 5000);
  const s = stats.data;
  if (stats.error && !s) return <ErrorState error={stats.error} onRetry={stats.reload} />;
  if (!s) return <Card><Loading lines={6} /></Card>;
  if (s.total.requests === 0) return <DecisionsEmpty />;

  const qs = Object.entries(s.questions).sort((a, b) => b[1].outcomes - a[1].outcomes || b[1].count - a[1].count);
  const withOutcomes = qs.filter(([, q]) => q.outcomes > 0);
  const without = qs.filter(([, q]) => q.outcomes === 0);
  return (
    <>
      <Callout tone="info" icon="info" title={t('cal.explain.title')}>
        <p>{t('cal.explain.body')}</p>
        <p className="muted small">{t('cal.explain.ece', { thin: THIN })}</p>
      </Callout>
      {withOutcomes.length === 0 && (
        <Card>
          <p>{t('cal.noOutcomes')} <a href={href('decisions/audit', { outcome: 'without' })}>{t('cal.recordLink')}</a></p>
        </Card>
      )}
      <div className="cal-grid">
        {withOutcomes.map(([id, q]) => <QuestionCalibration key={id} id={id} q={q} />)}
      </div>
      {without.length > 0 && (
        <p className="fine">
          {t('cal.without', { count: without.length })}{' '}
          {without.map(([id], i) => (
            <span key={id}>{i > 0 && ', '}<a href={href('decisions/audit', { question: id, outcome: 'without' })}><code>{id}</code></a></span>
          ))}
        </p>
      )}
      <p className="fine">{t('cal.window', { n: f.num(s.total.requests) })}</p>
    </>
  );
}

function QuestionCalibration({ id, q }: { id: string; q: QuestionStats }) {
  const { t, f } = useI18n();
  const thin = q.bins.filter((b) => b.n > 0 && b.n < THIN).length;
  const gap = q.accuracy != null ? q.accuracy - q.mean_confidence : null;
  // The average gap can hide errors that cancel out; ECE cannot, so it decides.
  const verdict = gap == null || q.ece == null ? null
    : q.ece <= 0.05 ? 'ok' : Math.abs(gap) <= 0.05 ? 'mixed' : gap > 0 ? 'under' : 'over';
  return (
    <Card
      title={<code>{id}</code>}
      subtitle={t('cal.card.sub', { outcomes: f.num(q.outcomes), count: f.num(q.count) })}
      actions={q.ece != null && <Badge tone={q.ece <= 0.05 ? 'good' : q.ece <= 0.15 ? 'warn' : 'bad'}>ECE {f.score(q.ece)}</Badge>}
    >
      <Reliability
        bins={q.bins}
        thin={THIN}
        label={t('cal.diagram', { question: id })}
        labels={{
          perfect: t('cal.perfect'),
          accuracy: t('dec.accuracy'),
          confidence: t('cal.confidence'),
          n: (n) => t('dec.outcomes', { count: n }),
          pct: (v) => f.pct(v),
        }}
      />
      <div className="mini-stats">
        <Stat label={t('dec.meanConf')} value={f.pct(q.mean_confidence)} />
        <Stat label={t('dec.accuracy')} value={q.accuracy != null ? f.pct(q.accuracy) : '—'} sub={t('cal.correctOf', { correct: q.correct, outcomes: q.outcomes })} />
        <Stat label={t('cal.gap')} hint={t('cal.gapHint')} value={gap != null ? `${gap >= 0 ? '+' : '−'}${f.pct(Math.abs(gap))}` : '—'}
          tone={verdict === 'ok' ? 'good' : verdict ? 'warn' : undefined}
          sub={verdict ? t(`cal.gap.${verdict}`) : undefined} />
      </div>
      {thin > 0 && <p className="fine">{t('cal.thin', { count: thin, min: THIN })}</p>}
    </Card>
  );
}
