// Who is who: provider of a route, vendor of a model, client of a request.
// Records carry `provider`, `model_vendor` and `client`; older records do not,
// so these fall back to a best guess from the base URL, the model id and the
// conversation id, and say so (inferred: true).
import type { RequestRecord, RouteView } from './api';

export type ProviderId =
  | 'anthropic' | 'openai' | 'openrouter' | 'google' | 'meta' | 'mistral' | 'deepseek' | 'qwen' | 'groq'
  | 'together' | 'xai' | 'zai' | 'ollama' | 'vllm' | 'lmstudio' | 'moonshot' | 'typesafe' | 'open-rlcd' | 'custom';

export const PROVIDER_NAMES: Record<ProviderId, string> = {
  anthropic: 'Anthropic', openai: 'OpenAI', openrouter: 'OpenRouter', google: 'Google', meta: 'Meta',
  mistral: 'Mistral AI', deepseek: 'DeepSeek', qwen: 'Qwen', groq: 'Groq', together: 'Together AI',
  xai: 'xAI', zai: 'Z.ai', ollama: 'Ollama', vllm: 'vLLM', lmstudio: 'LM Studio', moonshot: 'Moonshot AI', typesafe: 'TypeSafe', 'open-rlcd': 'open-rlcd', custom: 'Custom',
};

const HOSTS: [RegExp, ProviderId][] = [
  [/(^|\.)anthropic\.com$/, 'anthropic'],
  [/(^|\.)openrouter\.ai$/, 'openrouter'],
  [/(^|\.)(openai\.com|chatgpt\.com)$/, 'openai'],
  [/(^|\.)groq\.com$/, 'groq'],
  [/(^|\.)together\.(xyz|ai)$/, 'together'],
  [/(^|\.)deepseek\.com$/, 'deepseek'],
  [/(^|\.)mistral\.ai$/, 'mistral'],
  [/(^|\.)(googleapis\.com|google\.com)$/, 'google'],
  [/(^|\.)x\.ai$/, 'xai'],
  [/(^|\.)(z\.ai|bigmodel\.cn)$/, 'zai'],
  [/(^|\.)(moonshot\.(ai|cn))$/, 'moonshot'],
  [/(^|\.)(dashscope|aliyuncs)\./, 'qwen'],
  [/(^|\.)typesafe\.ai$/, 'typesafe'],
  [/open-rlcd|rlcd/, 'open-rlcd'],
];

const LOCAL_PORTS: Record<string, ProviderId> = { '11434': 'ollama', '1234': 'lmstudio' };

export function providerFromURL(baseURL: string | undefined): ProviderId {
  if (!baseURL) return 'custom';
  let u: URL;
  try {
    u = new URL(baseURL);
  } catch {
    return 'custom';
  }
  const host = u.hostname.toLowerCase();
  for (const [re, id] of HOSTS) if (re.test(host)) return id;
  if (LOCAL_PORTS[u.port]) return LOCAL_PORTS[u.port];
  if (/ollama/.test(host)) return 'ollama';
  if (/vllm/.test(host)) return 'vllm';
  if (/lmstudio|lm-studio/.test(host)) return 'lmstudio';
  return 'custom';
}

const asProvider = (p: string | undefined): ProviderId | undefined =>
  p && p in PROVIDER_NAMES ? (p as ProviderId) : undefined;

export function routeProvider(r: Pick<RouteView, 'base_url' | 'kind'> & { provider?: string }): ProviderId {
  return asProvider(r.provider) ?? (r.kind === 'openrouter' ? 'openrouter' : providerFromURL(r.base_url));
}

export function recordProvider(r: Pick<RequestRecord, 'upstream' | 'provider'>): ProviderId {
  return asProvider(r.provider) ?? providerFromURL(r.upstream);
}

/** The vendor of the model a record was served by. */
export function recordVendor(r: Pick<RequestRecord, 'model' | 'client_model' | 'model_vendor'>): ProviderId | undefined {
  return asProvider(r.model_vendor) ?? modelVendor(r.model || r.client_model);
}

/** BrandIcon slug for a vendor: the model family's mark where there is one. */
export const vendorIcon = (v: ProviderId | undefined) => (v === 'google' ? 'gemini' : v === 'anthropic' ? 'claude' : v);

const VENDORS: [RegExp, ProviderId][] = [
  [/claude|anthropic/, 'anthropic'],
  [/^(gpt|o[1-9]|chatgpt|codex|openai|davinci|text-embedding)/, 'openai'],
  [/gemini|gemma|palm|google/, 'google'],
  [/llama|meta/, 'meta'],
  [/mistral|mixtral|codestral|devstral|magistral|ministral|pixtral/, 'mistral'],
  [/deepseek/, 'deepseek'],
  [/qwen|qwq/, 'qwen'],
  [/grok|xai/, 'xai'],
  [/glm|zhipu|z-ai|zai/, 'zai'],
  [/kimi|moonshot/, 'moonshot'],
];

