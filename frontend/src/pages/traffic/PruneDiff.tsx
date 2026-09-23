// The kept/dropped diff of one request (stage_details.prune): what the
// pruner did, why, and a way to tell it when it was wrong.
import { useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import type { RequestDetail } from '../../lib/api';
import { isReason, pruneApi, type BlockReport, type PruneDetail, type Verdict } from '../../lib/pruneApi';
import { Badge, Button, Callout, Segmented, cx } from '../../ui';
import { asKind, KIND_COLOR } from './Inspector';

type Filter = 'decided' | 'dropped' | 'all';
type State = 'drop' | 'keep' | 'pending' | 'protected';

export function blockState(b: BlockReport): State {
  if (b.decision === 'drop') return 'drop';
  if (b.reason === 'protected') return 'protected';
  if (b.reason === 'pending' || b.reason === 'fail_open') return 'pending';
  return 'keep';
}

export const STATE_COLOR: Record<State, string> = {
  drop: 'var(--dropped)', keep: 'var(--kept)', pending: 'var(--pending)', protected: 'var(--protected)',
};

export function useReasonLabel() {
  const { t } = useI18n();
  return (r: string) => (isReason(r) ? t(`reason.${r}`) : r);
}

export function PruneDiff({ detail }: { detail: RequestDetail }) {
  const { t, f, tn } = useI18n();
  const reason = useReasonLabel();
  const rep = detail.stage_details?.prune as PruneDetail | undefined;
  const [filter, setFilter] = useState<Filter>('decided');
  const [open, setOpen] = useState<string | null>(null);

  const blocks = useMemo(() => rep?.blocks ?? [], [rep]);
  const rows = useMemo(() => {
    if (filter === 'dropped') return blocks.filter((b) => b.decision === 'drop');
    if (filter === 'decided') return blocks.filter((b) => b.reason !== 'protected');
    return blocks;
  }, [blocks, filter]);

  if (!rep) return null;
  const shadow = rep.mode === 'shadow' || !rep.applied;
  const saved = rep.saved_tokens;
  const dollars = rep.est_cost_before - rep.est_cost_after;
  const before = rep.est_tokens_before || 1;
  const counts = blocks.reduce<Record<State, number>>((m, b) => ((m[blockState(b)] += 1), m), { drop: 0, keep: 0, pending: 0, protected: 0 });

  return (
    <div className="prune">
      <div className={cx('prune-hero', shadow && 'is-shadow')}>
        <div className="prune-hero-top">
          <Badge tone={shadow ? 'shadow' : 'dropped'} icon={shadow ? 'eye' : 'savings'}>
            {shadow ? t('prune.mode.shadow') : t('prune.mode.enforced')}
          </Badge>
          <span className="muted small">
            {t('prune.meta', { preset: t(`preset.${rep.preset}`), threshold: rep.threshold })}
            {rep.epoch > 0 && ` · ${t('prune.epochN', { n: rep.epoch })}`}
          </span>
        </div>
        <div className="prune-headline">
          {saved > 0
            ? shadow
              ? tn('prune.headline.shadow', { n: <strong>{t('common.tokensShort', { n: f.tokens(saved) })}</strong>, count: rep.dropped })
              : tn('prune.headline.enforced', { after: <strong>{t('common.tokensShort', { n: f.tokens(rep.est_tokens_after) })}</strong>, before: t('common.tokensShort', { n: f.tokens(rep.est_tokens_before) }), count: rep.dropped })
            : shadow ? t('prune.headline.nothingShadow') : t('prune.headline.nothing')}
        </div>
        {/* Before/after: the dropped share is cut out of the "after" bar. */}
        <div className="beforeafter" aria-hidden>
          <div className="ba-row">
            <span className="ba-label">{t('prune.before')}</span>
            <span className="ba-bar"><span className="ba-fill kept" style={{ width: '100%' }} /></span>
            <span className="ba-val mono">{f.tokens(rep.est_tokens_before)}</span>
          </div>
          <div className="ba-row">
            <span className="ba-label">{shadow ? t('prune.wouldSend') : t('prune.after')}</span>
            <span className="ba-bar">
              <span className="ba-fill kept" style={{ width: `${(rep.est_tokens_after / before) * 100}%` }} />
              <span className="ba-fill dropped" style={{ width: `${(saved / before) * 100}%` }} />
            </span>
            <span className="ba-val mono">{f.tokens(rep.est_tokens_after)}</span>
          </div>
        </div>
        <div className="prune-facts">
          <div>
            <span className="fact-label">{shadow ? t('prune.fact.wouldCost') : t('prune.fact.cost')}</span>
            <strong>{f.usd(rep.est_cost_after)}</strong>
            <span className={cx('fact-sub', dollars < 0 ? 'tone-bad' : 'tone-good')}>
              {dollars >= 0 ? t('prune.fact.saves', { usd: f.usd(dollars), before: f.usd(rep.est_cost_before) }) : t('prune.fact.costs', { usd: f.usd(-dollars), before: f.usd(rep.est_cost_before) })}
            </span>
          </div>
          <div>
            <span className="fact-label">{t('prune.fact.epoch')}</span>
            <strong>{rep.epoch_ran ? t('prune.fact.epochRan', { count: rep.candidates }) : t('prune.fact.epochNo')}</strong>
            <span className="fact-sub">{rep.epoch_ran ? t('prune.fact.selector', { ms: f.ms(rep.selector_ms) }) : t('prune.fact.nextEpoch', { n: f.tokens(rep.next_epoch_at) })}</span>
          </div>
          <div>
            <span className="fact-label">{t('prune.fact.dropped')}</span>
            <strong>{f.num(rep.dropped)}</strong>
            <span className="fact-sub">{rep.new_drops ? t('prune.fact.newDrops', { count: rep.new_drops }) : t('prune.fact.allSticky')}</span>
          </div>
          <div>
            <span className="fact-label">{t('prune.fact.pricedAs')}</span>
            <strong className="mono">{rep.cost.priced_as}</strong>
            <span className="fact-sub">{rep.cost.cached ? t('prune.fact.cached') : t('prune.fact.notCached')}</span>
          </div>
        </div>
      </div>

      {rep.selector_error && <Callout tone="warn" title={t('prune.selectorFailed')}><span className="mono">{rep.selector_error}</span> — {t('prune.selectorFailedBody')}</Callout>}
      {rep.warning && <Callout tone="warn">{rep.warning}</Callout>}
      {rep.cache_invalidating && (
        <Callout tone="info" icon="database" title={shadow ? t('prune.cacheRewrite.titleShadow') : t('prune.cacheRewrite.title')}>
          {rep.cost.invalid_from_tokens >= 0
            ? t('prune.cacheRewrite.from', { n: f.tokens(rep.cost.invalid_from_tokens) })
            : t('prune.cacheRewrite.body')}
        </Callout>
      )}

      <div className="section-head">
        <h3>{t('prune.map.title')}</h3>
        <span className="muted small">{t('prune.map.sub')}</span>
      </div>
      <div className="blockmap" role="img" aria-label={t('prune.map.aria', { drop: counts.drop, keep: counts.keep, pending: counts.pending, protected: counts.protected })}>
        {blocks.map((b) => {
          const st = blockState(b);
          return (
            <button
              type="button"
              key={b.id + b.ir_key}
              className={cx('bm-cell', `bm-${st}`, open && open !== b.ir_key && 'dim', open === b.ir_key && 'on')}
              style={{ flexGrow: Math.max(b.tokens, 40) }}
              title={`${b.name ?? t(`kind.${asKind(b.kind)}`)} · ~${f.tokens(b.tokens)} · ${t(`prune.state.${st}`)} (${reason(b.reason)})`}
              aria-label={`${b.name ?? b.kind} ${t(`prune.state.${st}`)}`}
              tabIndex={-1}
              onClick={() => {
                setOpen(open === b.ir_key ? null : b.ir_key);
                if (b.reason === 'protected') setFilter('all');
                else if (b.decision !== 'drop' && filter === 'dropped') setFilter('decided');
              }}
            />
          );
        })}
      </div>
      <div className="legend">
        {(['drop', 'keep', 'pending', 'protected'] as State[]).map((s) => (
          <span key={s} className="legend-item static">
            <i className="legend-key" style={{ ['--c' as string]: STATE_COLOR[s] }} />
            {t(`prune.state.${s}`)} <span className="muted">{counts[s]}</span>
          </span>
        ))}
        <span className="toolbar-spacer" />
        <Segmented
          size="sm"
          label={t('prune.filter')}
          value={filter}
          onChange={setFilter}
          options={[
            { id: 'decided', label: t('prune.filter.decided') },
            { id: 'dropped', label: t('prune.filter.dropped') },
            { id: 'all', label: t('prune.filter.all') },
          ]}
        />
      </div>

      <ul className="blocklist">
        {rows.length === 0 && <li className="muted pad">{t('prune.noBlocks')}</li>}
        {rows.map((b) => (
          <BlockRow
            key={b.id + b.ir_key}
            b={b}
            requestId={detail.id}
            threshold={rep.threshold}
            open={open === b.ir_key}
            onToggle={() => setOpen(open === b.ir_key ? null : b.ir_key)}
          />
        ))}
      </ul>
    </div>
  );
}

function BlockRow({ b, requestId, threshold, open, onToggle }: { b: BlockReport; requestId: string; threshold: number; open: boolean; onToggle: () => void }) {
  const { t, f } = useI18n();
  const reason = useReasonLabel();
  const st = blockState(b);
  const [sent, setSent] = useState<Verdict | null>(null);
  const [busy, setBusy] = useState<Verdict | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState('');

  const send = async (verdict: Verdict) => {
    setErr(null);
    setBusy(verdict);
    try {
      await pruneApi.addFeedback({ request_id: requestId, key: b.key, verdict, note: note || undefined });
      setSent(verdict);
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <li className={cx('blk', `blk-${st}`, open && 'open')}>
      <button type="button" className="blk-row" onClick={onToggle} aria-expanded={open}>
        <span className="blk-state" style={{ ['--c' as string]: STATE_COLOR[st] }}>
          {st === 'drop' ? <Icon name="x" size={12} /> : st === 'protected' ? <Icon name="shield" size={12} /> : st === 'pending' ? <Icon name="clock" size={12} /> : <Icon name="check" size={12} />}
        </span>
        <span className="blk-main">
          <span className="blk-name">
            <i className="legend-key" style={{ ['--c' as string]: KIND_COLOR[asKind(b.kind)] }} />
            <strong>{b.name ?? t(`kind.${asKind(b.kind)}`)}</strong>
            {b.name && <span className="muted small">{t(`kind.${asKind(b.kind)}`)}</span>}
            {b.new && <Badge tone="accent" title={t('prune.newHint')}>{t('prune.new')}</Badge>}
            {b.is_error && <Badge tone="bad">{t('common.error')}</Badge>}
          </span>
          <span className="blk-what muted small clip" title={b.what ?? b.preview}>{b.what ?? b.preview}</span>
        </span>
        <span className="blk-reason small">{reason(b.reason)}{b.protected ? `: ${b.protected}` : ''}</span>
        <span className="blk-score" title={b.score != null ? t('prune.scoreHint', { score: f.score(b.score), threshold }) : undefined}>
          {b.score != null ? (
            <>
              <span className="score-bar">
                <span className={cx('score-fill', b.score < threshold && 'below')} style={{ width: `${b.score * 100}%` }} />
                <span className="score-mark" style={{ left: `${threshold * 100}%` }} />
              </span>
              <span className="mono small">{f.score(b.score)}</span>
            </>
          ) : <span className="muted small">—</span>}
        </span>
        <span className="blk-tokens mono small num">
          {b.decision === 'drop' ? <><s className="muted">{f.tokens(b.tokens)}</s> → {f.tokens(b.after)}</> : f.tokens(b.tokens)}
        </span>
        <Icon name={open ? 'chevronDown' : 'chevronRight'} size={14} />
      </button>
      {open && (
        <div className="blk-detail">
          {b.what && <div className="muted small">{t('prune.shownAs', { what: b.what })}</div>}
          <pre className="code code-sm">{b.preview || t('prune.empty')}</pre>
          {b.marker && (
            <div className="marker">
              <span className="fact-label">{t('prune.replacedBy')}</span>
              <code className="mono">{b.marker}</code>
              {b.first_req && b.first_req !== requestId && <span className="muted small">{t('prune.firstDropped', { req: b.first_req })}</span>}
            </div>
          )}
          {b.reason !== 'protected' && (
            <div className="feedback">
              <span className="fact-label">{t('prune.feedback.title')}</span>
              <div className="feedback-row">
                <input value={note} placeholder={t('prune.feedback.note')} aria-label={t('prune.feedback.note')} onChange={(e) => setNote(e.target.value)} />
                <Button size="sm" icon="thumbUp" loading={busy === 'should_keep'} className={cx(sent === 'should_keep' && 'is-on')} onClick={() => send('should_keep')}>
                  {t('prune.feedback.keep')}
                </Button>
                <Button size="sm" icon="thumbDown" loading={busy === 'should_drop'} className={cx(sent === 'should_drop' && 'is-on')} onClick={() => send('should_drop')}>
                  {t('prune.feedback.drop')}
                </Button>
              </div>
              {sent && <span className="tone-good small" role="status"><Icon name="check" size={12} /> {t('prune.feedback.saved')} <a href="#/savings">{t('prune.feedback.see')}</a></span>}
              {err && <span className="tone-bad small" role="alert">{err}</span>}
            </div>
          )}
        </div>
      )}
    </li>
  );
}
