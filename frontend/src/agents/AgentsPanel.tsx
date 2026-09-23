import { useCallback, useEffect, useState } from 'react';
import { RecallCard } from '../recall/RecallCard';
import { agentsApi, type AgentSource, type AgentStatus } from './agentsApi';

// Which agents point at the gateway, and how to set each one up (F3).
// Claude Code comes first and gets the full picture: effective settings
// source, overrides and auth mode. RecallCard belongs to the recall work
// (F4); keep it rendered here.
export function AgentsPanel() {
  const [agents, setAgents] = useState<AgentStatus[] | null>(null);
  const [project, setProject] = useState('');
  const [checked, setChecked] = useState('');
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((p: string) => {
    agentsApi
      .list(p)
      .then((a) => {
        setAgents(a);
        setChecked(p);
        setError(null);
      })
      .catch((e) => setError(String(e)));
  }, []);

  useEffect(() => load(''), [load]);

  return (
    <main className="panel agents">
      <h2>Agents</h2>
      <p className="muted">
        Each agent is pointed at the gateway through its base URL only. Its login stays as it is, so a subscription is
        still what serves the turn. Try the one-off command first; set up makes it permanent and can be undone.
      </p>

      <form
        className="agents-project"
        onSubmit={(e) => {
          e.preventDefault();
          load(project.trim());
        }}
      >
        <label>
          Project folder <span className="muted">(optional: its own settings files can override yours)</span>
          <input value={project} placeholder="/path/to/your/project" onChange={(e) => setProject(e.target.value)} />
        </label>
        <button type="submit">Check</button>
      </form>

      {error && <div className="banner" onClick={() => setError(null)}>{error}</div>}
      {!agents && !error && <p className="muted">Loading…</p>}

      <div className="agents-list">
        {agents?.map((a) => (
          <AgentCard key={a.name} agent={a} project={checked} onUpdated={setAgents} />
        ))}
      </div>

      <div id="recall">
        <RecallCard />
      </div>
    </main>
  );
}

type CardProps = { agent: AgentStatus; project: string; onUpdated: (a: AgentStatus[]) => void };

function AgentCard({ agent: a, project, onUpdated }: CardProps) {
  const [confirm, setConfirm] = useState<'setup' | 'undo' | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const primary = a.name === 'claude';

  const run = async (action: 'setup' | 'undo') => {
    setBusy(true);
    setResult(null);
    try {
      const r = await agentsApi.run(a.name, action, project);
      onUpdated(r.agents);
      setResult({ ok: r.ok, text: r.ok ? (r.message ?? 'Done.') : (r.error ?? 'Failed.') });
    } catch (e) {
      setResult({ ok: false, text: String(e) });
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  };

  const state = a.effective
    ? { cls: 'good', text: 'Using the gateway' }
    : a.configured
      ? { cls: 'warn', text: 'Set up, but overridden' }
      : { cls: 'off', text: 'Not using the gateway' };

  return (
    <section className={`agents-card ${primary ? 'primary' : ''}`}>
      <header className="agents-head">
        <h3>{a.title}</h3>
        <span className={`agents-badge ${state.cls}`}>{state.text}</span>
        <span className="muted small">
          {a.installed ? (a.version ? a.version : a.binary ? 'installed' : 'config found') : 'not found on PATH'}
        </span>
      </header>

      {a.error && <p className="bad">{a.error}</p>}

      <div className="agents-facts">
        <Fact label="Base URL in effect" value={a.current || 'provider default'} sub={a.effective_source && `from ${a.effective_source}`} />
        {a.auth && (
          <Fact
            label="Login"
            value={a.auth.label}
            sub={a.auth.source}
            tag={a.auth.keeps_subscription ? 'subscription kept' : undefined}
          />
        )}
        <Fact label="File setup changes" value={a.config_path} sub={a.config_exists ? undefined : 'does not exist yet'} mono />
      </div>

      {a.auth?.note && <p className="small">{a.auth.note}</p>}

      {a.warnings?.map((w) => (
        <p key={w} className="note">{w}</p>
      ))}

      <div className="agents-try">
        <span className="label">Try it without changing settings</span>
        <CopyLine text={a.try_command} />
      </div>

      {primary && a.sources && a.sources.length > 0 && <Sources sources={a.sources} effective={a.effective_source} />}
      {!primary && a.sources && a.sources.some((s) => s.exists) && (
        <details>
          <summary>Where the base URL comes from</summary>
          <Sources sources={a.sources} effective={a.effective_source} />
        </details>
      )}

      <p className="muted small">{a.changes}</p>

      {confirm ? (
        <div className="agents-confirm" role="alertdialog" aria-label={`Confirm ${confirm}`}>
          <p>
            {confirm === 'setup' ? 'Point ' + a.title + ' at the gateway?' : 'Undo the gateway setup for ' + a.title + '?'}{' '}
            This edits <code>{a.config_path}</code>. A timestamped backup is written next to it first.
          </p>
          <div className="actions">
            <button className="primary" disabled={busy} onClick={() => run(confirm)}>
              {busy ? 'Working…' : confirm === 'setup' ? 'Yes, set up' : 'Yes, undo'}
            </button>
            <button disabled={busy} onClick={() => setConfirm(null)}>Cancel</button>
          </div>
        </div>
      ) : (
        <div className="actions">
          <button onClick={() => setConfirm('setup')} disabled={a.configured && a.effective}>Set up…</button>
          <button onClick={() => setConfirm('undo')} disabled={!a.configured}>Undo…</button>
          <span className="muted small mono">{a.setup_command}</span>
        </div>
      )}

      {result && <pre className={`agents-result ${result.ok ? 'ok' : 'bad'}`}>{result.text}</pre>}

      {a.notes && a.notes.length > 0 && (
        <ul className="agents-notes muted small">
          {a.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}

      {primary && (
        <p className="small">
          Recall is for Claude Code: when pruning drops content, <code>rlcd_recall</code> lets the model fetch it back.{' '}
          <a href="#recall">See the Recall card below.</a>
        </p>
      )}
    </section>
  );
}

function Fact({ label, value, sub, tag, mono }: { label: string; value: string; sub?: string; tag?: string; mono?: boolean }) {
  return (
    <div className="fact">
      <span className="label">{label}</span>
      <span className={`value ${mono ? 'mono' : ''}`}>
        {value}
        {tag && <span className="agents-tag">{tag}</span>}
      </span>
      {sub && <span className="hint">{sub}</span>}
    </div>
  );
}

function Sources({ sources, effective }: { sources: AgentSource[]; effective?: string }) {
  return (
    <table className="agents-sources">
      <thead>
        <tr>
          <th>Level</th>
          <th>File</th>
          <th>Base URL</th>
        </tr>
      </thead>
      <tbody>
        {sources.map((s) => (
          <tr key={s.scope + s.path} className={s.set && s.scope === effective ? 'wins' : ''}>
            <td>{s.scope}</td>
            <td className="mono clip" title={s.path}>
              {s.path}
              {!s.exists && s.scope !== 'environment' && <span className="muted"> (none)</span>}
            </td>
            <td className="mono">
              {s.error ? (
                <span className="bad">{s.error}</span>
              ) : s.set ? (
                <>
                  {s.base_url}
                  {s.gateway && <span className="agents-tag">gateway</span>}
                  {s.scope === effective && <span className="agents-tag">in effect</span>}
                </>
              ) : (
                <span className="muted">—</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function CopyLine({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="agents-copy">
      <pre>{text}</pre>
      <button onClick={copy}>{copied ? 'Copied' : 'Copy'}</button>
    </div>
  );
}
