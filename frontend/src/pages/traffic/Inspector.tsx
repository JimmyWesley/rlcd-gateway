import { useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon, ClientIcon } from '../../icons/BrandIcon';
import { api, isFailed, protocolOf, purgedInfo, type Block, type PurgedInfo, type RequestDetail, type RequestRecord } from '../../lib/api';
import { recordClient, recordModel, recordProvider, recordVendor, PROVIDER_NAMES } from '../../lib/brands';
import { navigate } from '../../lib/router';
import { useFetch, useGateway } from '../../state/gateway';
import { SplitBar } from '../../charts';
import { Badge, Button, Callout, Disclosure, ErrorState, IconButton, Loading, ModelLabel, Segmented, cx, useCopy } from '../../ui';
import { PruneDiff } from './PruneDiff';
import { ChatView } from './ChatView';
import { PreviewText } from './Preview';
import { providerError } from '../../lib/conversation';
import { routeTag } from './Traffic';

// Order is the order the model reads the context in.
export const KINDS = ['system', 'tool', 'text', 'thinking', 'tool_use', 'tool_result', 'image', 'other'] as const;
export type Kind = (typeof KINDS)[number];
export const KIND_COLOR: Record<Kind, string> = {
  system: 'var(--k-system)', tool: 'var(--k-tool)', text: 'var(--k-text)', thinking: 'var(--k-thinking)',
  tool_use: 'var(--k-tool-use)', tool_result: 'var(--k-tool-result)', image: 'var(--k-image)', other: 'var(--k-other)',
};
export const asKind = (k: string): Kind => ((KINDS as readonly string[]).includes(k) ? (k as Kind) : 'other');

type Tab = 'chat' | 'prune' | 'xray' | 'raw';

