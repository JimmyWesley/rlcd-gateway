// The inspector's first tab for a System One call: a decision card instead
// of chat bubbles. The gateway's own decisions link to the turn they served.
import { useMemo } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { isFailed, type RequestDetail } from '../../lib/api';
import { providerError } from '../../lib/conversation';
import { requestQuestions, requestState, responseProbabilities } from '../../lib/decisionsApi';
import { href } from '../../lib/router';
import { Badge, Callout } from '../../ui';
import { DecisionCard } from './DecisionCard';

export function DecisionView({ d }: { d: RequestDetail }) {
  const { t, f } = useI18n();
  const dec = d.decisions;
  const probs = useMemo(() => responseProbabilities(d.response_body), [d.response_body]);
  const defs = useMemo(() => requestQuestions(d.request_body), [d.request_body]);
  const state = useMemo(() => requestState(d.request_body), [d.request_body]);
  const err = isFailed(d) ? providerError(d.response_body, d.error) : null;
  return (
    <div className="chat decision-view">
      <div className="chat-bar">
        <span className="small">
          {dec ? t('decision.summary', { count: dec.questions.length, backend: dec.backend }) : t('decision.noSummary')}
        </span>
        {dec?.forward_ms != null && <Badge tone="accent">{t('decision.forward', { ms: f.ms(dec.forward_ms) })}</Badge>}
        {dec?.think && <Badge>{t('decision.think')}</Badge>}
        {dec && dec.source !== 'client' && <Badge tone="info" icon="cpu">{t(`decision.source.${dec.source}`)}</Badge>}
      </div>
      {dec?.parent_id && (
        <Callout tone="info" icon="flow" title={t('decision.internal')}>
          {t('decision.internalBody', { source: t(`decision.source.${dec.source}`) })}{' '}
          <a href={href(`traffic/${dec.parent_id}`)}><Icon name="arrowRight" size={12} /> {t('decision.openParent')}</a>
        </Callout>
      )}
      {err && <Callout tone="bad" title={t('chat.error.title', { status: d.status || t('traffic.err') })}>{err.message}</Callout>}
      {dec && <DecisionCard questions={dec.questions} probs={probs} defs={defs} state={state} />}
      {!dec && !err && <p className="muted small">{t('decision.noSummary')}</p>}
      {dec && <p className="fine"><a href={href('decisions/audit', { question: '' })}>{t('decision.seeAudit')}</a></p>}
    </div>
  );
}
