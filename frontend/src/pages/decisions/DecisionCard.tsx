// A System One call as it reads best: the state, then each question with its
// answer, confidence and probabilities, the mirror's answer when there is one,
// and (in the audit log) a control to record what actually happened.
import { useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { decisionsApi, type DecisionQuestion, type Mirror } from '../../lib/decisionsApi';
import { Badge, Disclosure, cx } from '../../ui';

type Probs = Record<string, { probs: [string, number][] }>;
type Defs = Record<string, { instructions?: string; criteria?: unknown }>;
export type Outcome = string | boolean | number | null;

export function DecisionCard({ questions, probs, defs, state, mirror, requestId, editable, onChange }: {
  questions: DecisionQuestion[]; probs?: Probs; defs?: Defs; state?: string | null; mirror?: Mirror;
  requestId?: string; editable?: boolean; onChange?: (q: DecisionQuestion) => void;
}) {
  const { t, f } = useI18n();
  return (
    <div className="decision">
      {state != null && (
        <Disclosure title={<span className="chat-role"><Icon name="database" size={13} /> {t('decision.state')}</span>} meta={t('decision.stateSize', { n: f.bytes(state.length) })}>
          <pre className="code code-sm">{state}</pre>
        </Disclosure>
      )}
      <ul className="dq-list">
        {questions.map((q) => (
          <QuestionRow key={q.id} q={q} probs={probs?.[q.id]?.probs} def={defs?.[q.id]} mirror={mirror?.questions.find((m) => m.id === q.id)} mirrorBackend={mirror?.backend}
            requestId={requestId} editable={editable} onChange={onChange} />
        ))}
      </ul>
      {mirror && (
        <p className="fine">
          {t('decision.mirrorLine', { backend: mirror.backend, ms: f.ms(mirror.duration_ms), primary: f.ms(mirror.primary_duration_ms), agreed: mirror.agreed, compared: mirror.compared })}
          {mirror.error && <span className="tone-bad"> · {mirror.error}</span>}
        </p>
      )}
    </div>
  );
}

function ConfBar({ v }: { v: number | undefined }) {
  const { f } = useI18n();
  if (v == null) return <span className="muted small">—</span>;
  return (
    <span className="conf">
      <span className="conf-track"><span className={cx('conf-fill', v < 0.6 && 'low', v >= 0.9 && 'high')} style={{ width: `${v * 100}%` }} /></span>
      <span className="mono small">{f.pct(v)}</span>
    </span>
  );
}

function QuestionRow({ q, probs, def, mirror, mirrorBackend, requestId, editable, onChange }: {
  q: DecisionQuestion; probs?: [string, number][]; def?: { instructions?: string; criteria?: unknown };
  mirror?: { answer?: string; confidence?: number; agree: boolean }; mirrorBackend?: string;
  requestId?: string; editable?: boolean; onChange?: (q: DecisionQuestion) => void;
}) {
  const { t, f } = useI18n();
  const top = probs && probs.length ? Math.max(...probs.map((p) => p[1])) : 0;
  return (
    <li className="dq">
      <div className="dq-head">
        <code className="dq-id">{q.id}</code>
        <Badge>{q.type}</Badge>
        <span className="dq-answer">{q.answer ?? '—'}</span>
        {q.type === 'score' && q.score != null && <span className="muted small mono">{f.dec(q.score)}</span>}
        <span className="toolbar-spacer" />
        {q.correct != null && (q.correct ? <Badge tone="good" icon="check">{t('decision.correct')}</Badge> : <Badge tone="bad" icon="x">{t('decision.wrong')}</Badge>)}
        <ConfBar v={q.confidence} />
      </div>
      {def?.instructions && <div className="dq-instr">{def.instructions}</div>}
      {probs && probs.length > 0 && (
        <ul className="dq-probs">
          {probs.map(([label, p]) => (
            <li key={label} className={cx(p === top && 'is-top')}>
              <span className="dq-label">{label}</span>
              <span className="dq-bar"><span style={{ width: `${Math.max(0.5, p * 100)}%` }} /></span>
              <span className="mono small">{p < 0.001 ? '<0.1%' : f.pct(p, true)}</span>
            </li>
          ))}
        </ul>
      )}
      {mirror && (
        <div className={cx('dq-mirror', !mirror.agree && 'is-diff')}>
          <Icon name={mirror.agree ? 'check' : 'alert'} size={12} />
          {t('decision.mirrorSays', { backend: mirrorBackend ?? '', answer: mirror.answer ?? '—' })}
          {mirror.confidence != null && <span className="muted"> · {f.pct(mirror.confidence)}</span>}
        </div>
      )}
      {editable && requestId && <OutcomeControl q={q} requestId={requestId} onChange={onChange} />}
    </li>
  );
}

/** Records the ground truth for one question: the calibration feedback loop. */
function OutcomeControl({ q, requestId, onChange }: { q: DecisionQuestion; requestId: string; onChange?: (q: DecisionQuestion) => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const current = q.outcome == null ? ''
    : q.type === 'noul' ? (q.outcome ? 'yes' : 'no')
    : q.type === 'score' && typeof q.outcome === 'number' ? q.labels?.[q.outcome] ?? String(q.outcome)
    : String(q.outcome);
  const options: [string, string][] = q.type === 'noul' ? [['yes', t('common.yes')], ['no', t('common.no')]] : (q.labels ?? []).map((l) => [l, l]);

  const send = async (v: string) => {
    setBusy(true);
    setErr(null);
    const value: Outcome = v === '' ? null : q.type === 'noul' ? v === 'yes' : v;
    try {
      const item = await decisionsApi.outcome(requestId, q.id, value);
      const next = item.questions.find((x) => x.id === q.id);
      if (next) onChange?.(next);
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };

  if (!options.length) return null;
  return (
    <div className="dq-outcome">
      <span className="fact-label">{t('decision.outcome')}</span>
      <div className="dq-outcome-opts" role="radiogroup" aria-label={t('decision.outcomeFor', { question: q.id })}>
        {options.map(([v, label]) => (
          <button key={v} type="button" role="radio" aria-checked={current === v} disabled={busy}
            className={cx('chip', current === v && 'on', current === v && (q.correct ? 'is-good' : q.correct === false ? 'is-bad' : ''))}
            onClick={() => send(current === v ? '' : v)}>
            {label}
          </button>
        ))}
      </div>
      {current && <span className="muted small">{t('decision.outcomeHint')}</span>}
      {err && <span className="tone-bad small">{err}</span>}
    </div>
  );
}
