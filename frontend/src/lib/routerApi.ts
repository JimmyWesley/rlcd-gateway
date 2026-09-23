// Types mirror the Go structs in gateway/internal/router.
import { call, type Protocol } from './api';

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
  /** A protocol, or "openai" for both OpenAI formats. */
  protocol?: Protocol | 'openai';
};

export type QuestionType = 'choice' | 'score' | 'noul';
export type DecisionQuestion = {
  type: QuestionType;
  instructions: string;
  /** label -> description for choice, an ordered legend for score, absent for noul. */
  criteria?: Record<string, string> | string[] | null;
};
export const FACTS = ['latest_user_text', 'goal', 'recent_tool_calls', 'context_tokens', 'has_tools', 'has_images', 'client', 'protocol', 'model_requested'] as const;
export type Fact = (typeof FACTS)[number];
export type DecisionInputs = { facts?: string[]; goal_turns?: number; headers?: string[] };
export type BranchWhen = { equals?: string; in?: string[]; op?: '>=' | '<=' | '==' | '<' | 'between'; value?: number; min?: number; max?: number };
export type Target = { route?: string; model?: string; alias?: string; next_rule?: boolean };
export type Branch = { label?: string; when: BranchWhen; then: Target };
export type EvaluateMode = 'conversation_start' | 'every_request' | 'when_context_over';

export type Rule = {
  name: string;
  enabled: boolean;
  kind?: 'match' | 'auto' | 'decision';
  route?: string;
  candidates?: string[];
  timeout_ms?: number;
  min_confidence?: number;
  override_sticky?: boolean;
  when: Match;
  // Decision rules
  question?: DecisionQuestion;
  inputs?: DecisionInputs;
  backend?: string;
  backend_model?: string;
  branches?: Branch[];
  else?: Target | null;
  evaluate?: EvaluateMode;
  context_over_tokens?: number;
};

/** What a decision rule asked and got. */
export type DecisionTrace = {
  question_type: QuestionType;
  answer?: string;
  choice?: string;
  index?: number;
  max_index?: number;
  label?: string;
  score?: number;
  noul?: number;
  confidence?: number;
  min_confidence?: number;
  probabilities?: Record<string, number>;
  backend: string;
  provider?: string;
  model?: string;
  latency_ms: number;
  forward_ms?: number;
  branch: number;
  branch_label?: string;
  outcome: 'branch' | 'else' | 'fall_through';
  target?: Target;
  route?: string;
  upstream_model?: string;
  error?: string;
  dry_run?: boolean;
  state?: Record<string, unknown>;
  summary: string;
};

export type RuleTestResult = {
  rule: string;
  input: 'request' | 'text';
  request_id?: string;
  protocol: string;
  facts: Facts;
  conditions: Check[];
  conditions_ok: boolean;
  decision: DecisionTrace | null;
  route?: string;
  model?: string;
  reason: string;
  notes?: string[];
};

export type RuleTemplate = { id: string; title: string; description: string; rule: Rule; slots: { branch: number; label: string; hint: string }[] };

/** "opus", "cheap:qwen/qwen3", "alias 'smart'", "next rule": as the route reason writes a target. */
export function targetString(t: Target | null | undefined): string {
  if (!t) return 'next rule';
  if (t.alias) return `alias '${t.alias}'`;
  if (t.route && t.model) return `${t.route}:${t.model}`;
  if (t.route) return t.route;
  return 'next rule';
}

