// Builds the Flow graph from request records: clients -> gateway -> router
// -> pruner -> routes -> models. Pure functions, so the graph is the same
// whether it is drawn live or replayed up to a point in time.
import type { RequestRecord } from '../../lib/api';
import { isFailed } from '../../lib/api';
import { isMessagesPath, recordClient, recordModel, recordProvider, type ResolvedClient } from '../../lib/brands';

export type Col = 'client' | 'gateway' | 'router' | 'prune' | 'recall' | 'route' | 'model';

export type NodeStats = { count: number; errors: number; tokens: number; saved: number; savedUsd: number };
export type FlowNodeData = {
  col: Col;
  key: string;
  label: string;
  sub?: string;
  icon?: string;
  client?: ResolvedClient;
  stats: NodeStats;
  highlighted?: boolean;
  dimmed?: boolean;
  selected?: boolean;
};
export type FlowEdgeData = {
  stats: NodeStats;
  width: number;
  passthrough?: boolean;
  highlighted?: boolean;
  dimmed?: boolean;
  showSaved?: boolean;
  label: string;
  savedLabel?: string;
  errorsLabel?: string;
};

export type Metric = 'requests' | 'tokens' | 'saved';

const empty = (): NodeStats => ({ count: 0, errors: 0, tokens: 0, saved: 0, savedUsd: 0 });

type Prune = { saved_tokens?: number; est_cost_before?: number; est_cost_after?: number };

export function billed(r: RequestRecord): number {
  const u = r.usage;
  return u ? u.input_tokens + u.cache_read_input_tokens + u.cache_creation_input_tokens : r.est_tokens || 0;
}

/** The node ids a request passes through, in order. */
export function pathOf(r: RequestRecord): string[] {
  const c = recordClient(r);
  const ids = [`client:${c.id}`, 'gateway'];
  if (isMessagesPath(r.path)) ids.push('router', 'prune');
  ids.push(`route:${r.route}`);
  const m = recordModel(r);
  if (m) ids.push(`model:${m}`);
  return ids;
}

export const edgeId = (a: string, b: string) => `${a}->${b}`;

export function edgesOfPath(p: string[]): string[] {
  const out: string[] = [];
  for (let i = 1; i < p.length; i++) out.push(edgeId(p[i - 1], p[i]));
  return out;
}

export type Graph = {
  nodes: Map<string, FlowNodeData>;
  edges: Map<string, { source: string; target: string; stats: NodeStats; passthrough: boolean }>;
  byNode: Map<string, RequestRecord[]>;
  byEdge: Map<string, RequestRecord[]>;
};

export function buildGraph(reqs: RequestRecord[], labels: { unknownClient: string; gateway: string; router: string; prune: string; recall: string }, listen: string, recalls: number): Graph {
  const nodes = new Map<string, FlowNodeData>();
  const edges: Graph['edges'] = new Map();
  const byNode = new Map<string, RequestRecord[]>();
  const byEdge = new Map<string, RequestRecord[]>();

  const node = (id: string, init: () => Omit<FlowNodeData, 'stats' | 'key'>) => {
    let n = nodes.get(id);
    if (!n) {
      n = { ...init(), key: id, stats: empty() };
      nodes.set(id, n);
    }
    return n;
  };
  node('gateway', () => ({ col: 'gateway', label: labels.gateway, sub: listen, icon: 'rlcd' }));
  node('router', () => ({ col: 'router', label: labels.router, icon: 'routing' }));
  node('prune', () => ({ col: 'prune', label: labels.prune, icon: 'savings' }));
  const recall = node('recall', () => ({ col: 'recall', label: labels.recall, sub: 'MCP', icon: 'recall' }));
  recall.stats.count = recalls;

  const add = (s: NodeStats, r: RequestRecord, p: Prune | undefined) => {
    s.count++;
    if (isFailed(r)) s.errors++;
    s.tokens += billed(r);
    if (p?.saved_tokens) {
      s.saved += p.saved_tokens;
      s.savedUsd += (p.est_cost_before ?? 0) - (p.est_cost_after ?? 0);
    }
  };

  for (const r of reqs) {
    const c = recordClient(r);
    const model = recordModel(r);
    const p = r.stages?.prune as Prune | undefined;
    node(`client:${c.id}`, () => ({ col: 'client', label: c.name || labels.unknownClient, sub: c.inferred && c.name ? undefined : c.version, icon: c.icon, client: c }));
    node(`route:${r.route}`, () => ({ col: 'route', label: r.route, sub: r.upstream.replace(/^https?:\/\//, ''), icon: recordProvider(r) }));
    if (model) node(`model:${model}`, () => ({ col: 'model', label: model, icon: 'model' }));
    const path = pathOf(r);
    for (const id of path) {
      add(nodes.get(id)!.stats, r, p);
      let l = byNode.get(id);
      if (!l) byNode.set(id, (l = []));
      l.push(r);
    }
    for (let i = 1; i < path.length; i++) {
      const id = edgeId(path[i - 1], path[i]);
      let e = edges.get(id);
      if (!e) {
        e = { source: path[i - 1], target: path[i], stats: empty(), passthrough: path[i - 1] === 'gateway' && !isMessagesPath(r.path) };
        edges.set(id, e);
      }
      add(e.stats, r, p);
      let l = byEdge.get(id);
      if (!l) byEdge.set(id, (l = []));
      l.push(r);
    }
  }
  if (recalls > 0) edges.set(edgeId('prune', 'recall'), { source: 'prune', target: 'recall', stats: { ...empty(), count: recalls }, passthrough: false });
  return { nodes, edges, byNode, byEdge };
}

export const COL_X: Record<Col, number> = { client: 0, gateway: 250, router: 480, prune: 710, recall: 710, route: 960, model: 1230 };
export const ROW_H = 96;
