import { useCallback, useEffect, useState } from 'react';
import { api, call, fmt, type GatewayConfig } from '../api';

// Types mirror gateway/internal/recall (Settings, Event, Stats).
type RecallSettings = { enabled: boolean; max_bytes: number; mcp_path: string; tool: string };

type RecallEvent = {
  time: string;
  conversation_id?: string;
  req: string;
  key: string;
  block_key?: string;
  tool_use_id?: string;
  tool?: string;
  kind?: string;
  tokens: number;
  bytes: number;
  truncated?: boolean;
  ok: boolean;
  error?: string;
  message?: string;
  client?: string;
};

type RecallStats = {
  total: number;
  ok: number;
  errors: number;
  by_error: Record<string, number>;
  tokens_restored: number;
  conversations: number;
  last?: string;
  top_keys: { key: string; tool?: string; count: number }[];
  top_tools: { tool: string; count: number; tokens: number }[];
};

const recallApi = {
  settings: () => call<RecallSettings>('/api/recall/settings'),
  setSettings: (s: Partial<Pick<RecallSettings, 'enabled' | 'max_bytes'>>) =>
    call<RecallSettings>('/api/recall/settings', { method: 'PUT', body: JSON.stringify(s) }),
  stats: () => call<RecallStats>('/api/recall/stats'),
  events: (limit: number) => call<RecallEvent[]>(`/api/recall/events?limit=${limit}`),
};

const SERVER_NAME = 'rlcd-gateway';

// The address Claude Code should call: the gateway's own listen address,
// with a wildcard host replaced by loopback.
function mcpURL(config: GatewayConfig | null, path: string): string {
  const listen = config?.listen || window.location.host;
  const i = listen.lastIndexOf(':');
  const host = i >= 0 ? listen.slice(0, i) : listen;
  const port = i >= 0 ? listen.slice(i) : '';
  const h = host === '' || host === '0.0.0.0' || host === '[::]' ? '127.0.0.1' : host;
  return `http://${h}${port}${path}`;
}

