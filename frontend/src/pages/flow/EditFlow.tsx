// The Flow editor: the gateway's configuration as a graph you can change.
// Click a node for its settings; drag an alias onto another route to
// re-point it, or a route onto another to add a fallback; every direct
// change asks first, saves through the same API as the forms, and can be undone.
import { createContext, memo, useCallback, useContext, useEffect, useMemo, useRef, useState, type MouseEvent as RMouseEvent } from 'react';
import {
  BaseEdge, Controls, EdgeLabelRenderer, Handle, MiniMap, Position, ReactFlow, getBezierPath, useNodesInitialized, useReactFlow,
  type Connection, type Edge, type EdgeProps, type Node, type NodeProps,
} from '@xyflow/react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import { Icon, type IconName } from '../../icons/Icon';
import { api, recallApi, type Protocol } from '../../lib/api';
import { decisionsApi } from '../../lib/decisionsApi';
import { keysApi } from '../../lib/keysApi';
import { pruneApi } from '../../lib/pruneApi';
import { resilienceApi, type Override } from '../../lib/resilienceApi';
import { routerApi, targetString, type Alias, type Rule, type RulesDoc, type Target } from '../../lib/routerApi';
import { targetLabel, whenText } from '../routing/DecisionRule';
import { navigate } from '../../lib/router';
import { useTheme } from '../../lib/theme';
import { useWidth } from '../../charts';
import { useFetch, useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Drawer, ErrorState, Loading, cx } from '../../ui';
import { buildConfigGraph, CFG_W, sharesProtocol, type CfgEdge, type CfgEdgeKind, type CfgNodeData, type ConfigData } from './configGraph';
import {
  AliasPanel, DecisionSettings, EconomyModel, KeyPanel, NoKeyPanel, PruneSettings, RecallPanel, RoutePanel, RouterPanel, SwitchPanel, type Undoable,
} from './NodePanels';

type Proposal = { title: string; lines: string[]; x: number; y: number; nodeId: string; run: () => Promise<Undoable> };
type Toast = { text: string; tone: 'good' | 'bad'; undo?: () => Promise<unknown> };
type EdgeData = { kind: CfgEdgeKind; label?: string; onDelete?: (e: RMouseEvent) => void };

/** Node actions reach the custom nodes through context, not node data. */
const Ctx = createContext<{ open: (id: string) => void; toggleMode: (e: RMouseEvent) => void; add: (what: 'alias' | 'route' | 'rule') => void; addBranch: (rule: number) => void }>({
  open: () => {}, toggleMode: () => {}, add: () => {}, addBranch: () => {},
});

const msg = (e: unknown) => String(e instanceof Error ? e.message : e);

async function loadAll(): Promise<ConfigData> {
  const [config, routes, aliases, rules, prune, keys, res, dec, recall] = await Promise.all([
    api.config(), routerApi.routes(), routerApi.aliases(), routerApi.rules(), pruneApi.config(), keysApi.list(),
    resilienceApi.settings(), decisionsApi.settings(), recallApi.settings(),
  ]);
  return { config, routes, aliases, rules, prune, keys, res, dec, recall };
}

