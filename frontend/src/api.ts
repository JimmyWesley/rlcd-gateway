// Types mirror the Go structs in gateway/internal/{store,ir,api}.

export type Usage = {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
};

export type Protocol = 'anthropic-messages' | 'openai-chat' | 'openai-responses';

/** Who made a call, detected from its headers (gateway/internal/clients). */
export type ClientInfo = {
  id: string;
  name: string;
  version?: string;
  kind: 'agent' | 'sdk' | 'cli' | 'browser' | 'unknown';
  key_name?: string;
};

export type RequestRecord = {
  id: string;
  time: string;
  method: string;
  path: string;
  route: string;
  upstream: string;
  /** Absent on records logged before protocols existed: Anthropic Messages. */
  protocol?: Protocol;
  alias?: string;
  key_id?: string;
  key_name?: string;
  client?: ClientInfo;
  provider?: string;
  model_vendor?: string;
  client_model?: string;
  model?: string;
  auth_mode: 'oauth' | 'api-key' | 'bearer' | 'gateway-key' | 'none';
  stream: boolean;
  status: number;
  ttfb_ms: number;
  duration_ms: number;
  est_tokens: number;
  by_kind?: Record<string, number>;
  usage?: Usage;
  /** Estimated from the price table. */
  est_cost_usd?: number;
  stripped_thinking?: number;
  error?: string;
  conversation_id?: string;
  route_reason?: string;
  /** Small per-stage summaries, keyed by stage name (e.g. "prune"). */
  stages?: Record<string, unknown>;
  stage_errors?: Record<string, string>;
};

export type Block = {
  key: string;
  kind: string;
  role?: string;
  msg: number;
  index: number;
  name?: string;
  tool_use_id?: string;
  is_error?: boolean;
  chars: number;
  tokens: number;
  preview: string;
  cached?: boolean;
};

export type RequestDetail = RequestRecord & {
  request_headers: Record<string, string>;
  xray?: { model: string; messages: number; tokens: number; blocks: Block[]; by_kind: Record<string, number> };
  /** Large per-stage reports, keyed by stage name (e.g. the pruning diff). */
  stage_details?: Record<string, unknown>;
  /** What was actually forwarded, when a stage changed the body. */
  sent_body?: string;
  request_body?: string;
  response_body?: string;
};

export type RouteView = {
  name: string;
  kind: string;
  base_url: string;
  auth: 'passthrough' | 'key';
  model?: string;
  api_key_env?: string;
  has_key: boolean;
  provider: string;
  protocols: Protocol[];
};

export type SelectorView = {
  backend: 'open-rlcd-local' | 'open-rlcd-cloud' | 'jev';
  base_url: string;
  token_env?: string;
  model: string;
  has_token: boolean;
};

export type GatewayConfig = {
  listen: string;
  active_route: string;
  routes: RouteView[];
  selector: SelectorView;
  config_path: string;
  log_bodies: boolean;
  /** Route for OpenAI-format requests no alias or rule claims; "" = by login. */
  default_openai_route: string;
  /** Listening beyond loopback: keys are required and the dashboard is guarded. */
  exposed: boolean;
  require_keys: boolean;
  active_keys: number;
  allowed_hosts?: string[] | null;
};

export type Totals = Usage & { requests: number; errors: number; est_tokens: number; est_cost_usd: number };
export type Stats = { total: Totals; by_route: Record<string, Totals>; by_key: Record<string, Totals> };

export type ProbeResult =
  | { ok: true; result: { answers: Record<string, { type: string; noul?: number }>; wall_ms: number; forward_ms?: number } }
  | { ok: false; error: string };

/** Thrown when the gateway listens beyond loopback and wants the admin token. */
export class AdminLoginRequired extends Error {}

let onAdminLogin: (() => void) | null = null;
/** Called whenever a request is refused for want of the admin token. */
export function setAdminLoginHandler(fn: () => void) {
  onAdminLogin = fn;
}

export async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  const data = await res.json().catch(() => null);
  if (res.status === 401 && data?.admin_login !== undefined) {
    onAdminLogin?.();
    throw new AdminLoginRequired(data?.error ?? 'admin token required');
  }
  if (!res.ok) throw new Error(data?.error ?? `${res.status} ${path}`);
  return data as T;
}

export const api = {
  requests: () => call<RequestRecord[]>('/api/requests'),
  request: (id: string) => call<RequestDetail>(`/api/requests/${encodeURIComponent(id)}`),
  stats: () => call<Stats>('/api/stats'),
  config: () => call<GatewayConfig>('/api/config'),
  presets: () => call<SelectorView[]>('/api/selector/presets'),
  setRoute: (name: string) => call<{ active_route: string }>('/api/route', { method: 'PUT', body: JSON.stringify({ name }) }),
  setRequireKeys: (on: boolean) => call<{ require_keys: boolean }>('/api/require-keys', { method: 'PUT', body: JSON.stringify({ on }) }),
  /** Exchanges the admin token for a session cookie (remote dashboards only). */
  login: (token: string) => call<{ ok: boolean }>('/auth/admin', { method: 'POST', body: JSON.stringify({ token }) }),
  setSelector: (s: Partial<SelectorView> & { token?: string; clear_token?: boolean }) =>
    call<SelectorView>('/api/selector', { method: 'PUT', body: JSON.stringify(s) }),
  testSelector: () => call<ProbeResult>('/api/selector/test', { method: 'POST' }),
};

/** Live feed of finished requests. Returns an unsubscribe function. */
export function onRequest(fn: (r: RequestRecord) => void, onState?: (live: boolean) => void): () => void {
  const es = new EventSource('/api/events');
  es.addEventListener('request', (e) => fn(JSON.parse((e as MessageEvent).data)));
  es.onopen = () => onState?.(true);
  es.onerror = () => onState?.(false);
  return () => es.close();
}

export const PROTOCOL_LABEL: Record<Protocol, string> = {
  'anthropic-messages': 'Anthropic Messages',
  'openai-chat': 'OpenAI Chat',
  'openai-responses': 'OpenAI Responses',
};

/** A record's protocol; old records are all Anthropic Messages. */
export const protocolOf = (r: { protocol?: Protocol }): Protocol => r.protocol ?? 'anthropic-messages';

/** Whether a route can serve a protocol (the gateway never translates). */
export const speaks = (r: { protocols?: Protocol[]; kind: string }, p: Protocol) =>
  (r.protocols ?? (r.kind === 'openai' ? ['openai-chat', 'openai-responses'] : ['anthropic-messages'])).includes(p);

export const fmt = {
  n: (v: number | undefined) => (v == null ? '—' : v >= 10_000 ? `${(v / 1000).toFixed(1)}k` : v.toLocaleString('en-US')),
  ms: (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${v}ms`),
  time: (iso: string) => new Date(iso).toLocaleTimeString('en-US', { hour12: false }),
  usd: (v: number | undefined) => {
    if (v == null) return '—';
    const a = Math.abs(v);
    return `${v < 0 ? '−' : ''}$${a.toFixed(a >= 10 ? 2 : a >= 0.1 ? 3 : 4)}`;
  },
};

/** Copies text; resolves false when the browser refuses. */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
