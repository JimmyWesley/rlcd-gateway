import { useCallback, useEffect, useState } from 'react';
import { api, onRequest, type GatewayConfig, type RequestRecord, type Stats } from './api';
import { RequestList } from './RequestList';
import { RequestView } from './RequestView';
import { SelectorPanel } from './SelectorPanel';
import { PrunePanel } from './prune/PrunePanel';
import { RoutingPanel } from './routing/RoutingPanel';
import { AgentsPanel } from './agents/AgentsPanel';
import { StatsBar } from './StatsBar';

type Tab = 'traffic' | 'pruning' | 'routing' | 'agents' | 'selector';
const TABS: [Tab, string][] = [
  ['traffic', 'Traffic'],
  ['pruning', 'Pruning'],
  ['routing', 'Routing'],
  ['agents', 'Agents'],
  ['selector', 'Economy model'],
];

export function App() {
  const [config, setConfig] = useState<GatewayConfig | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [requests, setRequests] = useState<RequestRecord[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const [tab, setTab] = useState<Tab>('traffic');
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    api.stats().then(setStats).catch(() => {});
  }, []);

  useEffect(() => {
    api.config().then(setConfig).catch((e) => setError(String(e)));
    api.requests().then((rs) => {
      setRequests(rs);
      setSelected((cur) => cur ?? rs[0]?.id ?? null);
    });
    refresh();
    return onRequest((r) => {
      setRequests((rs) => [r, ...rs].slice(0, 500));
      refresh();
    }, setLive);
  }, [refresh]);

  const switchRoute = async (name: string) => {
    try {
      await api.setRoute(name);
      setConfig((c) => (c ? { ...c, active_route: name } : c));
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <span className={`dot ${live ? 'on' : ''}`} title={live ? 'live' : 'disconnected'} />
          <strong>RLCD Gateway</strong>
          <span className="muted">{config?.listen}</span>
        </div>
        <nav className="tabs">
          {TABS.map(([t, label]) => (
            <button key={t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)}>{label}</button>
          ))}
        </nav>
        {config && (
          <div className="routes" role="radiogroup" aria-label="Active route">
            <span className="muted">Route</span>
            {config.routes.map((r) => (
              <button
                key={r.name}
                role="radio"
                aria-checked={config.active_route === r.name}
                className={config.active_route === r.name ? 'active' : ''}
                onClick={() => switchRoute(r.name)}
                title={`${r.base_url}${r.model ? ` · ${r.model}` : ''}${r.auth === 'key' && !r.has_key ? ' · no key set' : ''}`}
              >
                {r.name}
                {r.auth === 'passthrough' ? <em>your login</em> : <em className={r.has_key ? '' : 'warn'}>{r.has_key ? 'gateway key' : 'no key'}</em>}
              </button>
            ))}
          </div>
        )}
      </header>

      {error && <div className="banner" onClick={() => setError(null)}>{error}</div>}

      {tab === 'traffic' && (
        <>
          <StatsBar stats={stats} />
          <main className="split">
            <RequestList requests={requests} selected={selected} onSelect={setSelected} />
            {selected ? <RequestView id={selected} /> : <Empty />}
          </main>
        </>
      )}
      {tab === 'pruning' && <PrunePanel />}
      {tab === 'routing' && config && <RoutingPanel config={config} onChanged={() => api.config().then(setConfig)} />}
      {tab === 'agents' && <AgentsPanel />}
      {tab === 'selector' && config && (
        <SelectorPanel config={config} onSaved={(s) => setConfig({ ...config, selector: s })} />
      )}
    </div>
  );
}

function Empty() {
  return (
    <section className="panel empty">
      <h2>No traffic yet</h2>
      <p>Point an agent at the gateway. Your login stays as it is:</p>
      <pre>ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude</pre>
    </section>
  );
}
