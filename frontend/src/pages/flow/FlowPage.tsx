// The Flow view: how requests travel through the gateway. Loaded lazily
// because React Flow is the largest dependency of the dashboard.
import '@xyflow/react/dist/base.css';
import { memo, useEffect, useMemo, useRef, useState } from 'react';
import {
  BaseEdge, EdgeLabelRenderer, Handle, Position, ReactFlow, ReactFlowProvider, getBezierPath, useNodesInitialized, useReactFlow,
  type Edge, type EdgeProps, type Node, type NodeProps,
} from '@xyflow/react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { BrandIcon, ClientIcon } from '../../icons/BrandIcon';
import { recallApi, type RequestRecord } from '../../lib/api';
import { isFailed } from '../../lib/api';
import { modelVendor, PROVIDER_NAMES, recordClient, recordModel, vendorIcon } from '../../lib/brands';
import { href, navigate } from '../../lib/router';
import { useTheme } from '../../lib/theme';
import { useGateway, useLiveFetch } from '../../state/gateway';
import { useWidth } from '../../charts';
import { Badge, Button, Card, EmptyState, IconButton, PageHeader, Segmented, cx } from '../../ui';
import { useWindowPref, WindowPicker } from '../overview/Overview';
import { buildGraph, COL_X, GRAPH_W, edgesOfPath, pathOf, ROW_H, type Col, type FlowEdgeData, type FlowNodeData, type Metric } from './flowModel';

const WINDOW_MS: Record<string, number> = { '1h': 3.6e6, '6h': 6 * 3.6e6, '24h': 24 * 3.6e6, '7d': 7 * 24 * 3.6e6, all: Infinity };

type Sel = { kind: 'node' | 'edge'; id: string } | null;

export default function FlowPage() {
  return (
    <ReactFlowProvider>
      <Flow />
    </ReactFlowProvider>
  );
}

