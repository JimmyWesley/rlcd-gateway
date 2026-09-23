import { useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { BrandIcon, ClientIcon } from '../../icons/BrandIcon';
import { api, gatewayURL, recallApi, WINDOWS, type Insights, type Window } from '../../lib/api';
import { isClass, resilienceApi } from '../../lib/resilienceApi';
import { agentsApi, type AgentStatus } from '../../lib/agentsApi';
import { storageApi } from '../../lib/storageApi';
import { clientFromSlug, modelVendor, PROVIDER_NAMES, routeProvider, vendorIcon } from '../../lib/brands';
import { href, navigate } from '../../lib/router';
import { useFetch, useGateway, useLiveFetch } from '../../state/gateway';
import { BarList, Donut, Sparkline, TimeChart } from '../../charts';
import { Badge, Button, Card, CopyField, Dot, EmptyState, ErrorState, PageHeader, Segmented, Skeleton, Stat, cx } from '../../ui';
import { load, save } from '../../lib/storage';

const ROUTE_COLORS = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)', 'var(--series-4)', 'var(--series-5)', 'var(--series-6)', 'var(--series-7)', 'var(--series-8)'];

export function useWindowPref(key: string, def: Window = '24h'): [Window, (w: Window) => void] {
  const [w, setW] = useState<Window>(() => {
    const v = load(key);
    return (WINDOWS as string[]).includes(v ?? '') ? (v as Window) : def;
  });
  return [w, (x) => { setW(x); save(key, x); }];
}

export function WindowPicker({ value, onChange }: { value: Window; onChange: (w: Window) => void }) {
  const { t } = useI18n();
  return (
    <Segmented
      label={t('window.label')}
      value={value}
      onChange={onChange}
      options={WINDOWS.map((w) => ({ id: w, label: t(`window.${w}`), title: t(`window.${w}.long`) }))}
      size="sm"
    />
  );
}

