// Types mirror gateway/internal/store (settings.go, retention) and the
// storage endpoints in gateway/internal/api.
import { call } from './api';

export type BodiesPolicy = 'full' | 'errors_only' | 'none';

export type StorageSettings = {
  detail_max_age: string;
  summary_max_age: string;
  max_total_bytes: number;
  bodies: BodiesPolicy;
  conversation_ttl: string;
};

export type StorageSettingsView = StorageSettings & {
  effective_bodies: BodiesPolicy;
  log_bodies: boolean;
  defaults: StorageSettings;
};

export type PurgeDetail = { id: string; time: string; reason: 'age' | 'size'; bytes: number };

export type JanitorReport = {
  time: string;
  duration_ms: number;
  dry_run: boolean;
  trigger: string;
  conversations_active: number;
  conversations_expired: number;
  expired_ids: string[];
  pinned_requests: number;
  expired_state: { prune?: number; router?: number; recall?: number };
  purge: {
    details?: PurgeDetail[];
    details_deleted: number;
    details_by_age: number;
    details_by_size: number;
    detail_bytes: number;
    pinned_kept: number;
    blobs_deleted: number;
    blob_bytes: number;
    blobs_pending: number;
    index_files_deleted?: string[];
    index_bytes: number;
    temp_files_deleted: number;
    total_bytes_before: number;
    total_bytes_after: number;
    over_limit: boolean;
    warnings?: string[];
  };
  errors?: string[];
};

export type StorageStats = {
  bytes: { blobs: number; records: number; index: number; prune_state: number; recall_events: number; router_state: number; total: number; managed_total: number };
  counts: { records: number; legacy_records: number; blobs: number; index_files: number; tombstones: number };
  oldest_record?: string | null;
  newest_record?: string | null;
  logical_bytes: number;
  deduped_bytes: number;
  stored_bytes: number;
  dedup_ratio: number;
  compression_ratio: number;
  total_ratio: number;
  pinned: { conversations: number; requests: number };
  settings: StorageSettingsView;
  last_janitor_run: JanitorReport | null;
  janitor_interval_seconds: number;
};

export const storageApi = {
  stats: () => call<StorageStats>('/api/storage'),
  settings: () => call<StorageSettingsView>('/api/storage/settings'),
  saveSettings: (s: Partial<StorageSettings>) => call<StorageSettingsView>('/api/storage/settings', { method: 'PUT', body: JSON.stringify(s) }),
  purge: (dryRun: boolean) => call<JanitorReport>('/api/storage/purge', { method: 'POST', body: JSON.stringify({ dry_run: dryRun }) }),
};

/** "14d" / "36h" / "1d12h" / "90m" / "0" / seconds -> hours (0 = keep forever). */
export function durationHours(s: string): number {
  if (!s || s === '0') return 0;
  if (/^\d+$/.test(s)) return Number(s) / 3600;
  let h = 0;
  for (const m of s.matchAll(/(\d+(?:\.\d+)?)([dhms])/g)) {
    const v = Number(m[1]);
    h += m[2] === 'd' ? v * 24 : m[2] === 'h' ? v : m[2] === 'm' ? v / 60 : v / 3600;
  }
  return h;
}

/** Hours -> the gateway's duration syntax ("14d", "36h", "0"). */
export function hoursToDuration(h: number): string {
  if (!h) return '0';
  return h % 24 === 0 ? `${h / 24}d` : `${Math.round(h)}h`;
}
