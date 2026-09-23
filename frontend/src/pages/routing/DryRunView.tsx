import { useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { isModelCall, recordModel } from '../../lib/brands';
import { protocolOf } from '../../lib/api';
import { routerApi, type DryRun } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, EmptyState, Field, Toggle, cx } from '../../ui';

export function DryRunView({ dirty }: { dirty: boolean }) {
  const { t, f } = useI18n();
  const { requests } = useGateway();
  const msgs = useMemo(() => requests.filter(isModelCall).slice(0, 100), [requests]);
  const [id, setId] = useState('');
  const [fresh, setFresh] = useState(true);
  const [res, setRes] = useState<DryRun | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const chosen = id || msgs[0]?.id || '';

  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      setRes(await routerApi.dryRun(chosen, fresh));
    } catch (e) {
      setRes(null);
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Card title={t('dryrun.title')} subtitle={t('dryrun.sub')}>
        {msgs.length === 0 ? (
          <EmptyState compact icon="traffic" title={t('dryrun.none')}>{t('dryrun.noneBody')}</EmptyState>
        ) : (
          <div className="dry-controls">
            <Field label={t('dryrun.request')} wide>
              <select value={chosen} onChange={(e) => setId(e.target.value)}>
                {msgs.map((r) => (
                  <option key={r.id} value={r.id}>
                    {f.time(r.time)} · {t(`protocol.${protocolOf(r)}`)} · {r.client_model ?? recordModel(r) ?? '?'} · ~{f.tokens(r.est_tokens)} · {r.route}
                  </option>
                ))}
              </select>
            </Field>
            <Toggle checked={fresh} onChange={setFresh} label={t('dryrun.fresh')} description={t('dryrun.freshHint')} />
            <Button variant="primary" icon="play" onClick={run} loading={busy} disabled={!chosen}>{t('dryrun.run')}</Button>
          </div>
        )}
        {dirty && <Callout tone="warn">{t('dryrun.dirty')}</Callout>}
        {err && <Callout tone="bad">{err}</Callout>}
      </Card>
      {res && <DryRunResult res={res} />}
    </>
  );
}

function DryRunResult({ res }: { res: DryRun }) {
  const { t, f } = useI18n();
  const fx = res.facts;
  const pin: Record<DryRun['sticky_action'], string> = {
    create: t('dryrun.pin.create'),
    replace: t('dryrun.pin.replace'),
    use: t('dryrun.pin.use'),
    none: fx.background ? t('dryrun.pin.background') : t('dryrun.pin.none'),
  };
  return (
    <Card title={t('dryrun.result')}>
      <div className={cx('dry-verdict', res.ok && 'ok')}>
        <Icon name="arrowRight" size={18} />
        <div>
          <div className="dry-route">
            <strong>{res.effective_route}</strong>
            {!res.ok && <span className="muted"> ({t('dryrun.activeRoute')})</span>}
          </div>
          <div>{res.decision.reason}</div>
          {res.decision.model && <div className="muted small">{t('dryrun.upstreamModel', { model: res.decision.model })}</div>}
          {res.decision.alias && <div className="muted small">{t('dryrun.alias', { alias: res.decision.alias })}</div>}
          {res.decision.error && <div className="tone-bad small">{res.decision.error}</div>}
          <div className="muted small">
            {pin[res.sticky_action]}
            {res.logged.route && <> · {t('dryrun.logged', { route: res.logged.route })}{res.logged.route_reason ? ` — ${res.logged.route_reason}` : ''}</>}
          </div>
          {res.notes?.map((n) => <div key={n} className="muted small">{n}</div>)}
        </div>
      </div>

      <dl className="dry-facts">
        <div><dt>model</dt><dd className="mono">{fx.model || '—'}</dd></div>
        {fx.protocol && <div><dt>{t('dryrun.fact.protocol')}</dt><dd>{t(`protocol.${fx.protocol}`)}</dd></div>}
        <div><dt>{t('dryrun.fact.context')}</dt><dd>~{f.tokens(fx.context_tokens)}</dd></div>
        <div><dt>{t('dryrun.fact.messages')}</dt><dd>{fx.messages}</dd></div>
        <div><dt>max_tokens</dt><dd>{fx.max_tokens}</dd></div>
        <div><dt>{t('dryrun.fact.tools')}</dt><dd>{fx.tools}</dd></div>
        <div><dt>{t('dryrun.fact.images')}</dt><dd>{fx.has_images ? t('common.yes') : t('common.no')}</dd></div>
        <div><dt>{t('dryrun.fact.thinking')}</dt><dd>{fx.has_thinking ? `${fx.thinking_param || t('dryrun.fact.blocks')}${fx.thinking_blocks ? ` · ${fx.thinking_blocks}` : ''}` : t('common.no')}</dd></div>
        <div><dt>{t('dryrun.fact.background')}</dt><dd>{fx.background ? `${t('common.yes')} — ${fx.background_signals?.join(', ')}` : t('common.no')}</dd></div>
        <div className="wide"><dt>{t('dryrun.fact.conversation')}</dt><dd className="mono small">{fx.conversation_id}</dd></div>
      </dl>

      {res.trace.length === 0 ? (
        <p className="muted">{t('dryrun.noRules')}</p>
      ) : (
        <ol className="trace">
          {res.trace.map((tr) => (
            <li key={tr.index} className={cx('trace-row', `res-${tr.result}`)}>
              <span className="rule-idx mono">{tr.index + 1}</span>
              <div className="trace-main">
                <div className="trace-head">
                  <strong>{tr.name}</strong>
                  <span className="muted small">→ {tr.kind === 'auto' ? `auto${tr.route ? `: ${tr.route}` : ''}` : tr.route}</span>
                  <Badge tone={tr.result === 'matched' ? 'good' : tr.result === 'error' ? 'bad' : 'neutral'}>{t(`dryrun.res.${tr.result}`)}</Badge>
                </div>
                {tr.reason && !redundant(tr) && <div className="small">{tr.reason}</div>}
                {tr.checks?.map((c, i) => (
                  <div key={i} className={cx('check small', c.ok ? 'tone-good' : 'tone-bad')}>
                    <Icon name={c.ok ? 'check' : 'x'} size={12} /> {c.condition} <span className="muted">({c.detail})</span>
                  </div>
                ))}
              </div>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}

// A plain match/no-match reason only repeats the checks listed under it.
function redundant(tr: DryRun['trace'][number]): boolean {
  if (!tr.checks?.length) return false;
  return tr.result === 'no_match' || (tr.result === 'matched' && tr.kind === 'match');
}