export function Inspector({ id, onClose, inDrawer, wide, onToggleWide }: { id: string; onClose: () => void; inDrawer?: boolean; wide?: boolean; onToggleWide?: () => void }) {
  const { t, f } = useI18n();
  const { data: d, error, cause, reload } = useFetch(() => api.request(id), [id]);
  const purged = purgedInfo(cause);
  const [tab, setTab] = useState<Tab | null>(null);
  const { config, requests } = useGateway();

  useEffect(() => setTab(null), [id]);

  if (purged) return <Purged info={purged} record={requests.find((r) => r.id === id)} onClose={onClose} inDrawer={inDrawer} />;
  if (error) return <div className="pad"><ErrorState error={error} onRetry={reload} /></div>;
  if (!d || d.id !== id) return <div className="pad"><Loading lines={8} /></div>;

  const hasPrune = !!d.stage_details?.prune;
  const current: Tab = tab ?? 'chat';
  const failed = isFailed(d);
  const c = recordClient(d);
  const prov = recordProvider(d);
  const route = config?.routes.find((r) => r.name === d.route);
  const swapped = !!d.client_model && !!d.model && d.client_model !== d.model;
  const tag = d.route_reason ? routeTag(d.route_reason) : null;
  const vendor = recordVendor(d);

  return (
    <div className="inspector">
      <header className="insp-head">
        <div className="insp-title">
          <ModelLabel model={recordModel(d) || d.path} vendor={recordVendor(d)} />
          {failed ? <Badge tone="bad">{d.status || t('traffic.err')}</Badge> : <Badge tone="good">{d.status}</Badge>}
          {d.stream && <Badge>{t('inspector.stream')}</Badge>}
        </div>
        <div className="insp-sub muted small">
          <span>{f.dayTime(d.time)}</span>
          <span className="mono">{d.method} {d.path}</span>
          <span className="mono clip" title={d.id}>{d.id}</span>
        </div>
        {!inDrawer && (
          <div className="insp-actions">
            {onToggleWide && <IconButton icon={wide ? 'chevronRight' : 'chevronLeft'} label={wide ? t('inspector.narrow') : t('inspector.wide')} onClick={onToggleWide} />}
            <IconButton icon="x" label={t('common.close')} onClick={onClose} />
          </div>
        )}
      </header>

      {d.error && <Callout tone="bad" title={t('inspector.failed')}>{providerError(d.response_body, d.error)?.message ?? d.error}</Callout>}

      <div className="facts">
        <Fact label={t('inspector.route')}>
          <span className="brand-label strong">
            <BrandIcon id={prov === 'custom' ? undefined : prov} label={PROVIDER_NAMES[prov]} size={14} />
            {d.route}
            {tag && tag.key !== 'default' && <Badge tone={tag.tone}>{t(`route.why.${tag.key}`)}</Badge>}
          </span>
          <span className="fact-sub" title={d.route_reason ?? d.upstream}>{d.route_reason ?? (route ? t('inspector.activeRoute') : d.upstream)}</span>
        </Fact>
        <Fact label={t('inspector.model')}>
          <ModelLabel model={recordModel(d)} vendor={recordVendor(d)} />
          <span className="fact-sub">
            {[d.alias && t('inspector.alias', { alias: d.alias }), swapped && t('inspector.askedFor', { model: d.client_model ?? '' }), vendor && t('inspector.by', { vendor: PROVIDER_NAMES[vendor] })].filter(Boolean).join(' · ')}
          </span>
        </Fact>
        <Fact label={t('inspector.client')}>
          <span className="brand-label">
            <ClientIcon client={c} size={16} />
            {c.name || t('client.unknown')}
            {c.version && <span className="muted mono small">{c.version}</span>}
          </span>
          <span className="fact-sub">
            {t(`protocol.${protocolOf(d)}`)} · {t(`auth.${d.auth_mode}`)}
            {c.keyName && ` · ${t('inspector.key', { name: c.keyName })}`}
            {c.inferred && c.name && ` · ${t('client.inferred')}`}
          </span>
        </Fact>
        <Fact label={t('inspector.latency')}>
          <strong>{f.ms(d.duration_ms)}</strong>
          <span className="fact-sub">
            {t('inspector.ttfb', { ms: f.ms(d.ttfb_ms) })}
            {d.est_cost_usd != null && d.est_cost_usd > 0 && ` · ${t('inspector.cost', { usd: f.usd(d.est_cost_usd) })}`}
          </span>
        </Fact>
      </div>

      {d.usage && <UsageBar d={d} />}

      {d.conversation_id && (
        <div className="insp-conv">
          <span className="fact-label">{t('inspector.conversation')}</span>
          <button type="button" className="linkish mono small" onClick={() => navigate('traffic', { conv: d.conversation_id })} title={t('inspector.convFilter')}>
            {d.conversation_id}
          </button>
        </div>
      )}

      {!!d.stripped_thinking && <Callout tone="info">{t('inspector.stripped', { count: d.stripped_thinking })}</Callout>}
      {d.stage_errors && Object.entries(d.stage_errors).map(([stage, e]) => (
        <Callout key={stage} tone="warn" title={t('inspector.stageFailed', { stage })}><span className="mono">{e}</span></Callout>
      ))}

      <div className="insp-tabs">
        <Segmented
          label={t('inspector.views')}
          value={current}
          onChange={setTab}
          options={[
            { id: 'chat' as Tab, label: t('inspector.tab.chat') },
            ...(hasPrune ? [{ id: 'prune' as Tab, label: t('inspector.tab.prune') }] : []),
            { id: 'xray' as Tab, label: t('inspector.tab.xray') },
            { id: 'raw' as Tab, label: t('inspector.tab.raw') },
          ]}
        />
      </div>

      {current === 'chat' && <ChatView d={d} />}
      {current === 'prune' && <PruneDiff detail={d} />}
      {current === 'xray' && (d.xray ? <XRay blocks={d.xray.blocks} total={d.xray.tokens} messages={d.xray.messages} /> : <p className="muted pad">{t('inspector.noXray')}</p>)}
      {current === 'raw' && <Raw d={d} />}
    </div>
  );
}

/** "12h (detail_max_age)" / "max_total_bytes" -> a readable reason. */
function purgedReason(t: ReturnType<typeof useI18n>['t'], reason: string | undefined): string {
  if (!reason) return t('purged.reason.age');
  if (/max_total_bytes|size/.test(reason)) return t('purged.reason.size');
  const age = /^(\S+)\s*\(detail_max_age\)/.exec(reason)?.[1];
  if (age) return t('purged.reason.ageOf', { age });
  return reason === 'age' ? t('purged.reason.age') : reason;
}

