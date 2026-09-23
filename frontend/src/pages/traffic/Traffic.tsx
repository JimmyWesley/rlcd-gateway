import { useEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { BrandIcon, ClientIcon } from '../../icons/BrandIcon';
import { gatewayURL, isFailed, protocolOf, PROTOCOLS, type RequestRecord } from '../../lib/api';
import { recordClient, recordModel, recordProvider, recordVendor, PROVIDER_NAMES, vendorIcon } from '../../lib/brands';
import { href, navigate, useLocation } from '../../lib/router';
import { useGateway } from '../../state/gateway';
import { load, save } from '../../lib/storage';
import { Badge, Button, CopyField, Drawer, EmptyState, ErrorState, Field, Loading, PageHeader, Segmented, cx } from '../../ui';
import { Inspector } from './Inspector';

type PruneStage = { saved_tokens?: number; applied?: boolean; mode?: string; dropped?: number };
export const pruneOf = (r: RequestRecord) => r.stages?.prune as PruneStage | undefined;

type StatusFilter = 'all' | 'errors' | 'pruned';
const FILTER_KEYS = ['route', 'model', 'client', 'protocol', 'key', 'conv', 'status', 'res', 'q'] as const;
const RES_FILTERS = ['recovered', 'failed', 'retried', 'fallback', 'guard'] as const;
type ResFilter = (typeof RES_FILTERS)[number];
const isResFilter = (v: string): v is ResFilter => (RES_FILTERS as readonly string[]).includes(v);

/** What the resilience layer did to a record, for the list's filter. */
function resMatch(r: RequestRecord, v: ResFilter): boolean {
  switch (v) {
    case 'recovered': return !!r.recovered;
    case 'failed': return !!r.retried && !r.recovered;
    case 'retried': return !!r.retried;
    case 'fallback': return !!r.fallback_route;
    case 'guard': return !!r.max_tokens_guard;
  }
}

export function Traffic({ selectedId }: { selectedId: string }) {
  const { t, f } = useI18n();
  const { requests, requestsLoaded, requestsError, config } = useGateway();
  const loc = useLocation();
  const q = (k: (typeof FILTER_KEYS)[number]) => loc.query.get(k) ?? '';
  const filters = { route: q('route'), model: q('model'), client: q('client'), protocol: q('protocol'), key: q('key'), conv: q('conv'), status: (q('status') || 'all') as StatusFilter, res: q('res'), q: q('q') };
  const [paused, setPaused] = useState(false);
  const [frozen, setFrozen] = useState<RequestRecord[] | null>(null);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [wide, setWideState] = useState(() => load('rlcd.inspector.wide') === '1');
  const setWide = (v: boolean) => {
    setWideState(v);
    save('rlcd.inspector.wide', v ? '1' : null);
  };
  const narrow = useNarrow(1180);

  const setFilter = (k: (typeof FILTER_KEYS)[number], v: string) => {
    const next: Record<string, string> = {};
    for (const key of FILTER_KEYS) if (q(key)) next[key] = q(key);
    if (v) next[k] = v;
    else delete next[k];
    navigate(['traffic', selectedId].filter(Boolean).join('/'), next, true);
  };
  const query = Object.fromEntries(FILTER_KEYS.map((k) => [k, q(k) || undefined]));

  const source = paused && frozen ? frozen : requests;
  const newCount = paused && frozen ? requests.length - frozen.length : 0;

  const rows = useMemo(() => {
    const needle = filters.q.toLowerCase();
    return source.filter((r) => {
      if (filters.route && r.route !== filters.route) return false;
      if (filters.model && recordModel(r) !== filters.model) return false;
      if (filters.client && recordClient(r).id !== filters.client) return false;
      if (filters.protocol && protocolOf(r) !== filters.protocol) return false;
      if (filters.key && (r.key_name ?? r.client?.key_name) !== filters.key) return false;
      if (filters.conv && r.conversation_id !== filters.conv) return false;
      if (filters.status === 'errors' && !isFailed(r)) return false;
      if (filters.status === 'pruned' && !pruneOf(r)?.saved_tokens) return false;
      if (filters.res && isResFilter(filters.res) && !resMatch(r, filters.res)) return false;
      if (needle) {
        const hay = `${r.id} ${r.route} ${r.model ?? ''} ${r.client_model ?? ''} ${r.alias ?? ''} ${r.key_name ?? ''} ${r.client?.name ?? ''} ${r.path} ${r.conversation_id ?? ''} ${r.error ?? ''}`.toLowerCase();
        if (!hay.includes(needle)) return false;
      }
      return true;
    });
  }, [source, filters.route, filters.model, filters.client, filters.protocol, filters.key, filters.conv, filters.status, filters.res, filters.q]);

  const routes = useMemo(() => [...new Set(requests.map((r) => r.route))].sort(), [requests]);
  const models = useMemo(() => [...new Set(requests.map(recordModel).filter(Boolean))].sort(), [requests]);
  const keys = useMemo(() => [...new Set(requests.map((r) => r.key_name ?? r.client?.key_name ?? '').filter(Boolean))].sort(), [requests]);
  const protocols = useMemo(() => PROTOCOLS.filter((p) => requests.some((r) => protocolOf(r) === p)), [requests]);
  const clients = useMemo(() => {
    const m = new Map<string, string>();
    for (const r of requests) {
      const c = recordClient(r);
      m.set(c.id, c.name || t('client.unknown'));
    }
    return [...m.entries()];
  }, [requests, t]);

  const select = (id: string | null) => navigate(id ? `traffic/${id}` : 'traffic', query);
  const listRef = useRef<HTMLDivElement>(null);

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'j' && e.key !== 'k') return;
    e.preventDefault();
    const i = rows.findIndex((r) => r.id === selectedId);
    const next = rows[Math.max(0, Math.min(rows.length - 1, i + (e.key === 'ArrowDown' || e.key === 'j' ? 1 : -1)))];
    if (next) {
      select(next.id);
      requestAnimationFrame(() => listRef.current?.querySelector<HTMLElement>(`[data-id="${next.id}"]`)?.focus());
    }
  };

  const activeFilters = FILTER_KEYS.filter((k) => q(k) && !(k === 'status' && q(k) === 'all'));
  const dimFilters = (['route', 'model', 'client', 'protocol', 'key', 'res'] as const).filter((k) => q(k)).length;

  const list = (
    <section className="traffic-list card card-flush" aria-label={t('traffic.listLabel')}>
      <div className="list-toolbar">
        <div className="search">
          <Icon name="search" size={15} />
          <input type="search" placeholder={t('traffic.search')} aria-label={t('traffic.search')} value={filters.q} onChange={(e) => setFilter('q', e.target.value)} />
        </div>
        <Segmented
          size="sm"
          label={t('traffic.status')}
          value={filters.status}
          onChange={(v) => setFilter('status', v === 'all' ? '' : v)}
          options={[
            { id: 'all', label: t('traffic.filter.all') },
            { id: 'errors', label: t('traffic.filter.errors') },
            { id: 'pruned', label: t('traffic.filter.pruned') },
          ]}
        />
        <Button size="sm" icon="filter" aria-expanded={filtersOpen} onClick={() => setFiltersOpen(!filtersOpen)} className={cx(dimFilters > 0 && 'is-on')}>
          {t('traffic.filters')}{dimFilters > 0 && ` · ${dimFilters}`}
        </Button>
        <span className="toolbar-spacer" />
        <Button
          size="sm"
          variant="ghost"
          icon={paused ? 'play' : 'pause'}
          aria-pressed={paused}
          onClick={() => {
            setFrozen(paused ? null : requests);
            setPaused(!paused);
          }}
        >
          {paused ? t('traffic.resume') : t('traffic.pause')}
        </Button>
      </div>
      {filtersOpen && (
        <div className="filter-panel">
          <Field label={t('traffic.col.route')}>
            <select value={filters.route} onChange={(e) => setFilter('route', e.target.value)}>
              <option value="">{t('traffic.allRoutes')}</option>
              {routes.map((r) => <option key={r} value={r}>{r}</option>)}
            </select>
          </Field>
          <Field label={t('traffic.col.model')}>
            <select value={filters.model} onChange={(e) => setFilter('model', e.target.value)}>
              <option value="">{t('traffic.allModels')}</option>
              {models.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </Field>
          <Field label={t('traffic.col.client')}>
            <select value={filters.client} onChange={(e) => setFilter('client', e.target.value)}>
              <option value="">{t('traffic.allClients')}</option>
              {clients.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
            </select>
          </Field>
          <Field label={t('traffic.col.protocol')}>
            <select value={filters.protocol} onChange={(e) => setFilter('protocol', e.target.value)}>
              <option value="">{t('traffic.allProtocols')}</option>
              {protocols.map((p) => <option key={p} value={p}>{t(`protocol.${p}`)}</option>)}
            </select>
          </Field>
          <Field label={t('traffic.filterName.res')}>
            <select value={filters.res} onChange={(e) => setFilter('res', e.target.value)}>
              <option value="">{t('traffic.res.all')}</option>
              {RES_FILTERS.map((v) => <option key={v} value={v}>{t(`traffic.res.${v}`)}</option>)}
            </select>
          </Field>
          <Field label={t('traffic.col.key')}>
            <select value={filters.key} onChange={(e) => setFilter('key', e.target.value)} disabled={keys.length === 0}>
              <option value="">{t('traffic.allKeys')}</option>
              {keys.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
          </Field>
        </div>
      )}
      {(activeFilters.length > 0 || filters.conv) && (
        <div className="filter-chips">
          {activeFilters.map((k) => (
            <button key={k} type="button" className="chip" onClick={() => setFilter(k, '')} aria-label={t('traffic.removeFilter', { name: t(`traffic.filterName.${k}`) })}>
              <span className="muted">{t(`traffic.filterName.${k}`)}</span> {k === 'conv' ? q(k).slice(0, 14) + '…' : k === 'res' && isResFilter(q(k)) ? t(`traffic.res.${q(k) as ResFilter}`) : q(k)}
              <Icon name="x" size={12} />
            </button>
          ))}
          <button type="button" className="linkish small" onClick={() => navigate(selectedId ? `traffic/${selectedId}` : 'traffic', {}, true)}>{t('traffic.clearFilters')}</button>
        </div>
      )}
      {newCount > 0 && (
        <button type="button" className="new-banner" onClick={() => { setFrozen(requests); }}>
          {t('traffic.newRequests', { count: newCount })}
        </button>
      )}
      <div className="rows-head" aria-hidden>
        <span>{t('traffic.col.time')}</span>
        <span>{t('traffic.col.client')}</span>
        <span>{t('traffic.col.route')}</span>
        <span>{t('traffic.col.model')}</span>
        <span className="num">{t('traffic.col.context')}</span>
        <span className="num">{t('traffic.col.status')}</span>
        <span className="num">{t('traffic.col.latency')}</span>
      </div>
      <div className="rows" ref={listRef} onKeyDown={onKey} role="list">
        {!requestsLoaded && <div className="pad"><Loading lines={6} /></div>}
        {requestsError && <div className="pad"><ErrorState error={requestsError} /></div>}
        {requestsLoaded && requests.length === 0 && <TrafficEmpty listen={config?.listen} />}
        {requestsLoaded && requests.length > 0 && rows.length === 0 && (
          <EmptyState compact icon="filter" title={t('traffic.noMatch')} actions={<Button size="sm" onClick={() => navigate('traffic', {}, true)}>{t('traffic.clearFilters')}</Button>} />
        )}
        {rows.map((r) => (
          <Row key={r.id} r={r} selected={r.id === selectedId} hrefTo={href(`traffic/${r.id}`, query)} fmt={f} />
        ))}
      </div>
      <div className="list-foot muted small">
        {t('traffic.showing', { shown: rows.length, total: source.length })}
      </div>
    </section>
  );

  return (
    <div className={cx('page', 'page-traffic', selectedId && !narrow && 'with-inspector', selectedId && !narrow && wide && 'inspector-wide')}>
      <PageHeader title={t('nav.traffic')} description={t('traffic.desc')} />
      <div className="traffic-split">
        {list}
        {selectedId && !narrow && (
          <section className="inspector-pane card card-flush" aria-label={t('inspector.label')}>
            <Inspector id={selectedId} onClose={() => select(null)} wide={wide} onToggleWide={() => setWide(!wide)} />
          </section>
        )}
      </div>
      {selectedId && narrow && (
        <Drawer open wide onClose={() => select(null)} title={t('inspector.label')}>
          <Inspector id={selectedId} onClose={() => select(null)} inDrawer />
        </Drawer>
      )}
    </div>
  );
}

function useNarrow(px: number) {
  const [n, setN] = useState(() => window.innerWidth < px);
  useEffect(() => {
    const on = () => setN(window.innerWidth < px);
    window.addEventListener('resize', on);
    return () => window.removeEventListener('resize', on);
  }, [px]);
  return n;
}

type Fmt = ReturnType<typeof useI18n>['f'];

/** Small badges for what the resilience layer did: recovered, retried, fallback, guard. */
function ResBadges({ r }: { r: RequestRecord }) {
  const { t } = useI18n();
  const n = r.attempts?.length ?? 1;
  return (
    <>
      {r.recovered ? <Badge tone="good" icon="check" className="badge-icon" title={t('traffic.res.recoveredHint', { count: n })}><span className="sr-only">{t('traffic.badge.recovered')}</span></Badge>
        : r.retried && <Badge tone="bad" title={t('traffic.res.failedHint', { count: n })}>×{n}<span className="sr-only"> {t('traffic.badge.retried', { count: n })}</span></Badge>}
      {r.fallback_route && <Badge tone="accent" icon="arrowRight" className="badge-icon" title={t('traffic.res.fallbackHint', { route: r.fallback_route, primary: r.primary_route ?? '' })}><span className="sr-only">{t('traffic.badge.fallback')}</span></Badge>}
      {r.max_tokens_guard && !r.retried && <Badge tone="neutral" icon="shield" className="badge-icon" title={t('traffic.res.guardHint', { from: r.max_tokens_guard.from ?? '—', to: r.max_tokens_guard.to })}><span className="sr-only">{t('traffic.badge.guard')}</span></Badge>}
    </>
  );
}

/** For a System One call, the context column shows its answers instead. */
function DecisionMini({ d, fmt: f }: { d: NonNullable<RequestRecord['decisions']>; fmt: Fmt }) {
  const { t } = useI18n();
  const confs = d.questions.flatMap((q) => (q.confidence != null ? [q.confidence] : []));
  const min = confs.length ? Math.min(...confs) : null;
  const title = d.questions.map((q) => `${q.id}: ${q.answer ?? '—'}${q.confidence != null ? ` (${f.pct(q.confidence)})` : ''}`).join('\n');
  return (
    <span className="c-ctx num dec-mini" title={title}>
      <span className="clip">{d.questions.length === 1 ? d.questions[0].answer : t('decision.nq', { count: d.questions.length })}</span>
      {min != null && <Badge tone={min < 0.6 ? 'warn' : 'neutral'} title={t('decision.lowest')}>{f.pct(min)}</Badge>}
    </span>
  );
}

function Row({ r, selected, hrefTo, fmt: f }: { r: RequestRecord; selected: boolean; hrefTo: string; fmt: Fmt }) {
  const { t } = useI18n();
  const failed = isFailed(r);
  const p = pruneOf(r);
  const c = recordClient(r);
  const prov = recordProvider(r);
  const model = recordModel(r);
  const vendor = recordVendor(r);
  const proto = protocolOf(r);
  const swapped = !!r.client_model && !!r.model && r.client_model !== r.model;
  const usage = r.usage;
  const input = usage ? usage.input_tokens + usage.cache_read_input_tokens + usage.cache_creation_input_tokens : 0;
  const cacheShare = usage && input ? usage.cache_read_input_tokens / input : null;
  return (
    <a
      href={hrefTo}
      className={cx('row', selected && 'on', failed && 'is-failed')}
      data-id={r.id}
      aria-current={selected ? 'true' : undefined}
      role="listitem"
    >
      <span className="c-time mono">{f.time(r.time)}</span>
      <span className="c-client" title={[c.name ? `${c.name}${c.version ? ` ${c.version}` : ''}${c.inferred ? ` (${t('client.inferred')})` : ''}` : t('client.unknown'), c.keyName && t('inspector.key', { name: c.keyName })].filter(Boolean).join(' · ')}>
        <ClientIcon client={c} size={16} />
        <span className="clip">{c.keyName || c.name || t('client.unknownShort')}</span>
      </span>
      <span className="c-route" title={r.route_reason ? `${r.route}: ${r.route_reason}` : r.route}>
        <BrandIcon id={prov === 'custom' ? undefined : prov} label={PROVIDER_NAMES[prov]} size={14} />
        <span className="clip">{r.route}</span>
        {r.route_reason && <RouteWhy reason={r.route_reason} />}
      </span>
      <span className="c-model" title={[t(`protocol.${proto}`), swapped ? `${r.client_model} → ${r.model}` : model, r.alias && t('traffic.aliasHint', { alias: r.alias }), r.est_cost_usd ? `~${f.usd(r.est_cost_usd)}` : ''].filter(Boolean).join('\n')}>
        {!model ? (
          <span className="muted mono clip">{r.path}</span>
        ) : (
          <>
            <BrandIcon id={vendorIcon(vendor)} label={vendor ? PROVIDER_NAMES[vendor] : model} size={14} />
            <span className="mono clip">{model}</span>
            {r.alias ? <Badge tone="accent" title={t('traffic.aliasHint', { alias: r.alias })}>{r.alias}</Badge>
              : swapped && <Badge tone="accent" title={t('traffic.swappedHint', { from: r.client_model ?? '' })}>{t('traffic.swapped')}</Badge>}
            {proto !== 'anthropic-messages' && <Badge tone="info" className="proto-badge" title={t(`protocol.${proto}`)}>{t(`protocol.short.${proto}`)}</Badge>}
          </>
        )}
        <ResBadges r={r} />
      </span>
      {r.decisions && r.decisions.questions.length > 0 ? (
        <DecisionMini d={r.decisions} fmt={f} />
      ) : (
      <span className="c-ctx num">
        <span className="mono">{r.est_tokens ? `~${f.tokens(r.est_tokens)}` : '—'}</span>
        {p?.saved_tokens ? (
          <Badge tone={p.applied ? 'dropped' : 'shadow'} title={p.applied ? t('traffic.prunedHint') : t('traffic.shadowHint')}>
            −{f.tokens(p.saved_tokens)}
          </Badge>
        ) : cacheShare != null ? (
          <span className="cache-mini" title={t('traffic.cacheHint', { pct: f.pct(cacheShare) })}>
            <span style={{ width: `${Math.round(cacheShare * 100)}%` }} />
          </span>
        ) : null}
      </span>
      )}
      <span className="c-status num">
        {failed ? <Badge tone="bad">{r.status || t('traffic.err')}</Badge> : <span className="mono muted">{r.status}</span>}
      </span>
      <span className="c-lat num mono">{f.ms(r.duration_ms)}</span>
    </a>
  );
}

export function routeTag(reason: string): { key: 'alias' | 'sticky' | 'auto' | 'override' | 'rule' | 'decision' | 'default'; tone: 'neutral' | 'accent' | 'info' | 'warn' } {
  if (reason.startsWith('alias')) return { key: 'alias', tone: 'accent' };
  if (reason.startsWith('decision rule')) return { key: 'decision', tone: 'accent' };
  if (reason.startsWith('sticky')) return { key: 'sticky', tone: 'neutral' };
  if (reason.startsWith('auto')) return { key: 'auto', tone: 'accent' };
  if (reason.includes('overrode sticky')) return { key: 'override', tone: 'warn' };
  if (reason.startsWith('rule')) return { key: 'rule', tone: 'info' };
  return { key: 'default', tone: 'neutral' };
}

function RouteWhy({ reason }: { reason: string }) {
  const { t } = useI18n();
  const tag = routeTag(reason);
  if (tag.key === 'default') return null;
  return <Badge tone={tag.tone}>{t(`route.why.${tag.key}`)}</Badge>;
}

function TrafficEmpty({ listen }: { listen?: string }) {
  const { t } = useI18n();
  const base = gatewayURL(listen);
  return (
    <EmptyState icon="traffic" title={t('traffic.empty.title')} actions={<Button size="sm" onClick={() => navigate('integrations')}>{t('traffic.empty.more')}</Button>}>
      <p>{t('traffic.empty.body')}</p>
      <CopyField text={`ANTHROPIC_BASE_URL=${base} claude`} label="Claude Code" />
    </EmptyState>
  );
}
