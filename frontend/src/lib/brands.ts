// Who is who: provider of a route, vendor of a model, client of a request.
// F5 adds explicit `provider` and `client` fields; until a record has them,
// these derive a best guess from the base URL, the model id and the
// conversation id, and say so (inferred: true).
import type { RequestRecord, RouteView } from './api';

export type ProviderId =
  | 'anthropic' | 'openai' | 'openrouter' | 'google' | 'meta' | 'mistral' | 'deepseek' | 'qwen' | 'groq'
  | 'together' | 'xai' | 'zai' | 'ollama' | 'vllm' | 'lmstudio' | 'moonshot' | 'custom';

export const PROVIDER_NAMES: Record<ProviderId, string> = {
  anthropic: 'Anthropic', openai: 'OpenAI', openrouter: 'OpenRouter', google: 'Google', meta: 'Meta',
  mistral: 'Mistral AI', deepseek: 'DeepSeek', qwen: 'Qwen', groq: 'Groq', together: 'Together AI',
  xai: 'xAI', zai: 'Z.ai', ollama: 'Ollama', vllm: 'vLLM', lmstudio: 'LM Studio', moonshot: 'Moonshot AI', custom: 'Custom',
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

export type ClientId = 'claude-code' | 'codex' | 'opencode' | 'anthropic-sdk' | 'openai-sdk' | 'curl' | 'browser' | 'unknown';

export type ResolvedClient = {
  id: string;
  /** Icon slug for BrandIcon. */
  icon: string;
  name: string;
  version?: string;
  kind: 'agent' | 'sdk' | 'cli' | 'browser' | 'unknown';
  keyName?: string;
  /** SDK language, shown as a small badge. */
  lang?: 'python' | 'nodejs';
  inferred: boolean;
};

const KNOWN_CLIENTS: Record<string, { icon: string; name: string; kind: ResolvedClient['kind']; lang?: 'python' | 'nodejs' }> = {
  'claude-code': { icon: 'claude-code', name: 'Claude Code', kind: 'agent' },
  codex: { icon: 'codex', name: 'Codex', kind: 'agent' },
  opencode: { icon: 'opencode', name: 'OpenCode', kind: 'agent' },
  'anthropic-sdk-python': { icon: 'anthropic', name: 'Anthropic SDK', kind: 'sdk', lang: 'python' },
  'anthropic-sdk-node': { icon: 'anthropic', name: 'Anthropic SDK', kind: 'sdk', lang: 'nodejs' },
  'anthropic-sdk': { icon: 'anthropic', name: 'Anthropic SDK', kind: 'sdk' },
  'openai-sdk-python': { icon: 'openai', name: 'OpenAI SDK', kind: 'sdk', lang: 'python' },
  'openai-sdk-node': { icon: 'openai', name: 'OpenAI SDK', kind: 'sdk', lang: 'nodejs' },
  'openai-sdk': { icon: 'openai', name: 'OpenAI SDK', kind: 'sdk' },
  curl: { icon: 'curl', name: 'curl', kind: 'cli' },
  browser: { icon: 'browser', name: 'Browser', kind: 'browser' },
};

export function recordClient(r: Pick<RequestRecord, 'client' | 'conversation_id' | 'path'>): ResolvedClient {
  const c = r.client;
  if (c && (c.id || c.name)) {
    const id = (c.id ?? c.name ?? 'unknown').toLowerCase();
    const known = KNOWN_CLIENTS[id];
    return {
      id,
      icon: known?.icon ?? id,
      name: c.name || known?.name || id,
      version: c.version,
      kind: c.kind ?? known?.kind ?? 'unknown',
      keyName: c.key_name,
      lang: known?.lang,
      inferred: false,
    };
  }
  // Fallback before F5: Claude Code puts its session id in metadata.user_id
  // ("cc-" conversations) and Codex sends a session header ("cx-").
  const conv = r.conversation_id ?? '';
  if (conv.startsWith('cc-')) return { ...KNOWN_CLIENTS['claude-code'], id: 'claude-code', inferred: true };
  if (conv.startsWith('cx-') && r.path.startsWith('/openai/')) return { ...KNOWN_CLIENTS.codex, id: 'codex', inferred: true };
  return { id: 'unknown', icon: 'unknown', name: '', kind: 'unknown', inferred: true };
}

/** The model to show for a record: what was sent upstream, else what the client asked for. */
export const recordModel = (r: Pick<RequestRecord, 'model' | 'client_model'>) => r.model || r.client_model || '';

export const isMessagesPath = (p: string) => p === '/v1/messages';
export const isOpenAIPath = (p: string) => p.startsWith('/openai/') || p === '/v1/responses' || p === '/v1/chat/completions';
