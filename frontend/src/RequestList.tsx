import { fmt, type RequestRecord } from './api';

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
                <td>{r.route}</td>
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
