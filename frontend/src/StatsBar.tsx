import { useEffect, useState } from 'react';
import { fmt, type Stats } from './api';
import { pruneApi, usd, type PruneStats } from './prune/pruneApi';

export function StatsBar({ stats }: { stats: Stats | null }) {
  const [prune, setPrune] = useState<PruneStats | null>(null);
  // The parent refreshes stats after every request; follow it.
  useEffect(() => {
    pruneApi.stats().then(setPrune).catch(() => setPrune(null));
  }, [stats]);

  const t = stats?.total;
  const input = t ? t.input_tokens + t.cache_read_input_tokens + t.cache_creation_input_tokens : 0;
  const cacheShare = t && input ? Math.round((t.cache_read_input_tokens / input) * 100) : null;
  const items: [string, string, string?][] = [
    ['Requests', fmt.n(t?.requests), t?.errors ? `${t.errors} errors` : undefined],
    ['Input sent', fmt.n(input), 'fresh + cache read + cache write'],
    ['Cache read', fmt.n(t?.cache_read_input_tokens), cacheShare != null ? `${cacheShare}% of input` : undefined],
    ['Cache write', fmt.n(t?.cache_creation_input_tokens)],
    ['Output', fmt.n(t?.output_tokens)],
  ];
  if (prune && prune.requests > 0) {
    const e = prune.enforce, s = prune.shadow;
    if (e.requests > 0 || s.requests === 0) {
      items.push(['Pruning saved', `~${fmt.n(e.saved_tokens)}`, `${usd(e.saved_usd)} est. · ${e.pruned} requests pruned`]);
    }
    if (s.requests > 0) {
      items.push(['Pruning would save', `~${fmt.n(s.saved_tokens)}`, `${usd(s.saved_usd)} est. · shadow mode, not applied`]);
    }
  }
  return (
    <section className="stats">
      {items.map(([label, value, hint]) => (
        <div key={label} className="stat">
          <span className="label">{label}</span>
          <span className="value">{value}</span>
          {hint && <span className="hint">{hint}</span>}
        </div>
      ))}
    </section>
  );
}
