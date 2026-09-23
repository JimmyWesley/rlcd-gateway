// Types mirror gateway/internal/decisions (audit.go, settings.go, mirror.go)
// and the record summary in gateway/internal/store/decisions.go.
import { call, type ClientInfo, type Usage } from './api';

export type QType = 'choice' | 'score' | 'noul' | string;
export type Source = 'client' | 'prune' | 'router';

/** One question as summarised on a record (and in the audit list). */
export type DecisionQuestion = {
  id: string;
  type: QType;
  labels?: string[];
  answer?: string;
  choice?: string;
  score?: number;
  noul?: number;
  confidence?: number;
  top_prob?: number;
  // Audit list only
  outcome?: string | boolean | number | null;
  outcome_time?: string;
  correct?: boolean;
  matched?: boolean;
};

/** record.decisions */
export type DecisionSummary = {
  backend: string;
  source: Source;
  parent_id?: string;
  think?: boolean;
  state_bytes: number;
  forward_ms?: number;
  total_ms?: number;
  questions: DecisionQuestion[];
};

export type MirrorQuestion = { id: string; answer?: string; confidence?: number; primary_answer?: string; primary_confidence?: number; agree: boolean };

export type Mirror = {
  request_id: string;
  time: string;
  backend: string;
  model: string;
  status: number;
  error?: string;
  duration_ms: number;
  forward_ms?: number;
  total_ms?: number;
  primary_backend: string;
  primary_model: string;
  primary_duration_ms: number;
  primary_forward_ms?: number;
  usage?: Usage;
  est_cost_usd?: number;
  questions: MirrorQuestion[];
  compared: number;
  agreed: number;
};

export type DecisionItem = {
  request_id: string;
  time: string;
  source: Source;
  parent_id?: string;
  backend: string;
  provider: string;
  model: string;
  client_model?: string;
  client?: ClientInfo;
  key_id?: string;
  key_name?: string;
  conversation_id?: string;
  status: number;
  error?: string;
  duration_ms: number;
  ttfb_ms: number;
  forward_ms?: number;
  total_ms?: number;
  think?: boolean;
  state_bytes: number;
  usage?: Usage;
  est_cost_usd?: number;
  questions: DecisionQuestion[];
  mirror?: Mirror;
};

export type DecisionList = { items: DecisionItem[]; total: number; next_cursor?: string };

export type Group = {
  requests: number;
  errors: number;
  error_rate: number;
  p50_ms: number;
  p95_ms: number;
  forward_p50_ms: number;
  forward_p95_ms: number;
  input_tokens: number;
  output_tokens: number;
  est_cost_usd: number;
};

export type Bin = { lo: number; hi: number; n: number; mean_confidence?: number; accuracy?: number };

export type QuestionStats = {
  count: number;
  types: Record<string, number> | string[];
  answers: Record<string, number>;
  confidence_histogram: number[];
  mean_confidence: number;
  outcomes: number;
  correct: number;
  accuracy?: number | null;
  ece?: number | null;
  bins: Bin[];
  mirror?: { compared: number; agreed: number; agreement_rate?: number | null };
};

export type MirrorStats = {
  calls: number;
  errors: number;
  compared: number;
  agreed: number;
  agreement_rate?: number | null;
  primary_p50_ms: number;
  mirror_p50_ms: number;
  primary_p95_ms: number;
  mirror_p95_ms: number;
};

export type DecisionStats = {
  total: Group;
  by_backend: Record<string, Group>;
  by_model: Record<string, Group>;
  by_source: Record<string, Group>;
  questions: Record<string, QuestionStats>;
  confidence_histogram: number[];
  mirror: MirrorStats & { by_backend?: Record<string, MirrorStats>; skipped: number; save_failed: number };
  internal: { logged: number; dropped: number; failed: number };
};

