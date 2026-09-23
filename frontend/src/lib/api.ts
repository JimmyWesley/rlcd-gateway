import type { DecisionSummary } from './decisionsApi';
import type { ResilienceFields } from './resilienceApi';
// Types mirror the Go structs in gateway/internal/{store,ir,api,recall,adapters}.

export type Usage = {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
};

export type Protocol = 'anthropic-messages' | 'openai-chat' | 'openai-responses' | 'systemone';
export const PROTOCOLS: Protocol[] = ['anthropic-messages', 'openai-chat', 'openai-responses', 'systemone'];

/** Who made a call, detected from its headers (gateway/internal/clients). Older records lack it. */
export type ClientInfo = {
  id: string;
  name: string;
  version?: string;
  kind: 'agent' | 'sdk' | 'cli' | 'browser' | 'internal' | 'unknown';
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
  /** The model alias the client asked for, when one matched. */
  alias?: string;
  key_id?: string;
  key_name?: string;
  client?: ClientInfo;
  /** Who served it: anthropic, openrouter, openai, groq, ..., custom. */
  provider?: string;
  /** Who made the model that was sent: anthropic, openai, qwen, meta, ... */
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
  /** System One calls (protocol systemone): what was asked and answered. */
  decisions?: DecisionSummary;
} & ResilienceFields;

export type Block = {
  key: string;
  /** Question blocks (System One): the criteria labels. */
  labels?: string[];
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

export type SelectorBackend = 'open-rlcd-local' | 'open-rlcd-cloud' | 'jev';

export type SelectorView = {
  backend: SelectorBackend;
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
  /** Route for OpenAI-format requests no alias or rule claims; "" = by the client's login. */
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

// GET /api/insights (gateway/internal/api/insights.go)
export type InsightBucket = {
  t: string;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
  saved_tokens: number;
  saved_usd: number;
  shadow_saved_tokens: number;
  shadow_saved_usd: number;
  est_cost_usd: number;
  latency_p50_ms: number;
  latency_p95_ms: number;
  epochs: number;
  selector_avg_ms: number;
};

export type InsightGroup = { name: string; label?: string; requests: number; errors: number; tokens: number; est_cost_usd: number };

export type Insights = {
  window: string;
  from: string;
  to: string;
  bucket_seconds: number;
  buckets: InsightBucket[];
  totals: {
    requests: number;
    errors: number;
    input_tokens: number;
    output_tokens: number;
    cache_read_input_tokens: number;
    cache_creation_input_tokens: number;
    saved_tokens: number;
    saved_usd: number;
    shadow_saved_tokens: number;
    shadow_saved_usd: number;
    est_cost_usd: number;
    pruned_requests: number;
    cache_invalidations: number;
    conversations: number;
  };
  latency: { p50_ms: number; p95_ms: number; p99_ms: number; ttfb_p50_ms: number };
  by_route: InsightGroup[];
  by_model: InsightGroup[];
  by_client: InsightGroup[];
  by_protocol: InsightGroup[];
  by_key: InsightGroup[];
  selector: { epochs: number; errors: number; avg_ms: number; p95_ms: number; last_epoch?: string; last_error?: string; last_error_at?: string };
};

export type Window = '1h' | '6h' | '24h' | '7d' | 'all';
export const WINDOWS: Window[] = ['1h', '6h', '24h', '7d', 'all'];

// Recall (gateway/internal/recall)
export type RecallSettings = { enabled: boolean; max_bytes: number; mcp_path: string; tool: string };

export type RecallEvent = {
  time: string;
  conversation_id?: string;
  req: string;
  key: string;
  block_key?: string;
  tool_use_id?: string;
  tool?: string;
  kind?: string;
  tokens: number;
  bytes: number;
  truncated?: boolean;
  ok: boolean;
  error?: string;
  message?: string;
  client?: string;
};

export type RecallStats = {
  total: number;
  ok: number;
  errors: number;
  by_error: Record<string, number>;
  tokens_restored: number;
  conversations: number;
  last?: string;
  top_keys: { key: string; tool?: string; count: number }[];
  top_tools: { tool: string; count: number; tokens: number }[];
};

// Adapters (gateway/internal/adapters): OpenAI-format upstreams.
export type AdapterSettings = { openai_base_url: string; chatgpt_base_url: string };

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly data?: unknown) {
    super(message);
  }
}

/** 404 body of a request whose details retention deleted. */
export type PurgedInfo = { error: string; purged: true; purged_at?: string; reason?: string };
export const purgedInfo = (e: unknown): PurgedInfo | null => {
  const d = e instanceof ApiError ? (e.data as Partial<PurgedInfo> | undefined) : undefined;
  return d?.purged ? (d as PurgedInfo) : null;
};

