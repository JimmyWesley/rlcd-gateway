// The Flow editor's graph: the gateway's configuration, not its traffic.
// Keys -> protocols -> Router -> Pruner -> aliases -> routes -> models, and
// below it the decisions lane: /v1/systemone -> decision backends -> models.
// Pure functions of the loaded settings; the layout is plain column math.
import type { GatewayConfig, Protocol, RecallSettings } from '../../lib/api';
import type { DecisionSettingsView } from '../../lib/decisionsApi';
import type { KeyView } from '../../lib/keysApi';
import type { PruneConfig } from '../../lib/pruneApi';
import type { SettingsView } from '../../lib/resilienceApi';
import { parseFallback } from '../../lib/resilienceApi';
import type { AliasView, RouterRoute, RulesDoc } from '../../lib/routerApi';

export type ConfigData = {
  config: GatewayConfig;
  routes: RouterRoute[];
  aliases: AliasView[];
  rules: RulesDoc;
  prune: PruneConfig;
  keys: KeyView[];
  res: SettingsView;
  dec: DecisionSettingsView;
  recall: RecallSettings;
};

export type CfgKind =
  | 'key' | 'nokey' | 'proto' | 'router' | 'prune' | 'recall' | 'economy'
  | 'alias' | 'route' | 'model' | 'systemone' | 'dbackend' | 'dmap' | 'head' | 'lane' | 'switch';

export type CfgNodeData = {
  kind: CfgKind;
  id: string;
  title: string;
  sub?: string;
  icon?: string;
  /** Small labelled values shown on the node (mode, preset, ...). */
  chips?: { label: string; tone?: 'good' | 'warn' | 'accent' | 'shadow' | 'bad' | 'neutral'; action?: 'toggleMode' }[];
  /** Ordered lines inside the node (the router's rules). */
  lines?: { text: string; on: boolean }[];
  /** A decision rule's outputs, each with its own handle ("br:0", ..., "br:else"). */
  branches?: { text: string; handle: string; empty?: boolean }[];
  /** The rule's index in rules[] (Switch nodes). */
  rule?: number;
  off?: boolean;
  protocols?: Protocol[];
  /** Column heads carry an add button. */
  add?: 'alias' | 'route' | 'rule';
  error?: string;
  editable: boolean;
  width: number;
  height: number;
};

export type CfgEdgeKind = 'flow' | 'alias' | 'fallback' | 'model' | 'economy' | 'mirror' | 'key' | 'branch';
export type CfgEdge = { id: string; source: string; target: string; kind: CfgEdgeKind; label?: string; sourceHandle?: string; targetHandle?: string; index?: number };
export type CfgNode = { id: string; x: number; y: number; data: CfgNodeData };

type L = {
  noKey: string; noKeySub: string; router: string; prune: string; recall: string; economy: string;
  protocol: (p: Protocol) => string; endpoint: (p: Protocol) => string;
  rule: (name: string, target: string) => string; defaults: (a: string, o: string) => string;
  mode: (m: string) => string; preset: (p: string) => string; profile: (p: string) => string;
  on: string; off: string; keepsModel: string; aliasSub: (route: string, model: string) => string;
  heads: Record<'keys' | 'protocols' | 'router' | 'prune' | 'aliases' | 'routes' | 'models', string>;
  lane: { proxy: string; proxySub: string; decisions: string; decisionsSub: string };
  systemone: string; backend: string; mirror: (b: string, pct: string) => string; defaultBackend: string; maps: string;
  recallSub: (on: boolean) => string; economySub: string; keySub: (k: KeyView) => string;
  switchSub: (r: RulesDoc['rules'][number]) => string; branch: (r: RulesDoc['rules'][number], i: number) => string;
};

const X = { keys: 0, protocols: 260, router: 520, prune: 790, aliases: 1060, routes: 1320, models: 1700 } as const;
export const CFG_W = X.models + 240;
const GAP = 18;
const W = 200;

const PROXY_PROTOCOLS: Protocol[] = ['anthropic-messages', 'openai-chat', 'openai-responses'];