function Flow() {
  const { t, f } = useI18n();
  const { requests, requestsLoaded } = useGateway();
  const { resolved } = useTheme();
  const [win, setWin] = useWindowPref('rlcd.flow.window');
  const [metric, setMetric] = useState<Metric>('requests');
  const [sel, setSel] = useState<Sel>(null);
  const [focusReq, setFocusReq] = useState<string | null>(null);
  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  const [canvasRef, canvasW] = useWidth<HTMLDivElement>();
  const [cursor, setCursor] = useState<number | null>(null); // null = live (everything)
  const [playing, setPlaying] = useState(false);
  const recallEvents = useLiveFetch(() => recallApi.events(1000), [], 10000);

  // Requests in the window, oldest first.
  const inWindow = useMemo(() => {
    const since = Date.now() - WINDOW_MS[win];
    return requests.filter((r) => Date.parse(r.time) >= since).slice().reverse();
  }, [requests, win]);

  const n = inWindow.length;
  const upto = cursor == null ? n : Math.min(cursor, n);
  const visible = useMemo(() => inWindow.slice(0, upto), [inWindow, upto]);
  const cursorTime = visible.length ? Date.parse(visible[visible.length - 1].time) : Date.now();
  const latest = cursor != null && visible.length ? visible[visible.length - 1] : null;
  const highlightReq = (focusReq && visible.find((r) => r.id === focusReq)) || latest;

  // Replay: step through the window's requests.
  useEffect(() => {
    if (!playing) return;
    const step = Math.max(1, Math.round(n / 120));
    const id = window.setInterval(() => {
      setCursor((c) => {
        const next = (c ?? 0) + step;
        if (next >= n) {
          setPlaying(false);
          return null;
        }
        return next;
      });
    }, 160);
    return () => window.clearInterval(id);
  }, [playing, n]);

  const recalls = useMemo(() => {
    const since = Date.now() - WINDOW_MS[win];
    return (recallEvents.data ?? []).filter((e) => {
      const tm = Date.parse(e.time);
      return tm >= since && tm <= cursorTime + 1000 && e.ok;
    }).length;
  }, [recallEvents.data, win, cursorTime]);

  const graph = useMemo(
    () => buildGraph(visible, { unknownClient: t('client.unknown'), router: t('flow.node.router'), prune: t('flow.node.prune'), recall: t('flow.node.recall'), decisionBackend: t('flow.node.decisionBackend'), protocol: (p) => t(`protocol.node.${p}`) }, recalls),
    [visible, t, recalls],
  );

  const hlPath = useMemo(() => (highlightReq ? pathOf(highlightReq) : null), [highlightReq]);
  const hlEdges = useMemo(() => new Set(hlPath ? edgesOfPath(hlPath) : []), [hlPath]);
  const selNodes = useMemo(() => {
    if (!sel) return null;
    if (sel.kind === 'node') return new Set([sel.id]);
    const e = graph.edges.get(sel.id);
    return e ? new Set([e.source, e.target]) : null;
  }, [sel, graph]);

  const { nodes, edges, rows } = useMemo(() => {
    // Lay out each column top to bottom by traffic, centered on the gateway row.
    // Two lanes, one above the other: the LLM proxy, then your systems' decisions.
    const out: Node<FlowNodeData>[] = [];
    let y0 = 0;
    let maxRows = 0;
    const laneNode = (lane: 'proxy' | 'decisions', y: number): Node<FlowNodeData> => ({
      id: `lane:${lane}`, type: 'lane', position: { x: -24, y }, draggable: false, selectable: false, focusable: false,
      data: { col: 'client', key: `lane:${lane}`, label: t(`flow.lane.${lane}`), sub: t(`flow.lane.${lane}Sub`), stats: { count: 0, errors: 0, tokens: 0, saved: 0, savedUsd: 0 } },
    });
    const twoLanes = [...graph.nodes.values()].some((x) => x.lane === 'decisions') && [...graph.nodes.values()].some((x) => x.col === 'client' && (x.lane ?? 'proxy') === 'proxy');
    if (twoLanes) out.push(laneNode('proxy', -46));
    for (const lane of ['proxy', 'decisions'] as const) {
      const cols = new Map<Col, FlowNodeData[]>();
      for (const d of graph.nodes.values()) {
        if (d.col === 'recall' || (d.lane ?? 'proxy') !== lane) continue;
        if (lane === 'proxy' && (d.col === 'router' || d.col === 'prune') && !graph.byNode.get(d.key)?.length && [...graph.nodes.values()].some((x) => x.lane === 'decisions')) continue;
        const l = cols.get(d.col) ?? [];
        l.push(d);
        cols.set(d.col, l);
      }
      if (!cols.size) continue;
      const rowsHere = Math.max(1, ...[...cols.values()].map((l) => l.length));
      if (lane === 'decisions') {
        y0 += ROW_H * 0.9;
        out.push(laneNode('decisions', y0 - 46));
      }
      for (const [col, list] of cols) {
        list.sort((a, b) => b.stats.count - a.stats.count || a.label.localeCompare(b.label));
        const top = y0 + ((rowsHere - list.length) * ROW_H) / 2;
        list.forEach((d, i) => {
          out.push({
            id: d.key,
            type: 'gw',
            position: { x: COL_X[col], y: top + i * ROW_H },
            data: { ...d, highlighted: !!hlPath?.includes(d.key), selected: !!selNodes?.has(d.key), dimmed: !!(hlPath && !hlPath.includes(d.key)) },
            draggable: false,
          });
        });
      }
      y0 += rowsHere * ROW_H;
      maxRows += rowsHere + (lane === 'decisions' ? 1 : 0);
      const pruneNode = lane === 'proxy' ? out.find((x) => x.id === 'prune') : undefined;
      if (pruneNode) {
        // Recall hangs below the pruner; the next lane starts below it.
        const y = pruneNode.position.y + ROW_H * 1.35;
        out.push({ id: 'recall', type: 'gw', draggable: false, position: { x: COL_X.recall, y },
          data: { ...graph.nodes.get('recall')!, dimmed: !!hlPath, selected: !!selNodes?.has('recall') } });
        if (y + ROW_H > y0) {
          maxRows += Math.ceil((y + ROW_H - y0) / ROW_H);
          y0 = y + ROW_H;
        }
      }
    }

    const vals = [...graph.edges.values()].map((e) => (metric === 'requests' ? e.stats.count : metric === 'tokens' ? e.stats.tokens : e.stats.saved));
    const max = Math.max(1, ...vals);
    const es: Edge<FlowEdgeData>[] = [...graph.edges.entries()].map(([id, e]) => {
      const v = metric === 'requests' ? e.stats.count : metric === 'tokens' ? e.stats.tokens : e.stats.saved;
      const isRecall = e.target === 'recall';
      const fb = !!e.fallback;
      return {
        id,
        source: e.source,
        target: e.target,
        type: 'gw',
        sourceHandle: isRecall ? 'bottom' : fb ? 'fbout' : undefined,
        targetHandle: isRecall ? 'top' : fb ? 'fbin' : undefined,
        data: {
          stats: e.stats,
          width: fb ? 2 + 4 * Math.sqrt(v / max) : 1.5 + 9 * Math.sqrt(v / max),
          passthrough: e.passthrough,
          fallback: fb,
          highlighted: hlEdges.has(id) || sel?.id === id,
          dimmed: !!(hlPath && !hlEdges.has(id)),
          label: isRecall ? t('flow.edge.recalls', { count: e.stats.count }) : fb ? t('flow.edge.fallback', { count: e.stats.count }) : edgeLabel(t, f, metric, e.stats),
          savedLabel: e.source === 'prune' && e.stats.saved > 0 ? t('flow.edge.saved', { tokens: f.tokens(e.stats.saved), usd: f.usd(e.stats.savedUsd) }) : undefined,
          // Errors show once, on the edge that reaches the model (or route).
          errorsLabel: e.stats.errors && e.target.startsWith('model:') ? t('flow.edge.errors', { count: e.stats.errors }) : undefined,
          showLabel: isRecall || fb || v / max >= 0.3 || hoverEdge === id,
        },
      };
    });
    return { nodes: out, edges: es, rows: maxRows };
  }, [graph, metric, hlPath, hlEdges, selNodes, sel, t, f, hoverEdge]);

  // The canvas is as tall as the graph is at the scale that fits its width.
  const scale = canvasW ? Math.min(1, (canvasW - 40) / GRAPH_W) : 0.8;
  const canvasH = Math.round(Math.min(760, Math.max(280, (rows * ROW_H + 30) * scale + 56)));

  // Requests behind the selection, newest first.
  const selected = useMemo(() => {
    if (!sel) return null;
    const l = sel.kind === 'node' ? graph.byNode.get(sel.id) : graph.byEdge.get(sel.id);
    return (l ?? []).slice().reverse();
  }, [sel, graph]);

  const openInTraffic = () => {
    if (!sel) return;
    const ids = sel.kind === 'node' ? [sel.id] : [graph.edges.get(sel.id)?.source ?? '', graph.edges.get(sel.id)?.target ?? ''];
    const q: Record<string, string> = {};
    for (const id of ids) {
      const [kind, ...rest] = id.split(':');
      const v = rest.join(':');
      if (kind === 'client' || kind === 'dclient') {
        const [client, key] = v.split('|');
        if (key) q.key = key;
        else q.client = client;
      }
      if (kind === 'proto') q.protocol = v;
      if (kind === 'route') q.route = v;
      if (kind === 'model' || kind === 'dmodel') q.model = v;
      if (kind === 'dbackend') q.protocol = 'systemone';
      if (id === 'prune' && sel.kind === 'edge') q.status = 'pruned';
    }
    navigate('traffic', q);
  };

  const hasData = requestsLoaded && requests.length > 0;

  return (
    <div className="page page-flow">
      <PageHeader title={t('nav.flow')} description={t('flow.desc')} actions={<WindowPicker value={win} onChange={(w) => { setWin(w); setCursor(null); setPlaying(false); }} />} />
      {!hasData ? (
        <Card>
          <EmptyState icon="flow" title={t('flow.empty')} actions={<Button onClick={() => navigate('integrations')}>{t('traffic.empty.more')}</Button>}>{t('flow.emptyBody')}</EmptyState>
        </Card>
      ) : (
        <div className="flow-layout">
          <Card flush className="flow-card">
            <div className="flow-toolbar">
              <Segmented size="sm" label={t('flow.metric')} value={metric} onChange={setMetric}
                options={[{ id: 'requests', label: t('flow.metric.requests') }, { id: 'tokens', label: t('flow.metric.tokens') }, { id: 'saved', label: t('flow.metric.saved') }]} />
              <span className="toolbar-spacer" />
              <span className="muted small">{t('flow.hint')}</span>
            </div>
            <div className="flow-canvas" ref={canvasRef} style={{ height: canvasH }} aria-label={t('flow.canvas', { count: visible.length })} role="figure">
              <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                edgeTypes={edgeTypes}
                colorMode={resolved}
                fitView
                fitViewOptions={{ padding: 0.06 }}
                minZoom={0.3}
                maxZoom={1.6}
                nodesConnectable={false}
                nodesDraggable={false}
                elementsSelectable
                proOptions={{ hideAttribution: true }}
                onNodeClick={(_, nd) => { setSel(sel?.id === nd.id ? null : { kind: 'node', id: nd.id }); setFocusReq(null); }}
                onEdgeClick={(_, ed) => { setSel(sel?.id === ed.id ? null : { kind: 'edge', id: ed.id }); setFocusReq(null); }}
                onEdgeMouseEnter={(_, ed) => setHoverEdge(ed.id)}
                onEdgeMouseLeave={() => setHoverEdge(null)}
                onPaneClick={() => { setSel(null); setFocusReq(null); }}
              >
                <FitOnChange keyStr={`${win}|${graph.nodes.size}|${rows}|${canvasH}`} />
              </ReactFlow>
            </div>
            <div className="scrubber">
              <IconButton icon={playing ? 'pause' : 'play'} label={playing ? t('flow.pause') : t('flow.replay')} onClick={() => {
                if (playing) setPlaying(false);
                else {
                  if (cursor == null || cursor >= n) setCursor(1);
                  setPlaying(true);
                }
              }} />
              <input
                type="range"
                min={1}
                max={Math.max(1, n)}
                value={upto}
                aria-label={t('flow.scrub')}
                aria-valuetext={visible.length ? f.dayTime(cursorTime) : ''}
                onChange={(e) => {
                  const v = Number(e.target.value);
                  setPlaying(false);
                  setCursor(v >= n ? null : v);
                }}
              />
              <span className="scrub-time mono small">{cursor == null ? t('flow.live') : f.dayTime(cursorTime)}</span>
              <span className="muted small">{t('flow.count', { shown: upto, total: n })}</span>
              {cursor != null && <Button size="sm" variant="ghost" onClick={() => { setCursor(null); setPlaying(false); }}>{t('flow.goLive')}</Button>}
            </div>
          </Card>

          <Card className="flow-side" title={sel ? selTitle(t, graph, sel) : t('flow.recent')} subtitle={sel ? t('flow.selSub', { count: selected?.length ?? 0 }) : t('flow.recentSub')}
            actions={sel ? <IconButton icon="x" label={t('common.close')} onClick={() => setSel(null)} /> : undefined}>
            {sel && selected && <SelStats reqs={selected} />}
            <ul className="flow-reqs">
              {(selected ?? visible.slice().reverse()).slice(0, 30).map((r) => (
                <FlowReq key={r.id} r={r} on={highlightReq?.id === r.id} onClick={() => setFocusReq(focusReq === r.id ? null : r.id)} />
              ))}
            </ul>
            {sel && <Button icon="traffic" onClick={openInTraffic}>{t('flow.openTraffic')}</Button>}
          </Card>
        </div>
      )}
    </div>
  );
}

