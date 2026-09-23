// Types mirror gateway/internal/setup (AgentStatus) and the endpoints in
// gateway/internal/adapters/agents.go.
import { call } from './api';

export type AgentSource = {
  scope: string;
  path: string;
  exists: boolean;
  set: boolean;
  base_url?: string;
  gateway: boolean;
  error?: string;
};

export type AgentAuth = {
  mode: 'subscription' | 'api-key' | 'bearer' | 'api-key-helper' | 'profile' | 'cloud' | 'unknown';
  label: string;
  source?: string;
  keeps_subscription: boolean;
  note?: string;
};

export type AgentStatus = {
  name: 'claude' | 'codex' | 'opencode';
  title: string;
  installed: boolean;
  binary?: string;
  version?: string;
  config_path: string;
  config_exists: boolean;
  configured: boolean;
  effective: boolean;
  effective_source?: string;
  current?: string;
  sources?: AgentSource[];
  auth?: AgentAuth;
  changes: string;
  try_command: string;
  setup_command: string;
  undo_command: string;
  warnings?: string[];
  notes?: string[];
  error?: string;
};

export type AgentActionResult = { ok: boolean; message?: string; error?: string; agents: AgentStatus[] };

export const agentsApi = {
  list: (project: string) =>
    call<AgentStatus[]>(`/api/agents${project ? `?project=${encodeURIComponent(project)}` : ''}`),
  // The confirm body is required by the gateway: these edit the user's config files.
  run: async (name: string, action: 'setup' | 'undo', project: string): Promise<AgentActionResult> => {
    const res = await fetch(`/api/agents/${encodeURIComponent(name)}/${action}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ confirm: true, project }),
    });
    const data = await res.json();
    if (!data || !Array.isArray(data.agents)) throw new Error(data?.error ?? `${res.status}`);
    return data as AgentActionResult;
  },
};
