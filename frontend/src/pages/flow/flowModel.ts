// Builds the Flow graph from request records: clients -> protocol entry
// points -> router -> pruner -> routes -> models. Pure functions, so the graph is the same
// whether it is drawn live or replayed up to a point in time.
import type { Protocol, RequestRecord } from '../../lib/api';
import { isFailed, protocolOf } from '../../lib/api';
import { isDecisionCall, isModelCall, recordClient, recordModel, recordProvider, recordVendor, type ProviderId, type ResolvedClient } from '../../lib/brands';

export type Col = 'client' | 'protocol' | 'router' | 'prune' | 'recall' | 'route' | 'model';

export type NodeStats = { count: number; errors: number; tokens: number; saved: number; savedUsd: number; errorTexts?: string[]; retried?: number; recovered?: number };
/** Two lanes: the LLM proxy, and decisions your own systems make through /v1/systemone. */
export type Lane = 'proxy' | 'decisions';
export type FlowNodeData = {
  col: Col;
  lane?: Lane;
  key: string;
  label: string;
  sub?: string;
  icon?: string;
  client?: ResolvedClient;
  protocol?: Protocol;
  vendor?: ProviderId;
  stats: NodeStats;
  highlighted?: boolean;
  dimmed?: boolean;
  selected?: boolean;
};
export type FlowEdgeData = {
  stats: NodeStats;
  width: number;
  passthrough?: boolean;
  /** A primary route -> fallback route edge: requests the fallback served. */
  fallback?: boolean;
  highlighted?: boolean;
  dimmed?: boolean;
  /** Labels only show on the main edges, and on the one under the pointer. */
  showLabel?: boolean;
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

const clientKey = (r: RequestRecord) => {
  const c = recordClient(r);
  return `client:${c.id}${c.keyName ? `|${c.keyName}` : ''}`;
};

/** A decision one of your systems asked for (not the gateway's own pruning or routing call). */
export const isExternalDecision = (r: RequestRecord) => isDecisionCall(r) && (r.decisions?.source ?? 'client') === 'client';
const decisionBackend = (r: RequestRecord) => r.decisions?.backend || r.route;

/** The node ids a request passes through, in order. */
export function pathOf(r: RequestRecord): string[] {
  if (isDecisionCall(r)) {
    const ids = [`d${clientKey(r)}`, 'proto:systemone', `dbackend:${decisionBackend(r)}`];
    const m = recordModel(r);
    if (m) ids.push(`dmodel:${m}`);
    return ids;
  }
  const ids = [clientKey(r), `proto:${protocolOf(r)}`];
  if (isModelCall(r)) ids.push('router', 'prune');
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
  edges: Map<string, { source: string; target: string; stats: NodeStats; passthrough: boolean; fallback?: boolean }>;
  byNode: Map<string, RequestRecord[]>;
  byEdge: Map<string, RequestRecord[]>;
};

type Labels = { unknownClient: string; router: string; prune: string; recall: string; decisionBackend: string; protocol: (p: Protocol) => string };

const ENDPOINT: Record<Protocol, string> = {
  'anthropic-messages': '/v1/messages',
  'openai-chat': '/v1/chat/completions',
  'openai-responses': '/v1/responses',
  systemone: '/v1/systemone',
};

export function buildGraph(reqs: RequestRecord[], labels: Labels, recalls: number): Graph {
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
  node('router', () => ({ col: 'router', label: labels.router, icon: 'routing' }));
  node('prune', () => ({ col: 'prune', label: labels.prune, icon: 'savings' }));
  const recall = node('recall', () => ({ col: 'recall', label: labels.recall, sub: 'MCP', icon: 'recall' }));
  recall.stats.count = recalls;

  const add = (s: NodeStats, r: RequestRecord, p: Prune | undefined) => {
    s.count++;
    if (isFailed(r)) {
      s.errors++;
      const msg = r.error || String(r.status);
      s.errorTexts ??= [];
      if (!s.errorTexts.includes(msg) && s.errorTexts.length < 5) s.errorTexts.push(msg);
    }
    s.tokens += billed(r);
    if (r.retried) {
      s.retried = (s.retried ?? 0) + 1;
      if (r.recovered) s.recovered = (s.recovered ?? 0) + 1;
    }
    if (p?.saved_tokens) {
      s.saved += p.saved_tokens;
      s.savedUsd += (p.est_cost_before ?? 0) - (p.est_cost_after ?? 0);
    }
  };

  for (const r of reqs) {
    // The gateway's own economy-model calls are part of pruning and routing, not a lane of their own.
    if (isDecisionCall(r) && !isExternalDecision(r)) continue;
    const c = recordClient(r);
    const model = recordModel(r);
    const p = r.stages?.prune as Prune | undefined;
    const proto = protocolOf(r);
    const dec = isDecisionCall(r);
    const lane: Lane = dec ? 'decisions' : 'proxy';
    node(`${dec ? 'd' : ''}${clientKey(r)}`, () => ({
      col: 'client', lane, label: c.keyName || c.name || labels.unknownClient,
      sub: c.keyName ? c.name || labels.unknownClient : c.inferred ? undefined : c.version, icon: c.icon, client: c,
    }));
    node(`proto:${proto}`, () => ({ col: 'protocol', lane, label: labels.protocol(proto), sub: ENDPOINT[proto], protocol: proto }));
    if (dec) {
      const b = decisionBackend(r);
      node(`dbackend:${b}`, () => ({ col: 'route', lane, label: b, sub: labels.decisionBackend, icon: recordProvider(r) }));
      if (model) node(`dmodel:${model}`, () => ({ col: 'model', lane, label: model, icon: 'model', vendor: recordVendor(r) }));
    } else {
      node(`route:${r.route}`, () => ({ col: 'route', lane, label: r.route, sub: r.upstream.replace(/^https?:\/\//, ''), icon: recordProvider(r) }));
      if (model) node(`model:${model}`, () => ({ col: 'model', lane, label: model, icon: 'model', vendor: recordVendor(r) }));
    }
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
        e = { source: path[i - 1], target: path[i], stats: empty(), passthrough: path[i - 1].startsWith('proto:') && !isModelCall(r) && !dec };
        edges.set(id, e);
      }
      add(e.stats, r, p);
      let l = byEdge.get(id);
      if (!l) byEdge.set(id, (l = []));
      l.push(r);
    }
    // The primary route failed and a fallback served the request.
    if (r.fallback_route && r.primary_route && r.primary_route !== r.route) {
      const from = `route:${r.primary_route}`;
      node(from, () => ({ col: 'route', label: r.primary_route!, sub: '', icon: undefined }));
      const id = edgeId(from, `route:${r.route}`);
      let e = edges.get(id);
      if (!e) {
        e = { source: from, target: `route:${r.route}`, stats: empty(), passthrough: false, fallback: true };
        edges.set(id, e);
      }
      add(e.stats, r, p);
      let l = byEdge.get(id);
      if (!l) byEdge.set(id, (l = []));
      l.push(r);
      let n = byNode.get(from);
      if (!n) byNode.set(from, (n = []));
      n.push(r);
    }
  }
  if (recalls > 0) edges.set(edgeId('prune', 'recall'), { source: 'prune', target: 'recall', stats: { ...empty(), count: recalls }, passthrough: false });
  return { nodes, edges, byNode, byEdge };
}

export const COL_X: Record<Col, number> = { client: 0, protocol: 270, router: 530, prune: 760, recall: 760, route: 1010, model: 1290 };
/** Width of the laid-out graph: the model column plus a model node. */
export const GRAPH_W = 1290 + 220;
export const ROW_H = 84;
