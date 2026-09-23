// "What the gateway did": every upstream call made for one request, the
// failure each one met, and what the gateway changed before the next.
import { useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import { Icon, type IconName } from '../../icons/Icon';
import type { RequestRecord } from '../../lib/api';
import { asProvider, PROVIDER_NAMES } from '../../lib/brands';
import {
  attemptOk, hadTrouble, isAction, isClass, isReason, isSource,
  type Attempt, type MaxTokensChange,
} from '../../lib/resilienceApi';
import { Badge, cx } from '../../ui';

type T = ReturnType<typeof useI18n>['t'];
type F = ReturnType<typeof useI18n>['f'];

export const classLabel = (t: T, c: string | undefined) => (isClass(c) ? t(`res.class.${c}`) : c ?? '');
export const sourceLabel = (t: T, s: string | undefined) => (isSource(s) ? t(`res.source.${s.replace('override:', 'override_') as 'builtin'}`) : s ?? '');

/** One sentence for a max_tokens rewrite: what it was, what it became, and why. */
export function guardSentence(t: T, f: F, c: MaxTokensChange): string {
  const reason = isReason(c.reason) ? c.reason : 'provider_error';
  const base = t(`res.guard.${reason}`, { field: c.field, from: c.from != null ? f.num(c.from) : '—', to: f.num(c.to) });
  const src = c.limit_source ? t('res.guard.source', { source: sourceLabel(t, c.limit_source) }) : '';
  const thinking = c.thinking_budget_from != null && c.thinking_budget_to != null
    ? ' ' + t('res.guard.thinking', { from: f.num(c.thinking_budget_from), to: f.num(c.thinking_budget_to) }) : '';
  return `${base}${src ? ` ${src}` : ''}${thinking}`;
}

function limitsTitle(t: T, f: F, c: MaxTokensChange): string {
  return [
    c.context_window && t('res.limits.window', { n: f.num(c.context_window) }),
    c.max_output_tokens && t('res.limits.maxOut', { n: f.num(c.max_output_tokens) }),
    c.est_input_tokens && t('res.limits.input', { n: f.num(c.est_input_tokens) }),
  ].filter(Boolean).join(' · ');
}

/** The subtle one-liner for a preventive guard rewrite on an otherwise clean call. */
export function GuardLine({ c }: { c: MaxTokensChange }) {
  const { t, f } = useI18n();
  return (
    <p className="guard-line" title={limitsTitle(t, f, c)}>
      <Icon name="shield" size={13} /> {guardSentence(t, f, c)}
    </p>
  );
}

function Where({ a }: { a: Attempt }) {
  const prov = asProvider(a.provider);
  return (
    <span className="att-where">
      <BrandIcon id={prov} label={prov ? PROVIDER_NAMES[prov] : a.provider ?? a.route} size={13} />
      <strong>{a.route}</strong>
      {a.upstream_provider && <span className="muted">/{a.upstream_provider}</span>}
      {a.model && <span className="mono muted small">· {a.model}</span>}
    </span>
  );
}

const ACTION_ICON: Record<string, IconName> = {
  clamp_max_tokens: 'shield', emergency_prune: 'savings', ignore_provider: 'x', backoff: 'clock', fallback: 'arrowRight', none: 'alert', give_up: 'alert',
};

/** What happened between attempt `a` and the next one, from the next one's changes. */
function ActionLine({ a, next, onEmergency }: { a: Attempt; next?: Attempt; onEmergency?: () => void }) {
  const { t, f } = useI18n();
  if (!a.action) return null;
  const act = isAction(a.action) ? a.action : 'none';
  const ch = next?.changes;
  let text: React.ReactNode;
  switch (act) {
    case 'clamp_max_tokens':
      text = ch?.max_tokens ? guardSentence(t, f, ch.max_tokens) : t('res.action.clamp_max_tokens');
      break;
    case 'emergency_prune': {
      const e = ch?.emergency_prune;
      text = (
        <>
          {e ? t('res.emergency', { count: e.dropped, saved: f.tokens(e.saved_tokens), before: f.tokens(e.tokens_before), after: f.tokens(e.tokens_after) }) : t('res.action.emergency_prune')}
          {ch?.cache_invalidated && <Badge tone="warn">{t('res.cacheInvalidated')}</Badge>}
          {onEmergency && <button type="button" className="linkish small" onClick={onEmergency}>{t('res.seeEmergency')}</button>}
        </>
      );
      break;
    }
    case 'ignore_provider':
      text = t('res.ignored', { providers: (ch?.ignored_providers ?? [a.upstream_provider ?? '']).join(', ') });
      break;
    case 'backoff':
      text = t('res.waited', { ms: f.ms(a.wait_ms ?? 0) });
      break;
    case 'fallback':
      text = next ? t('res.fellBack', { route: next.route, model: next.model ?? '' }) : t('res.action.fallback');
      break;
    default:
      text = t(`res.action.${act}`);
  }
  return (
    <div className={cx('att-action', (act === 'none' || act === 'give_up') && 'is-end')}>
      <Icon name={ACTION_ICON[act] ?? 'arrowRight'} size={13} />
      <span className="att-action-text">
        {text}
        {a.wait_ms != null && a.wait_ms > 0 && act !== 'backoff' && <span className="muted"> · {t('res.waited', { ms: f.ms(a.wait_ms) })}</span>}
      </span>
      {/* The detail repeats what the change says, unless nothing more could be done. */}
      {a.action_detail && (act === 'none' || act === 'give_up' || !ch) && <span className="att-detail">{a.action_detail}</span>}
    </div>
  );
}

function Message({ text }: { text: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const long = text.length > 220;
  return (
    <blockquote className="att-msg">
      {long && !open ? `${text.slice(0, 220)}…` : text}
      {long && <button type="button" className="linkish small" onClick={() => setOpen(!open)}>{open ? t('chat.showLess') : t('chat.showAll', { n: text.length })}</button>}
    </blockquote>
  );
}

/**
 * The full story when the gateway did more than one clean call; the guard's
 * one-liner when it only rewrote max_tokens before sending.
 */
export function Resilience({ r, onEmergency }: { r: RequestRecord; onEmergency?: () => void }) {
  const { t, f } = useI18n();
  const attempts = r.attempts ?? [];
  if (!hadTrouble(r)) {
    const g = r.max_tokens_guard ?? attempts[0]?.changes?.max_tokens;
    return g ? <GuardLine c={g} /> : null;
  }
  const last = attempts[attempts.length - 1];
  const ok = !!last && attemptOk(last) && r.status < 400;
  const verdict = ok
    ? r.fallback_route
      ? t('res.verdict.fallback', { route: r.fallback_route, primary: r.primary_route ?? attempts[0]?.route ?? '', count: attempts.length })
      : attempts.length > 1 ? t('res.verdict.recovered', { count: attempts.length }) : t('res.verdict.ok')
    : t('res.verdict.failed', { cls: classLabel(t, r.error_class ?? last?.class), count: attempts.length });
  // Attempt 1 carries the preventive guard (if any) as its own changes.
  const firstGuard = attempts[0]?.changes?.max_tokens;
  return (
    <section className={cx('resilience', ok ? 'is-ok' : 'is-failed')} aria-label={t('res.title')}>
      <header className="res-head">
        <span className="fact-label">{t('res.title')}</span>
        <Badge tone={ok ? 'good' : 'bad'} icon={ok ? 'check' : 'alert'}>{verdict}</Badge>
      </header>
      {firstGuard && firstGuard.reason !== 'provider_error' && <GuardLine c={firstGuard} />}
      <ol className="att-list">
        {attempts.map((a, i) => {
          const good = attemptOk(a);
          return (
            <li key={a.n} className={cx('att', good ? 'is-ok' : 'is-bad')}>
              <span className="att-dot" aria-hidden>{a.n}</span>
              <div className="att-body">
                <div className="att-row">
                  <span className="att-n">{t('res.attempt', { n: a.n })}</span>
                  <Where a={a} />
                  <Badge tone={good ? 'good' : 'bad'}>{a.status || t('res.noResponse')}</Badge>
                  {a.class && <Badge tone="warn">{classLabel(t, a.class)}</Badge>}
                  <span className="mono small muted">{f.ms(a.duration_ms)}</span>
                </div>
                {a.message && <Message text={a.message} />}
                <ActionLine a={a} next={attempts[i + 1]} onEmergency={onEmergency} />
              </div>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