/** Thrown when the gateway listens beyond loopback and wants the admin token. */
export class AdminLoginRequired extends ApiError {}

let onAdminLogin: (() => void) | null = null;
/** Called whenever a request is refused for want of the admin token. */
export function setAdminLoginHandler(fn: (() => void) | null) {
  onAdminLogin = fn;
}

export async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    // Not JSON (e.g. a proxy error page); report the status instead.
  }
  if (res.status === 401 && (data as { admin_login?: boolean } | null)?.admin_login !== undefined) {
    onAdminLogin?.();
    throw new AdminLoginRequired((data as { error?: string }).error ?? 'admin token required', 401);
  }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? `${res.status} ${res.statusText || path}`;
    throw new ApiError(msg, res.status, data);
  }
  return data as T;
}

const put = (body: unknown): RequestInit => ({ method: 'PUT', body: JSON.stringify(body) });

export const api = {
  requests: () => call<RequestRecord[]>('/api/requests'),
  request: (id: string) => call<RequestDetail>(`/api/requests/${encodeURIComponent(id)}`),
  stats: () => call<Stats>('/api/stats'),
  insights: (w: Window) => call<Insights>(`/api/insights?window=${w}`),
  config: () => call<GatewayConfig>('/api/config'),
  presets: () => call<SelectorView[]>('/api/selector/presets'),
  setRoute: (name: string) => call<{ active_route: string }>('/api/route', put({ name })),
  setRequireKeys: (on: boolean) => call<{ require_keys: boolean }>('/api/require-keys', put({ on })),
  /** Exchanges the admin token for an HttpOnly session cookie (remote dashboards only). */
  login: (token: string) => call<{ ok: boolean }>('/auth/admin', { method: 'POST', body: JSON.stringify({ token }) }),
  logout: () => call<{ ok: boolean }>('/auth/admin', { method: 'DELETE' }),
  setSelector: (s: Partial<SelectorView> & { token?: string; clear_token?: boolean }) =>
    call<SelectorView>('/api/selector', put(s)),
  testSelector: () => call<ProbeResult>('/api/selector/test', { method: 'POST' }),
  adapters: () => call<AdapterSettings>('/api/adapters'),
  setAdapters: (s: AdapterSettings) => call<AdapterSettings>('/api/adapters', put(s)),
};

export const recallApi = {
  settings: () => call<RecallSettings>('/api/recall/settings'),
  setSettings: (s: Partial<Pick<RecallSettings, 'enabled' | 'max_bytes'>>) => call<RecallSettings>('/api/recall/settings', put(s)),
  stats: () => call<RecallStats>('/api/recall/stats'),
  events: (limit: number) => call<RecallEvent[]>(`/api/recall/events?limit=${limit}`),
};

/** Live feed of finished requests. Returns an unsubscribe function. */
export function onRequest(fn: (r: RequestRecord) => void, onState?: (live: boolean) => void): () => void {
  const es = new EventSource('/api/events');
  es.addEventListener('request', (e) => {
    try {
      fn(JSON.parse((e as MessageEvent).data));
    } catch {
      // A malformed event is skipped; the next one still arrives.
    }
  });
  es.onopen = () => onState?.(true);
  es.onerror = () => onState?.(false);
  return () => es.close();
}

/** The gateway's address as an agent should call it: a wildcard host becomes loopback. */
export function gatewayURL(listen: string | undefined, path = ''): string {
  const l = listen || window.location.host;
  const i = l.lastIndexOf(':');
  const host = i >= 0 ? l.slice(0, i) : l;
  const port = i >= 0 ? l.slice(i) : '';
  const h = host === '' || host === '0.0.0.0' || host === '[::]' ? '127.0.0.1' : host;
  return `http://${h}${port}${path}`;
}

/** A record's protocol; old records are all Anthropic Messages. */
export const protocolOf = (r: { protocol?: Protocol }): Protocol => r.protocol ?? 'anthropic-messages';

/** Whether a route can serve a protocol (the gateway never translates). */
export const speaks = (r: { protocols?: Protocol[]; kind: string }, p: Protocol) =>
  (r.protocols ?? (r.kind === 'openai' ? ['openai-chat', 'openai-responses'] : ['anthropic-messages'])).includes(p);

export const isFailed = (r: Pick<RequestRecord, 'status' | 'error'>) => r.status >= 400 || !!r.error;
