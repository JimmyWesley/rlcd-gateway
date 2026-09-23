import { useMemo, useState } from 'react';
import { fmt, type RequestDetail } from '../api';
import { pruneApi, REASONS, usd, type BlockReport, type PruneDetail, type Verdict } from './pruneApi';

type Filter = 'decided' | 'dropped' | 'all';

// Kept/dropped diff of one request, from stage_details.prune (F1).
export function PruneDiff({ detail }: { detail: RequestDetail }) {
  const rep = detail.stage_details?.prune as PruneDetail | undefined;
  const [filter, setFilter] = useState<Filter>('decided');
  const [open, setOpen] = useState<string | null>(null);

  const rows = useMemo(() => {
    if (!rep) return [];
    const bs = rep.blocks ?? [];
    if (filter === 'dropped') return bs.filter((b) => b.decision === 'drop');
    if (filter === 'decided') return bs.filter((b) => b.reason !== 'protected');
    return bs;
  }, [rep, filter]);

  if (!rep) return null;
  const shadow = rep.mode === 'shadow' || !rep.applied;
  const saved = rep.saved_tokens;
  const dollars = rep.est_cost_before - rep.est_cost_after;

  return (
    <div className="prune-diff">
      <div className="xray-head">
        <h3>Pruning</h3>
        <span className={`prune-mode ${shadow ? 'shadow' : 'enforce'}`}>{shadow ? 'shadow' : 'enforced'}</span>
        <span className="muted">
          {rep.preset} · keep if score ≥ {rep.threshold}
          {rep.epoch > 0 && <> · epoch {rep.epoch}</>}
        </span>
      </div>

      <div className={`prune-headline ${shadow ? 'shadow' : ''}`}>
        {saved > 0 ? (
          shadow ? (
            <>
              <strong>Shadow mode: nothing was changed.</strong> Enforcing would have saved ~{fmt.n(saved)} tokens
              (estimate) on this turn: {rep.dropped} block{rep.dropped === 1 ? '' : 's'} replaced by markers.
            </>
          ) : (
            <>
              <strong>Sent ~{fmt.n(rep.est_tokens_after)} instead of ~{fmt.n(rep.est_tokens_before)} tokens</strong>{' '}
              (estimate): {rep.dropped} block{rep.dropped === 1 ? '' : 's'} replaced by markers the model can recall.
            </>
          )
        ) : (
          <>Nothing {shadow ? 'would be' : 'was'} dropped on this turn.</>
        )}
      </div>

      <div className="facts prune-facts">
        <Fact
          label={shadow ? 'Would cost (est.)' : 'Input cost (est.)'}
          value={`${usd(rep.est_cost_after)}`}
          sub={`vs ${usd(rep.est_cost_before)} unpruned · ${dollars >= 0 ? 'saves' : 'costs'} ${usd(Math.abs(dollars))}`}
        />
        <Fact
          label="Epoch"
          value={rep.epoch_ran ? `ran · ${rep.candidates} new` : 'not this turn'}
          sub={rep.epoch_ran ? `selector ${fmt.ms(rep.selector_ms)}` : `next at ~${fmt.n(rep.next_epoch_at)} tokens`}
        />
        <Fact label="Dropped" value={`${rep.dropped}`} sub={rep.new_drops ? `${rep.new_drops} new this turn` : 'all sticky'} />
        <Fact
          label="Priced as"
          value={rep.cost.priced_as}
          sub={rep.cost.cached ? 'with prompt caching' : 'no cache_control in request'}
        />
      </div>

      {rep.selector_error && (
        <p className="note">Selector failed ({rep.selector_error}): everything new was kept. It will be asked again at the next epoch.</p>
      )}
      {rep.warning && <p className="note">{rep.warning}</p>}
      {rep.cache_invalidating && (
        <p className="note">
          This turn {shadow ? 'would change' : 'changes'} the cached prefix
          {rep.cost.invalid_from_tokens >= 0 && <> from ~{fmt.n(rep.cost.invalid_from_tokens)} tokens on</>}: the rest of
          the prompt is written to the cache again (1.25× input) once. Later turns reuse the same markers and read it at ~0.1×.
        </p>
      )}

      <div className="bar prune-bar" role="img" aria-label="Blocks by pruning decision">
        {rep.blocks.map((b) => (
          <span
            key={b.id + b.ir_key}
            className={`seg ${segClass(b)} ${open && open !== b.ir_key ? 'dim' : ''}`}
            style={{ flexGrow: b.tokens || 1 }}
            title={`${b.ir_key} ${b.kind}${b.name ? ` ${b.name}` : ''}: ~${fmt.n(b.tokens)} tokens · ${b.decision} (${REASONS[b.reason] ?? b.reason})`}
            onClick={() => {
              setOpen(open === b.ir_key ? null : b.ir_key);
              if (b.reason === 'protected') setFilter('all');
            }}
          />
        ))}
      </div>
      <div className="legend">
        <span><i className="sw prune-drop" />dropped</span>
        <span><i className="sw prune-keep" />kept</span>
        <span><i className="sw prune-pending" />pending</span>
        <span><i className="sw prune-protected" />protected</span>
        <span className="spacer" />
        {(['decided', 'dropped', 'all'] as Filter[]).map((f) => (
          <button key={f} className={filter === f ? 'active' : ''} onClick={() => setFilter(f)}>
            {f === 'decided' ? 'decided' : f === 'dropped' ? 'dropped only' : 'all blocks'}
          </button>
        ))}
      </div>

      <div className="scroll-x">
        <table className="blocks prune-blocks">
          <thead>
            <tr>
              <th>Key</th>
              <th>Block</th>
              <th className="num">~Tokens</th>
              <th>Decision</th>
              <th>Reason</th>
              <th className="num">Keep score</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr><td colSpan={6} className="muted">No blocks in this view.</td></tr>
            )}
            {rows.map((b) => (
              <Row
                key={b.id + b.ir_key}
                b={b}
                requestId={detail.id}
                open={open === b.ir_key}
                threshold={rep.threshold}
                onToggle={() => setOpen(open === b.ir_key ? null : b.ir_key)}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function segClass(b: BlockReport): string {
  if (b.decision === 'drop') return 'prune-drop';
  if (b.reason === 'protected') return 'prune-protected';
  if (b.reason === 'pending' || b.reason === 'fail_open') return 'prune-pending';
  return 'prune-keep';
}

function Row(props: { b: BlockReport; requestId: string; open: boolean; threshold: number; onToggle: () => void }) {
  const { b, requestId, open, threshold, onToggle } = props;
  const [sent, setSent] = useState<Verdict | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState('');

  const send = async (verdict: Verdict) => {
    setErr(null);
    try {
      await pruneApi.addFeedback({ request_id: requestId, key: b.key, verdict, note: note || undefined });
      setSent(verdict);
    } catch (e) {
      setErr(String(e));
    }
  };

  return (
    <>
      <tr className={`prune-row ${segClass(b)}-row ${open ? 'open' : ''}`} onClick={onToggle}>
        <td className="mono">
          {b.key}
          {b.new && <span className="tag" title="dropped for the first time on this turn">new</span>}
        </td>
        <td className="clip" title={b.what}>
          <i className={`sw k-${b.kind}`} />
          {b.kind}
          {b.name && <span className="muted"> {b.name}</span>}
          {b.is_error && <span className="bad"> error</span>}
        </td>
        <td className="num mono">
          {b.decision === 'drop' ? <>{fmt.n(b.tokens)} → {fmt.n(b.after)}</> : fmt.n(b.tokens)}
        </td>
        <td><span className={`chip ${b.decision}`}>{b.decision}</span></td>
        <td className="muted">{REASONS[b.reason] ?? b.reason}{b.protected ? `: ${b.protected}` : ''}</td>
        <td className={`num mono ${b.score != null && b.score < threshold ? 'bad' : ''}`}>
          {b.score != null ? b.score.toFixed(3) : '—'}
        </td>
      </tr>
      {open && (
        <tr className="prune-expand">
          <td colSpan={6}>
            {b.what && <div className="muted small">Shown to the selector as: {b.what}</div>}
            <pre className="raw prune-preview">{b.preview || '(empty)'}</pre>
            {b.marker && (
              <div className="small">
                Replaced by <code className="mono">{b.marker}</code>
                {b.first_req && b.first_req !== requestId && <span className="muted"> (first dropped in {b.first_req})</span>}
              </div>
            )}
            {b.reason !== 'protected' && (
              <div className="prune-feedback" onClick={(e) => e.stopPropagation()}>
                <input value={note} placeholder="note (optional)" onChange={(e) => setNote(e.target.value)} />
                <button className={sent === 'should_keep' ? 'active' : ''} onClick={() => send('should_keep')}>
                  Should have kept
                </button>
                <button className={sent === 'should_drop' ? 'active' : ''} onClick={() => send('should_drop')}>
                  Should have dropped
                </button>
                {sent && <span className="muted small">Saved as a replay case.</span>}
                {err && <span className="bad small">{err}</span>}
              </div>
            )}
          </td>
        </tr>
      )}
    </>
  );
}

function Fact({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="fact">
      <span className="label">{label}</span>
      <span className="value">{value}</span>
      {sub && <span className="hint">{sub}</span>}
    </div>
  );
}