type T = ReturnType<typeof useI18n>['t'];
type F = ReturnType<typeof useI18n>['f'];

function edgeLabel(t: T, f: F, metric: Metric, s: { count: number; tokens: number; saved: number }) {
  if (metric === 'tokens') return t('flow.edge.tokens', { n: f.tokens(s.tokens) });
  if (metric === 'saved') return s.saved ? t('flow.edge.savedShort', { n: f.tokens(s.saved) }) : t('flow.edge.requests', { count: s.count });
  return t('flow.edge.requests', { count: s.count });
}

function selTitle(t: T, g: ReturnType<typeof buildGraph>, sel: NonNullable<Sel>) {
  if (sel.kind === 'node') return g.nodes.get(sel.id)?.label ?? sel.id;
  const e = g.edges.get(sel.id);
  return e ? `${g.nodes.get(e.source)?.label} → ${g.nodes.get(e.target)?.label}` : t('flow.recent');
}

function SelStats({ reqs }: { reqs: RequestRecord[] }) {
  const { t, f } = useI18n();
  const errors = reqs.filter(isFailed).length;
  const saved = reqs.reduce((s, r) => s + ((r.stages?.prune as { saved_tokens?: number } | undefined)?.saved_tokens ?? 0), 0);
  const tokens = reqs.reduce((s, r) => s + (r.usage ? r.usage.input_tokens + r.usage.cache_read_input_tokens + r.usage.cache_creation_input_tokens : 0), 0);
  return (
    <div className="mini-stats">
      <div className="stat"><div className="stat-label">{t('flow.metric.requests')}</div><div className="stat-value">{f.num(reqs.length)}</div>{errors > 0 && <div className="stat-sub tone-bad">{t('overview.kpi.errors', { count: errors })}</div>}</div>
      <div className="stat"><div className="stat-label">{t('flow.metric.tokens')}</div><div className="stat-value">{f.compact(tokens)}</div></div>
      <div className="stat"><div className="stat-label">{t('flow.metric.saved')}</div><div className="stat-value tone-good">{f.compact(saved)}</div></div>
    </div>
  );
}