/** A request whose details retention deleted: what the list still knows about it. */
function Purged({ info, record, onClose, inDrawer }: { info: PurgedInfo; record?: RequestRecord; onClose: () => void; inDrawer?: boolean }) {
  const { t, f } = useI18n();
  return (
    <div className="inspector">
      <header className="insp-head">
        <div className="insp-title">
          <ModelLabel model={record ? recordModel(record) || record.path : undefined} vendor={record ? recordVendor(record) : undefined} />
          {record && (isFailed(record) ? <Badge tone="bad">{record.status || t('traffic.err')}</Badge> : <Badge tone="good">{record.status}</Badge>)}
        </div>
        {record && <div className="insp-sub muted small"><span>{f.dayTime(record.time)}</span><span className="mono">{record.method} {record.path}</span></div>}
        {!inDrawer && <div className="insp-actions"><IconButton icon="x" label={t('common.close')} onClick={onClose} /></div>}
      </header>
      <Callout tone="info" icon="database" title={info.purged_at ? t('purged.title', { date: f.dayTime(info.purged_at) }) : t('purged.titleNoDate')}>
        {t('purged.body', { reason: purgedReason(t, info.reason) })}
        {' '}<a href="#/settings/storage">{t('purged.settings')}</a>
      </Callout>
      {record && (
        <div className="facts">
          <Fact label={t('inspector.route')}><strong>{record.route}</strong><span className="fact-sub">{record.route_reason ?? record.upstream}</span></Fact>
          <Fact label={t('inspector.client')}><span>{recordClient(record).name || t('client.unknown')}</span><span className="fact-sub">{t(`protocol.${protocolOf(record)}`)}</span></Fact>
          <Fact label={t('inspector.latency')}><strong>{f.ms(record.duration_ms)}</strong><span className="fact-sub">{t('inspector.ttfb', { ms: f.ms(record.ttfb_ms) })}</span></Fact>
          <Fact label={t('inspector.usage')}>
            <strong>{record.usage ? t('inspector.usageSum', { input: f.num(record.usage.input_tokens + record.usage.cache_read_input_tokens + record.usage.cache_creation_input_tokens), output: f.num(record.usage.output_tokens) }) : '—'}</strong>
          </Fact>
        </div>
      )}
      <p className="fine mono">{info.error}</p>
    </div>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="fact">
      <span className="fact-label">{label}</span>
      <span className="fact-value">{children}</span>
    </div>
  );
}

function UsageBar({ d }: { d: RequestDetail }) {
  const { t, f } = useI18n();
  const u = d.usage!;
  const segs = [
    { key: 'read', label: t('usage.cacheRead'), value: u.cache_read_input_tokens, color: 'var(--cache-read)' },
    { key: 'write', label: t('usage.cacheWrite'), value: u.cache_creation_input_tokens, color: 'var(--cache-write)' },
    { key: 'fresh', label: t('usage.fresh'), value: u.input_tokens, color: 'var(--fresh)' },
  ];
  const input = segs.reduce((s, x) => s + x.value, 0);
  return (
    <div className="usage-block">
      <div className="usage-head">
        <span className="fact-label">{t('inspector.usage')}</span>
        <span className="muted small">{t('inspector.usageSum', { input: f.num(input), output: f.num(u.output_tokens) })}</span>
      </div>
      <SplitBar segments={segs} label={t('inspector.usage')} height={10} format={(v) => f.num(v)} />
      <div className="usage-legend">
        {segs.map((s) => (
          <span key={s.key}><i className="legend-key" style={{ ['--c' as string]: s.color }} />{s.label} <strong className="mono">{f.num(s.value)}</strong></span>
        ))}
        <span><i className="legend-key" style={{ ['--c' as string]: 'var(--output)' }} />{t('usage.output')} <strong className="mono">{f.num(u.output_tokens)}</strong></span>
      </div>
    </div>
  );
}