export function Overview() {
  const { t, f } = useI18n();
  const { requests, requestsLoaded, config } = useGateway();
  const [win, setWin] = useWindowPref('rlcd.overview.window');
  const ins = useLiveFetch(() => api.insights(win), [win]);
  const d = ins.data;

  if (requestsLoaded && requests.length === 0 && !ins.error) {
    return (
      <div className="page">
        <PageHeader title={t('nav.overview')} description={t('overview.descEmpty')} />
        <GetStarted listen={config?.listen} />
        <div className="grid grid-2">
          <SelectorCard insights={null} />
          <AgentsCard />
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <PageHeader
        title={t('nav.overview')}
        description={t('overview.desc', { window: t(`window.${win}.long`) })}
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      {ins.error && !d && <ErrorState error={ins.error} onRetry={ins.reload} />}
      {!d && !ins.error && <OverviewSkeleton />}
      {d && (
        <>
          <div className="grid grid-hero">
            <SavingsHero d={d} />
            <div className="kpi-grid">
              <RequestsKpi d={d} />
              <CacheKpi d={d} />
              <LatencyKpi d={d} />
              <SpendKpi d={d} />
              <InputKpi d={d} />
              <ConvKpi d={d} />
            </div>
          </div>
          <div className="grid grid-2">
            <Card title={t('overview.requests.title')} subtitle={t('overview.requests.sub')}>
              <TimeChart
                kind="columns"
                stacked
                times={d.buckets.map((b) => Date.parse(b.t))}
                series={[
                  { key: 'ok', label: t('overview.series.ok'), color: 'var(--series-1)', values: d.buckets.map((b) => b.requests - b.errors) },
                  { key: 'err', label: t('overview.series.errors'), color: 'var(--bad)', values: d.buckets.map((b) => b.errors) },
                ]}
                yFormat={(v) => f.compact(v)}
                xFormat={(x) => axisTime(f, d, x)}
                tipTitle={(x) => bucketTitle(f, d, x)}
                label={t('overview.requests.title')}
                empty={t('overview.noData')}
              />
            </Card>
            <Card title={t('overview.tokens.title')} subtitle={t('overview.tokens.sub')}>
              <TimeChart
                kind="columns"
                stacked
                times={d.buckets.map((b) => Date.parse(b.t))}
                series={[
                  { key: 'read', label: t('usage.cacheRead'), color: 'var(--cache-read)', values: d.buckets.map((b) => b.cache_read_input_tokens) },
                  { key: 'write', label: t('usage.cacheWrite'), color: 'var(--cache-write)', values: d.buckets.map((b) => b.cache_creation_input_tokens) },
                  { key: 'fresh', label: t('usage.fresh'), color: 'var(--fresh)', values: d.buckets.map((b) => b.input_tokens) },
                ]}
                yFormat={(v) => f.compact(v)}
                xFormat={(x) => axisTime(f, d, x)}
                tipTitle={(x) => bucketTitle(f, d, x)}
                label={t('overview.tokens.title')}
                empty={t('overview.noData')}
              />
            </Card>
          </div>
          <div className="grid grid-3">
            <ClientsCard d={d} />
            <RoutesCard d={d} />
            <ModelsCard d={d} />
          </div>
          <div className="grid grid-3">
            <CacheCard d={d} />
            <Card title={t('overview.latency.title')} subtitle={t('overview.latency.sub', { p50: f.ms(d.latency.p50_ms), p95: f.ms(d.latency.p95_ms) })}>
              <TimeChart
                kind="line"
                times={d.buckets.map((b) => Date.parse(b.t))}
                series={[
                  { key: 'p50', label: 'p50', color: 'var(--series-1)', values: d.buckets.map((b) => b.latency_p50_ms) },
                  { key: 'p95', label: 'p95', color: 'var(--series-7)', values: d.buckets.map((b) => b.latency_p95_ms) },
                ]}
                yFormat={(v) => f.ms(v)}
                xFormat={(x) => axisTime(f, d, x)}
                tipTitle={(x) => bucketTitle(f, d, x)}
                label={t('overview.latency.title')}
                height={180}
                empty={t('overview.noData')}
              />
            </Card>
            <SelectorCard insights={d} />
          </div>
          <ResilienceCard win={win} />
          <div className="grid grid-3">
            <RecallCard />
            <StorageCard />
            <AgentsCard />
          </div>
        </>
      )}
    </div>
  );
}

type F = ReturnType<typeof useI18n>['f'];
export const axisTime = (f: F, d: Pick<Insights, 'bucket_seconds'>, x: number) =>
  d.bucket_seconds >= 86400 ? f.day(x) : d.bucket_seconds >= 21600 ? f.dayTime(x) : d.bucket_seconds >= 3600 ? f.hour(x) : f.hm(x);
const bucketTitle = (f: F, d: Insights, x: number) => `${f.dayTime(x)} – ${f.hm(x + d.bucket_seconds * 1000)}`;

function OverviewSkeleton() {
  return (
    <div className="grid grid-hero" aria-hidden>
      <Card><Skeleton h={20} w="40%" /><Skeleton h={48} w="60%" className="mt-3" /><Skeleton h={120} className="mt-4" /></Card>
      <div className="kpi-grid">{[0, 1, 2, 3].map((i) => <Card key={i}><Skeleton h={14} w="50%" /><Skeleton h={28} w="70%" className="mt-2" /></Card>)}</div>
    </div>
  );
}

function cumulative(xs: number[]): number[] {
  let s = 0;
  return xs.map((x) => (s += x));
}

function SavingsHero({ d }: { d: Insights }) {
  const { t, f, tn } = useI18n();
  const tt = d.totals;
  const enforced = tt.saved_tokens > 0 || tt.shadow_saved_tokens === 0;
  const tokens = enforced ? tt.saved_tokens : tt.shadow_saved_tokens;
  const usd = enforced ? tt.saved_usd : tt.shadow_saved_usd;
  const negative = usd < 0;
  const times = d.buckets.map((b) => Date.parse(b.t));
  const hasShadow = tt.shadow_saved_tokens > 0;
  const hasEnforce = tt.saved_tokens > 0;
  return (
    <Card className="hero-card">
      <div className="hero-top">
        <div>
          <div className="hero-kicker">
            <Icon name="savings" size={14} />
            {enforced ? t('overview.hero.saved') : t('overview.hero.wouldSave')}
            {!enforced && <Badge tone="shadow">{t('prune.mode.shadow')}</Badge>}
          </div>
          <div className="hero-value">
            {tn('overview.hero.tokens', { n: <span className="hero-num">{f.compact(tokens)}</span> })}
          </div>
          <div className={cx('hero-usd', negative ? 'tone-bad' : 'tone-good')}>
            {negative ? t('overview.hero.usdNegative', { usd: f.usd(usd) }) : t('overview.hero.usd', { usd: f.usd(usd) })}
          </div>
        </div>
        <div className="hero-side">
          {hasEnforce && hasShadow && (
            <div className="hero-mini">
              <span className="muted">{t('overview.hero.wouldSave')}</span>
              <strong>{t('common.tokensShort', { n: f.compact(tt.shadow_saved_tokens) })}</strong>
              <span className={tt.shadow_saved_usd < 0 ? 'tone-bad' : 'muted'}>{f.usd(tt.shadow_saved_usd)}</span>
            </div>
          )}
          <div className="hero-mini">
            <span className="muted">{t('overview.hero.pruned')}</span>
            <strong>{f.num(tt.pruned_requests)}</strong>
            <span className="muted">{t('overview.hero.ofRequests', { n: tt.requests })}</span>
          </div>
          <div className="hero-mini">
            <span className="muted">{t('overview.hero.rewrites')}</span>
            <strong>{f.num(tt.cache_invalidations)}</strong>
            <span className="muted">{t('overview.hero.rewritesSub')}</span>
          </div>
        </div>
      </div>
      <TimeChart
        kind="line"
        times={times}
        series={[
          ...(hasEnforce || !hasShadow ? [{ key: 'enf', label: t('overview.hero.cumEnforced'), color: 'var(--saved)', values: cumulative(d.buckets.map((b) => b.saved_usd)) }] : []),
          ...(hasShadow ? [{ key: 'sh', label: t('overview.hero.cumShadow'), color: 'var(--shadow)', values: cumulative(d.buckets.map((b) => b.shadow_saved_usd)) }] : []),
        ]}
        dashed={['sh']}
        yFormat={(v) => f.usdAxis(v)}
        xFormat={(x) => axisTime(f, d, x)}
        tipTitle={(x) => t('overview.hero.until', { time: f.dayTime(x + d.bucket_seconds * 1000) })}
        label={t('overview.hero.chart')}
        height={150}
        empty={t('overview.hero.noPruning')}
      />
      <p className="fine">
        {negative ? t('overview.hero.negativeWhy') : t('overview.hero.estimate')}{' '}
        <a href="#/savings">{t('overview.hero.details')}</a>
      </p>
    </Card>
  );
}

function RequestsKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  return (
    <Card className="kpi">
      <Stat
        icon="traffic"
        label={t('overview.kpi.requests')}
        value={f.compact(tt.requests)}
        sub={tt.errors ? <span className="tone-bad">{t('overview.kpi.errors', { count: tt.errors })}</span> : t('overview.kpi.noErrors')}
        trend={<Sparkline values={d.buckets.map((b) => b.requests)} label={t('overview.kpi.requests')} />}
      />
    </Card>
  );
}

function CacheKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  const input = tt.input_tokens + tt.cache_read_input_tokens + tt.cache_creation_input_tokens;
  const share = input ? tt.cache_read_input_tokens / input : null;
  return (
    <Card className="kpi">
      <Stat
        icon="database"
        label={t('overview.kpi.cache')}
        hint={t('overview.kpi.cacheHint')}
        value={f.pct(share)}
        sub={t('overview.kpi.cacheSub', { n: f.compact(tt.cache_read_input_tokens) })}
        trend={
          <Sparkline
            values={d.buckets.map((b) => {
              const i = b.input_tokens + b.cache_read_input_tokens + b.cache_creation_input_tokens;
              return i ? b.cache_read_input_tokens / i : 0;
            })}
            color="var(--cache-read)"
            label={t('overview.kpi.cache')}
          />
        }
      />
    </Card>
  );
}

function LatencyKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  return (
    <Card className="kpi">
      <Stat
        icon="clock"
        label={t('overview.kpi.latency')}
        value={f.ms(d.latency.p50_ms)}
        sub={t('overview.kpi.latencySub', { p95: f.ms(d.latency.p95_ms), ttfb: f.ms(d.latency.ttfb_p50_ms) })}
        trend={<Sparkline values={d.buckets.map((b) => b.latency_p50_ms)} color="var(--series-7)" label={t('overview.kpi.latency')} />}
      />
    </Card>
  );
}

function SpendKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  const input = tt.input_tokens + tt.cache_read_input_tokens + tt.cache_creation_input_tokens;
  return (
    <Card className="kpi">
      <Stat
        icon="dollar"
        label={t('overview.kpi.spend')}
        hint={t('overview.kpi.spendHint')}
        value={f.usd(tt.est_cost_usd)}
        sub={t('overview.kpi.spendSub', { input: f.compact(input), out: f.compact(tt.output_tokens) })}
        trend={<Sparkline values={d.buckets.map((b) => b.est_cost_usd)} color="var(--series-4)" label={t('overview.kpi.spend')} />}
      />
    </Card>
  );
}

function InputKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  const input = tt.input_tokens + tt.cache_read_input_tokens + tt.cache_creation_input_tokens;
  return (
    <Card className="kpi">
      <Stat
        icon="layers"
        label={t('overview.kpi.input')}
        value={f.compact(input)}
        sub={t('overview.kpi.inputSub', { out: f.compact(tt.output_tokens) })}
        trend={<Sparkline values={d.buckets.map((b) => b.input_tokens + b.cache_read_input_tokens + b.cache_creation_input_tokens)} color="var(--fresh)" label={t('overview.kpi.input')} />}
      />
    </Card>
  );
}

function ConvKpi({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  const clients = (d.by_client ?? []).filter((c) => c.name !== 'unknown').length;
  return (
    <Card className="kpi">
      <Stat
        icon="flow"
        label={t('overview.kpi.convs')}
        value={f.num(tt.conversations)}
        sub={t('overview.kpi.convsSub', { count: clients })}
        trend={<Sparkline values={d.buckets.map((b) => b.requests)} color="var(--series-3)" label={t('overview.kpi.convs')} area={false} />}
      />
    </Card>
  );
}

function ClientsCard({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const groups = d.by_client ?? [];
  const keys = d.by_key ?? [];
  return (
    <Card title={t('overview.clients.title')} subtitle={t('overview.clients.sub')}>
      {groups.length === 0 ? (
        <EmptyState compact icon="integrations" title={t('overview.noData')} />
      ) : (
        <BarList
          label={t('overview.clients.title')}
          items={groups.slice(0, 6).map((g, i) => {
            const c = clientFromSlug(g.name, g.label);
            return {
              key: g.name,
              label: <span className="brand-label"><ClientIcon client={c} size={16} /><span className="clip">{c.name || t('client.unknown')}</span></span>,
              value: g.requests,
              display: f.compact(g.requests),
              sub: t('overview.clients.row', { tokens: f.compact(g.tokens), usd: f.usd(g.est_cost_usd) }),
              color: ROUTE_COLORS[(i + 4) % ROUTE_COLORS.length],
              onClick: () => navigate('traffic', { client: g.name }),
              title: t('overview.routes.click'),
            };
          })}
        />
      )}
      {keys.length > 0 && (
        <div className="key-strip">
          <span className="fact-label">{t('overview.clients.keys')}</span>
          <div className="chips">
            {keys.slice(0, 6).map((k) => (
              <a key={k.name} className="chip" href={`#/traffic?key=${encodeURIComponent(k.name)}`}>
                <Icon name="key" size={12} /> {k.name} <span className="muted">{f.compact(k.requests)}</span>
              </a>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

function CacheCard({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const tt = d.totals;
  const input = tt.input_tokens + tt.cache_read_input_tokens + tt.cache_creation_input_tokens;
  return (
    <Card title={t('overview.cache.title')} subtitle={t('overview.cache.sub')}>
      <Donut
        label={t('overview.cache.title')}
        format={(v) => f.compact(v)}
        segments={[
          { key: 'read', label: t('usage.cacheRead'), value: tt.cache_read_input_tokens, color: 'var(--cache-read)' },
          { key: 'write', label: t('usage.cacheWrite'), value: tt.cache_creation_input_tokens, color: 'var(--cache-write)' },
          { key: 'fresh', label: t('usage.fresh'), value: tt.input_tokens, color: 'var(--fresh)' },
        ]}
        center={
          <>
            <div className="donut-value">{f.compact(input)}</div>
            <div className="donut-label">{t('overview.cache.center')}</div>
          </>
        }
      />
    </Card>
  );
}

function RoutesCard({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  const { config } = useGateway();
  const routeMeta = (name: string) => config?.routes.find((r) => r.name === name);
  const colorOf = useMemo(() => {
    const names = [...(config?.routes.map((r) => r.name) ?? []), ...d.by_route.map((r) => r.name)];
    const uniq = [...new Set(names)];
    return (n: string) => ROUTE_COLORS[uniq.indexOf(n) % ROUTE_COLORS.length];
  }, [config, d.by_route]);
  return (
    <Card title={t('overview.routes.title')} subtitle={t('overview.routes.sub')} actions={<Button size="sm" variant="ghost" onClick={() => navigate('routes')}>{t('route.manage')}</Button>}>
      {d.by_route.length === 0 ? (
        <EmptyState compact icon="routing" title={t('overview.noData')} />
      ) : (
        <BarList
          label={t('overview.routes.title')}
          items={d.by_route.slice(0, 6).map((g) => {
            const r = routeMeta(g.name);
            const p = r ? routeProvider(r) : g.name === 'chatgpt' || g.name.startsWith('openai') ? 'openai' : g.name === 'passthrough' ? 'anthropic' : undefined;
            return {
              key: g.name,
              label: (
                <span className="brand-label">
                  <BrandIcon id={p} label={p ? PROVIDER_NAMES[p] : g.name} size={14} />
                  <span className="clip">{g.name}</span>
                  {config?.active_route === g.name && <Badge tone="accent">{t('route.activeShort')}</Badge>}
                </span>
              ),
              value: g.requests,
              display: f.compact(g.requests),
              sub: t('overview.routes.row', { tokens: f.compact(g.tokens), count: g.errors }),
              color: colorOf(g.name),
              onClick: () => navigate('traffic', { route: g.name }),
              title: t('overview.routes.click'),
              action: r ? <a className="icon-btn" href={href('routes', { edit: g.name })} aria-label={t('routes.editNamed', { name: g.name })} title={t('routes.editNamed', { name: g.name })}><Icon name="edit" size={14} /></a> : undefined,
            };
          })}
        />
      )}
    </Card>
  );
}

function ModelsCard({ d }: { d: Insights }) {
  const { t, f } = useI18n();
  return (
    <Card title={t('overview.models.title')} subtitle={t('overview.models.sub')}>
      {d.by_model.length === 0 ? (
        <EmptyState compact icon="cpu" title={t('overview.noData')} />
      ) : (
        <BarList
          label={t('overview.models.title')}
          items={d.by_model.slice(0, 6).map((g, i) => {
            const v = modelVendor(g.name);
            const isPath = g.name.startsWith('/');
            return {
              key: g.name,
              label: (
                <span className="brand-label mono">
                  {isPath ? <Icon name="terminal" size={14} /> : <BrandIcon id={vendorIcon(v)} label={v ? PROVIDER_NAMES[v] : g.name} size={14} />}
                  <span className="clip">{isPath ? t('overview.models.noModel', { path: g.name }) : g.name}</span>
                </span>
              ),
              value: g.requests,
              display: f.compact(g.requests),
              sub: t('overview.models.row', { tokens: f.compact(g.tokens) }),
              color: ROUTE_COLORS[(i + 2) % ROUTE_COLORS.length],
              onClick: isPath ? undefined : () => navigate('traffic', { model: g.name }),
              title: isPath ? undefined : t('overview.routes.click'),
            };
          })}
        />
      )}
    </Card>
  );
}

function SelectorCard({ insights }: { insights: Insights | null }) {
  const { t, f } = useI18n();
  const { config } = useGateway();
  const sel = insights?.selector;
  const s = config?.selector;
  const lastErrRecent = sel?.last_error_at && (!sel.last_epoch || sel.last_error_at >= sel.last_epoch);
  const tone = !sel || sel.epochs === 0 ? 'neutral' : lastErrRecent ? 'warn' : 'good';
  const status = !sel || sel.epochs === 0 ? t('selector.health.idle') : lastErrRecent ? t('selector.health.degraded') : t('selector.health.ok');
  return (
    <Card
      title={t('overview.selector.title')}
      subtitle={t('overview.selector.sub')}
      actions={<Button size="sm" variant="ghost" onClick={() => navigate('settings')}>{t('common.configure')}</Button>}
    >
      <div className="sel-head">
        <div className="sel-name">
          <strong>{s ? t(`selector.backend.${s.backend}.title`) : '…'}</strong>
          <span className="muted mono small">{s?.model}</span>
        </div>
        <Badge tone={tone === 'neutral' ? 'neutral' : tone}>
          <Dot tone={tone} /> {status}
        </Badge>
      </div>
      <div className="mini-stats">
        <Stat label={t('overview.selector.epochs')} value={f.num(sel?.epochs ?? 0)} />
        <Stat label={t('overview.selector.avg')} value={sel?.epochs ? f.ms(sel.avg_ms) : '—'} sub={sel?.epochs ? t('overview.selector.p95', { v: f.ms(sel.p95_ms) }) : undefined} />
        <Stat label={t('overview.selector.errors')} value={f.num(sel?.errors ?? 0)} tone={sel?.errors ? 'warn' : undefined} sub={t('overview.selector.failOpen')} />
      </div>
      {insights && insights.buckets.some((b) => b.epochs > 0) && (
        <Sparkline values={insights.buckets.map((b) => b.selector_avg_ms)} color="var(--accent)" height={32} label={t('overview.selector.avg')} />
      )}
      {sel?.last_error && <p className="fine tone-warn mono clip" title={sel.last_error}>{t('overview.selector.lastError', { error: sel.last_error })}</p>}
    </Card>
  );
}

const DAYS: Record<Window, number> = { '1h': 1, '6h': 1, '24h': 1, '7d': 7, all: 0 };

/** What recovery did: how many failed calls still ended well, by class, and who fails most. */
function ResilienceCard({ win }: { win: Window }) {
  const { t, f } = useI18n();
  const st = useLiveFetch(() => resilienceApi.stats(DAYS[win]), [win], 10000);
  const s = st.data;
  const classes = s ? Object.entries(s.by_class).filter(([, c]) => c.failures > 0).sort((a, b) => b[1].requests - a[1].requests) : [];
  const guard = s ? s.guard.filled_missing + s.guard.above_max_output + s.guard.exceeds_context_window : 0;
  return (
    <Card
      title={t('overview.res.title')}
      subtitle={DAYS[win] === 1 ? t('overview.res.sub24h') : DAYS[win] ? t('overview.res.subDays', { n: DAYS[win] }) : t('overview.res.subAll')}
      actions={<Button size="sm" variant="ghost" onClick={() => navigate('settings/resilience')}>{t('common.configure')}</Button>}
    >
      {st.error && !s && <ErrorState error={st.error} onRetry={st.reload} />}
      {!s && !st.error && <Skeleton h={80} />}
      {s && (
        <div className="res-overview">
          <div className="mini-stats">
            <Stat icon="shield" label={t('overview.res.rate')} value={s.retried ? f.pct(s.recovery_rate) : '—'} tone={s.retried ? (s.recovery_rate >= 0.8 ? 'good' : 'warn') : undefined}
              sub={s.retried ? t('overview.res.recovered', { recovered: f.num(s.recovered), retried: f.num(s.retried) }) : t('overview.res.noRetries')} />
            <Stat label={t('overview.res.failed')} value={f.num(s.failed_after_retry)} tone={s.failed_after_retry ? 'bad' : undefined}
              sub={s.fallbacks ? t('overview.res.fallbacks', { count: s.fallbacks }) : undefined} />
            <Stat label={t('overview.res.guard')} hint={t('overview.res.guardHint')} value={f.num(guard)}
              sub={t('overview.res.guardSub', { filled: f.num(s.guard.filled_missing), clamped: f.num(s.guard.above_max_output + s.guard.exceeds_context_window) })} />
          </div>
          <div>
            <div className="fact-label">{t('overview.res.byClass')}</div>
            {classes.length ? (
              <BarList
                label={t('overview.res.byClass')}
                items={classes.map(([c, v]) => ({
                  key: c, label: isClass(c) ? t(`res.class.${c}`) : c, value: v.requests, display: `${f.num(v.recovered)} / ${f.num(v.requests)}`,
                  sub: t('overview.res.classRow', { recovered: f.num(v.recovered), requests: f.num(v.requests) }),
                  color: v.recovered === v.requests ? 'var(--good)' : 'var(--warn)',
                  onClick: () => navigate('traffic', { res: 'retried' }),
                }))}
              />
            ) : <p className="muted small">{t('overview.res.noFailures')}</p>}
          </div>
          <div>
            <div className="fact-label">{t('overview.res.providers')}</div>
            {s.top_failing_providers.length ? (
              <BarList
                label={t('overview.res.providers')}
                items={s.top_failing_providers.slice(0, 5).map((p) => ({
                  key: p.name, label: <span className="mono">{p.name}</span>, value: p.failures, display: f.num(p.failures),
                  sub: t('overview.res.provRow', { recovered: f.num(p.recovered), failures: f.num(p.failures) }),
                  color: 'var(--bad)',
                }))}
              />
            ) : <p className="muted small">{t('overview.res.noFailures')}</p>}
          </div>
        </div>
      )}
    </Card>
  );
}

function StorageCard() {
  const { t, f } = useI18n();
  const st = useLiveFetch(() => storageApi.stats(), [], 30000);
  const s = st.data;
  return (
    <Card
      title={t('overview.storage.title')}
      subtitle={t('overview.storage.sub')}
      actions={<Button size="sm" variant="ghost" onClick={() => navigate('settings/storage')}>{t('common.manage')}</Button>}
    >
      {st.error && !s && <ErrorState error={st.error} onRetry={st.reload} />}
      {!s && !st.error && <Skeleton h={60} />}
      {s && (
        <>
          <div className="mini-stats">
            <Stat icon="database" label={t('overview.storage.onDisk')} value={f.bytes(s.bytes.total)} sub={t('storage.stat.blobs', { n: f.num(s.counts.blobs) })} />
            <Stat label={t('overview.storage.smaller')} value={f.ratio(s.total_ratio)} tone={s.total_ratio > 1 ? 'good' : undefined} sub={t('overview.storage.from', { bytes: f.bytes(s.logical_bytes) })} />
          </div>
          <p className="fine">{t('overview.storage.retention', { detail: s.settings.detail_max_age, pinned: s.pinned.requests })}</p>
        </>
      )}
    </Card>
  );
}

function RecallCard() {
  const { t, f } = useI18n();
  const st = useLiveFetch(() => recallApi.stats(), [], 5000);
  const s = st.data;
  return (
    <Card
      title={t('overview.recall.title')}
      subtitle={t('overview.recall.sub')}
      actions={<Button size="sm" variant="ghost" onClick={() => navigate('integrations/recall')}>{t('common.details')}</Button>}
    >
      {st.error && !s && <ErrorState error={st.error} onRetry={st.reload} />}
      {s && (
        <div className="mini-stats">
          <Stat icon="recall" label={t('overview.recall.count')} value={f.num(s.total)} sub={s.errors ? <span className="tone-warn">{t('overview.recall.failed', { count: s.errors })}</span> : t('overview.recall.none')} />
          <Stat label={t('overview.recall.restored')} value={f.compact(s.tokens_restored)} sub={t('common.estimated')} />
          <Stat label={t('overview.recall.last')} value={s.last ? f.ago(s.last) : '—'} sub={s.conversations ? t('overview.recall.convs', { count: s.conversations }) : undefined} />
        </div>
      )}
      <p className="fine">{t('overview.recall.why')}</p>
    </Card>
  );
}

function agentTone(a: AgentStatus): 'good' | 'warn' | 'neutral' {
  return a.effective ? 'good' : a.configured ? 'warn' : 'neutral';
}

export function AgentsCard() {
  const { t } = useI18n();
  const ag = useFetch(() => agentsApi.list(''), []);
  return (
    <Card
      title={t('overview.agents.title')}
      subtitle={t('overview.agents.sub')}
      actions={<Button size="sm" variant="ghost" onClick={() => navigate('integrations')}>{t('common.manage')}</Button>}
    >
      {ag.error && <ErrorState error={ag.error} onRetry={ag.reload} />}
      {!ag.data && !ag.error && <Skeleton h={80} />}
      <ul className="agent-mini">
        {ag.data?.map((a) => (
          <li key={a.name}>
            <BrandIcon id={a.name === 'claude' ? 'claude-code' : a.name} label={a.title} size={20} />
            <div className="agent-mini-text">
              <strong>{a.title}</strong>
              <span className="muted small">{a.installed ? a.version || t('agents.installed') : t('agents.notFound')}</span>
            </div>
            <Badge tone={agentTone(a)}>
              <Dot tone={agentTone(a)} />
              {a.effective ? t('agents.state.using') : a.configured ? t('agents.state.overridden') : t('agents.state.off')}
            </Badge>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function GetStarted({ listen }: { listen?: string }) {
  const { t, tn } = useI18n();
  const base = gatewayURL(listen);
  return (
    <Card className="getstarted">
      <EmptyState icon="zap" title={t('onboarding.title')}>{t('onboarding.body')}</EmptyState>
      <ol className="steps">
        <li>
          <span className="step-n">1</span>
          <div>
            <div className="step-title">{t('onboarding.step1.title')}</div>
            <p className="muted">{t('onboarding.step1.body')}</p>
            <CopyField text={`ANTHROPIC_BASE_URL=${base} claude`} label="Claude Code" multiline />
            <p className="fine">{tn('onboarding.step1.more', { link: <a href="#/integrations">{t('nav.integrations')}</a> })}</p>
          </div>
        </li>
        <li>
          <span className="step-n">2</span>
          <div>
            <div className="step-title">{t('onboarding.step2.title')}</div>
            <p className="muted">{t('onboarding.step2.body')}</p>
            <CopyField text={`claude mcp add --transport http --scope user rlcd-gateway ${base}/mcp`} label="MCP" multiline />
          </div>
        </li>
        <li>
          <span className="step-n">3</span>
          <div>
            <div className="step-title">{t('onboarding.step3.title')}</div>
            <p className="muted">{t('onboarding.step3.body')}</p>
            <Button icon="traffic" onClick={() => navigate('traffic')}>{t('onboarding.step3.cta')}</Button>
          </div>
        </li>
      </ol>
    </Card>
  );
}