/** A decision rule's route reason, parsed: "decision rule 'x': <answer> (conf 0.81, jev 190 ms) → target". */
export type DecisionReason = { rule: string; redecided: boolean; dryRun: boolean; answer?: string; confidence?: number; backend?: string; ms?: number; error?: string; outcome: 'branch' | 'else' | 'fall_through'; target: string };
export function parseDecisionReason(reason: string | undefined): DecisionReason | null {
  const m = /^decision rule '([^']+)'( re-decided)?: (.*)$/.exec(reason ?? '');
  if (!m) return null;
  let rest = m[3];
  const dryRun = rest.startsWith('[dry run] ');
  if (dryRun) rest = rest.slice(10);
  const arrow = rest.lastIndexOf(' → ');
  const tail = arrow >= 0 ? rest.slice(arrow + 3) : 'next rule';
  let head = arrow >= 0 ? rest.slice(0, arrow) : rest;
  const outcome = tail === 'next rule' ? 'fall_through' : tail.startsWith('else ') ? 'else' : 'branch';
  const meta = /\((?:conf ([\d.]+), )?(\S+) (\d+) ms\)/.exec(head);
  let error: string | undefined;
  const colon = head.indexOf('): ');
  if (colon >= 0) { error = head.slice(colon + 3); head = head.slice(0, colon + 1); }
  if (error === 'no branch matched') error = undefined;
  const answer = head.replace(/\s*\(.*\)\s*$/, '').trim() || undefined;
  return {
    rule: m[1], redecided: !!m[2], dryRun, answer, error, outcome, target: outcome === 'else' ? tail.slice(5) : tail,
    confidence: meta?.[1] ? Number(meta[1]) : undefined, backend: meta?.[2], ms: meta?.[3] ? Number(meta[3]) : undefined,
  };
}

export type RulesDoc = {
  sticky: boolean;
  ttl_hours: number;
  background_bypass: boolean;
  rules: Rule[];
};

export type RouteKind = 'anthropic' | 'openrouter' | 'openai';

export type RouterRoute = {
  name: string;
  kind: RouteKind;
  base_url: string;
  auth: 'passthrough' | 'key';
  model?: string;
  api_key_env?: string;
  has_key: boolean;
  headers: Record<string, string>;
  provider: string;
  protocols: Protocol[];
  description?: string;
  active: boolean;
  openai_default: boolean;
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
  headers: Record<string, string>;
  provider: string;
};

export type Alias = { name: string; route: string; model?: string; description?: string };
export type AliasView = Alias & { protocols: Protocol[]; provider: string };

export type Check = { condition: string; ok: boolean; detail: string };

export type RuleTrace = {
  index: number;
  name: string;
  kind: 'match' | 'auto' | 'decision';
  route?: string;
  result: 'matched' | 'no_match' | 'disabled' | 'skipped' | 'fallback' | 'error' | 'not_reached' | 'else';
  reason?: string;
  checks?: Check[];
  decision?: DecisionTrace;
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
  protocol?: Protocol;
};

export type DryRun = {
  request_id: string;
  decision: { route: string; reason: string; model?: string; alias?: string; error?: string };
  ok: boolean;
  source: 'rule' | 'auto' | 'sticky' | 'override' | 'none' | 'alias' | 'decision';
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
  aliases: () => call<AliasView[]>('/api/router/aliases'),
  saveAliases: (a: Alias[]) => call<AliasView[]>('/api/router/aliases', json('PUT', a)),
  setOpenAIDefault: (name: string) => call<RouterRoute[]>('/api/router/openai-default', json('PUT', { name })),
  dryRun: (request_id: string, ignore_sticky: boolean) =>
    call<DryRun>('/api/router/dryrun', json('POST', { request_id, ignore_sticky })),
  conversations: () => call<Assignment[]>('/api/router/conversations'),
  resetConversation: (id: string) => call<{ ok: boolean }>(`/api/router/conversations/${enc(id)}`, { method: 'DELETE' }),
  testRule: (body: { rule: Rule; request_id?: string; text?: string; protocol?: string }) => call<RuleTestResult>('/api/router/rules/test', json('POST', body)),
  templates: () => call<RuleTemplate[]>('/api/router/rule-templates'),
  resetAll: () => call<{ removed: number }>('/api/router/conversations', { method: 'DELETE' }),
};