export function EditFlow({ openNode, onOpen, onSeeTraffic }: { openNode: string | null; onOpen: (id: string | null) => void; onSeeTraffic: (id: string) => void }) {
  const { t, f } = useI18n();
  const { resolved } = useTheme();
  const { reloadConfig } = useGateway();
  const data = useFetch(loadAll, []);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState<Toast | null>(null);
  const [adding, setAdding] = useState<'alias' | 'route' | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [newBranch, setNewBranch] = useState(false);
  const canvas = useRef<HTMLDivElement>(null);
  const [sizer, width] = useWidth<HTMLDivElement>();
  // onConnect fires before onConnectEnd, which knows where the pointer was let go.
  const pending = useRef<Connection | null>(null);
  const d = data.data;

  useEffect(() => {
    if (!toast) return;
    const id = window.setTimeout(() => setToast(null), toast.tone === 'bad' ? 12000 : 9000);
    return () => window.clearTimeout(id);
  }, [toast]);

  const reload = useCallback(() => { data.reload(); reloadConfig(); }, [data, reloadConfig]);
  const saved = useCallback((u?: Undoable) => {
    reload();
    if (u) setToast({ text: u.text, tone: 'good', undo: u.undo });
  }, [reload]);
  const failed = useCallback((nodeId: string, e: string) => {
    setErrors((m) => ({ ...m, [nodeId]: e }));
    setToast({ text: e, tone: 'bad' });
  }, []);

  const L = useMemo(() => ({
    noKey: t('fedit.node.noKey'), noKeySub: t('fedit.node.noKeySub'), router: t('flow.node.router'), prune: t('flow.node.prune'),
    recall: t('flow.node.recall'), economy: t('fedit.node.economy'),
    protocol: (p: Protocol) => t(`protocol.node.${p}`), endpoint: (p: Protocol) => ({ 'anthropic-messages': '/v1/messages', 'openai-chat': '/v1/chat/completions', 'openai-responses': '/v1/responses', systemone: '/v1/systemone' }[p]),
    rule: (name: string, target: string) => `${name} → ${target}`,
    defaults: (a: string, o: string) => t('fedit.node.defaults', { a, o: o || '—' }),
    mode: (m: string) => t(`fedit.mode.${m === 'enforce' ? 'enforce' : 'shadow'}`), preset: (p: string) => p, profile: (p: string) => p,
    on: t('rset.on'), off: t('rset.off'), keepsModel: t('routes.keepsModel'),
    aliasSub: (route: string, model: string) => (model ? `→ ${route} · ${model}` : `→ ${route}`),
    heads: {
      keys: t('fedit.head.keys'), protocols: t('fedit.head.protocols'), router: t('fedit.head.router'), prune: t('fedit.head.prune'),
      aliases: t('fedit.head.aliases'), routes: t('fedit.head.routes'), models: t('fedit.head.models'),
    },
    lane: { proxy: t('flow.lane.proxy'), proxySub: t('flow.lane.proxySub'), decisions: t('flow.lane.decisions'), decisionsSub: t('flow.lane.decisionsSub') },
    systemone: t('protocol.node.systemone'), backend: t('flow.node.decisionBackend'),
    mirror: (_b: string, pct: string) => t('fedit.node.mirror', { pct }), defaultBackend: t('dset.default'), maps: t('fedit.node.maps'),
    recallSub: (on: boolean) => (on ? t('fedit.node.recallOn') : t('fedit.node.recallOff')), economySub: '',
    keySub: (k: { rpm?: number; tokens_per_day?: number; aliases: string[]; routes: string[] }) =>
      [k.rpm ? t('fedit.node.rpm', { n: k.rpm }) : '', k.aliases.length || k.routes.length ? t('fedit.node.scoped') : t('fedit.node.anyModel')].filter(Boolean).join(' · '),
    switchSub: (r: Rule) => `${r.backend || t('drule.economyShort')} · ${t(`drule.type.${r.question?.type ?? 'choice'}`)}`,
    branch: (r: Rule, i: number) => (i === -1
      ? `${t('drule.else')} → ${targetLabel(r.else, t('drule.nextRule'))}`
      : `${r.branches![i].label || whenText(r, r.branches![i].when, f)} → ${targetLabel(r.branches![i].then, '—')}`),
  }), [t, f]);

  const graph = useMemo(() => (d ? buildConfigGraph(d, L, errors) : null), [d, L, errors]);

  // From the view's pencil: a key or a decision rule is known there by its name only.
  useEffect(() => {
    if (!d || !openNode) return;
    if (openNode.startsWith('keyname:')) {
      const k = d.keys.find((x) => x.name === openNode.slice(8));
      onOpen(k ? `key:${k.id}` : null);
    } else if (openNode.startsWith('drulename:')) {
      const i = d.rules.rules.findIndex((r) => r.kind === 'decision' && r.name === openNode.slice(10));
      onOpen(i >= 0 ? `drule:${i}` : null);
    }
  }, [d, openNode, onOpen]);

  const seeTraffic = (id: string) => {
    const [kind, ...rest] = id.split(':');
    const v = rest.join(':');
    if (kind === 'key') return onSeeTraffic(`keyname:${d?.keys.find((k) => k.id === v)?.name ?? ''}`);
    if (kind === 'alias') return navigate('traffic', { q: v });
    if (kind === 'economy') return onSeeTraffic('prune');
    if (kind === 'drule') return onSeeTraffic(`switch:${d?.rules.rules[Number(v)]?.name ?? ''}`);
    onSeeTraffic(id);
  };

  const at = (e: { clientX: number; clientY: number }) => {
    // Beside the pointer, never over what was just connected: left of it near the right edge.
    const r = canvas.current?.getBoundingClientRect();
    const w = r?.width ?? 600, px = e.clientX - (r?.left ?? 0), py = e.clientY - (r?.top ?? 0);
    const x = px + 360 > w ? px - 350 : px + 24;
    return { x: Math.max(12, Math.min(w - 332, x)), y: Math.max(12, Math.min((r?.height ?? 400) - 170, py - 40)) };
  };

  // ---- direct manipulation ----
  const override = (route: string): Override | undefined => d?.res.settings.routes[route];
  const setFallbacks = (route: string, list: string[]) => {
    const prev = override(route);
    const next: Override = { ...(prev ?? {}), fallbacks: list };
    if (!list.length) delete next.fallbacks;
    return {
      save: () => resilienceApi.save({ routes: { [route]: Object.keys(next).length ? next : null } }),
      undo: () => resilienceApi.save({ routes: { [route]: prev ?? null } }),
    };
  };

  const onConnect = (c: Connection, pos: { x: number; y: number }) => {
    if (!d || !c.source || !c.target) return;
    if (c.source.startsWith('alias:') && c.target.startsWith('route:')) {
      const alias = d.aliases.find((a) => `alias:${a.name}` === c.source);
      const to = c.target.slice(6);
      if (!alias || alias.route === to) return;
      const toRoute = d.routes.find((r) => r.name === to);
      const prev: Alias[] = d.aliases.map((a) => ({ name: a.name, route: a.route, model: a.model || undefined, description: a.description || undefined }));
      const next = prev.map((a) => (a.name === alias.name ? { ...a, route: to } : a));
      setProposal({
        ...pos, nodeId: c.source, title: t('fedit.confirm.repoint', { alias: alias.name }),
        lines: [
          t('fedit.confirm.fromTo', { from: alias.route, to }),
          ...(toRoute ? [t('fedit.confirm.serves', { protocols: toRoute.protocols.map((p) => t(`protocol.short.${p}`)).join(', ') })] : []),
          ...(alias.model ? [t('fedit.confirm.keepsModel', { model: alias.model })] : []),
        ],
        run: async () => {
          await routerApi.saveAliases(next);
          return { text: t('fedit.done.repoint', { alias: alias.name, to }), undo: () => routerApi.saveAliases(prev) };
        },
      });
      return;
    }
    if (c.source.startsWith('drule:') && c.sourceHandle?.startsWith('br:') && (c.target.startsWith('route:') || c.target.startsWith('alias:'))) {
      const ri = Number(c.source.slice(6));
      const rule = d.rules.rules[ri];
      if (!rule) return;
      const which = c.sourceHandle.slice(3);
      const to: Target = c.target.startsWith('alias:') ? { alias: c.target.slice(6) } : { route: c.target.slice(6) };
      const prev = d.rules;
      const cur = which === 'else' ? rule.else : rule.branches?.[Number(which)]?.then;
      const next: RulesDoc = { ...prev, rules: prev.rules.map((r, j) => j !== ri ? r : which === 'else' ? { ...r, else: to } : { ...r, branches: (r.branches ?? []).map((b, k) => (k === Number(which) ? { ...b, then: to } : b)) }) };
      const name = which === 'else' ? t('drule.else') : rule.branches?.[Number(which)]?.label || whenText(rule, rule.branches![Number(which)].when, f);
      setProposal({
        ...pos, nodeId: c.source, title: t('fedit.confirm.branch', { rule: rule.name, branch: name }),
        lines: [t('fedit.confirm.fromTo', { from: targetLabel(cur, t('drule.nextRule')), to: targetString(to) })],
        run: async () => {
          await routerApi.saveRules(next);
          return { text: t('fedit.done.branch', { branch: name, to: targetString(to) }), undo: () => routerApi.saveRules(prev) };
        },
      });
      return;
    }
    if (c.source.startsWith('route:') && c.target.startsWith('route:') && c.source !== c.target) {
      const from = c.source.slice(6), to = c.target.slice(6);
      const a = d.routes.find((r) => r.name === from), b = d.routes.find((r) => r.name === to);
      if (!sharesProtocol(a, b)) {
        setToast({ tone: 'bad', text: t('fedit.refused.protocols', { to, protocols: (a?.protocols ?? []).map((p) => t(`protocol.short.${p}`)).join(', ') }) });
        return;
      }
      const cur = override(from)?.fallbacks ?? [];
      if (cur.includes(to)) return;
      const ops = setFallbacks(from, [...cur, to]);
      setProposal({
        ...pos, nodeId: c.source, title: t('fedit.confirm.fallback', { route: from }),
        lines: [t('fedit.confirm.fallbackLine', { to, n: cur.length + 1 }), t('fedit.confirm.chain', { chain: [from, ...cur, to].join(' → ') })],
        run: async () => {
          await ops.save();
          return { text: t('fedit.done.fallback', { to, route: from }), undo: ops.undo };
        },
      });
    }
  };

  const deleteFallback = (route: string, index: number, e: RMouseEvent) => {
    const cur = override(route)?.fallbacks ?? [];
    const gone = cur[index];
    if (!gone) return;
    const ops = setFallbacks(route, cur.filter((_, i) => i !== index));
    setProposal({
      ...at(e), nodeId: `route:${route}`, title: t('fedit.confirm.removeFallback', { route }),
      lines: [t('fedit.confirm.chain', { chain: [route, ...cur.filter((_, i) => i !== index)].join(' → ') || route })],
      run: async () => {
        await ops.save();
        return { text: t('fedit.done.removeFallback', { to: gone, route }), undo: ops.undo };
      },
    });
  };

  const toggleMode = (e: RMouseEvent) => {
    e.stopPropagation();
    if (!d) return;
    const prev = d.prune.settings;
    const mode = (prev.mode || 'shadow') === 'enforce' ? 'shadow' : 'enforce';
    setProposal({
      ...at(e), nodeId: 'prune', title: t('fedit.confirm.mode'),
      lines: [t('fedit.confirm.fromTo', { from: t(`fedit.mode.${mode === 'enforce' ? 'shadow' : 'enforce'}`), to: t(`fedit.mode.${mode}`) }), t(`fedit.confirm.mode.${mode}`)],
      run: async () => {
        await pruneApi.saveConfig({ ...prev, mode });
        return { text: t('fedit.done.mode', { mode: t(`fedit.mode.${mode}`) }), undo: () => pruneApi.saveConfig(prev) };
      },
    });
  };

  const confirm = async () => {
    if (!proposal) return;
    setBusy(true);
    try {
      const u = await proposal.run();
      setErrors((m) => { const n = { ...m }; delete n[proposal.nodeId]; return n; });
      setProposal(null);
      saved(u);
    } catch (e) {
      failed(proposal.nodeId, msg(e));
      setProposal(null);
    } finally {
      setBusy(false);
    }
  };

  const undo = async () => {
    const u = toast?.undo;
    setToast(null);
    if (!u) return;
    try {
      await u();
      saved({ text: t('fedit.undone') });
    } catch (e) {
      setToast({ tone: 'bad', text: msg(e) });
    }
  };

  // ---- React Flow nodes and edges ----
  const nodes: Node<CfgNodeData>[] = useMemo(() => (graph?.nodes ?? []).map((n) => ({
    id: n.id, type: n.data.kind === 'lane' ? 'lane' : n.data.kind === 'head' ? 'head' : 'cfg', position: { x: n.x, y: n.y }, data: n.data,
    initialWidth: n.data.width, initialHeight: n.data.height,
    draggable: false, selectable: n.data.editable, focusable: n.data.editable, selected: openNode === n.id,
  })), [graph, openNode]);
  const edges: Edge<EdgeData>[] = useMemo(() => (graph?.edges ?? []).map((e: CfgEdge) => ({
    id: e.id, source: e.source, target: e.target, type: 'cfg', sourceHandle: e.sourceHandle ?? 'out', targetHandle: e.targetHandle ?? 'in',
    data: {
      kind: e.kind, label: e.label,
      onDelete: e.kind === 'fallback' ? (ev: RMouseEvent) => deleteFallback(e.source.slice(6), e.index ?? 0, ev) : undefined,
    },
    selectable: false,
  })), [graph]); // eslint-disable-line react-hooks/exhaustive-deps

  // Enter opens the focused node; Esc closes the drawer (the drawer handles it).
  useEffect(() => {
    const el = canvas.current;
    if (!el) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Enter') return;
      const n = (document.activeElement as HTMLElement | null)?.closest('.react-flow__node') as HTMLElement | null;
      const id = n?.dataset.id;
      if (id && graph?.nodes.find((x) => x.id === id)?.data.editable) {
        e.preventDefault();
        onOpen(id);
      }
    };
    el.addEventListener('keydown', onKey);
    return () => el.removeEventListener('keydown', onKey);
  }, [graph, onOpen]);

  if (data.error && !d) return <div className="pad"><ErrorState error={data.error} onRetry={data.reload} /></div>;
  if (!d || !graph) return <div className="pad"><Loading lines={8} /></div>;

  const openData = openNode ? graph.nodes.find((n) => n.id === openNode)?.data : undefined;
  const ctx = {
    open: (id: string) => { setNewBranch(false); onOpen(id); }, toggleMode,
    add: (w: 'alias' | 'route' | 'rule') => (w === 'rule' ? onOpen('router') : setAdding(w)),
    addBranch: (i: number) => { setNewBranch(true); onOpen(`drule:${i}`); },
  };
  // As tall as the graph is at the scale that fits the width.
  const graphH = Math.max(...graph.nodes.map((n) => n.y + n.data.height)) + 140;
  const scale = width ? Math.min(1, (width - 40) / (CFG_W + 80)) : 0.65;
  const height = Math.round(Math.min(1100, Math.max(560, graphH * scale + 40)));

  return (
    <Ctx.Provider value={ctx}>
      <div ref={sizer} />
      <div className={cx('flow-canvas flow-edit', connecting && 'is-connecting')} ref={canvas} style={{ height }} role="application" aria-label={t('fedit.canvas')}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={cfgNodeTypes}
          edgeTypes={cfgEdgeTypes}
          colorMode={resolved}
          fitView
          fitViewOptions={{ padding: 0.05 }}
          minZoom={0.25}
          maxZoom={1.6}
          nodesDraggable={false}
          nodesConnectable
          nodesFocusable
          connectionRadius={36}
          isValidConnection={(c) => (c.source?.startsWith('drule:') && !!c.sourceHandle?.startsWith('br:') && (c.target?.startsWith('route:') || c.target?.startsWith('alias:')) && c.targetHandle === 'in')
            || (c.source?.startsWith('alias:') && c.target?.startsWith('route:') && c.targetHandle === 'in') || (c.source?.startsWith('route:') && c.sourceHandle === 'fbout' && c.target?.startsWith('route:') && c.source !== c.target)}
          onConnect={(c) => { pending.current = c; }}
          onConnectStart={() => setConnecting(true)}
          onConnectEnd={(e) => {
            setConnecting(false);
            const c = pending.current;
            pending.current = null;
            if (c) onConnect(c, at('changedTouches' in e ? e.changedTouches[0] : e));
          }}
          onNodeClick={(_, n) => n.data.editable && onOpen(n.id)}
          onPaneClick={() => setProposal(null)}
          proOptions={{ hideAttribution: true }}
        >
          <MiniMap pannable zoomable position="bottom-right" style={{ width: 150, height: 96 }} nodeBorderRadius={4} maskColor="rgba(120, 120, 140, 0.12)"
            nodeColor={(n) => ((n.data as CfgNodeData).kind === 'route' ? '#5b4cf0' : (n.data as CfgNodeData).kind === 'lane' || (n.data as CfgNodeData).kind === 'head' ? 'transparent' : '#a4a4b0')} ariaLabel={t('fedit.minimap')} />
          <Controls showInteractive={false} fitViewOptions={{ padding: 0.05 }} />
          <FitOnce keyStr={`${graph.nodes.length}`} />
        </ReactFlow>
        {proposal && (
          <div className="fconfirm" role="dialog" aria-label={proposal.title} style={{ left: proposal.x, top: proposal.y }}>
            <strong>{proposal.title}</strong>
            <ul>{proposal.lines.map((l) => <li key={l}>{l}</li>)}</ul>
            <div className="btn-row">
              <Button size="sm" variant="primary" loading={busy} onClick={confirm} autoFocus>{t('fedit.confirm.apply')}</Button>
              <Button size="sm" onClick={() => setProposal(null)}>{t('common.cancel')}</Button>
            </div>
          </div>
        )}
        {toast && (
          <div className={cx('ftoast', toast.tone === 'bad' && 'is-bad')} role="status">
            <Icon name={toast.tone === 'bad' ? 'alert' : 'check'} size={14} />
            <span>{toast.text}</span>
            {toast.undo && <Button size="sm" variant="ghost" onClick={undo}>{t('fedit.undo')}</Button>}
            <button type="button" className="linkish small" onClick={() => setToast(null)} aria-label={t('common.dismiss')}><Icon name="x" size={12} /></button>
          </div>
        )}
      </div>
      <p className="fine flow-edit-hint">{t('fedit.hint')}</p>

      <Drawer
        open={!!openData || !!adding}
        wide
        onClose={() => { onOpen(null); setAdding(null); }}
        title={adding ? t(`fedit.add.${adding}`) : openData ? <span className="brand-label"><Badge>{t(`fedit.kind.${openData.kind}`)}</Badge>{openData.title}</span> : ''}
        actions={openData && openNode && seeable(openData) ? <Button size="sm" icon="traffic" onClick={() => seeTraffic(openNode)}>{t('fedit.seeTraffic')}</Button> : undefined}
      >
        {openData?.error && <Callout tone="bad" title={t('fedit.lastError')}>{openData.error}</Callout>}
        <Panel key={adding ?? openNode ?? ''} id={adding ? `new:${adding}` : openNode ?? ''} d={d} newBranch={newBranch}
          onSaved={(u) => { if (openNode) setErrors((m) => { const n = { ...m }; delete n[openNode]; return n; }); setAdding(null); saved(u); }}
          onError={(e) => failed(openNode ?? '', e)} />
      </Drawer>
    </Ctx.Provider>
  );
}

