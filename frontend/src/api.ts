// Types mirror the Go structs in gateway/internal/{store,ir,api}.

export type Usage = {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
};

export type RequestRecord = {
  id: string;
  time: string;
  method: string;
  path: string;
  route: string;
  upstream: string;
  client_model?: string;
  model?: string;
  auth_mode: 'oauth' | 'api-key' | 'bearer' | 'none';
  stream: boolean;
  status: number;
  ttfb_ms: number;
  duration_ms: number;
  est_tokens: number;
  by_kind?: Record<string, number>;
  usage?: Usage;
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
};

export type Totals = Usage & { requests: number; errors: number; est_tokens: number };
export type Stats = { total: Totals; by_route: Record<string, Totals> };

export type ProbeResult =
  | { ok: true; result: { answers: Record<string, { type: string; noul?: number }>; wall_ms: number; forward_ms?: number } }
  | { ok: false; error: string };

export async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  const data = await res.json();
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

export const fmt = {
  n: (v: number | undefined) => (v == null ? '—' : v >= 10_000 ? `${(v / 1000).toFixed(1)}k` : v.toLocaleString('en-US')),
  ms: (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${v}ms`),
  time: (iso: string) => new Date(iso).toLocaleTimeString('en-US', { hour12: false }),
};
