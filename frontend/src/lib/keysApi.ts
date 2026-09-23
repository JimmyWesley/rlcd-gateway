// Types mirror the Go structs in gateway/internal/keys.
import { call } from './api';

export type KeyLimits = {
  aliases: string[];
  routes: string[];
  rpm?: number;
  tokens_per_day?: number;
};

export type KeyUsage = {
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
  est_cost_usd: number;
};

export type KeyView = KeyLimits & {
  id: string;
  name: string;
  hint: string;
  created: string;
  revoked?: string;
  last_used?: string;
  usage: KeyUsage;
  today_tokens: number;
};

/** The only response that ever carries a key in plaintext. */
export type KeyCreated = { key: string; view: KeyView; note: string };

const json = (method: string, body: unknown): RequestInit => ({ method, body: JSON.stringify(body) });
const enc = encodeURIComponent;

export const keysApi = {
  list: () => call<KeyView[]>('/api/keys'),
  create: (name: string, l: KeyLimits) => call<KeyCreated>('/api/keys', json('POST', { name, ...l })),
  update: (id: string, name: string, l: KeyLimits) => call<KeyView>(`/api/keys/${enc(id)}`, json('PUT', { name, ...l })),
  revoke: (id: string) => call<KeyView>(`/api/keys/${enc(id)}/revoke`, { method: 'POST' }),
};
