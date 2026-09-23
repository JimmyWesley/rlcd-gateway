// "Should have kept / dropped" for one block: how a person tells the pruner
// it got a decision wrong. Saved as a replay case (Savings > Feedback cases).
import { useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { pruneApi, type Verdict } from '../../lib/pruneApi';
import { Button, cx } from '../../ui';

export function FeedbackButtons({ requestId, blockKey, withNote, compact }: { requestId: string; blockKey: string; withNote?: boolean; compact?: boolean }) {
  const { t } = useI18n();
  const [sent, setSent] = useState<Verdict | null>(null);
  const [busy, setBusy] = useState<Verdict | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState('');

  const send = async (verdict: Verdict) => {
    setErr(null);
    setBusy(verdict);
    try {
      await pruneApi.addFeedback({ request_id: requestId, key: blockKey, verdict, note: note || undefined });
      setSent(verdict);
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className={cx('fb', compact && 'fb-compact')} onClick={(e) => e.stopPropagation()}>
      {withNote && <input value={note} placeholder={t('prune.feedback.note')} aria-label={t('prune.feedback.note')} onChange={(e) => setNote(e.target.value)} />}
      <Button size="sm" variant={compact ? 'ghost' : 'secondary'} icon="thumbUp" loading={busy === 'should_keep'} className={cx(sent === 'should_keep' && 'is-on')} onClick={() => send('should_keep')}>
        {t('prune.feedback.keep')}
      </Button>
      <Button size="sm" variant={compact ? 'ghost' : 'secondary'} icon="thumbDown" loading={busy === 'should_drop'} className={cx(sent === 'should_drop' && 'is-on')} onClick={() => send('should_drop')}>
        {t('prune.feedback.drop')}
      </Button>
      {sent && <span className="tone-good small" role="status"><Icon name="check" size={12} /> {t('prune.feedback.saved')}</span>}
      {err && <span className="tone-bad small" role="alert">{err}</span>}
    </div>
  );
}