export function buildConfigGraph(d: ConfigData, l: L, errors: Record<string, string>): { nodes: CfgNode[]; edges: CfgEdge[] } {
  const nodes: CfgNode[] = [];
  const edges: CfgEdge[] = [];
  const colY: Record<string, number> = {};
  const put = (col: keyof typeof X, data: Omit<CfgNodeData, 'width' | 'height'> & { width?: number; height?: number }) => {
    const height = data.height ?? 58;
    const y = colY[col] ?? 44;
    nodes.push({ id: data.id, x: X[col], y, data: { width: W, ...data, height, error: errors[data.id] } });
    colY[col] = y + height + GAP;
  };
  const head = (col: keyof typeof X, add?: CfgNodeData['add']) => {
    nodes.push({ id: `head:${col}`, x: X[col], y: 0, data: { kind: 'head', id: `head:${col}`, title: l.heads[col], add, editable: false, width: W, height: 28 } });
  };

  // ---- LLM proxy lane ----
  (['keys', 'protocols', 'router', 'prune', 'aliases', 'routes', 'models'] as const).forEach((c) =>
    head(c, c === 'aliases' ? 'alias' : c === 'routes' ? 'route' : c === 'router' ? 'rule' : undefined));

  const keys = d.keys.filter((k) => !k.revoked);
  for (const k of keys) put('keys', { kind: 'key', id: `key:${k.id}`, title: k.name, sub: l.keySub(k), icon: 'key', editable: true });
  if (!d.config.require_keys) put('keys', { kind: 'nokey', id: 'nokey', title: l.noKey, sub: l.noKeySub, icon: 'apps', editable: true });

  for (const p of PROXY_PROTOCOLS) {
    put('protocols', { kind: 'proto', id: `proto:${p}`, title: l.protocol(p), sub: l.endpoint(p), editable: false, protocols: [p] });
    edges.push({ id: `e:proto:${p}->router`, source: `proto:${p}`, target: 'router', kind: 'flow' });
    for (const k of keys) edges.push({ id: `e:key:${k.id}->${p}`, source: `key:${k.id}`, target: `proto:${p}`, kind: 'key' });
    if (!d.config.require_keys) edges.push({ id: `e:nokey->${p}`, source: 'nokey', target: `proto:${p}`, kind: 'key' });
  }

  const openaiDefault = d.routes.find((r) => r.openai_default)?.name ?? '';
  const lines = d.rules.rules.map((r) => ({ text: l.rule(r.name, r.kind === 'auto' ? 'auto' : r.route ?? ''), on: r.enabled }));
  put('router', {
    kind: 'router', id: 'router', title: l.router, sub: l.defaults(d.config.active_route, openaiDefault), icon: 'routing', editable: true,
    lines, height: 66 + Math.max(1, lines.length) * 20,
  });
  const ps = d.prune.settings;
  const mode = ps.mode || 'shadow';
  put('prune', {
    kind: 'prune', id: 'prune', title: l.prune, icon: 'savings', editable: true, height: 96,
    chips: [
      { label: l.mode(mode), tone: mode === 'enforce' ? 'accent' : 'shadow', action: 'toggleMode' },
      { label: l.preset(ps.preset || 'balanced') },
      { label: l.profile(ps.profile || 'auto') },
      ...(ps.enabled === false ? [{ label: l.off, tone: 'bad' as const }] : []),
    ],
  });
  edges.push({ id: 'e:router->prune', source: 'router', target: 'prune', kind: 'flow' });

  // Decision rules: Switch nodes under the router, one output per branch.
  const targetId = (t: { route?: string; alias?: string } | null | undefined) => (t?.alias ? `alias:${t.alias}` : t?.route ? `route:${t.route}` : null);
  d.rules.rules.forEach((r, i) => {
    if (r.kind !== 'decision') return;
    const bs = r.branches ?? [];
    const els = targetId(r.else);
    put('router', {
      kind: 'switch', id: `drule:${i}`, title: r.name, sub: l.switchSub(r), icon: 'cpu', editable: true, rule: i, off: !r.enabled,
      branches: [...bs.map((b, j) => ({ text: l.branch(r, j), handle: `br:${j}`, empty: !targetId(b.then) })), { text: l.branch(r, -1), handle: 'br:else', empty: !els }],
      height: 74 + (bs.length + 1) * 22,
    });
    edges.push({ id: `e:router->drule:${i}`, source: 'router', target: `drule:${i}`, kind: 'economy', sourceHandle: 'down', targetHandle: 'top' });
    bs.forEach((b, j) => {
      const to = targetId(b.then);
      if (to) edges.push({ id: `br:${i}:${j}`, source: `drule:${i}`, target: to, kind: 'branch', sourceHandle: `br:${j}`, targetHandle: 'in' });
    });
    if (els) edges.push({ id: `br:${i}:else`, source: `drule:${i}`, target: els, kind: 'branch', sourceHandle: 'br:else', targetHandle: 'in' });
  });

  // The economy model serves the pruner and the router's auto rule; recall hangs below the pruner.
  colY.router = Math.max(colY.router ?? 0, colY.prune ?? 0) + 30;
  put('router', { kind: 'economy', id: 'economy', title: l.economy, sub: `${d.config.selector.backend} · ${d.config.selector.model}`, icon: 'cpu', editable: true });
  edges.push({ id: 'e:economy->prune', source: 'economy', target: 'prune', kind: 'economy' });
  if (d.rules.rules.some((r) => r.kind === 'auto')) edges.push({ id: 'e:economy->router', source: 'economy', target: 'router', kind: 'economy', sourceHandle: 'top', targetHandle: 'bottom' });
  colY.prune = (colY.prune ?? 0) + 30;
  put('prune', { kind: 'recall', id: 'recall', title: l.recall, sub: l.recallSub(d.recall.enabled), icon: 'recall', editable: true });
  edges.push({ id: 'e:prune->recall', source: 'prune', target: 'recall', kind: 'economy', sourceHandle: 'bottom', targetHandle: 'top' });

  for (const a of d.aliases) {
    put('aliases', { kind: 'alias', id: `alias:${a.name}`, title: a.name, sub: l.aliasSub(a.route, a.model ?? ''), editable: true, protocols: a.protocols });
    edges.push({ id: `e:alias:${a.name}`, source: `alias:${a.name}`, target: `route:${a.route}`, kind: 'alias', sourceHandle: 'out' });
  }

  const models = new Set<string>();
  for (const r of d.routes) {
    const eff = d.res.effective.routes[r.name];
    put('routes', {
      kind: 'route', id: `route:${r.name}`, title: r.name, sub: r.model || l.keepsModel, icon: r.provider, editable: true, protocols: r.protocols,
      chips: [
        ...(r.name === d.config.active_route ? [{ label: 'Anthropic', tone: 'accent' as const }] : []),
        ...(r.openai_default ? [{ label: 'OpenAI', tone: 'accent' as const }] : []),
      ],
      height: r.name === d.config.active_route || r.openai_default ? 76 : 58,
    });
    edges.push({ id: `e:prune->route:${r.name}`, source: 'prune', target: `route:${r.name}`, kind: 'flow' });
    if (r.model) {
      models.add(r.model);
      edges.push({ id: `e:route:${r.name}->model:${r.model}`, source: `route:${r.name}`, target: `model:${r.model}`, kind: 'model' });
    }
    (eff?.fallbacks ?? []).forEach((fb, i) => {
      const p = parseFallback(fb);
      edges.push({
        id: `fb:${r.name}:${i}`, source: `route:${r.name}`, target: `route:${p.route}`, kind: 'fallback',
        sourceHandle: 'fbout', targetHandle: 'fbin', label: `${i + 1}${p.model ? ` · ${p.model}` : ''}`, index: i,
      });
    });
  }
  for (const a of d.aliases) if (a.model) models.add(a.model);
  for (const m of [...models].sort()) put('models', { kind: 'model', id: `model:${m}`, title: m, icon: 'model', editable: false });
  for (const a of d.aliases) if (a.model) edges.push({ id: `e:alias:${a.name}->model`, source: `route:${a.route}`, target: `model:${a.model}`, kind: 'model' });

  // ---- Decisions lane ----
  const top = Math.max(...Object.values(colY)) + 80;
  nodes.push({ id: 'lane:decisions', x: -20, y: top - 56, data: { kind: 'lane', id: 'lane:decisions', title: l.lane.decisions, sub: l.lane.decisionsSub, editable: false, width: 400, height: 40 } });
  nodes.push({ id: 'lane:proxy', x: -20, y: -64, data: { kind: 'lane', id: 'lane:proxy', title: l.lane.proxy, sub: l.lane.proxySub, editable: false, width: 400, height: 40 } });
  for (const c of Object.keys(colY)) colY[c] = top;
  put('protocols', { kind: 'systemone', id: 'proto:systemone', title: l.systemone, sub: '/v1/systemone', editable: true, protocols: ['systemone'] });
  for (const k of keys) edges.push({ id: `e:key:${k.id}->systemone`, source: `key:${k.id}`, target: 'proto:systemone', kind: 'key' });
  if (!d.config.require_keys) edges.push({ id: 'e:nokey->systemone', source: 'nokey', target: 'proto:systemone', kind: 'key' });
  const dec = d.dec;
  for (const b of dec.backends) {
    const def = b.name === (dec.default_backend || 'economy');
    const mir = dec.mirror?.backend === b.name;
    put('routes', {
      kind: 'dbackend', id: `dbackend:${b.name}`, title: b.name, sub: b.base_url.replace(/^https?:\/\//, ''), icon: b.provider, editable: true,
      chips: [...(def ? [{ label: l.defaultBackend, tone: 'accent' as const }] : []), ...(mir ? [{ label: l.mirror(b.name, `${Math.round((dec.mirror!.sample_rate) * 100)}%`), tone: 'shadow' as const }] : [])],
      height: def || mir ? 76 : 58,
    });
    edges.push({ id: `e:systemone->${b.name}`, source: 'proto:systemone', target: `dbackend:${b.name}`, kind: 'flow' });
  }
  for (const [pattern, backend] of Object.entries(dec.models)) {
    put('models', { kind: 'dmap', id: `dmap:${pattern}`, title: pattern, sub: l.maps, icon: 'model', editable: true });
    edges.push({ id: `e:dmap:${pattern}`, source: `dbackend:${backend}`, target: `dmap:${pattern}`, kind: 'model' });
  }
  if (dec.mirror) {
    const from = dec.default_backend || 'economy';
    if (from !== dec.mirror.backend) edges.push({ id: 'e:mirror', source: `dbackend:${from}`, target: `dbackend:${dec.mirror.backend}`, kind: 'mirror', sourceHandle: 'fbout', targetHandle: 'fbin' });
  }
  return { nodes, edges };
}

/** Routes a fallback may point at: another route speaking one of the primary's protocols. */
export function sharesProtocol(a: RouterRoute | undefined, b: RouterRoute | undefined): boolean {
  return !!a && !!b && a.protocols.some((p) => b.protocols.includes(p));
}
