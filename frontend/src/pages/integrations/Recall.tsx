import { useState } from 'react';
import { useI18n } from '../../i18n';
import { gatewayURL, recallApi, type RecallSettings } from '../../lib/api';
import { href } from '../../lib/router';
import { useFetch, useGateway, useLiveFetch } from '../../state/gateway';
import { BarList } from '../../charts';
import { Badge, Button, Callout, Card, CopyField, EmptyState, ErrorState, Loading, Segmented, Stat, Toggle } from '../../ui';

const SERVER = 'rlcd-gateway';

export function Recall() {
  const { t, f, tn } = useI18n();
  const { config } = useGateway();
  const settings = useFetch(() => recallApi.settings(), []);
  const stats = useLiveFetch(() => recallApi.stats(), [], 5000);
  const events = useLiveFetch(() => recallApi.events(50), [], 5000);
  const [scope, setScope] = useState<'user' | 'local'>('user');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [maxKB, setMaxKB] = useState<string>('');

  const s = settings.data;
  const url = gatewayURL(config?.listen, s?.mcp_path ?? '/mcp');
  const cmd = `claude mcp add --transport http${scope === 'user' ? ' --scope user' : ''} ${SERVER} ${url}`;
  const undo = `claude mcp remove${scope === 'user' ? ' --scope user' : ''} ${SERVER}`;
  const tool = `mcp__${SERVER}__${s?.tool ?? 'rlcd_recall'}`;

  const update = async (patch: Partial<Pick<RecallSettings, 'enabled' | 'max_bytes'>>) => {
    setBusy(true);
    setErr(null);
    try {
      settings.setData(await recallApi.setSettings(patch));
      setMaxKB('');
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };

  const st = stats.data;
  return (
    <>
      <div className="grid grid-2-1">
        <Card title={t('recall.install.title')} subtitle={t('recall.install.sub')}>
          <div className="scope-row">
            <span className="field-label">{t('recall.install.scope')}</span>
            <Segmented size="sm" label={t('recall.install.scope')} value={scope} onChange={setScope}
              options={[{ id: 'user', label: t('recall.install.user') }, { id: 'local', label: t('recall.install.local') }]} />
          </div>
          <CopyField text={cmd} label="claude mcp add" />
          <p className="fine">{tn('recall.install.after', { tool: <code>{tool}</code>, undo: <code>{undo}</code> })}</p>
          {config && !config.log_bodies && <Callout tone="warn">{tn('recall.needsBodies', { key: <code>log_bodies</code> })}</Callout>}
          <p className="fine">{t('recall.install.how')}</p>
        </Card>
        <Card title={t('recall.settings.title')}>
          {settings.error && <ErrorState error={settings.error} onRetry={settings.reload} />}
          {!s && !settings.error && <Loading />}
          {s && (
            <div className="form-stack">
              <Toggle checked={s.enabled} disabled={busy} onChange={(v) => update({ enabled: v })} label={s.enabled ? t('recall.enabled') : t('recall.disabled')} description={t('recall.enabledHelp')} />
              <form className="inline-field" onSubmit={(e) => { e.preventDefault(); const kb = Number(maxKB); if (kb > 0) update({ max_bytes: Math.round(kb * 1000) }); }}>
                <span>{t('recall.maxBytes')}</span>
                <input type="number" min={1} className="input-sm" aria-label={t('recall.maxBytes')} placeholder={String(Math.round(s.max_bytes / 1000))} value={maxKB} onChange={(e) => setMaxKB(e.target.value)} />
                <span>KB</span>
                <Button size="sm" type="submit" disabled={!maxKB || busy}>{t('common.save')}</Button>
              </form>
              <p className="fine">{t('recall.maxBytesHelp', { kb: Math.round(s.max_bytes / 1000) })}</p>
            </div>
          )}
          {err && <Callout tone="bad">{err}</Callout>}
        </Card>
      </div>

      <Callout tone="accent" icon="recall" title={t('recall.quality.title')}>{t('recall.quality.body')}</Callout>

      {stats.error && !st && <ErrorState error={stats.error} onRetry={stats.reload} />}
      {st && (
        <div className="kpi-row kpi-row-4">
          <Card className="kpi"><Stat icon="recall" label={t('recall.stat.total')} value={f.num(st.total)} sub={st.errors ? <span className="tone-warn">{t('overview.recall.failed', { count: st.errors })}</span> : t('overview.recall.none')} /></Card>
          <Card className="kpi"><Stat label={t('recall.stat.restored')} value={f.compact(st.tokens_restored)} sub={t('common.estimated')} /></Card>
          <Card className="kpi"><Stat label={t('recall.stat.convs')} value={f.num(st.conversations)} /></Card>
          <Card className="kpi"><Stat label={t('recall.stat.last')} value={st.last ? f.ago(st.last) : '—'} sub={st.last ? f.dayTime(st.last) : undefined} /></Card>
        </div>
      )}

      {st && st.total > 0 && (
        <div className="grid grid-3">
          <Card title={t('recall.top.tools')}>
            <BarList label={t('recall.top.tools')} items={st.top_tools.map((x) => ({ key: x.tool, label: <span className="mono">{x.tool}</span>, value: x.count, display: `${x.count}×`, sub: t('common.tokensShort', { n: f.compact(x.tokens) }), color: 'var(--series-2)' }))} />
          </Card>
          <Card title={t('recall.top.blocks')}>
            <BarList label={t('recall.top.blocks')} items={st.top_keys.map((x) => ({ key: x.key, label: <span className="mono clip">{x.key}</span>, value: x.count, display: `${x.count}×`, sub: x.tool, color: 'var(--series-3)' }))} />
          </Card>
          <Card title={t('recall.top.failures')}>
            {Object.keys(st.by_error).length === 0 ? <EmptyState compact icon="check" title={t('recall.top.noFailures')} /> : (
              <BarList label={t('recall.top.failures')} items={Object.entries(st.by_error).map(([code, n]) => ({ key: code, label: <RecallErr code={code} />, value: n, display: `${n}×`, color: 'var(--bad)' }))} />
            )}
          </Card>
        </div>
      )}

      <Card title={t('recall.events.title')} subtitle={t('recall.events.sub')}>
        {events.error && !events.data && <ErrorState error={events.error} onRetry={events.reload} />}
        {events.data && events.data.length === 0 && <EmptyState compact icon="recall" title={t('recall.events.empty')}>{t('recall.events.emptyBody')}</EmptyState>}
        {events.data && events.data.length > 0 && (
          <div className="scroll-x">
            <table className="table">
              <thead>
                <tr>
                  <th>{t('recall.col.time')}</th>
                  <th>{t('recall.col.result')}</th>
                  <th>{t('recall.col.block')}</th>
                  <th>{t('recall.col.tool')}</th>
                  <th className="num">{t('recall.col.tokens')}</th>
                  <th>{t('recall.col.request')}</th>
                </tr>
              </thead>
              <tbody>
                {events.data.map((e, i) => (
                  <tr key={`${e.time}-${i}`} className={e.ok ? '' : 'is-failed'} title={e.message}>
                    <td className="mono small">{f.time(e.time)}</td>
                    <td>{e.ok ? <Badge tone="good">{t('recall.restoredOk')}</Badge> : <Badge tone={e.error === 'expired' ? 'warn' : 'bad'} title={e.message}>{e.error ? <RecallErr code={e.error} /> : null}</Badge>}{e.truncated && <Badge tone="warn">{t('recall.truncated')}</Badge>}</td>
                    <td className="mono small clip" title={e.tool_use_id || e.key}>{e.block_key ?? e.key}</td>
                    <td className="small">{e.tool ?? (e.kind ? <span className="muted">{e.kind}</span> : '')}</td>
                    <td className="num mono small">{e.ok ? f.num(e.tokens) : '—'}</td>
                    <td className="mono small clip"><a href={href(`traffic/${e.req}`)} title={e.req}>{e.req}</a></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

const RECALL_ERRORS = ['bad_args', 'disabled', 'unknown_request', 'body_not_logged', 'key_not_found', 'store_error', 'expired'] as const;

/** A recall error code with a readable label; the code stays as the tooltip. */
function RecallErr({ code }: { code: string }) {
  const { t } = useI18n();
  const known = (RECALL_ERRORS as readonly string[]).includes(code);
  return <span title={code}>{known ? t(`recall.err.${code as (typeof RECALL_ERRORS)[number]}`) : code}</span>;
}
