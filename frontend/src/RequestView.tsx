import { useEffect, useMemo, useState } from 'react';
import { api, fmt, type Block, type RequestDetail } from './api';

// Order is the order the model reads the context in.
export const KINDS = ['system', 'tool', 'text', 'thinking', 'tool_use', 'tool_result', 'image', 'other'] as const;

const AUTH_LABEL: Record<string, string> = {
  oauth: 'subscription (OAuth)',
  'api-key': 'API key',
  bearer: 'bearer token',
  none: 'no credentials',
};

export function RequestView({ id }: { id: string }) {
  const [d, setD] = useState<RequestDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setD(null);
    setErr(null);
    api.request(id).then(setD).catch((e) => setErr(String(e)));
  }, [id]);

  if (err) return <section className="panel detail"><p className="bad">{err}</p></section>;
  if (!d) return <section className="panel detail"><p className="muted">Loading…</p></section>;

  const u = d.usage;
  return (
    <section className="panel detail">
      <div className="facts">
        <Fact label="Route" value={d.route} sub={d.upstream} />
        <Fact label="Model" value={d.model ?? '—'} sub={d.client_model && d.client_model !== d.model ? `agent asked for ${d.client_model}` : undefined} />
        <Fact label="Auth from agent" value={AUTH_LABEL[d.auth_mode] ?? d.auth_mode} />
        <Fact label="Latency" value={fmt.ms(d.duration_ms)} sub={`first byte ${fmt.ms(d.ttfb_ms)}`} />
        <Fact label="Status" value={String(d.status || 'ERR')} bad={d.status >= 400 || !!d.error} sub={d.error} />
      </div>

      {u && (
        <div className="usage">
          <Fact label="Fresh input" value={fmt.n(u.input_tokens)} />
          <Fact label="Cache read" value={fmt.n(u.cache_read_input_tokens)} />
          <Fact label="Cache write" value={fmt.n(u.cache_creation_input_tokens)} />
          <Fact label="Output" value={fmt.n(u.output_tokens)} />
        </div>
      )}
      {!!d.stripped_thinking && (
        <p className="note">{d.stripped_thinking} signed thinking block(s) removed: they are only valid on the model that wrote them.</p>
      )}

      {d.xray && <XRay blocks={d.xray.blocks} total={d.xray.tokens} messages={d.xray.messages} />}

      <details>
        <summary>Request headers <span className="muted">(secrets masked)</span></summary>
        <table className="kv">
          <tbody>
            {Object.entries(d.request_headers).sort().map(([k, v]) => (
              <tr key={k}><td className="mono">{k}</td><td className="mono clip">{v}</td></tr>
            ))}
          </tbody>
        </table>
      </details>
      {d.request_body && <Raw title="Request body" body={d.request_body} />}
      {d.response_body && <Raw title="Response body" body={d.response_body} />}
    </section>
  );
}

function XRay({ blocks, total, messages }: { blocks: Block[]; total: number; messages: number }) {
  const [kind, setKind] = useState<string | null>(null);
  const [sort, setSort] = useState<'order' | 'size'>('size');

  const byKind = useMemo(() => {
    const m: Record<string, { tokens: number; count: number }> = {};
    for (const b of blocks) {
      m[b.kind] ??= { tokens: 0, count: 0 };
      m[b.kind].tokens += b.tokens;
      m[b.kind].count++;
    }
    return m;
  }, [blocks]);

  const rows = useMemo(() => {
    const r = kind ? blocks.filter((b) => b.kind === kind) : [...blocks];
    if (sort === 'size') r.sort((a, b) => b.tokens - a.tokens);
    return r.slice(0, 200);
  }, [blocks, kind, sort]);

  return (
    <div className="xray">
      <div className="xray-head">
        <h3>Context X-ray</h3>
        <span className="muted">
          ~{fmt.n(total)} tokens (estimate) · {blocks.length} blocks · {messages} messages
        </span>
      </div>
      <div className="bar" role="img" aria-label="Context tokens by block kind">
        {KINDS.filter((k) => byKind[k]).map((k) => (
          <span
            key={k}
            className={`seg k-${k} ${kind && kind !== k ? 'dim' : ''}`}
            style={{ flexGrow: byKind[k].tokens || 1 }}
            title={`${k}: ~${fmt.n(byKind[k].tokens)} tokens`}
            onClick={() => setKind(kind === k ? null : k)}
          />
        ))}
      </div>
      <div className="legend">
        {KINDS.filter((k) => byKind[k]).map((k) => (
          <button key={k} className={kind === k ? 'active' : ''} onClick={() => setKind(kind === k ? null : k)}>
            <i className={`sw k-${k}`} />
            {k} <span className="muted">{byKind[k].count} · ~{fmt.n(byKind[k].tokens)}</span>
          </button>
        ))}
        <span className="spacer" />
        <button onClick={() => setSort(sort === 'size' ? 'order' : 'size')}>
          sort: {sort === 'size' ? 'largest first' : 'context order'}
        </button>
      </div>
      <div className="scroll-x">
      <table className="blocks">
        <thead>
          <tr><th>Key</th><th>Kind</th><th>Role</th><th className="num">~Tokens</th><th>Preview</th></tr>
        </thead>
        <tbody>
          {rows.map((b) => (
            <tr key={b.key} className={b.is_error ? 'err' : ''}>
              <td className="mono">{b.key}{b.cached && <span className="tag" title="cache_control breakpoint">cache</span>}</td>
              <td><i className={`sw k-${b.kind}`} />{b.kind}{b.name && <span className="muted"> {b.name}</span>}</td>
              <td className="muted">{b.role ?? ''}</td>
              <td className="num mono">{fmt.n(b.tokens)}</td>
              <td className="clip preview">{b.preview}</td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
    </div>
  );
}

function Fact({ label, value, sub, bad }: { label: string; value: string; sub?: string; bad?: boolean }) {
  return (
    <div className="fact">
      <span className="label">{label}</span>
      <span className={`value ${bad ? 'bad' : ''}`}>{value}</span>
      {sub && <span className="hint clip" title={sub}>{sub}</span>}
    </div>
  );
}

function Raw({ title, body }: { title: string; body: string }) {
  const pretty = useMemo(() => {
    try {
      return JSON.stringify(JSON.parse(body), null, 2);
    } catch {
      return body;
    }
  }, [body]);
  return (
    <details>
      <summary>{title} <span className="muted">{fmt.n(body.length)} chars</span></summary>
      <pre className="raw">{pretty}</pre>
    </details>
  );
}