// Recall (MCP) status and install instructions, shown in the Agents tab (F4).
export function RecallCard() {
  const [config, setConfig] = useState<GatewayConfig | null>(null);
  const [settings, setSettings] = useState<RecallSettings | null>(null);
  const [stats, setStats] = useState<RecallStats | null>(null);
  const [events, setEvents] = useState<RecallEvent[]>([]);
  const [scope, setScope] = useState<'local' | 'user'>('user');
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    recallApi.stats().then(setStats).catch(() => {});
    recallApi.events(20).then(setEvents).catch(() => {});
  }, []);

  useEffect(() => {
    api.config().then(setConfig).catch(() => {});
    recallApi.settings().then(setSettings).catch((e) => setError(String(e)));
    refresh();
    const t = window.setInterval(refresh, 5000);
    return () => window.clearInterval(t);
  }, [refresh]);

  const url = mcpURL(config, settings?.mcp_path ?? '/mcp');
  const command = `claude mcp add --transport http${scope === 'user' ? ' --scope user' : ''} ${SERVER_NAME} ${url}`;
  const toolName = `mcp__${SERVER_NAME}__${settings?.tool ?? 'rlcd_recall'}`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setError('Copy failed: select the command and copy it by hand.');
    }
  };

  const toggle = async () => {
    if (!settings) return;
    try {
      setSettings(await recallApi.setSettings({ enabled: !settings.enabled }));
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <section className="recall">
      <div className="recall-head">
        <h3>Recall</h3>
        {settings && (
          <span className={`recall-state ${settings.enabled ? 'on' : 'off'}`}>{settings.enabled ? 'enabled' : 'disabled'}</span>
        )}
        <span className="spacer" />
        {settings && <button onClick={toggle}>{settings.enabled ? 'Disable' : 'Enable'}</button>}
      </div>
      <p className="muted">
        When pruning omits a block, the model sees a marker with a key and a request id. Recall is an MCP tool the model
        calls with those two values to get the original content back.
        {settings && <> Recalls larger than {Math.round(settings.max_bytes / 1000)} KB are truncated.</>}
      </p>

      <div className="recall-install">
        <div className="recall-scope">
          <span className="label">Register it with Claude Code</span>
          <span className="spacer" />
          <button className={scope === 'user' ? 'active' : ''} onClick={() => setScope('user')}>All projects</button>
          <button className={scope === 'local' ? 'active' : ''} onClick={() => setScope('local')}>This project</button>
        </div>
        <div className="recall-cmd">
          <code className="mono">{command}</code>
          <button onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
        </div>
        <p className="muted small">
          The model then sees the tool as <code className="mono">{toolName}</code>. Undo with{' '}
          <code className="mono">claude mcp remove{scope === 'user' ? ' --scope user' : ''} {SERVER_NAME}</code>. Recall needs
          request bodies in the log (<code className="mono">log_bodies</code>
          {config ? (config.log_bodies ? ' is on' : ' is off — recalls will fail') : ''}).
        </p>
      </div>

      {error && <p className="bad small">{error}</p>}

      <div className="facts recall-facts">
        <Fact label="Recalls" value={fmt.n(stats?.total)} hint={stats?.errors ? `${stats.errors} failed` : undefined} />
        <Fact label="Tokens restored" value={fmt.n(stats?.tokens_restored)} hint="estimated" />
        <Fact label="Conversations" value={fmt.n(stats?.conversations)} />
        <Fact label="Last recall" value={stats?.last ? fmt.time(stats.last) : '—'} />
      </div>

      {stats && stats.total > 0 && (
        <div className="recall-tops">
          <div>
            <span className="label">Most recalled tools</span>
            <table className="small">
              <tbody>
                {stats.top_tools.map((t) => (
                  <tr key={t.tool}>
                    <td className="mono">{t.tool}</td>
                    <td className="num">{t.count}×</td>
                    <td className="num muted">{fmt.n(t.tokens)} tok</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div>
            <span className="label">Most recalled blocks</span>
            <table className="small">
              <tbody>
                {stats.top_keys.map((k) => (
                  <tr key={k.key}>
                    <td className="mono clip" title={k.key}>{k.key}</td>
                    <td className="muted">{k.tool ?? ''}</td>
                    <td className="num">{k.count}×</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {Object.keys(stats.by_error).length > 0 && (
            <div>
              <span className="label">Failures</span>
              <table className="small">
                <tbody>
                  {Object.entries(stats.by_error).map(([code, n]) => (
                    <tr key={code}>
                      <td className="mono">{code}</td>
                      <td className="num">{n}×</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      <details open={events.length > 0}>
        <summary>Recent recalls ({events.length})</summary>
        {events.length === 0 ? (
          <p className="muted small">
            None yet. Each recall means pruning dropped something the model needed; they are logged as feedback for the
            pruner.
          </p>
        ) : (
          <div className="scroll-x">
            <table className="small recall-events">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Result</th>
                  <th>Block</th>
                  <th>Tool</th>
                  <th className="num">Tokens</th>
                  <th>Request</th>
                </tr>
              </thead>
              <tbody>
                {events.map((e, i) => (
                  <tr key={`${e.time}-${i}`} className={e.ok ? '' : 'err'} title={e.message}>
                    <td className="mono">{fmt.time(e.time)}</td>
                    <td className={e.ok ? '' : 'bad'}>
                      {e.ok ? 'restored' : e.error}
                      {e.truncated && <span className="tag">truncated</span>}
                    </td>
                    <td className="mono clip" title={e.tool_use_id || e.key}>{e.block_key ?? e.key}</td>
                    <td>{e.tool ?? (e.kind ? <span className="muted">{e.kind}</span> : '')}</td>
                    <td className="num">{e.ok ? fmt.n(e.tokens) : '—'}</td>
                    <td className="mono clip" title={e.req}>{e.req}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </details>
    </section>
  );
}

function Fact({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="fact">
      <span className="label">{label}</span>
      <span className="value">{value}</span>
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}