function FlowReq({ r, on, onClick }: { r: RequestRecord; on: boolean; onClick: () => void }) {
  const { t, f } = useI18n();
  const c = recordClient(r);
  const p = r.stages?.prune as { saved_tokens?: number; applied?: boolean } | undefined;
  return (
    <li className={cx('flow-req', on && 'on', isFailed(r) && 'is-failed')}>
      <button type="button" onClick={onClick} aria-pressed={on} title={t('flow.highlight')}>
        <span className="mono small muted">{f.time(r.time)}</span>
        <ClientIcon client={c} size={14} />
        <span className="mono small clip">{recordModel(r) || r.path}</span>
        {p?.saved_tokens ? <Badge tone={p.applied ? 'dropped' : 'shadow'}>−{f.tokens(p.saved_tokens)}</Badge> : null}
        {isFailed(r) && <Badge tone="bad">{r.status || t('traffic.err')}</Badge>}
      </button>
      <a href={href(`traffic/${r.id}`)} className="flow-req-open" aria-label={t('flow.openRequest')} title={t('flow.openRequest')}><Icon name="external" size={13} /></a>
    </li>
  );
}

// Refit once the nodes have their real sizes, and whenever the graph's shape
// changes (window, node count, canvas height).
function FitOnChange({ keyStr }: { keyStr: string }) {
  const rf = useReactFlow();
  const ready = useNodesInitialized();
  const first = useRef(true);
  useEffect(() => {
    if (!ready) return;
    const duration = first.current ? 0 : 300;
    first.current = false;
    const id = requestAnimationFrame(() => rf.fitView({ padding: 0.06, duration }));
    return () => cancelAnimationFrame(id);
  }, [keyStr, rf, ready]);
  return null;
}

