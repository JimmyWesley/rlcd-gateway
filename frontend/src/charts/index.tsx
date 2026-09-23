// Lightweight SVG charts: time series (area, line, columns), sparkline,
// donut and bar list. Colors are CSS variables so both themes come from
// tokens.css. Every chart has a hover layer, an aria label and a hidden data
// table for screen readers.
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { cx } from '../ui';

export function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(([e]) => setW(Math.round(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

/** Round tick values: 0, 250, 500... */
export function niceTicks(min: number, max: number, count = 4): number[] {
  if (min === max) {
    max = min + 1;
  }
  const span = max - min;
  const raw = span / count;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const norm = raw / mag;
  const step = (norm >= 5 ? 10 : norm >= 2 ? 5 : norm >= 1 ? 2 : 1) * mag;
  const lo = Math.floor(min / step) * step;
  const hi = Math.ceil(max / step) * step;
  const out: number[] = [];
  for (let v = lo; v <= hi + step / 2; v += step) out.push(Math.abs(v) < step / 1e6 ? 0 : v);
  return out;
}

export type Series = { key: string; label: string; color: string; values: number[] };

type TimeChartProps = {
  times: number[];
  series: Series[];
  kind?: 'area' | 'line' | 'columns';
  stacked?: boolean;
  height?: number;
  yFormat: (v: number) => string;
  xFormat: (t: number) => string;
  tipTitle: (t: number) => string;
  label: string;
  /** Series drawn as a dashed line instead of a fill (e.g. a projection). */
  dashed?: string[];
  legend?: boolean;
  empty?: ReactNode;
};

const PAD = { top: 10, right: 12, bottom: 24, left: 48 };

export function TimeChart({
  times, series, kind = 'area', stacked = false, height = 200, yFormat, xFormat, tipTitle, label, dashed = [], legend = true, empty,
}: TimeChartProps) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const n = times.length;
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const shown = series.filter((s) => !hidden.has(s.key));

  const { stacks, yMin, yMax } = useMemo(() => {
    // stacks[s][i] = [y0, y1] for each shown series at each index.
    const st: [number, number][][] = [];
    const posBase = new Array(n).fill(0);
    const negBase = new Array(n).fill(0);
    let lo = 0, hi = 0;
    for (const s of shown) {
      const row: [number, number][] = [];
      for (let i = 0; i < n; i++) {
        const raw = s.values[i];
        const v = Number.isFinite(raw) ? raw : 0;
        if (stacked) {
          const base = v >= 0 ? posBase : negBase;
          row.push([base[i], base[i] + v]);
          base[i] += v;
        } else row.push([0, v]);
        lo = Math.min(lo, row[i][0], row[i][1]);
        hi = Math.max(hi, row[i][0], row[i][1]);
      }
      st.push(row);
    }
    return { stacks: st, yMin: lo, yMax: hi };
  }, [shown, n, stacked]);

  const ticks = niceTicks(yMin, yMax || 1, 4);
  const lo = ticks[0], hi = ticks[ticks.length - 1];
  const iw = Math.max(10, width - PAD.left - PAD.right);
  const ih = height - PAD.top - PAD.bottom;
  const band = n > 0 ? iw / n : iw;
  const x = (i: number) => (kind === 'columns' ? PAD.left + band * i + band / 2 : PAD.left + (n <= 1 ? iw / 2 : (iw * i) / (n - 1)));
  const y = (v: number) => PAD.top + ih - ((v - lo) / (hi - lo || 1)) * ih;
  const allZero = series.every((s) => s.values.every((v) => !v));

  const xTickEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 72))));

  const onMove = (e: React.PointerEvent<SVGRectElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - r.left;
    const i = kind === 'columns' ? Math.floor(px / band) : Math.round((px / iw) * (n - 1));
    setHover(Math.max(0, Math.min(n - 1, i)));
  };

  const toggle = (k: string) =>
    setHidden((h) => {
      const next = new Set(h);
      if (next.has(k)) next.delete(k);
      else if (series.length - next.size > 1) next.add(k);
      return next;
    });

  return (
    <div className="chart" ref={ref}>
      {legend && series.length > 1 && (
        <div className="legend" role="group" aria-label={label}>
          {series.map((s) => (
            <button key={s.key} type="button" className={cx('legend-item', hidden.has(s.key) && 'off')} aria-pressed={!hidden.has(s.key)} onClick={() => toggle(s.key)}>
              <i className={cx('legend-key', dashed.includes(s.key) && 'dashed', kind === 'line' && 'line')} style={{ ['--c' as string]: s.color }} />
              {s.label}
            </button>
          ))}
        </div>
      )}
      <div className="chart-plot" style={{ height }}>
        {width > 0 && (
          <svg width={width} height={height} role="img" aria-label={label}>
            {ticks.map((tv) => (
              <g key={tv} className="tick">
                <line x1={PAD.left} x2={width - PAD.right} y1={y(tv)} y2={y(tv)} className={tv === 0 ? 'axis-zero' : 'grid'} />
                <text x={PAD.left - 8} y={y(tv)} dy="0.32em" textAnchor="end">{yFormat(tv)}</text>
              </g>
            ))}
            {times.map((tm, i) =>
              i % xTickEvery === 0 ? (
                <text key={tm} className="tick-x" x={x(i)} y={height - 6} textAnchor="middle">{xFormat(tm)}</text>
              ) : null,
            )}

            {kind === 'columns' &&
              shown.map((s, si) => (
                <g key={s.key} style={{ fill: s.color }}>
                  {stacks[si].map(([a, b], i) => {
                    if (a === b) return null;
                    const top = y(Math.max(a, b));
                    const bottom = y(Math.min(a, b));
                    const bw = Math.min(24, Math.max(2, band - Math.max(2, band * 0.3)));
                    // 2px surface gap between stacked segments
                    const gap = stacked && a !== 0 ? 1 : 0;
                    const h = Math.max(1, bottom - top - gap);
                    const isTop = !stacked || si === lastNonZero(stacks, i);
                    return (
                      <path
                        key={i}
                        className={cx('col', hover != null && hover !== i && 'dim')}
                        d={colPath(x(i) - bw / 2, top, bw, h, isTop && b >= 0 ? Math.min(4, bw / 2, h) : 0)}
                      />
                    );
                  })}
                </g>
              ))}

            {kind !== 'columns' &&
              shown.map((s, si) => {
                const pts = stacks[si].map(([, b], i) => [x(i), y(b)] as const);
                const base = stacks[si].map(([a], i) => [x(i), y(a)] as const);
                // NaN marks a gap (no data in that slot): lift the pen there.
                const gap = (i: number) => !Number.isFinite(s.values[i]);
                const line = pts.map(([px, py], i) => (gap(i) ? '' : `${i && !gap(i - 1) ? 'L' : 'M'}${px},${py}`)).join('');
                const area = line + base.reverse().map(([px, py]) => `L${px},${py}`).join('') + 'Z';
                const isDashed = dashed.includes(s.key);
                return (
                  <g key={s.key}>
                    {kind === 'area' && !isDashed && <path d={area} className="area" style={{ fill: s.color }} />}
                    <path d={line} className={cx('line', isDashed && 'dashed')} style={{ stroke: s.color }} />
                    {pts.map(([px, py], i) => (n === 1 || (gap(i - 1) && gap(i + 1))) && !gap(i) ? <circle key={i} cx={px} cy={py} r={3} style={{ fill: s.color }} className="pt" /> : null)}
                  </g>
                );
              })}

            {hover != null && kind !== 'columns' && (
              <g>
                <line className="crosshair" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + ih} />
                {shown.map((s, si) => Number.isFinite(s.values[hover]) && (
                  <circle key={s.key} cx={x(hover)} cy={y(stacks[si][hover][1])} r={4} className="pt" style={{ fill: s.color }} />
                ))}
              </g>
            )}
            <rect
              x={PAD.left}
              y={PAD.top}
              width={iw}
              height={ih}
              fill="transparent"
              onPointerMove={onMove}
              onPointerLeave={() => setHover(null)}
            />
          </svg>
        )}
        {allZero && empty && <div className="chart-empty">{empty}</div>}
        {hover != null && width > 0 && (
          <div
            className="chart-tip"
            style={{ left: Math.min(Math.max(x(hover), 90), width - 90), top: 0 }}
            role="presentation"
          >
            <div className="tip-title">{tipTitle(times[hover])}</div>
            {shown.map((s) => (
              <div key={s.key} className="tip-row">
                <i className="legend-key" style={{ ['--c' as string]: s.color }} />
                <span className="tip-label">{s.label}</span>
                <span className="tip-val">{Number.isFinite(s.values[hover]) ? yFormat(s.values[hover]) : '—'}</span>
              </div>
            ))}
          </div>
        )}
      </div>
      <table className="sr-only">
        <caption>{label}</caption>
        <thead>
          <tr><th>{''}</th>{series.map((s) => <th key={s.key}>{s.label}</th>)}</tr>
        </thead>
        <tbody>
          {times.map((tm, i) => (
            <tr key={tm}><th>{tipTitle(tm)}</th>{series.map((s) => <td key={s.key}>{yFormat(s.values[i] ?? 0)}</td>)}</tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function lastNonZero(stacks: [number, number][][], i: number): number {
  for (let s = stacks.length - 1; s >= 0; s--) if (stacks[s][i][0] !== stacks[s][i][1]) return s;
  return -1;
}

/** A column rounded on its top corners only (square at the baseline). */
function colPath(x: number, y: number, w: number, h: number, r: number): string {
  if (r <= 0) return `M${x},${y}h${w}v${h}h${-w}Z`;
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

export function Sparkline({ values, color = 'var(--accent)', height = 28, label, area = true }: {
  values: number[]; color?: string; height?: number; label: string; area?: boolean;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const n = values.length;
  const max = Math.max(...values, 0);
  const min = Math.min(...values, 0);
  const x = (i: number) => (n <= 1 ? width / 2 : 1 + ((width - 2) * i) / (n - 1));
  const y = (v: number) => 2 + (height - 4) * (1 - (v - min) / (max - min || 1));
  const d = values.map((v, i) => `${i ? 'L' : 'M'}${x(i)},${y(v)}`).join('');
  return (
    <div className="sparkline" ref={ref} style={{ height }}>
      {width > 0 && n > 0 && (
        <svg width={width} height={height} role="img" aria-label={label}>
          {area && <path d={`${d}L${x(n - 1)},${y(min)}L${x(0)},${y(min)}Z`} className="area" style={{ fill: color }} />}
          <path d={d} className="line" style={{ stroke: color }} />
          <circle cx={x(n - 1)} cy={y(values[n - 1])} r={2.5} style={{ fill: color }} />
        </svg>
      )}
    </div>
  );
}

export type Segment = { key: string; label: string; value: number; color: string };

export function Donut({ segments, size = 148, thickness = 16, center, label, format }: {
  segments: Segment[]; size?: number; thickness?: number; center?: ReactNode; label: string; format: (v: number) => string;
}) {
  const [hover, setHover] = useState<string | null>(null);
  const total = segments.reduce((s, x) => s + Math.max(0, x.value), 0);
  const r = (size - thickness) / 2;
  const c = size / 2;
  const circ = 2 * Math.PI * r;
  const gap = total > 0 && segments.filter((s) => s.value > 0).length > 1 ? 2 : 0;
  let acc = 0;
  const hovered = segments.find((s) => s.key === hover);
  return (
    <div className="donut-wrap">
      <div className="donut" style={{ width: size, height: size }}>
        <svg width={size} height={size} role="img" aria-label={label}>
          <circle cx={c} cy={c} r={r} className="donut-track" strokeWidth={thickness} fill="none" />
          {total > 0 &&
            segments.map((s) => {
              const len = (Math.max(0, s.value) / total) * circ;
              const dash = Math.max(0, len - gap);
              const el = (
                <circle
                  key={s.key}
                  cx={c}
                  cy={c}
                  r={r}
                  fill="none"
                  strokeWidth={thickness}
                  strokeDasharray={`${dash} ${circ - dash}`}
                  strokeDashoffset={-acc}
                  transform={`rotate(-90 ${c} ${c})`}
                  className={cx('donut-seg', hover && hover !== s.key && 'dim')}
                  style={{ stroke: s.color }}
                  onPointerEnter={() => setHover(s.key)}
                  onPointerLeave={() => setHover(null)}
                />
              );
              acc += len;
              return el;
            })}
        </svg>
        <div className="donut-center">
          {hovered ? (
            <>
              <div className="donut-value">{total ? Math.round((hovered.value / total) * 100) : 0}%</div>
              <div className="donut-label">{hovered.label}</div>
            </>
          ) : (
            center
          )}
        </div>
      </div>
      <ul className="donut-legend">
        {segments.map((s) => (
          <li key={s.key} onPointerEnter={() => setHover(s.key)} onPointerLeave={() => setHover(null)} className={cx(hover === s.key && 'on')}>
            <i className="legend-key" style={{ ['--c' as string]: s.color }} />
            <span className="dl-label">{s.label}</span>
            <span className="dl-val">{format(s.value)}</span>
            <span className="dl-pct">{total ? `${Math.round((s.value / total) * 100)}%` : '—'}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export type BarItem = { key: string; label: ReactNode; value: number; display: string; sub?: ReactNode; color?: string; tone?: 'bad'; onClick?: () => void; title?: string; /** A small action beside the row (e.g. an edit link). */ action?: ReactNode };

export function BarList({ items, label, max }: { items: BarItem[]; label: string; max?: number }) {
  const top = max ?? Math.max(...items.map((i) => i.value), 1);
  return (
    <ul className="barlist" aria-label={label}>
      {items.map((it) => {
        const inner = (
          <>
            <div className="bl-row">
              <span className="bl-label">{it.label}</span>
              <span className="bl-val">{it.display}</span>
            </div>
            <div className="bl-track">
              <span className="bl-fill" style={{ width: `${Math.max(1.5, (it.value / top) * 100)}%`, background: it.color ?? 'var(--series-1)' }} />
            </div>
            {it.sub && <div className="bl-sub">{it.sub}</div>}
          </>
        );
        return (
          <li key={it.key} title={it.title} className={it.action ? 'bl-has-action' : undefined}>
            {it.onClick ? <button type="button" className="bl-btn" onClick={it.onClick}>{inner}</button> : inner}
            {it.action && <span className="bl-action">{it.action}</span>}
          </li>
        );
      })}
    </ul>
  );
}

/** A single 100% bar split into segments (context by kind, kept vs dropped). */
export function SplitBar({ segments, label, height = 12, onSelect, selected, format }: {
  segments: (Segment & { title?: string })[]; label: string; height?: number; onSelect?: (key: string) => void; selected?: string | null;
  format?: (v: number) => string;
}) {
  const total = segments.reduce((s, x) => s + x.value, 0) || 1;
  return (
    <div className="splitbar" style={{ height }} role="img" aria-label={label}>
      {segments.map((s) =>
        s.value > 0 ? (
          <span
            key={s.key}
            className={cx('split-seg', selected && selected !== s.key && 'dim', onSelect && 'clickable')}
            style={{ flexGrow: s.value / total, background: s.color }}
            title={s.title ?? `${s.label}: ${format ? format(s.value) : s.value}`}
            onClick={onSelect ? () => onSelect(s.key) : undefined}
          />
        ) : null,
      )}
    </div>
  );
}

/** Vertical bars over categories (a histogram), with a hover tooltip. */
export function Bars({ labels, values, label, color = 'var(--series-1)', height = 150, format, highlight }: {
  labels: string[]; values: number[]; label: string; color?: string; height?: number; format: (v: number) => string; highlight?: (i: number) => boolean;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const n = values.length;
  const max = Math.max(1, ...values);
  const pad = { top: 8, bottom: 22, left: 4, right: 4 };
  const iw = Math.max(10, width - pad.left - pad.right);
  const ih = height - pad.top - pad.bottom;
  const band = iw / Math.max(1, n);
  const bw = Math.min(28, band - 4);
  return (
    <div className="chart" ref={ref}>
      <div className="chart-plot" style={{ height }}>
        {width > 0 && (
          <svg width={width} height={height} role="img" aria-label={label}>
            <line className="axis-zero" x1={pad.left} x2={width - pad.right} y1={pad.top + ih} y2={pad.top + ih} />
            {values.map((v, i) => {
              const h = (v / max) * ih;
              const x = pad.left + band * i + (band - bw) / 2;
              return (
                <g key={i} onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(null)}>
                  <rect x={pad.left + band * i} y={pad.top} width={band} height={ih} fill="transparent" />
                  {v > 0 && <path className={cx('col', hover != null && hover !== i && 'dim')} style={{ fill: highlight?.(i) ? 'var(--warn)' : color }} d={colPath(x, pad.top + ih - h, bw, Math.max(1, h), Math.min(4, bw / 2, h))} />}
                  {(i % Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 44)))) === 0 || i === n - 1) && (
                    <text className="tick-x" x={pad.left + band * i + band / 2} y={height - 6} textAnchor="middle">{labels[i]}</text>
                  )}
                </g>
              );
            })}
          </svg>
        )}
        {hover != null && width > 0 && (
          <div className="chart-tip" style={{ left: Math.min(Math.max(pad.left + band * hover + band / 2, 70), width - 70), top: 0 }}>
            <div className="tip-title">{labels[hover]}</div>
            <div className="tip-row"><span className="tip-val">{format(values[hover])}</span></div>
          </div>
        )}
      </div>
      <table className="sr-only">
        <caption>{label}</caption>
        <tbody>{labels.map((l, i) => <tr key={i}><th>{l}</th><td>{format(values[i])}</td></tr>)}</tbody>
      </table>
    </div>
  );
}

export type ReliabilityBin = { lo: number; hi: number; n: number; mean_confidence?: number; accuracy?: number };

/**
 * A reliability diagram: per confidence bin, how often the answer was right.
 * On the diagonal is perfect calibration; bins with few outcomes fade.
 */
export function Reliability({ bins, label, labels: L, height = 240, thin = 5 }: {
  bins: ReliabilityBin[]; label: string; height?: number; /** Bins with fewer items than this fade. */ thin?: number;
  labels: { perfect: string; accuracy: string; confidence: string; n: (n: number) => string; pct: (v: number) => string };
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const pad = { top: 10, right: 10, bottom: 38, left: 40 };
  const size = Math.max(10, Math.min(width - pad.left - pad.right, height - pad.top - pad.bottom));
  const x = (v: number) => pad.left + v * size;
  const y = (v: number) => pad.top + (1 - v) * size;
  const ticks = [0, 0.25, 0.5, 0.75, 1];
  return (
    <div className="chart reliability" ref={ref}>
      <div className="chart-plot" style={{ height: size + pad.top + pad.bottom }}>
        {width > 0 && (
          <svg width={width} height={size + pad.top + pad.bottom} role="img" aria-label={label}>
            {ticks.map((tv) => (
              <g key={tv} className="tick">
                <line className="grid" x1={x(0)} x2={x(1)} y1={y(tv)} y2={y(tv)} />
                <text x={x(0) - 6} y={y(tv)} dy="0.32em" textAnchor="end">{L.pct(tv)}</text>
                <text className="tick-x" x={x(tv)} y={y(0) + 14} textAnchor="middle">{L.pct(tv)}</text>
              </g>
            ))}
            <line className="rel-diag" x1={x(0)} y1={y(0)} x2={x(1)} y2={y(1)} />
            {bins.map((b, i) => {
              const bw = (b.hi - b.lo) * size - 3;
              const acc = b.accuracy;
              const fade = b.n >= thin ? 1 : 0.3;
              return (
                <g key={i} onPointerEnter={() => setHover(i)} onPointerLeave={() => setHover(null)}>
                  <rect x={x(b.lo)} y={pad.top} width={(b.hi - b.lo) * size} height={size} fill="transparent" />
                  {acc != null && b.n > 0 && (
                    <rect className={cx('rel-bar', hover === i && 'on')} x={x(b.lo) + 1.5} y={y(acc)} width={Math.max(1, bw)} height={Math.max(1, y(0) - y(acc))} rx={3} style={{ opacity: fade }} />
                  )}
                  {b.mean_confidence != null && acc != null && b.n > 0 && (
                    <circle className="rel-pt" cx={x(b.mean_confidence)} cy={y(acc)} r={4} style={{ opacity: Math.max(0.35, fade) }} />
                  )}
                  <text className={cx('rel-n', b.n > 0 && b.n < thin && 'thin')} x={x(b.lo) + ((b.hi - b.lo) * size) / 2} y={y(0) + 28} textAnchor="middle">{b.n || ''}</text>
                </g>
              );
            })}
          </svg>
        )}
        {hover != null && width > 0 && bins[hover] && (
          <div className="chart-tip" style={{ left: Math.min(Math.max(x(bins[hover].lo), 90), width - 90), top: 0 }}>
            <div className="tip-title">{L.pct(bins[hover].lo)} – {L.pct(bins[hover].hi)} · {L.n(bins[hover].n)}</div>
            {bins[hover].mean_confidence != null && <div className="tip-row"><span className="tip-label">{L.confidence}</span><span className="tip-val">{L.pct(bins[hover].mean_confidence!)}</span></div>}
            {bins[hover].accuracy != null && <div className="tip-row"><span className="tip-label">{L.accuracy}</span><span className="tip-val">{L.pct(bins[hover].accuracy!)}</span></div>}
          </div>
        )}
      </div>
      <div className="legend">
        <span className="legend-item static"><i className="legend-key" style={{ ['--c' as string]: 'var(--series-1)' }} />{L.accuracy}</span>
        <span className="legend-item static"><i className="legend-key dashed" style={{ ['--c' as string]: 'var(--text-4)' }} />{L.perfect}</span>
      </div>
      <table className="sr-only">
        <caption>{label}</caption>
        <thead><tr><th>{L.confidence}</th><th>n</th><th>{L.accuracy}</th></tr></thead>
        <tbody>{bins.map((b, i) => <tr key={i}><th>{L.pct(b.lo)}–{L.pct(b.hi)}</th><td>{b.n}</td><td>{b.accuracy != null ? L.pct(b.accuracy) : '—'}</td></tr>)}</tbody>
      </table>
    </div>
  );
}
