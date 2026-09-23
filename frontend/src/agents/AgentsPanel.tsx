import { RecallCard } from '../recall/RecallCard';

// Which agents point at the gateway, and how to set each one up (F3).
// RecallCard belongs to the recall work (F4); keep it rendered here.
export function AgentsPanel() {
  return (
    <main className="panel">
      <h2>Agents</h2>
      <p className="muted">Not implemented yet.</p>
      <RecallCard />
    </main>
  );
}
