// Types mirror the Go structs in gateway/internal/router.
import { call } from '../api';

export type HeaderCond = { name: string; equals?: string; contains?: string };

export type Match = {
  model?: string;
  min_context_tokens?: number;
  max_context_tokens?: number;
  has_tools?: boolean;
  has_images?: boolean;
  has_thinking?: boolean;
  background?: boolean;
  max_tokens_lte?: number;
  headers?: HeaderCond[];
  conversation?: string;
};

export type Rule = {
  name: string;
  enabled: boolean;
  kind?: 'match' | 'auto';
  route?: string;
  candidates?: string[];
  timeout_ms?: number;
  min_confidence?: number;
  override_sticky?: boolean;
  when: Match;
};

export type RulesDoc = {
  sticky: boolean;
  ttl_hours: number;
  background_bypass: boolean;
  rules: Rule[];
};

export type RouterRoute = {
  name: string;
  kind: 'anthropic' | 'openrouter';
  base_url: string;
  auth: 'passthrough' | 'key';
  model?: string;
  api_key_env?: string;
  has_key: boolean;
  description?: string;
  active: boolean;
  used_by: string[];
};

export type RouteInput = {
  kind: string;
  base_url: string;
  auth: string;
  model: string;
  api_key?: string;
  api_key_env: string;
  clear_key?: boolean;
  description: string;
};

export type Check = { condition: string; ok: boolean; detail: string };

export type RuleTrace = {
  index: number;
  name: string;
  kind: 'match' | 'auto';
  route?: string;
  result: 'matched' | 'no_match' | 'disabled' | 'skipped' | 'fallback' | 'error' | 'not_reached';
  reason?: string;
  checks?: Check[];
};

export type Assignment = {
  conversation_id: string;
  route: string;
  rule?: string;
  reason: string;
  created: string;
  last_seen: string;
  turns: number;
};

export type Facts = {
  model: string;
  context_tokens: number;
  messages: number;
  max_tokens: number;
  tools: number;
  has_images: boolean;
  thinking_param?: string;
  thinking_blocks?: number;
  has_thinking: boolean;
  cache_control: boolean;
  background: boolean;
  background_signals?: string[];
  conversation_id: string;
};

export type DryRun = {
  request_id: string;
  decision: { route: string; reason: string };
  ok: boolean;
  source: 'rule' | 'auto' | 'sticky' | 'override' | 'none';
  sticky_action: 'create' | 'replace' | 'use' | 'none';
  sticky?: Assignment;
  facts: Facts;
  trace: RuleTrace[];
  notes?: string[];
  effective_route: string;
  ignore_sticky: boolean;
  logged: { route: string; route_reason?: string };
};

const json = (method: string, body: unknown): RequestInit => ({ method, body: JSON.stringify(body) });
const enc = encodeURIComponent;

export const routerApi = {
  rules: () => call<RulesDoc>('/api/router/rules'),
  saveRules: (d: RulesDoc) => call<RulesDoc>('/api/router/rules', json('PUT', d)),
  routes: () => call<RouterRoute[]>('/api/router/routes'),
  saveRoute: (name: string, r: RouteInput) => call<RouterRoute[]>(`/api/router/routes/${enc(name)}`, json('PUT', r)),
  deleteRoute: (name: string) => call<RouterRoute[]>(`/api/router/routes/${enc(name)}`, { method: 'DELETE' }),
  dryRun: (request_id: string, ignore_sticky: boolean) =>
    call<DryRun>('/api/router/dryrun', json('POST', { request_id, ignore_sticky })),
  conversations: () => call<Assignment[]>('/api/router/conversations'),
  resetConversation: (id: string) => call<{ ok: boolean }>(`/api/router/conversations/${enc(id)}`, { method: 'DELETE' }),
  resetAll: () => call<{ removed: number }>('/api/router/conversations', { method: 'DELETE' }),
};

/** One-line summary of a rule's conditions. */
export function describeMatch(m: Match): string {
  const parts: string[] = [];
  const flag = (v: boolean | undefined, name: string) => {
    if (v !== undefined) parts.push(v ? name : `no ${name}`);
  };
  if (m.model) parts.push(`model ~ /${m.model}/`);
  if (m.min_context_tokens) parts.push(`context ≥ ${m.min_context_tokens}`);
  if (m.max_context_tokens) parts.push(`context ≤ ${m.max_context_tokens}`);
  flag(m.has_tools, 'tools');
  flag(m.has_images, 'images');
  flag(m.has_thinking, 'thinking');
  if (m.background !== undefined) parts.push(m.background ? 'background' : 'not background');
  if (m.max_tokens_lte) parts.push(`max_tokens ≤ ${m.max_tokens_lte}`);
  for (const h of m.headers ?? []) {
    parts.push(h.equals ? `${h.name} = ${h.equals}` : h.contains ? `${h.name} ∋ ${h.contains}` : `${h.name} present`);
  }
  if (m.conversation) parts.push(`conversation ${m.conversation.slice(0, 14)}…`);
  return parts.length ? parts.join(' · ') : 'every request';
}