const GwNode = memo(function GwNode({ data }: NodeProps<Node<FlowNodeData>>) {
  const { t, f } = useI18n();
  const d = data;
  const vendor = d.col === 'model' ? d.vendor ?? modelVendor(d.label) : undefined;
  const stage = d.col === 'router' || d.col === 'prune' || d.col === 'recall';
  return (
    <div className={cx('fnode', `fnode-${d.col}`, d.highlighted && 'hl', d.dimmed && 'dim', d.selected && 'sel')}>
      {d.col !== 'client' && <Handle type="target" position={Position.Left} className="fh" isConnectable={false} />}
      {d.col === 'recall' && <Handle type="target" position={Position.Top} id="top" className="fh" isConnectable={false} />}
      <span className="fnode-icon">
        {d.col === 'protocol' ? (d.protocol === 'anthropic-messages' ? <BrandIcon id="anthropic" label="Anthropic" size={16} /> : d.protocol === 'systemone' ? <BrandIcon id="rlcd" label="System One" size={16} /> : <BrandIcon id="openai" label="OpenAI" size={16} />)
          : stage ? <Icon name={d.icon as 'routing'} size={16} />
            : d.col === 'client' && d.client ? <ClientIcon client={d.client} size={20} />
              : d.col === 'model' ? <BrandIcon id={vendorIcon(vendor)} label={vendor ? PROVIDER_NAMES[vendor] : d.label} size={18} />
                : <BrandIcon id={d.icon === 'custom' ? undefined : d.icon} label={d.label} size={18} />}
      </span>
      <span className="fnode-text">
        <span className={cx('fnode-label', (d.col === 'model' || d.col === 'route') && 'mono')} title={d.label}>{d.col === 'model' ? shortModel(d.label) : d.label}</span>
        <span className="fnode-sub">
          {d.col === 'recall'
            ? t('flow.edge.recalls', { count: d.stats.count })
            : d.col === 'prune' && d.stats.saved
              ? t('flow.node.saved', { n: f.tokens(d.stats.saved) })
              : d.sub && d.col !== 'route' ? d.sub : t('flow.edge.requests', { count: d.stats.count })}
        </span>
      </span>
      {d.stats.errors > 0 && d.col === 'route' && (
        <span className="fnode-err" title={[t('flow.edge.errors', { count: d.stats.errors }), ...(d.stats.errorTexts ?? [])].join('\n')}>
          <Icon name="alert" size={11} /> {d.stats.errors}
        </span>
      )}
      {!!d.stats.retried && d.col === 'route' && (
        <span className={cx('fnode-rec', d.stats.recovered === d.stats.retried && 'all')} title={t('flow.node.recoveredHint', { recovered: d.stats.recovered ?? 0, retried: d.stats.retried })}>
          <Icon name="refresh" size={11} /> {t('flow.node.recovered', { pct: f.pct((d.stats.recovered ?? 0) / d.stats.retried) })}
        </span>
      )}
      {d.col === 'route' && (
        <>
          <Handle type="source" position={Position.Right} id="fbout" className="fh fh-fb" isConnectable={false} style={{ top: '70%' }} />
          <Handle type="target" position={Position.Right} id="fbin" className="fh fh-fb" isConnectable={false} style={{ top: '30%' }} />
        </>
      )}
      {d.col !== 'model' && d.col !== 'recall' && <Handle type="source" position={Position.Right} className="fh" isConnectable={false} />}
      {d.col === 'prune' && <Handle type="source" position={Position.Bottom} id="bottom" className="fh" isConnectable={false} />}
    </div>
  );
});

function GwEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data }: EdgeProps<Edge<FlowEdgeData>>) {
  const d = data!;
  // A fallback joins two routes in the same column: loop out to the right.
  const [path, lx, ly] = d.fallback
    ? [`M${sourceX},${sourceY} C${sourceX + 80},${sourceY} ${targetX + 80},${targetY} ${targetX},${targetY}`, Math.max(sourceX, targetX) + 60, (sourceY + targetY) / 2] as const
    : getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  const errShare = d.stats.count ? d.stats.errors / d.stats.count : 0;
  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        className={cx('fedge', d.highlighted && 'hl', d.dimmed && 'dim', d.passthrough && 'pass', d.fallback && 'fb', errShare > 0.5 && !d.fallback && 'bad')}
        style={{ strokeWidth: d.width }}
        interactionWidth={Math.max(16, d.width + 8)}
      />
      {d.highlighted && <path d={path} className="fedge-flow" style={{ strokeWidth: Math.max(2, d.width * 0.45) }} />}
      {(d.showLabel || d.highlighted) && <EdgeLabelRenderer>
        <div className={cx('flabel', d.dimmed && 'dim', d.highlighted && 'hl', d.fallback && 'flabel-fb')} style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)` }}>
          <span>{d.label}</span>
          {d.savedLabel && <span className="flabel-saved">{d.savedLabel}</span>}
          {d.errorsLabel && <span className="flabel-err">{d.errorsLabel}</span>}
        </div>
      </EdgeLabelRenderer>}
    </>
  );
}

/** A model id without its vendor prefix and date suffix: the full id is the tooltip. */
function shortModel(m: string): string {
  const s = m.includes('/') ? m.slice(m.indexOf('/') + 1) : m;
  return s.replace(/-\d{8}$/, '');
}

/** A lane's title, above its nodes. */
const LaneNode = memo(function LaneNode({ data }: NodeProps<Node<FlowNodeData>>) {
  return (
    <div className="flane">
      <strong>{data.label}</strong>
      <span>{data.sub}</span>
    </div>
  );
});

const nodeTypes = { gw: GwNode, lane: LaneNode };
const edgeTypes = { gw: GwEdge };