export type BackendView = { name: string; base_url: string; auth: 'key' | 'passthrough'; token_env?: string; has_token: boolean; provider: string; implicit?: boolean };
export type MirrorSettings = { backend: string; sample_rate: number; model?: string };
export type DecisionSettingsView = {
  backends: BackendView[];
  default_backend: string;
  models: Record<string, string>;
  mirror: MirrorSettings | null;
  log_internal: boolean;
  error?: string;
};
export type BackendInput = { base_url: string; auth: string; token?: string; token_env?: string; provider?: string; clear_token?: boolean };
export type DecisionSettingsInput = {
  backends: Record<string, BackendInput>;
  default_backend: string;
  models: Record<string, string>;
  mirror: MirrorSettings | null;
  log_internal: boolean;
};

export type DecisionFilters = Partial<{
  since: string;
  from: string;
  to: string;
  question: string;
  model: string;
  backend: string;
  client: string;
  key: string;
  answer: string;
  source: Source | '';
  status: 'ok' | 'error' | '';
  outcome: 'with' | 'without' | '';
  confidence_below: string;
  limit: string;
  cursor: string;
}>;

export function query(f: DecisionFilters): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) if (v) q.set(k, String(v));
  const s = q.toString();
  return s ? `?${s}` : '';
}

export const decisionsApi = {
  list: (f: DecisionFilters) => call<DecisionList>(`/api/decisions${query(f)}`),
  stats: (f: DecisionFilters) => call<DecisionStats>(`/api/decisions/stats${query(f)}`),
  exportURL: (f: DecisionFilters, format: 'csv' | 'jsonl') => {
    const q = query({ ...f, limit: '', cursor: '' });
    return `/api/decisions/export${q}${q ? '&' : '?'}format=${format}`;
  },
  outcome: (id: string, question: string, outcome: string | boolean | number | null) =>
    call<DecisionItem>(`/api/decisions/${encodeURIComponent(id)}/outcome`, { method: 'POST', body: JSON.stringify({ question, outcome }) }),
  settings: () => call<DecisionSettingsView>('/api/decisions/settings'),
  saveSettings: (s: DecisionSettingsInput) => call<DecisionSettingsView>('/api/decisions/settings', { method: 'PUT', body: JSON.stringify(s) }),
};

/** Probabilities from a System One response body, per question: label -> p. */
export function responseProbabilities(body: string | undefined): Record<string, { probs: [string, number][]; legend?: Record<string, string> }> {
  const out: Record<string, { probs: [string, number][]; legend?: Record<string, string> }> = {};
  try {
    const v = JSON.parse(body ?? '') as { answers?: Record<string, { probabilities?: Record<string, number>; legend?: Record<string, string>; noul?: number; type?: string }> };
    for (const [q, a] of Object.entries(v.answers ?? {})) {
      let probs: [string, number][] = Object.entries(a.probabilities ?? {});
      if (a.legend) probs = probs.map(([k, p]) => [a.legend![k] ?? k, p]);
      if (!probs.length && typeof a.noul === 'number') probs = [['yes', a.noul], ['no', 1 - a.noul]];
      out[q] = { probs, legend: a.legend };
    }
  } catch {
    // Not JSON (or not logged): the summary still has the answer and confidence.
  }
  return out;
}

/** The System One state from a logged request body, pretty-printed. */
export function requestState(body: string | undefined): string | null {
  try {
    const v = JSON.parse(body ?? '') as { state?: unknown };
    if (v.state === undefined) return null;
    return typeof v.state === 'string' ? v.state : JSON.stringify(v.state, null, 2);
  } catch {
    return null;
  }
}

/** Question definitions (instructions, criteria) from a logged request body. */
export function requestQuestions(body: string | undefined): Record<string, { instructions?: string; criteria?: unknown }> {
  try {
    const v = JSON.parse(body ?? '') as { questions?: Record<string, { instructions?: string; criteria?: unknown }> };
    return v.questions ?? {};
  } catch {
    return {};
  }
}
