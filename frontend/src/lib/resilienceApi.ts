// Types mirror gateway/internal/store/attempts.go and gateway/internal/resilience
// (api.go, settings.go, stats.go, limits.go).
import { call, type RequestRecord } from './api';

export const CLASSES = ['output_too_large', 'context_overflow', 'rate_limited', 'overloaded', 'provider_error', 'model_unavailable', 'auth', 'bad_request', 'unknown'] as const;
export type ErrorClass = (typeof CLASSES)[number];
export const ACTIONS = ['clamp_max_tokens', 'emergency_prune', 'ignore_provider', 'backoff', 'fallback', 'none', 'give_up'] as const;
export type Action = (typeof ACTIONS)[number];
export const REASONS = ['filled_missing', 'above_max_output', 'exceeds_context_window', 'provider_error'] as const;
export type GuardReason = (typeof REASONS)[number];
export const SOURCES = ['builtin', 'openrouter', 'learned', 'override:model', 'override:route', 'override:alias', 'error'] as const;
export type LimitSource = (typeof SOURCES)[number];

export const isClass = (c: string | undefined): c is ErrorClass => !!c && (CLASSES as readonly string[]).includes(c);
export const isAction = (a: string | undefined): a is Action => !!a && (ACTIONS as readonly string[]).includes(a);
export const isReason = (r: string | undefined): r is GuardReason => !!r && (REASONS as readonly string[]).includes(r);
export const isSource = (s: string | undefined): s is LimitSource => !!s && (SOURCES as readonly string[]).includes(s);

export type MaxTokensChange = {
  field: string;
  from: number | null;
  to: number;
  reason: string;
  limit_source?: string;
  context_window?: number;
  max_output_tokens?: number;
  est_input_tokens?: number;
  thinking_budget_from?: number;
  thinking_budget_to?: number;
};

export type EmergencyPruneChange = { dropped: number; saved_tokens: number; tokens_before: number; tokens_after: number; keep_threshold: number };

export type Attempt = {
  n: number;
  route: string;
  provider?: string;
  upstream_provider?: string;
  model?: string;
  status: number;
  class?: string;
  message?: string;
  action?: string;
  action_detail?: string;
  duration_ms: number;
  wait_ms?: number;
  changes?: {
    max_tokens?: MaxTokensChange;
    emergency_prune?: EmergencyPruneChange;
    ignored_providers?: string[];
    cache_invalidated?: boolean;
  };
};

/** The fields a record carries when the resilience layer was involved. */
export type ResilienceFields = {
  attempts?: Attempt[];
  retried?: boolean;
  recovered?: boolean;
  error_class?: string;
  primary_route?: string;
  fallback_route?: string;
  max_tokens_guard?: MaxTokensChange;
};

export const attemptOk = (a: Attempt) => a.status >= 200 && a.status < 400 && !a.class;

/** More than a clean call: a failure, a retry or a fallback. */
export const hadTrouble = (r: RequestRecord) =>
  !!r.retried || !!r.fallback_route || !!r.error_class || (r.attempts ?? []).some((a) => !attemptOk(a));

export type Backoff = { base_ms: number; max_ms: number; jitter: number; retries: number };
export type Guard = { enabled: boolean; fill_missing: 'auto' | 'always' | 'never'; default_max_tokens: number; clamp: boolean; safety_margin_tokens: number };
export type Emergency = { enabled: boolean; keep_threshold: number };
export type Policy = {
  enabled: boolean;
  max_attempts: number;
  time_budget_ms: number;
  backoff: Backoff;
  max_tokens_guard: Guard;
  emergency_prune: Emergency;
  ignore_provider: boolean;
};
export type Limits = { context_window?: number; max_output_tokens?: number; source?: string };
/** A route's or alias's override: any policy field, partially, plus fallbacks and limits. */
export type Override = Partial<Omit<Policy, 'backoff' | 'max_tokens_guard' | 'emergency_prune'>> & {
  backoff?: Partial<Backoff>;
  max_tokens_guard?: Partial<Guard>;
  emergency_prune?: Partial<Emergency>;
  fallbacks?: string[];
  context_window?: number;
  max_output_tokens?: number;
};
export type Settings = Policy & {
  openrouter_catalog: boolean;
  catalog_ttl_hours: number;
  models: Record<string, Limits>;
  routes: Record<string, Override>;
  aliases: Record<string, Override>;
};
export type EffectivePolicy = Policy & { fallbacks: string[]; context_window?: number; max_output_tokens?: number };
export type CatalogStatus = { url: string; models: number; fetched_at: string | null; stale: boolean; last_error?: string; learned: number };
export type SettingsView = {
  settings: Settings;
  defaults: Settings;
  effective: { routes: Record<string, EffectivePolicy>; aliases: Record<string, EffectivePolicy> };
  catalog: CatalogStatus;
  classes: string[];
  actions: string[];
  builtin_as_of: string;
};
/** PUT body: a partial merge; null removes a route, alias or model override. */
export type SettingsPatch = Partial<Policy> & {
  openrouter_catalog?: boolean;
  catalog_ttl_hours?: number;
  models?: Record<string, Limits | null>;
  routes?: Record<string, Override | null>;
  aliases?: Record<string, Override | null>;
};

export type Top = { name: string; failures: number; recovered: number };
export type Stats = {
  days: number;
  since: string | null;
  requests: number;
  engaged: number;
  first_attempt_failed: number;
  retried: number;
  recovered: number;
  failed_after_retry: number;
  recovery_rate: number;
  fallbacks: number;
  guard: { filled_missing: number; above_max_output: number; exceeds_context_window: number };
  by_class: Record<string, { failures: number; requests: number; recovered: number }>;
  by_action: Record<string, { count: number; succeeded: number }>;
  top_failing_providers: Top[];
  top_failing_models: Top[];
  fallback_routes: { from: string; to: string; count: number }[];
};
export type LimitsView = { model: string; route?: string; alias?: string; limits: Limits; known: boolean };

/** Empty maps are omitted on the wire; the UI always wants them. */
const normalize = (v: SettingsView): SettingsView => ({
  ...v,
  settings: { ...v.settings, models: v.settings.models ?? {}, routes: v.settings.routes ?? {}, aliases: v.settings.aliases ?? {} },
  effective: { routes: v.effective?.routes ?? {}, aliases: v.effective?.aliases ?? {} },
});

export const resilienceApi = {
  settings: () => call<SettingsView>('/api/resilience/settings').then(normalize),
  save: (patch: SettingsPatch) => call<SettingsView>('/api/resilience/settings', { method: 'PUT', body: JSON.stringify(patch) }).then(normalize),
  stats: (days = 30) => call<Stats>(`/api/resilience/stats?days=${days}`),
  limits: (q: { model: string; route?: string; alias?: string }) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v) p.set(k, v);
    return call<LimitsView>(`/api/resilience/limits?${p}`);
  },
  refreshCatalog: () => call<CatalogStatus>('/api/resilience/catalog/refresh', { method: 'POST' }),
};

/** "route:model" or "route". */
export function parseFallback(s: string): { route: string; model?: string } {
  const i = s.indexOf(':');
  return i < 0 ? { route: s } : { route: s.slice(0, i), model: s.slice(i + 1) };
}
