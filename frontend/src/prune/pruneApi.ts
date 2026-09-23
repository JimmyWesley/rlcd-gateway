// Types mirror the Go structs in gateway/internal/prune.
import { call } from '../api';

export type Mode = 'shadow' | 'enforce';
export type PresetName = 'conservative' | 'balanced' | 'aggressive';

export type Price = { input: number; output: number; cache_read: number; cache_write: number };

/** The stored section: an absent/null field falls back to the preset. */
export type PruneSettings = {
  enabled?: boolean | null;
  mode?: Mode | '';
  preset?: PresetName | '';
  keep_errors?: boolean | null;
  keep_edits?: boolean | null;
  drop_superseded_reads?: boolean | null;
  keep_last_n_turns?: number | null;
  min_block_tokens?: number | null;
  always_keep_user_text?: boolean | null;
  keep_threshold?: number | null;
  criteria?: string;
  prune_tools?: boolean | null;
  prune_system?: boolean | null;
  prune_conversation_text?: boolean | null;
  epoch_tokens?: number | null;
  floor_tokens?: number | null;
  selector_timeout_ms?: number | null;
  edit_tools?: string[];
  read_tools?: string[];
  prices?: Record<string, Price>;
};

export type Effective = {
  enabled: boolean;
  mode: Mode;
  preset: PresetName;
  keep_errors: boolean;
  keep_edits: boolean;
  drop_superseded_reads: boolean;
  keep_last_n_turns: number;
  min_block_tokens: number;
  always_keep_user_text: boolean;
  keep_threshold: number;
  criteria: string;
  prune_tools: boolean;
  prune_system: boolean;
  prune_conversation_text: boolean;
  epoch_tokens: number;
  floor_tokens: number;
  selector_timeout_ms: number;
  edit_tools: string[];
  read_tools: string[];
  prices: Record<string, Price>;
};

export type Preset = {
  name: PresetName;
  description: string;
  keep_errors: boolean;
  keep_edits: boolean;
  drop_superseded_reads: boolean;
  keep_last_n_turns: number;
  min_block_tokens: number;
  always_keep_user_text: boolean;
  keep_threshold: number;
  epoch_tokens: number;
  floor_tokens: number;
};

export type PruneConfig = {
  settings: PruneSettings;
  effective: Effective;
  presets: Preset[];
  default_criteria: string;
  default_prices: Record<string, Price>;
  prices_as_of: string;
  log_bodies: boolean;
};

export type PruneSummary = {
  mode: Mode;
  applied: boolean;
  epoch_ran: boolean;
  candidates: number;
  dropped: number;
  new_drops: number;
  est_tokens_before: number;
  est_tokens_after: number;
  saved_tokens: number;
  est_cost_before: number;
  est_cost_after: number;
  cache_invalidating: boolean;
  selector_ms: number;
  selector_error?: string;
  next_epoch_at: number;
  warning?: string;
};

export type Decision = 'keep' | 'drop';

export type BlockReport = {
  key: string;
  id: string;
  ir_key: string;
  tool_use_id?: string;
  kind: string;
  role?: string;
  name?: string;
  what?: string;
  msg: number;
  is_error?: boolean;
  tokens: number;
  after: number;
  decision: Decision;
  reason: string;
  protected?: string;
  score?: number;
  preview: string;
  marker?: string;
  first_req?: string;
  new?: boolean;
  recalled?: boolean;
};

export type PruneDetail = PruneSummary & {
  preset: PresetName;
  threshold: number;
  epoch: number;
  goal: string;
  recent_activity: string;
  protocol?: string;
  cost: { before: number; after: number; priced_as: string; cached: boolean; automatic_cache?: boolean; invalid_from_tokens: number };
  blocks: BlockReport[];
};

export type Verdict = 'should_keep' | 'should_drop';

export type Feedback = {
  id: string;
  time: string;
  request_id: string;
  key: string;
  verdict: Verdict;
  note?: string;
  /** "recall": derived from a successful rlcd_recall, not deletable here. */
  source?: 'recall';
  kind: string;
  name?: string;
  what?: string;
  tokens: number;
  preview: string;
  goal: string;
  decision: Decision;
  reason: string;
  score?: number;
};

export type ReplayCase = {
  id: string;
  request_id: string;
  key: string;
  verdict: Verdict;
  decision: Decision;
  reason: string;
  score?: number;
  agrees: boolean;
  error?: string;
  then: Decision;
  then_agreed: boolean;
};

export type ReplayReport = {
  cases: ReplayCase[];
  agree: number;
  disagree: number;
  errors: number;
  fixed: number;
  broken: number;
  selector_ms: number;
};

export type ModeTotals = {
  requests: number;
  pruned: number;
  saved_tokens: number;
  saved_usd: number;
  cache_invalidations: number;
};

export type PruneStats = {
  requests: number;
  epochs: number;
  selector_errors: number;
  avg_selector_ms: number;
  enforce: ModeTotals;
  shadow: ModeTotals;
  feedback: number;
  recall_feedback: number;
  window: number;
};

export const pruneApi = {
  config: () => call<PruneConfig>('/api/prune/config'),
  saveConfig: (s: PruneSettings) => call<PruneConfig>('/api/prune/config', { method: 'PUT', body: JSON.stringify(s) }),
  stats: () => call<PruneStats>('/api/prune/stats'),
  feedback: () => call<Feedback[]>('/api/prune/feedback'),
  addFeedback: (f: { request_id: string; key: string; verdict: Verdict; note?: string }) =>
    call<Feedback>('/api/prune/feedback', { method: 'POST', body: JSON.stringify(f) }),
  deleteFeedback: (id: string) => call<{ deleted: boolean }>(`/api/prune/feedback/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  replay: () => call<ReplayReport>('/api/prune/replay', { method: 'POST' }),
};

/** Dollar estimates are small per turn; keep enough digits to be readable. */
export function usd(v: number | undefined): string {
  if (v == null) return '—';
  const a = Math.abs(v);
  const digits = a >= 10 ? 2 : a >= 0.1 ? 3 : 4;
  return `${v < 0 ? '−' : ''}$${a.toFixed(digits)}`;
}

export const REASONS: Record<string, string> = {
  protected: 'protected',
  sticky: 'sticky (decided earlier)',
  selector: 'selector',
  pending: 'pending next epoch',
  fail_open: 'kept: selector failed',
  no_answer: 'kept: no answer',
  drop_superseded_reads: 'superseded read',
  keep_errors: 'error result',
  keep_edits: 'edit result',
  always_keep_user_text: 'user text',
  min_block_tokens: 'too small',
  recalled: 'recalled by the model',
};