export function XRay({ blocks, total, messages }: { blocks: Block[]; total: number; messages: number }) {
  const { t, f } = useI18n();
  const [kind, setKind] = useState<Kind | null>(null);
  const [sort, setSort] = useState<'size' | 'order'>('size');

  const byKind = useMemo(() => {
    const m = new Map<Kind, { tokens: number; count: number }>();
    for (const b of blocks) {
      const k = asKind(b.kind);
      const e = m.get(k) ?? { tokens: 0, count: 0 };
      e.tokens += b.tokens;
      e.count++;
      m.set(k, e);
    }
    return m;
  }, [blocks]);

  const rows = useMemo(() => {
    const r = kind ? blocks.filter((b) => asKind(b.kind) === kind) : [...blocks];
    if (sort === 'size') r.sort((a, b) => b.tokens - a.tokens);
    return r.slice(0, 200);
  }, [blocks, kind, sort]);
  const max = Math.max(...rows.map((b) => b.tokens), 1);

  return (
    <div className="xray">
      <div className="section-head">
        <h3>{t('xray.title')}</h3>
        <span className="muted small">{t('xray.summary', { tokens: f.tokens(total), blocks: blocks.length, messages })}</span>
      </div>
      <SplitBar
        label={t('xray.barLabel')}
        height={18}
        selected={kind}
        onSelect={(k) => setKind(kind === k ? null : (k as Kind))}
        segments={KINDS.filter((k) => byKind.get(k)).map((k) => ({
          key: k, label: t(`kind.${k}`), value: byKind.get(k)!.tokens, color: KIND_COLOR[k],
          title: `${t(`kind.${k}`)}: ~${f.tokens(byKind.get(k)!.tokens)}`,
        }))}
      />
      <div className="legend">
        {KINDS.filter((k) => byKind.get(k)).map((k) => (
          <button key={k} type="button" className={cx('legend-item', kind && kind !== k && 'off')} aria-pressed={kind === k} onClick={() => setKind(kind === k ? null : k)}>
            <i className="legend-key" style={{ ['--c' as string]: KIND_COLOR[k] }} />
            {t(`kind.${k}`)} <span className="muted">{byKind.get(k)!.count} · ~{f.tokens(byKind.get(k)!.tokens)}</span>
          </button>
        ))}
        <span className="toolbar-spacer" />
        <Segmented size="sm" label={t('xray.sort')} value={sort} onChange={setSort}
          options={[{ id: 'size', label: t('xray.sort.size') }, { id: 'order', label: t('xray.sort.order') }]} />
      </div>
      <table className="table blocks">
        <thead>
          <tr><th>{t('xray.col.block')}</th><th className="num">{t('xray.col.tokens')}</th><th>{t('xray.col.preview')}</th></tr>
        </thead>
        <tbody>
          {rows.map((b) => (
            <tr key={b.key} className={b.is_error ? 'is-failed' : ''}>
              <td>
                <div className="blk-name">
                  <i className="legend-key" style={{ ['--c' as string]: KIND_COLOR[asKind(b.kind)] }} />
                  <span>{t(`kind.${asKind(b.kind)}`)}</span>
                  {b.name && <span className="mono muted">{b.name}</span>}
                  {b.cached && <Badge tone="info" title={t('xray.cacheHint')}>{t('xray.cache')}</Badge>}
                  {b.is_error && <Badge tone="bad">{t('common.error')}</Badge>}
                </div>
                <div className="mono muted tiny">{b.key}{b.role ? ` · ${b.role}` : ''}</div>
              </td>
              <td className="num">
                <span className="mono">{f.num(b.tokens)}</span>
                <span className="size-bar"><span style={{ width: `${(b.tokens / max) * 100}%` }} /></span>
              </td>
              <td className="preview clip" title={b.preview}><PreviewText text={b.preview} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      {blocks.length > rows.length && <p className="fine">{t('xray.truncated', { shown: rows.length, total: blocks.length })}</p>}
    </div>
  );
}

function Raw({ d }: { d: RequestDetail }) {
  const { t, f } = useI18n();
  return (
    <div className="raw-list">
      <Disclosure title={t('raw.headers')} meta={t('raw.masked')} defaultOpen>
        <HeadersTable headers={d.request_headers ?? {}} />
      </Disclosure>
      {d.request_body && <Body title={t('raw.request')} body={d.request_body} meta={t('raw.chars', { n: f.compact(d.request_body.length) })} />}
      {d.sent_body && <Body title={t('raw.sent')} body={d.sent_body} meta={t('raw.chars', { n: f.compact(d.sent_body.length) })} />}
      {d.response_body && <Body title={t('raw.response')} body={d.response_body} meta={t('raw.chars', { n: f.compact(d.response_body.length) })} />}
      {!d.request_body && <Callout tone="info">{t('raw.noBodies')}</Callout>}
    </div>
  );
}

function HeadersTable({ headers }: { headers: Record<string, string> }) {
  const { t } = useI18n();
  const [copy, copied] = useCopy();
  const entries = Object.entries(headers).sort(([a], [b]) => a.localeCompare(b));
  const all = entries.map(([k, v]) => `${k}: ${v}`).join('\n');
  return (
    <div className="headers-wrap">
      <div className="headers-bar">
        <span className="muted small">{t('raw.headerCount', { count: entries.length })}</span>
        <Button size="sm" variant="ghost" icon={copied ? 'check' : 'copy'} onClick={() => copy(all)}>{copied ? t('common.copied') : t('raw.copyHeaders')}</Button>
      </div>
      <dl className="headers-list">
        {entries.map(([k, v]) => (
          <div key={k}>
            <dt className="mono">{k}</dt>
            <dd className="mono">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function Body({ title, body, meta }: { title: string; body: string; meta: string }) {
  const pretty = useMemo(() => {
    try {
      return JSON.stringify(JSON.parse(body), null, 2);
    } catch {
      return body;
    }
  }, [body]);
  return (
    <Disclosure title={title} meta={meta}>
      <pre className="code">{pretty.length > 200_000 ? pretty.slice(0, 200_000) + '\n…' : pretty}</pre>
    </Disclosure>
  );
}
