import { useEffect, useState } from 'react';
import { api, fmt, PROTOCOL_LABEL, protocolOf, type RequestRecord } from '../api';
import { routerApi, type DryRun } from './types';

const RESULT_LABEL: Record<string, string> = {
  matched: 'matched',
  no_match: 'no match',
  disabled: 'disabled',
  skipped: 'skipped',
  fallback: 'fell through',
  error: 'error',
  not_reached: 'not reached',
};

export function DryRunPanel({ dirty }: { dirty: boolean }) {
  const [requests, setRequests] = useState<RequestRecord[]>([]);
  const [id, setId] = useState('');
  const [fresh, setFresh] = useState(true);
  const [res, setRes] = useState<DryRun | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    api.requests().then((rs) => {
      // Model calls of every protocol: /v1/messages, chat completions, responses.
      const msgs = rs.filter((r) => r.method === 'POST' && /\/(messages|chat\/completions|responses)$/.test(r.path)).slice(0, 100);
      setRequests(msgs);
      setId((cur) => cur || msgs[0]?.id || '');
    }).catch(() => {});
  useEffect(() => {
    load();
  }, []);

  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      setRes(await routerApi.dryRun(id, fresh));
    } catch (e) {
      setRes(null);
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel rt-section">
      <div className="rt-head">
        <h3>Dry run</h3>
        <span className="muted small">Replay a logged request through the saved rules — the same code that routes live traffic.</span>
      </div>
      <div className="rt-dry-controls">
        <select value={id} onChange={(e) => setId(e.target.value)} onFocus={load}>
          {requests.length === 0 && <option value="">no logged requests yet</option>}
          {requests.map((r) => (
            <option key={r.id} value={r.id}>
              {fmt.time(r.time)} · {PROTOCOL_LABEL[protocolOf(r)]} · {r.client_model ?? r.model ?? '?'} · ~{fmt.n(r.est_tokens)} tok · {r.route}
            </option>
          ))}
        </select>
        <label className="rt-check">
          <input type="checkbox" checked={fresh} onChange={(e) => setFresh(e.target.checked)} />
          <span>As a new conversation <span className="muted small">(ignore its sticky pin)</span></span>
        </label>
        <button className="primary" onClick={run} disabled={!id || busy}>{busy ? 'Running…' : 'Run'}</button>
      </div>
      {dirty && <p className="note small">You have unsaved rule changes; the dry run uses the saved rules.</p>}
      {err && <div className="probe bad">{err}</div>}
      {res && <DryRunResult res={res} />}
    </section>
  );
}

function DryRunResult({ res }: { res: DryRun }) {
  const f = res.facts;
  const pinText: Record<DryRun['sticky_action'], string> = {
    create: 'would pin the conversation to this decision',
    replace: 'would move the conversation’s pin',
    use: 'served by the existing pin',
    none: res.facts.background ? 'background request: no pin' : 'no pin',
  };
  return (
    <div className="rt-dry">
      <div className={`probe ${res.ok ? 'ok' : ''}`}>
        <strong>→ {res.effective_route}</strong>
        {!res.ok && <span className="muted"> (active route)</span>}
        <div>{res.decision.reason}</div>
        {res.decision.model && <div className="muted small">upstream model {res.decision.model}</div>}
        {res.decision.error && <div className="bad">{res.decision.error}</div>}
        <div className="muted small">
          {pinText[res.sticky_action]}
          {res.logged.route && (
            <>
              {' · '}logged: {res.logged.route}
              {res.logged.route_reason ? ` — ${res.logged.route_reason}` : ''}
            </>
          )}
        </div>
        {res.notes?.map((n) => <div key={n} className="muted small">{n}</div>)}
      </div>

      <div className="rt-facts">
        <Fact k="model" v={f.model || '—'} />
        <Fact k="context" v={`~${fmt.n(f.context_tokens)} tok`} />
        <Fact k="messages" v={String(f.messages)} />
        <Fact k="max_tokens" v={String(f.max_tokens)} />
        <Fact k="tools" v={String(f.tools)} />
        <Fact k="images" v={f.has_images ? 'yes' : 'no'} />
        <Fact k="thinking" v={f.has_thinking ? `${f.thinking_param || 'blocks'}${f.thinking_blocks ? ` · ${f.thinking_blocks} blocks` : ''}` : 'no'} />
        <Fact k="background" v={f.background ? `yes — ${f.background_signals?.join(', ')}` : 'no'} />
        <Fact k="conversation" v={f.conversation_id} mono />
      </div>

      {res.trace.length === 0 ? (
        <p className="muted">No rules.</p>
      ) : (
        <table className="rt-trace">
          <thead>
            <tr>
              <th>#</th>
              <th>Rule</th>
              <th>Result</th>
              <th>Why</th>
            </tr>
          </thead>
          <tbody>
            {res.trace.map((t) => (
              <tr key={t.index} className={`rt-res-${t.result}`}>
                <td className="mono">{t.index + 1}</td>
                <td>
                  {t.name}
                  <div className="muted small">→ {t.kind === 'auto' ? `auto${t.route ? `: ${t.route}` : ''}` : t.route}</div>
                </td>
                <td><span className="rt-badge">{RESULT_LABEL[t.result] ?? t.result}</span></td>
                <td className="small">
                  {t.reason && !redundant(t) && <div>{t.reason}</div>}
                  {t.checks?.map((c, i) => (
                    <div key={i} className={c.ok ? 'rt-ok' : 'bad'}>
                      {c.ok ? '✓' : '✗'} {c.condition} <span className="muted">({c.detail})</span>
                    </div>
                  ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// A plain match/no-match reason only repeats the checks listed under it.
function redundant(t: DryRun['trace'][number]): boolean {
  if (!t.checks?.length) return false;
  return t.result === 'no_match' || (t.result === 'matched' && t.kind === 'match');
}

function Fact({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="fact">
      <span className="label">{k}</span>
      <span className={`value ${mono ? 'mono small' : ''}`}>{v}</span>
    </div>
  );
}
