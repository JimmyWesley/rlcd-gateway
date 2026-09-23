import { useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { api } from '../../lib/api';
import { pruneApi, type Feedback, type ReplayReport } from '../../lib/pruneApi';
import { recordModel } from '../../lib/brands';
import { href, navigate } from '../../lib/router';
import { useFetch, useGateway, useLiveFetch } from '../../state/gateway';
import { TimeChart } from '../../charts';
import { Badge, Button, Callout, Card, EmptyState, ErrorState, Loading, ModelLabel, PageHeader, Stat, SubNav, cx } from '../../ui';
import { axisTime, useWindowPref, WindowPicker } from '../overview/Overview';
import { pruneOf } from '../traffic/Traffic';
import { useReasonLabel } from '../traffic/PruneDiff';
import { PruneSettings } from './PruneSettings';

export function Savings({ sub }: { sub: string }) {
  const { t } = useI18n();
  const tab = sub === 'settings' ? 'settings' : 'results';
  return (
    <div className="page">
      <PageHeader
        title={t('nav.savings')}
        description={t('savings.desc')}
        tabs={
          <SubNav
            label={t('nav.savings')}
            active={tab}
            items={[
              { id: 'results', label: t('savings.tab.results'), href: '#/savings' },
              { id: 'settings', label: t('savings.tab.settings'), href: '#/savings/settings' },
            ]}
          />
        }
      />
      {tab === 'results' ? <Results /> : <PruneSettings />}
    </div>
  );
}

function Results() {
  const { t, f } = useI18n();
  const cfg = useFetch(() => pruneApi.config(), []);
  const stats = useLiveFetch(() => pruneApi.stats(), []);
  const [win, setWin] = useWindowPref('rlcd.savings.window');
  const ins = useLiveFetch(() => api.insights(win), [win]);
  const s = stats.data;
  const eff = cfg.data?.effective;

  return (
    <>
      {eff && !eff.enabled && (
        <Callout tone="warn" title={t('savings.disabled.title')} action={<Button size="sm" onClick={() => navigate('savings/settings')}>{t('savings.openSettings')}</Button>}>
          {t('savings.disabled.body')}
        </Callout>
      )}
      {eff && eff.enabled && eff.mode === 'shadow' && (
        <Callout tone="accent" icon="eye" title={t('savings.shadow.title')} action={<Button size="sm" variant="primary" onClick={() => navigate('savings/settings')}>{t('savings.shadow.cta')}</Button>}>
          {t('savings.shadow.body')}
        </Callout>
      )}
      {stats.error && !s && <ErrorState error={stats.error} onRetry={stats.reload} />}
      {!s && !stats.error && <Loading />}
      {s && (
        <div className="kpi-row">
          <Card className="kpi">
            <Stat icon="savings" label={t('savings.kpi.saved')} value={t('common.tokensShort', { n: f.tokens(s.enforce.saved_tokens) })}
              tone={s.enforce.saved_usd < 0 ? 'bad' : s.enforce.saved_tokens ? 'good' : undefined}
              sub={t('savings.kpi.savedSub', { usd: f.usd(s.enforce.saved_usd), pruned: s.enforce.pruned, total: s.enforce.requests })} />
          </Card>
          <Card className="kpi">
            <Stat icon="eye" label={t('savings.kpi.shadow')} value={t('common.tokensShort', { n: f.tokens(s.shadow.saved_tokens) })}
              sub={t('savings.kpi.shadowSub', { usd: f.usd(s.shadow.saved_usd), pruned: s.shadow.pruned, total: s.shadow.requests })} />
          </Card>
          <Card className="kpi">
            <Stat icon="zap" label={t('savings.kpi.epochs')} value={f.num(s.epochs)} sub={s.epochs ? t('savings.kpi.epochsSub', { ms: f.ms(s.avg_selector_ms) }) : t('savings.kpi.noEpochs')} />
          </Card>
          <Card className="kpi">
            <Stat icon="database" label={t('savings.kpi.rewrites')} hint={t('savings.kpi.rewritesHint')} value={f.num(s.enforce.cache_invalidations + s.shadow.cache_invalidations)} sub={t('savings.kpi.rewritesSub')} />
          </Card>
          <Card className="kpi">
            <Stat icon="alert" label={t('savings.kpi.selectorErrors')} value={f.num(s.selector_errors)} tone={s.selector_errors ? 'warn' : undefined} sub={t('savings.kpi.selectorErrorsSub')} />
          </Card>
        </div>
      )}
      {s && <p className="fine">{t('savings.window', { count: s.window })}</p>}

      <Card title={t('savings.chart.title')} subtitle={t('savings.chart.sub')} actions={<WindowPicker value={win} onChange={setWin} />}>
        {ins.error && !ins.data && <ErrorState error={ins.error} onRetry={ins.reload} />}
        {ins.data && (
          <TimeChart
            kind="columns"
            times={ins.data.buckets.map((b) => Date.parse(b.t))}
            series={[
              { key: 'enf', label: t('savings.series.enforced'), color: 'var(--saved)', values: ins.data.buckets.map((b) => b.saved_tokens) },
              { key: 'sh', label: t('savings.series.shadow'), color: 'var(--shadow)', values: ins.data.buckets.map((b) => b.shadow_saved_tokens) },
            ]}
            stacked
            yFormat={(v) => f.compact(v)}
            xFormat={(x) => axisTime(f, ins.data!, x)}
            tipTitle={(x) => f.dayTime(x)}
            label={t('savings.chart.title')}
            empty={t('savings.chart.empty')}
          />
        )}
      </Card>

      <TopPruned />
      <FeedbackCases dirtyHint={false} />
    </>
  );
}

function TopPruned() {
  const { t, f } = useI18n();
  const { requests } = useGateway();
  const top = useMemo(
    () => requests.filter((r) => pruneOf(r)?.saved_tokens).sort((a, b) => (pruneOf(b)!.saved_tokens ?? 0) - (pruneOf(a)!.saved_tokens ?? 0)).slice(0, 8),
    [requests],
  );
  return (
    <Card title={t('savings.top.title')} subtitle={t('savings.top.sub')}>
      {top.length === 0 ? (
        <EmptyState compact icon="savings" title={t('savings.top.empty')}>{t('savings.top.emptyBody')}</EmptyState>
      ) : (
        <ul className="toplist">
          {top.map((r) => {
            const p = pruneOf(r)!;
            const share = r.est_tokens ? (p.saved_tokens ?? 0) / r.est_tokens : 0;
            return (
              <li key={r.id}>
                <a href={href(`traffic/${r.id}`)} className="toplist-row">
                  <span className="mono small muted">{f.time(r.time)}</span>
                  <ModelLabel model={recordModel(r)} />
                  <span className="toplist-bar"><span style={{ width: `${share * 100}%` }} /></span>
                  <Badge tone={p.applied ? 'dropped' : 'shadow'}>−{f.tokens(p.saved_tokens)}</Badge>
                  <span className="muted small num">{f.pct(share)}</span>
                </a>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

/** Feedback cases from the inspector, and replay against the saved settings. */
export function FeedbackCases({ dirtyHint }: { dirtyHint: boolean }) {
  const { t, f, tn } = useI18n();
  const reason = useReasonLabel();
  const fb = useFetch(() => pruneApi.feedback(), []);
  const [replay, setReplay] = useState<ReplayReport | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const list: Feedback[] = fb.data ?? [];
  const byId = new Map(replay?.cases.map((c) => [c.id, c]) ?? []);

  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      setReplay(await pruneApi.replay());
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };
  const del = async (id: string) => {
    try {
      await pruneApi.deleteFeedback(id);
      fb.setData((xs) => (xs ?? []).filter((x) => x.id !== id));
      setReplay((r) => (r ? { ...r, cases: r.cases.filter((c) => c.id !== id) } : r));
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    }
  };

  return (
    <Card
      title={<>{t('feedback.title')} <Badge>{list.length}</Badge></>}
      subtitle={tn('feedback.sub', { recall: <code>rlcd_recall</code> })}
      actions={
        <Button size="sm" icon="play" loading={busy} disabled={list.length === 0 || dirtyHint} onClick={run} title={dirtyHint ? t('feedback.saveFirst') : undefined}>
          {t('feedback.replay')}
        </Button>
      }
    >
      {fb.error && <ErrorState error={fb.error} onRetry={fb.reload} />}
      {err && <Callout tone="bad">{err}</Callout>}
      {replay && (
        <div className="replay-sum">
          <Badge tone="good">{t('feedback.agree', { count: replay.agree })}</Badge>
          <Badge tone={replay.disagree ? 'bad' : 'neutral'}>{t('feedback.disagree', { count: replay.disagree })}</Badge>
          {replay.errors > 0 && <Badge tone="bad">{t('feedback.errors', { count: replay.errors })}</Badge>}
          <span className="muted small">{t('feedback.fixedBroken', { fixed: replay.fixed, broken: replay.broken, ms: f.ms(replay.selector_ms) })}</span>
        </div>
      )}
      {fb.data && list.length === 0 && (
        <EmptyState compact icon="thumbUp" title={t('feedback.empty')}>{t('feedback.emptyBody')}</EmptyState>
      )}
      {list.length > 0 && (
        <div className="scroll-x">
          <table className="table">
            <thead>
              <tr>
                <th>{t('feedback.col.block')}</th>
                <th>{t('feedback.col.verdict')}</th>
                <th>{t('feedback.col.then')}</th>
                <th>{t('feedback.col.now')}</th>
                <th className="num">{t('feedback.col.score')}</th>
                <th><span className="sr-only">{t('common.actions')}</span></th>
              </tr>
            </thead>
            <tbody>
              {list.map((x) => {
                const r = byId.get(x.id);
                return (
                  <tr key={x.id}>
                    <td className="clip" title={`${x.request_id} · ${x.key}\n${x.preview}`}>
                      <a href={href(`traffic/${x.request_id}`)} className="mono small">{x.name || x.kind}</a>
                      <div className="muted tiny clip">{x.what || x.key}</div>
                      {x.note && <div className="small">“{x.note}”</div>}
                      {x.source === 'recall' && <Badge tone="accent" icon="recall">{t('feedback.recallTag')}</Badge>}
                    </td>
                    <td><Badge tone={x.verdict === 'should_keep' ? 'kept' : 'dropped'}>{x.verdict === 'should_keep' ? t('feedback.shouldKeep') : t('feedback.shouldDrop')}</Badge></td>
                    <td><Badge tone={x.decision === 'drop' ? 'dropped' : 'kept'}>{t(`decision.${x.decision}`)}</Badge> <span className="muted tiny">{reason(x.reason)}</span></td>
                    <td>
                      {r ? (
                        r.error ? <span className="tone-bad small">{r.error}</span> : (
                          <span className="now-cell">
                            <Badge tone={r.decision === 'drop' ? 'dropped' : 'kept'}>{t(`decision.${r.decision}`)}</Badge>
                            <span className={cx(r.agrees ? 'tone-good' : 'tone-bad')}>{r.agrees ? '✓' : '✗'}</span>
                            {r.agrees !== r.then_agreed && <Badge tone={r.agrees ? 'good' : 'bad'}>{r.agrees ? t('feedback.fixed') : t('feedback.broken')}</Badge>}
                          </span>
                        )
                      ) : <span className="muted">—</span>}
                    </td>
                    <td className="num mono small">{f.score(r?.score ?? x.score)}</td>
                    <td className="num">
                      {x.source === 'recall'
                        ? <span className="muted tiny" title={t('feedback.fromRecallHint')}>{t('feedback.fromRecall')}</span>
                        : <Button size="sm" variant="ghost" icon="trash" onClick={() => del(x.id)} aria-label={t('feedback.delete')} />}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