const seeable = (n: CfgNodeData) => ['key', 'router', 'switch', 'prune', 'recall', 'route', 'alias', 'systemone', 'dbackend', 'economy'].includes(n.kind);

function Panel({ id, d, onSaved, onError, newBranch }: { id: string; d: ConfigData; onSaved: (u?: Undoable) => void; onError: (e: string) => void; newBranch?: boolean }) {
  const p = { d, onSaved, onError };
  const [kind, ...rest] = id.split(':');
  const name = rest.join(':');
  if (id === 'new:alias') return <AliasPanel {...p} name={null} />;
  if (id === 'new:route') return <RoutePanel {...p} name={null} />;
  switch (kind) {
    case 'key': return <KeyPanel {...p} id={name} />;
    case 'nokey': return <NoKeyPanel d={d} />;
    case 'router': return <RouterPanel {...p} />;
    case 'drule': return <SwitchPanel {...p} index={Number(name)} newBranch={!!newBranch} />;
    case 'prune': return <PruneSettings />;
    case 'recall': return <RecallPanel {...p} />;
    case 'economy': return <EconomyModel />;
    case 'alias': return <AliasPanel {...p} name={name} />;
    case 'route': return <RoutePanel {...p} name={name} />;
    case 'proto': case 'dbackend': case 'dmap': return <DecisionSettings />;
    default: return null;
  }
}