/** The company behind a model id: "qwen/qwen3-235b" -> qwen, "claude-sonnet-4-5" -> anthropic. */
export function modelVendor(model: string | undefined): ProviderId | undefined {
  if (!model) return undefined;
  const m = model.toLowerCase();
  const slash = m.indexOf('/');
  if (slash > 0) {
    const org = m.slice(0, slash);
    const byOrg: Record<string, ProviderId> = {
      anthropic: 'anthropic', openai: 'openai', google: 'google', 'meta-llama': 'meta', meta: 'meta',
      mistralai: 'mistral', deepseek: 'deepseek', 'deepseek-ai': 'deepseek', qwen: 'qwen', 'x-ai': 'xai',
      'z-ai': 'zai', thudm: 'zai', moonshotai: 'moonshot',
    };
    if (byOrg[org]) return byOrg[org];
  }
  const rest = slash > 0 ? m.slice(slash + 1) : m;
  for (const [re, id] of VENDORS) if (re.test(rest)) return id;
  return undefined;
}

export type ResolvedClient = {
  id: string;
  /** Icon slug for BrandIcon. */
  icon: string;
  name: string;
  version?: string;
  kind: 'agent' | 'sdk' | 'cli' | 'browser' | 'internal' | 'unknown';
  keyName?: string;
  /** SDK language, shown as a small badge. */
  lang?: 'python' | 'nodejs' | 'go';
  inferred: boolean;
};

// Client ids the gateway detects (gateway/internal/clients), with the icon
// each one gets. SDKs show their vendor's mark with a language badge.
const KNOWN_CLIENTS: Record<string, { icon: string; name: string; kind: ResolvedClient['kind']; lang?: ResolvedClient['lang'] }> = {
  'claude-code': { icon: 'claude-code', name: 'Claude Code', kind: 'agent' },
  codex: { icon: 'codex', name: 'Codex', kind: 'agent' },
  opencode: { icon: 'opencode', name: 'OpenCode', kind: 'agent' },
  'anthropic-python': { icon: 'anthropic', name: 'Anthropic Python SDK', kind: 'sdk', lang: 'python' },
  'anthropic-node': { icon: 'anthropic', name: 'Anthropic Node SDK', kind: 'sdk', lang: 'nodejs' },
  'anthropic-go': { icon: 'anthropic', name: 'Anthropic Go SDK', kind: 'sdk', lang: 'go' },
  'anthropic-sdk': { icon: 'anthropic', name: 'Anthropic SDK', kind: 'sdk' },
  'openai-python': { icon: 'openai', name: 'OpenAI Python SDK', kind: 'sdk', lang: 'python' },
  'openai-node': { icon: 'openai', name: 'OpenAI Node SDK', kind: 'sdk', lang: 'nodejs' },
  'openai-go': { icon: 'openai', name: 'OpenAI Go SDK', kind: 'sdk', lang: 'go' },
  'openai-sdk': { icon: 'openai', name: 'OpenAI SDK', kind: 'sdk' },
  'python-requests': { icon: 'python', name: 'Python requests', kind: 'sdk' },
  'python-httpx': { icon: 'python', name: 'Python httpx', kind: 'sdk' },
  curl: { icon: 'curl', name: 'curl', kind: 'cli' },
  browser: { icon: 'browser', name: 'Browser', kind: 'browser' },
  'rlcd-gateway': { icon: 'rlcd', name: 'RLCD Gateway', kind: 'internal' },
};

export function recordClient(r: Pick<RequestRecord, 'client' | 'conversation_id' | 'path' | 'key_name'>): ResolvedClient {
  const c = r.client;
  const keyName = c?.key_name || r.key_name || undefined;
  if (c && c.id && c.id !== 'unknown') {
    const id = c.id.toLowerCase();
    const known = KNOWN_CLIENTS[id];
    return {
      id,
      icon: known?.icon ?? id,
      name: c.name || known?.name || id,
      version: c.version,
      kind: c.kind ?? known?.kind ?? 'unknown',
      keyName,
      lang: known?.lang,
      inferred: false,
    };
  }
  // Fallback before F5: Claude Code puts its session id in metadata.user_id
  // ("cc-" conversations) and Codex sends a session header ("cx-").
  const conv = r.conversation_id ?? '';
  if (!c && conv.startsWith('cc-')) return { ...KNOWN_CLIENTS['claude-code'], id: 'claude-code', keyName, inferred: true };
  if (!c && conv.startsWith('cx-') && r.path.startsWith('/openai/')) return { ...KNOWN_CLIENTS.codex, id: 'codex', keyName, inferred: true };
  return { id: 'unknown', icon: 'unknown', name: '', kind: 'unknown', keyName, inferred: !c };
}

/** The model to show for a record: what was sent upstream, else what the client asked for. */
export const recordModel = (r: Pick<RequestRecord, 'model' | 'client_model'>) => r.model || r.client_model || '';

/** A model call of any protocol (not count_tokens, models, embeddings...). */
export const isModelCall = (r: Pick<RequestRecord, 'method' | 'path'>) =>
  r.method === 'POST' && /\/(messages|chat\/completions|responses|responses\/compact)$/.test(r.path);

/** A System One decision call (POST /v1/systemone or /v1/decisions). */
export const isDecisionCall = (r: Pick<RequestRecord, 'path' | 'protocol'>) => r.protocol === 'systemone' || /\/v1\/(systemone|decisions)$/.test(r.path);

/** Resolve a client slug the way BrandIcon expects (for insights groups). */
export function clientFromSlug(id: string, label?: string): ResolvedClient {
  const known = KNOWN_CLIENTS[id];
  if (!known) return { id, icon: id === 'unknown' ? 'unknown' : id, name: id === 'unknown' ? '' : label || id, kind: 'unknown', inferred: false };
  return { ...known, id, name: label || known.name, inferred: false };
}
