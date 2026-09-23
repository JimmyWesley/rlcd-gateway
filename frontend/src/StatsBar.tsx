import { fmt, type Stats } from './api';

export function StatsBar({ stats }: { stats: Stats | null }) {
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
