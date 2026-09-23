import type { GatewayConfig } from '../api';

// Routes and routing rules (F2).
export function RoutingPanel({ config, onChanged }: { config: GatewayConfig; onChanged: () => void }) {
  void config;
  void onChanged;
  return <main className="panel"><h2>Routing</h2><p className="muted">Not implemented yet.</p></main>;
}
