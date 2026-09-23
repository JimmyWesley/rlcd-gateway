// The request as the conversation it is, with every pruning decision shown
// in place: removed blocks stay where they were, marked, and open to show
// the original next to the marker the model received.
import { createContext, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { isFailed, protocolOf, type RequestDetail } from '../../lib/api';
import {
  conversationFromXray, isRecallTool, parseConversation, parseResponse, providerError, recallTarget,
  type Message, type Part,
} from '../../lib/conversation';
import type { BlockReport, PruneDetail } from '../../lib/pruneApi';
import { recordClient } from '../../lib/brands';
import { splitInjected, type Segment } from '../../lib/injected';
import { Badge, Button, Callout, Disclosure, cx } from '../../ui';
import { FeedbackButtons } from './Feedback';
import { useReasonLabel } from './PruneDiff';

/** Who injected context into user turns (the client's name), for the chips. */
const SourceCtx = createContext('');

type Status = 'removed' | 'candidate' | 'protected' | 'none';

type Row = {
  part: Part;
  role: Message['role'] | 'response';
  status: Status;
  report?: BlockReport;
  tokens: number;
  /** Key of the removed part a rlcd_recall call restored. */
  restores?: string;
  /** Key of the rlcd_recall call that restored this removed part. */
  recalledBy?: string;
  mistake?: boolean;
};

const statusOf = (r: BlockReport | undefined): Status =>
  !r ? 'none' : r.decision === 'drop' ? 'removed' : r.reason === 'protected' ? 'protected' : 'candidate';

export function ChatView({ d }: { d: RequestDetail }) {
  const { t, f } = useI18n();
  const protocol = protocolOf(d);
  const rep = d.stage_details?.prune as PruneDetail | undefined;
  const shadow = !!rep && (rep.mode === 'shadow' || !rep.applied);
  const listRef = useRef<HTMLDivElement>(null);
  const [focus, setFocus] = useState<string | null>(null);

  const model = useMemo(() => {
    const fromBody = parseConversation(protocol, d.request_body);
    const conv = fromBody ?? (d.xray ? conversationFromXray(d.xray.blocks) : null);
    const reports = new Map((rep?.blocks ?? []).map((b) => [b.ir_key, b]));
    const tokens = new Map((d.xray?.blocks ?? []).map((b) => [b.key, b.tokens]));
    const row = (part: Part, role: Row['role']): Row => {
      const report = reports.get(part.key);
      return { part, role, report, status: statusOf(report), tokens: report?.tokens ?? tokens.get(part.key) ?? 0, mistake: report?.recalled };
    };
    const system = (conv?.system ?? []).map((p) => row(p, 'system'));
    const messages = (conv?.messages ?? []).map((m) => ({ ...m, rows: m.parts.map((p) => row(p, m.role)) }));
    const all = [...system, ...messages.flatMap((m) => m.rows)];
    // Link each rlcd_recall call to the removed block it asked for.
    const removed = all.filter((r) => r.status === 'removed' && r.report);
    for (const r of all) {
      if (r.part.kind !== 'tool_use' || !isRecallTool(r.part.name)) continue;
      const target = recallTarget(r.part.args);
      if (!target) continue;
      const hit = removed.find((x) => x.report!.key === target.key || x.report!.key.startsWith(target.key) || x.part.callId === target.key);
      if (hit) {
        r.restores = hit.part.key;
        hit.recalledBy = r.part.key;
        hit.mistake = true;
      }
    }
    const failed = isFailed(d);
    const response = failed ? [] : parseResponse(protocol, d.response_body).map((p) => row(p, 'response'));
    const error = failed ? providerError(d.response_body, d.error) : null;
    return { conv, fromBody: !!fromBody, system, messages, response, error, all: [...all, ...response] };
  }, [d, protocol, rep]);

  const removedRows = model.all.filter((r) => r.status === 'removed');
  const removedTokens = removedRows.reduce((s, r) => s + r.tokens, 0);

  const jump = (key: string) => {
    setFocus(key);
    listRef.current?.querySelector<HTMLElement>(`[data-part="${CSS.escape(key)}"]`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  };
  const nextRemoval = (dir: 1 | -1) => {
    if (!removedRows.length) return;
    const i = removedRows.findIndex((r) => r.part.key === focus);
    const n = removedRows[(i + dir + removedRows.length) % removedRows.length];
    jump(n.part.key);
  };

  if (!model.conv) return <Callout tone="info">{t('chat.noBody')}</Callout>;

  return (
    <SourceCtx.Provider value={recordClient(d).name}>
    <div className="chat">
      <div className="chat-bar">
        {rep ? (
          <span className="small">
            {removedRows.length
              ? t(shadow ? 'chat.summary.shadow' : 'chat.summary.removed', { count: removedRows.length, tokens: f.tokens(removedTokens) })
              : t('chat.summary.none')}
          </span>
        ) : <span className="small muted">{t('chat.summary.noPrune')}</span>}
        {!model.fromBody && <Badge tone="warn">{t('chat.previewOnly')}</Badge>}
        <span className="toolbar-spacer" />
        {removedRows.length > 0 && (
          <div className="btn-row">
            <Button size="sm" variant="ghost" icon="up" onClick={() => nextRemoval(-1)}>{t('chat.prevRemoval')}</Button>
            <Button size="sm" variant="ghost" icon="down" onClick={() => nextRemoval(1)}>{t('chat.nextRemoval')}</Button>
          </div>
        )}
      </div>
      <div className="chat-body">
        <div className="chat-list" ref={listRef}>
          {(model.system.length > 0 || model.conv.tools.length > 0) && (
            <div className="chat-system">
              <Disclosure
                title={<span className="chat-role"><Icon name="settings" size={13} /> {t('chat.system')}</span>}
                meta={[model.system.length && t('chat.systemParts', { count: model.system.length, tokens: f.tokens(model.system.reduce((s, r) => s + r.tokens, 0)) }), model.conv.tools.length && t('chat.tools', { count: model.conv.tools.length })].filter(Boolean).join(' · ')}
              >
                {model.system.map((r) => <PartCard key={r.part.key} r={r} d={d} shadow={shadow} focus={focus} onJump={jump} />)}
                {model.conv.tools.length > 0 && (
                  <div className="chat-tools">
                    <span className="fact-label">{t('chat.toolsAvailable')}</span>
                    <div className="chips">{model.conv.tools.map((n, i) => <code key={i}>{n}</code>)}</div>
                  </div>
                )}
              </Disclosure>
            </div>
          )}

          {model.messages.map((m) => (
            <div key={m.msg} className={cx('chat-msg', `chat-${m.role}`)}>
              <div className="chat-who">{t(`chat.role.${m.role}`)}</div>
              <div className="chat-parts">
                {m.rows.map((r) => <PartCard key={r.part.key} r={r} d={d} shadow={shadow} focus={focus} onJump={jump} />)}
              </div>
            </div>
          ))}

          <div className="chat-divider"><span>{t('chat.thisTurn')}</span></div>

          {model.error ? (
            <div className="chat-msg chat-assistant">
              <div className="chat-who">{t('chat.role.assistant')}</div>
              <div className="chat-parts">
                <div className="chat-part chat-error" data-part="error" role="alert">
                  <div className="part-head">
                    <Icon name="alert" size={14} />
                    <strong>{t('chat.error.title', { status: d.status || t('traffic.err') })}</strong>
                    {model.error.provider && <Badge tone="bad">{model.error.provider}</Badge>}
                  </div>
                  <p className="chat-error-msg">{model.error.message}</p>
                  {d.error && d.error !== model.error.message && <p className="fine">{t('chat.error.outer', { message: d.error })}</p>}
                  {d.response_body && (
                    <Disclosure title={t('chat.error.raw')}>
                      <pre className="code code-sm">{prettyMaybe(d.response_body)}</pre>
                    </Disclosure>
                  )}
                </div>
              </div>
            </div>
          ) : model.response.length > 0 ? (
            <div className="chat-msg chat-assistant chat-response">
              <div className="chat-who">{t('chat.response')}</div>
              <div className="chat-parts">
                {model.response.map((r) => <PartCard key={r.part.key} r={r} d={d} shadow={shadow} focus={focus} onJump={jump} />)}
              </div>
            </div>
          ) : (
            <p className="muted small chat-empty">{d.response_body ? t('chat.noResponseParsed') : t('chat.noResponse')}</p>
          )}
        </div>
        <MiniMap rows={[...model.all, ...(model.error ? [{ part: { key: 'error', kind: 'other', text: '' }, role: 'response', status: 'none', tokens: 1 } as Row] : [])]} error={!!model.error} focus={focus} onJump={jump} />
      </div>
    </div>
    </SourceCtx.Provider>
  );
}

function prettyMaybe(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

function MiniMap({ rows, error, focus, onJump }: { rows: Row[]; error: boolean; focus: string | null; onJump: (k: string) => void }) {
  const { t } = useI18n();
  return (
    <nav className="minimap" aria-label={t('chat.minimap')}>
      {rows.map((r) => (
        <button
          key={r.part.key}
          type="button"
          tabIndex={-1}
          className={cx('mm-seg', `mm-${r.status}`, r.mistake && 'mm-mistake', r.part.key === 'error' && error && 'mm-error', focus === r.part.key && 'on', r.role === 'response' && 'mm-response')}
          style={{ flexGrow: Math.max(1, Math.sqrt(r.tokens || 1)) }}
          title={`${r.role} · ${r.part.name ?? r.part.kind}${r.tokens ? ` · ~${r.tokens}` : ''}`}
          onClick={() => onJump(r.part.key)}
        />
      ))}
    </nav>
  );
}

const LONG = 900;

function Clamp({ text, mono }: { text: string; mono?: boolean }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  if (!text) return <span className="muted small">{t('prune.empty')}</span>;
  const long = text.length > LONG;
  return (
    <div className={cx('clamp', mono && 'mono', long && !open && 'is-clamped')}>
      <div className="clamp-text">{long && !open ? text.slice(0, LONG) + '…' : text}</div>
      {long && <button type="button" className="linkish small" onClick={() => setOpen(!open)}>{open ? t('chat.showLess') : t('chat.showAll', { n: text.length })}</button>}
    </div>
  );
}

function PartBody({ p }: { p: Part }) {
  const { t } = useI18n();
  switch (p.kind) {
    case 'tool_use':
      return <pre className="code code-sm">{p.args || '{}'}</pre>;
    case 'image':
      return p.image
        ? <img className="chat-img" src={p.image} alt={t('chat.image')} loading="lazy" />
        : <div className="chat-img-ph"><Icon name="eye" size={16} /> {t('chat.imageOmitted')}</div>;
    case 'other':
      return <span className="muted small">{t('chat.unsupported', { type: p.label ?? p.kind })}</span>;
    default:
      return (
        <>
          {p.kind === 'text' ? <RichText text={p.text} /> : <Clamp text={p.text} mono={p.kind === 'tool_result'} />}
          {p.image && <img className="chat-img" src={p.image} alt={t('chat.image')} loading="lazy" />}
        </>
      );
  }
}

/** Text with injected context shown as chips, in order. */
function RichText({ text }: { text: string }) {
  const segs = useMemo(() => splitInjected(text), [text]);
  if (segs.length === 1 && segs[0].type === 'human') return <Clamp text={text} />;
  return (
    <div className="rich-text">
      {segs.map((s, i) => (s.type === 'human' ? <Clamp key={i} text={s.text} /> : <InjectedChip key={i} s={s} />))}
    </div>
  );
}

export function InjectedChip({ s }: { s: Extract<Segment, { type: 'injected' }> }) {
  const { t, f } = useI18n();
  const source = useContext(SourceCtx);
  const [open, setOpen] = useState(false);
  const kind = s.kind === 'other' ? s.title || s.tag : t(`inject.kind.${s.kind}`);
  const detail = s.kind === 'command' || s.kind === 'output' ? s.title : '';
  return (
    <div className={cx('inject', open && 'open')}>
      <button type="button" className="inject-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name={open ? 'chevronDown' : 'chevronRight'} size={13} />
        <Icon name="layers" size={13} />
        <span className="inject-title">
          {source ? t('inject.by', { source }) : t('inject.generic')}
          <strong> · {kind}</strong>
          {detail && <span className="mono"> {detail}</span>}
        </span>
        <span className="inject-size">{t('common.tokensShort', { n: f.tokens(s.tokens) })}</span>
      </button>
      {open && <div className="inject-body"><Clamp text={s.body} mono /></div>}
    </div>
  );
}

function PartCard({ r, d, shadow, focus, onJump }: { r: Row; d: RequestDetail; shadow: boolean; focus: string | null; onJump: (k: string) => void }) {
  const { t, f } = useI18n();
  const reason = useReasonLabel();
  const p = r.part;
  const rep = r.report;
  const [open, setOpen] = useState(false);
  const recall = p.kind === 'tool_use' && isRecallTool(p.name);

  if (p.kind === 'thinking') {
    return (
      <div className={cx('chat-part', 'chat-thinking', focus === p.key && 'is-focus')} data-part={p.key}>
        <Disclosure title={<span className="chat-role"><Icon name="zap" size={13} /> {t('chat.thinking')}</span>} meta={r.tokens ? t('common.tokensShort', { n: f.tokens(r.tokens) }) : p.label}>
          {p.text ? <Clamp text={p.text} /> : <span className="muted small">{t('chat.thinkingHidden')}</span>}
        </Disclosure>
      </div>
    );
  }

  const head: ReactNode[] = [];
  if (p.kind === 'tool_use') head.push(<span key="n" className="part-tool"><Icon name={recall ? 'recall' : 'terminal'} size={13} /><code>{p.name}</code></span>);
  if (p.kind === 'tool_result') head.push(<span key="n" className="part-tool"><Icon name="arrowRight" size={13} />{t('chat.resultOf')} <code>{p.name || p.callId}</code></span>);
  if (p.kind === 'image') head.push(<span key="n" className="part-tool"><Icon name="eye" size={13} />{t('chat.image')}</span>);
  if (p.isError) head.push(<Badge key="e" tone="bad">{t('common.error')}</Badge>);

  // Removed (or would be, in shadow mode): a hatched card, collapsed.
  if (r.status === 'removed' && rep) {
    return (
      <div className={cx('chat-part', 'is-removed', shadow && 'is-shadow', r.mistake && 'is-mistake', focus === p.key && 'is-focus', p.kind !== 'text' && 'is-tool')} data-part={p.key}>
        <button type="button" className="part-head part-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} size={14} />
          <span className="removed-label">
            <strong>{shadow ? t('chat.wouldRemove') : t('chat.removed')}</strong>
            {' · '}{t('common.tokensShort', { n: f.num(rep.tokens) })}
            {rep.score != null && <> · {t('chat.score', { score: f.score(rep.score) })}</>}
            {' · '}{t('chat.reason', { reason: reason(rep.reason) })}
          </span>
          {head}
          {rep.new && <Badge tone="accent">{t('prune.new')}</Badge>}
          {r.mistake && <Badge tone="bad" icon="recall">{t('chat.recalled')}</Badge>}
        </button>
        {!open && <div className="removed-peek">{(p.kind === 'tool_use' ? p.args : p.text)?.slice(0, 180)}</div>}
        {r.mistake && (
          <p className="chat-mistake">
            <Icon name="alert" size={13} /> {t('chat.mistake')}
            {r.recalledBy && <> <button type="button" className="linkish" onClick={() => onJump(r.recalledBy!)}>{t('chat.seeRecall')}</button></>}
          </p>
        )}
        {open && (
          <div className="removed-open">
            <div className="removed-col">
              <span className="fact-label">{t('chat.original')}</span>
              <PartBody p={p} />
            </div>
            <div className="removed-col">
              <span className="fact-label">{shadow ? t('chat.wouldReceive') : t('chat.received')}</span>
              {rep.marker ? <code className="marker-code">{rep.marker}</code> : <span className="muted small">—</span>}
              {rep.what && <p className="fine">{t('prune.shownAs', { what: rep.what })}</p>}
            </div>
          </div>
        )}
        <FeedbackButtons requestId={d.id} blockKey={rep.key} compact />
      </div>
    );
  }

  const segs = p.kind === 'text' ? splitInjected(p.text) : null;
  if (segs && segs.some((x) => x.type === 'injected')) {
    return (
      <div className={cx('chat-injected-group', focus === p.key && 'is-focus')} data-part={p.key}>
        {segs.map((x, i) => x.type === 'injected' ? <InjectedChip key={i} s={x} /> : (
          <div key={i} className="chat-part chat-text"><Clamp text={x.text} /></div>
        ))}
        {r.status === 'candidate' && rep && (
          <div className="inject-foot">
            <span className="part-score">{rep.score != null ? t('chat.kept', { score: f.score(rep.score) }) : reason(rep.reason)}</span>
            <FeedbackButtons requestId={d.id} blockKey={rep.key} compact />
          </div>
        )}
      </div>
    );
  }

  return (
    <div className={cx('chat-part', `chat-${p.kind}`, p.isError && 'is-error', focus === p.key && 'is-focus', recall && 'is-recall')} data-part={p.key}>
      {(head.length > 0 || r.status === 'protected' || r.status === 'candidate') && (
        <div className="part-head">
          {head}
          <span className="toolbar-spacer" />
          {r.status === 'protected' && (
            <span className="part-lock" title={t('chat.protected', { why: rep?.protected ? rep.protected : reason('protected') })}>
              <Icon name="lock" size={12} />
            </span>
          )}
          {r.status === 'candidate' && rep && (
            <span className="part-score" title={t('chat.keptHint', { reason: reason(rep.reason) })}>
              {rep.score != null ? t('chat.kept', { score: f.score(rep.score) }) : reason(rep.reason)}
            </span>
          )}
        </div>
      )}
      {recall && r.restores && (
        <p className="chat-recall-link"><Icon name="recall" size={13} /> {t('chat.restores')} <button type="button" className="linkish" onClick={() => onJump(r.restores!)}>{t('chat.jumpRemoved')}</button></p>
      )}
      <PartBody p={p} />
      {r.status === 'candidate' && rep && <FeedbackButtons requestId={d.id} blockKey={rep.key} compact />}
    </div>
  );
}