function FitOnce({ keyStr }: { keyStr: string }) {
  const rf = useReactFlow();
  const ready = useNodesInitialized();
  useEffect(() => {
    if (!ready) return;
    const id = requestAnimationFrame(() => rf.fitView({ padding: 0.05 }));
    return () => cancelAnimationFrame(id);
  }, [keyStr, rf, ready]);
  return null;
}

const KIND_ICON: Partial<Record<CfgNodeData['kind'], IconName>> = {
  key: 'key', nokey: 'apps', router: 'routing', switch: 'cpu', prune: 'savings', recall: 'recall', economy: 'cpu', alias: 'layers', model: 'cpu', dmap: 'cpu',
};

const CfgNodeView = memo(function CfgNodeView({ data: n }: NodeProps<Node<CfgNodeData>>) {
  const { t } = useI18n();
  const ctx = useContext(Ctx);
  const route = n.kind === 'route' || n.kind === 'dbackend';
  const noIn = n.kind === 'key' || n.kind === 'nokey';
  const sw = n.kind === 'switch';
  const noOut = n.kind === 'model' || n.kind === 'dmap' || n.kind === 'recall';
  return (
    <div className={cx('cnode', `cnode-${n.kind}`, n.error && 'has-error', n.editable && 'is-editable', n.off && 'is-off')} style={{ width: n.width, minHeight: n.height }} title={n.error}>
      {!noIn && <Handle type="target" position={Position.Left} id="in" className="fh" isConnectable={n.kind === 'route' || n.kind === 'alias'} />}
      {!noOut && !sw && <Handle type="source" position={Position.Right} id="out" className="fh" isConnectable={n.kind === 'alias'} />}
      {route && <Handle type="target" position={Position.Top} id="fbin" className="fh fh-fb" isConnectable={n.kind === 'route'} />}
      {route && <Handle type="source" position={Position.Bottom} id="fbout" className="fh fh-fb" isConnectable={n.kind === 'route'} title={n.kind === 'route' ? t('fedit.dragFallback') : undefined} />}
      {n.kind === 'prune' && <Handle type="source" position={Position.Bottom} id="bottom" className="fh" isConnectable={false} />}
      {n.kind === 'recall' && <Handle type="target" position={Position.Top} id="top" className="fh" isConnectable={false} />}
      {n.kind === 'economy' && <Handle type="source" position={Position.Top} id="top" className="fh" isConnectable={false} />}
      {n.kind === 'router' && <Handle type="target" position={Position.Bottom} id="bottom" className="fh" isConnectable={false} />}
      {n.kind === 'router' && <Handle type="source" position={Position.Bottom} id="down" className="fh" isConnectable={false} />}
      {sw && <Handle type="target" position={Position.Top} id="top" className="fh" isConnectable={false} />}
      <div className="cnode-head">
        <span className="fnode-icon">
          {KIND_ICON[n.kind] ? <Icon name={KIND_ICON[n.kind]!} size={15} />
            : n.kind === 'systemone' ? <BrandIcon id="rlcd" label="System One" size={16} />
              : n.kind === 'proto' ? <BrandIcon id={n.protocols?.[0] === 'anthropic-messages' ? 'anthropic' : 'openai'} label={n.title} size={16} />
                : <BrandIcon id={n.icon === 'custom' ? undefined : n.icon} label={n.title} size={16} />}
        </span>
        <span className="fnode-text">
          <span className={cx('fnode-label', (n.kind === 'route' || n.kind === 'model' || n.kind === 'alias' || n.kind === 'dmap') && 'mono')} title={n.title}>{n.title}</span>
          {n.sub && <span className="fnode-sub" title={n.sub}>{n.sub}</span>}
        </span>
        {n.editable && (
          <button type="button" className="cnode-edit nodrag" aria-label={t('fedit.editNode', { name: n.title })} onClick={(e) => { e.stopPropagation(); ctx.open(n.id); }}>
            <Icon name="edit" size={13} />
          </button>
        )}
      </div>
      {n.chips && n.chips.length > 0 && (
        <div className="cnode-chips">
          {n.chips.map((c) => c.action === 'toggleMode'
            ? <button key={c.label} type="button" className={cx('cchip nodrag', `cchip-${c.tone ?? 'neutral'}`, 'is-action')} onClick={ctx.toggleMode} title={t('fedit.toggleMode')}>{c.label}<Icon name="refresh" size={10} /></button>
            : <span key={c.label} className={cx('cchip', `cchip-${c.tone ?? 'neutral'}`)}>{c.label}</span>)}
        </div>
      )}
      {n.lines && (
        <ol className="cnode-lines">
          {n.lines.length === 0 && <li className="muted">{t('fedit.node.noRules')}</li>}
          {n.lines.map((l, i) => <li key={i} className={cx(!l.on && 'off')}><span className="cnode-n">{i + 1}</span>{l.text}</li>)}
        </ol>
      )}
      {sw && n.branches && (
        <>
          <ol className="cnode-branches">
            {n.branches.map((b) => (
              <li key={b.handle} className={cx(b.handle === 'br:else' && 'is-else', b.empty && 'is-empty')}>
                <span className="clip">{b.text}</span>
                <Handle type="source" position={Position.Right} id={b.handle} className="fh fh-branch" isConnectable title={t('fedit.dragBranch')} />
              </li>
            ))}
          </ol>
          <button type="button" className="cnode-add nodrag" onClick={(e) => { e.stopPropagation(); ctx.addBranch(n.rule ?? 0); }}>
            <Icon name="plus" size={11} />{t('drule.addBranchShort')}
          </button>
        </>
      )}
      {n.error && <div className="cnode-err"><Icon name="alert" size={11} /> {n.error}</div>}
    </div>
  );
});

