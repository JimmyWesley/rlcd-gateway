import { fmt, type RequestRecord } from './api';

// Short label for how the router picked the route; the full reason is the tooltip.
function routeTag(reason: string): { label: string; cls: string } {
  if (reason.startsWith('sticky')) return { label: 'sticky', cls: 'sticky' };
  if (reason.startsWith('auto')) return { label: 'auto', cls: 'auto' };
  if (reason.includes('overrode sticky')) return { label: 'override', cls: 'rule' };
  if (reason.startsWith('rule')) return { label: 'rule', cls: 'rule' };
  return { label: '!', cls: 'warn' };
}

type Props = { requests: RequestRecord[]; selected: string | null; onSelect: (id: string) => void };

export function RequestList({ requests, selected, onSelect }: Props) {
  return (
    <section className="panel list">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Route</th>
            <th>Model</th>
            <th className="num">Context</th>
            <th className="num">Status</th>
            <th className="num">Time</th>
          </tr>
        </thead>
        <tbody>
          {requests.map((r) => {
            const failed = r.status >= 400 || !!r.error;
            return (
              <tr key={r.id} className={r.id === selected ? 'selected' : ''} onClick={() => onSelect(r.id)}>
                <td className="mono">{fmt.time(r.time)}</td>
                <td className="rt-route-cell" title={r.route_reason ? `${r.route}: ${r.route_reason}` : 'active route'}>
                  {r.route}
                  {r.route_reason && (() => {
                    const t = routeTag(r.route_reason);
                    return <span className={`rt-why ${t.cls}`}>{t.label}</span>;
                  })()}
                </td>
                <td className="mono clip" title={r.client_model !== r.model ? `${r.client_model} → ${r.model}` : r.model}>
                  {r.path !== '/v1/messages' ? <span className="muted">{r.path}</span> : r.model ?? '—'}
                  {r.client_model && r.model && r.client_model !== r.model && <span className="swap">swapped</span>}
                </td>
                <td className="num mono">{r.est_tokens ? `~${fmt.n(r.est_tokens)}` : '—'}</td>
                <td className={`num mono ${failed ? 'bad' : ''}`}>{r.status || 'ERR'}</td>
                <td className="num mono">{fmt.ms(r.duration_ms)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