const HeadNode = memo(function HeadNode({ data: n }: NodeProps<Node<CfgNodeData>>) {
  const { t } = useI18n();
  const ctx = useContext(Ctx);
  return (
    <div className="chead" style={{ width: n.width }}>
      <span>{n.title}</span>
      {n.add && (
        <button type="button" className="chead-add nodrag" onClick={() => ctx.add(n.add!)} title={t(`fedit.add.${n.add}`)}>
          <Icon name="plus" size={12} />{t(`fedit.addShort.${n.add}`)}
        </button>
      )}
    </div>
  );
});

const LaneNode = memo(function LaneNode({ data: n }: NodeProps<Node<CfgNodeData>>) {
  return <div className="flane"><strong>{n.title}</strong><span>{n.sub}</span></div>;
});

function CfgEdgeView({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data }: EdgeProps<Edge<EdgeData>>) {
  const { t } = useI18n();
  const d = data!;
  const loop = d.kind === 'fallback' || d.kind === 'mirror';
  const [path, lx, ly] = loop
    // Loops out past the right edge of the route column, so the label never sits on a node.
    ? [`M${sourceX},${sourceY} C${sourceX + 240},${sourceY + 50} ${targetX + 240},${targetY - 50} ${targetX},${targetY}`, Math.max(sourceX, targetX) + 180, (sourceY + targetY) / 2] as const
    : getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  return (
    <>
      <BaseEdge id={id} path={path} className={cx('cedge', `cedge-${d.kind}`)} />
      {(d.label || d.onDelete || d.kind === 'mirror') && (
        <EdgeLabelRenderer>
          <div className={cx('flabel', 'flabel-fb', 'nodrag nopan')} style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)`, pointerEvents: 'all' }}>
            <span>{d.kind === 'mirror' ? t('fedit.edge.mirror') : t('fedit.edge.fallback', { n: d.label ?? '' })}</span>
            {d.onDelete && <button type="button" className="flabel-x" onClick={d.onDelete} aria-label={t('fedit.removeFallback')} title={t('fedit.removeFallback')}><Icon name="x" size={10} /></button>}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

const cfgNodeTypes = { cfg: CfgNodeView, head: HeadNode, lane: LaneNode };
const cfgEdgeTypes = { cfg: CfgEdgeView };
export const EDIT_W = CFG_W;
