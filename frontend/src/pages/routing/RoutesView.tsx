import { useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import type { Protocol } from '../../lib/api';
import { PROVIDER_NAMES, routeProvider, type ProviderId } from '../../lib/brands';
import { routerApi, type RouteInput, type RouterRoute } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { useQueryParam } from '../../lib/router';
import { Badge, Button, Callout, Card, Confirm, Drawer, EmptyState, Field, IconButton, Loading, ModelLabel, Popover, MenuItem, Toggle } from '../../ui';

type Draft = RouteInput & { name: string; isNew: boolean; has_key: boolean; original_base_url: string; headerRows: [string, string][] };

type Preset = { id: string; label: string; icon: ProviderId; group: 'anthropic' | 'openai'; draft: Partial<Draft> };

// Anthropic-format routes serve /v1/messages; OpenAI-compatible ones serve
// /v1/chat/completions and /v1/responses. The gateway never translates.
const PRESETS: Preset[] = [
  { group: 'anthropic', id: 'claude-login', label: 'Claude login', icon: 'anthropic',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'passthrough', api_key_env: '' } },
  { group: 'anthropic', id: 'anthropic-key', label: 'Anthropic API key', icon: 'anthropic',
    draft: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'key', api_key_env: 'ANTHROPIC_API_KEY' } },
  { group: 'anthropic', id: 'openrouter-anthropic', label: 'OpenRouter', icon: 'openrouter',
    draft: { kind: 'openrouter', base_url: 'https://openrouter.ai/api', auth: 'key', api_key_env: 'OPENROUTER_API_KEY' } },
  { group: 'openai', id: 'openrouter-openai', label: 'OpenRouter', icon: 'openrouter',
    draft: { kind: 'openai', base_url: 'https://openrouter.ai/api/v1', auth: 'key', api_key_env: 'OPENROUTER_API_KEY',
      headerRows: [['HTTP-Referer', 'https://github.com/JimmyWesley/rlcd-gateway'], ['X-Title', 'RLCD Gateway']] } },
  { group: 'openai', id: 'openai-key', label: 'OpenAI', icon: 'openai',
    draft: { kind: 'openai', base_url: 'https://api.openai.com/v1', auth: 'key', api_key_env: 'OPENAI_API_KEY' } },
  { group: 'openai', id: 'groq', label: 'Groq', icon: 'groq',
    draft: { kind: 'openai', base_url: 'https://api.groq.com/openai/v1', auth: 'key', api_key_env: 'GROQ_API_KEY' } },
  { group: 'openai', id: 'together', label: 'Together AI', icon: 'together',
    draft: { kind: 'openai', base_url: 'https://api.together.xyz/v1', auth: 'key', api_key_env: 'TOGETHER_API_KEY' } },
  { group: 'openai', id: 'deepseek', label: 'DeepSeek', icon: 'deepseek',
    draft: { kind: 'openai', base_url: 'https://api.deepseek.com/v1', auth: 'key', api_key_env: 'DEEPSEEK_API_KEY' } },
  { group: 'openai', id: 'mistral', label: 'Mistral AI', icon: 'mistral',
    draft: { kind: 'openai', base_url: 'https://api.mistral.ai/v1', auth: 'key', api_key_env: 'MISTRAL_API_KEY' } },
  { group: 'openai', id: 'ollama', label: 'Ollama', icon: 'ollama',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:11434/v1', auth: 'passthrough', api_key_env: '' } },
  { group: 'openai', id: 'vllm', label: 'vLLM', icon: 'vllm',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:8000/v1', auth: 'passthrough', api_key_env: '' } },
  { group: 'openai', id: 'lmstudio', label: 'LM Studio', icon: 'lmstudio',
    draft: { kind: 'openai', base_url: 'http://127.0.0.1:1234/v1', auth: 'passthrough', api_key_env: '' } },
];

const KINDS = ['anthropic', 'openrouter', 'openai'] as const;
// Providers the gateway knows a logo for (config.Providers); "" derives it from the base URL.
const PROVIDERS = ['anthropic', 'openrouter', 'openai', 'groq', 'together', 'deepseek', 'mistral', 'google', 'ollama', 'vllm', 'lmstudio', 'custom'];

function fromRoute(r: RouterRoute): Draft {
  return {
    name: r.name, isNew: false, has_key: r.has_key, original_base_url: r.base_url,
    kind: r.kind, base_url: r.base_url, auth: r.auth, model: r.model ?? '',
    api_key_env: r.api_key_env ?? '', description: r.description ?? '', api_key: '',
    headers: r.headers ?? {}, headerRows: Object.entries(r.headers ?? {}), provider: '',
  };
}

export function ProtocolBadges({ protocols }: { protocols: Protocol[] }) {
  const { t } = useI18n();
  const openai = protocols.some((p) => p !== 'anthropic-messages');
  return <Badge tone={openai ? 'info' : 'accent'}>{openai ? t('protocol.family.openai') : t('protocol.family.anthropic')}</Badge>;
}

type Props = { routes: RouterRoute[] | null; activeRoute?: string; onSaved: (rs: RouterRoute[]) => void; onError: (e: string) => void };

export function RoutesView({ routes, activeRoute, onSaved, onError }: Props) {
  const { t } = useI18n();
  const { switchRoute } = useGateway();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [del, setDel] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  // #/routes?edit=<name> opens that route's editor (linked from the Overview and the top bar).
  const [editParam, setEditParam] = useQueryParam('edit');
  useEffect(() => {
    if (!editParam || !routes) return;
    const r = routes.find((x) => x.name === editParam);
    if (r) setDraft(fromRoute(r));
    setEditParam(null);
  }, [editParam, routes, setEditParam]);

  if (!routes) return <Card><Loading lines={4} /></Card>;

  const startNew = (p: Preset) => {
    const taken = new Set(routes.map((r) => r.name));
    let name = p.id;
    for (let n = 2; taken.has(name); n++) name = `${p.id}-${n}`;
    setDraft({
      name, isNew: true, has_key: false, original_base_url: '', description: '', api_key: '',
      kind: 'openai', base_url: '', auth: 'key', model: '', api_key_env: '', headers: {}, headerRows: [], provider: '',
      ...p.draft,
    });
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const { name, kind, base_url, auth, model, api_key, api_key_env, clear_key, description, headerRows, provider } = draft;
      const headers: Record<string, string> = {};
      for (const [k, v] of headerRows) if (k.trim()) headers[k.trim()] = v.trim();
      const body: RouteInput = {
        kind, base_url, auth, model, api_key_env, description, headers, provider,
        ...(api_key ? { api_key } : {}), ...(clear_key ? { clear_key } : {}),
      };
      onSaved(await routerApi.saveRoute(name.trim(), body));
      setDraft(null);
    } catch (e) {
      onError(String(e instanceof Error ? e.message : e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!del) return;
    setDeleting(true);
    try {
      onSaved(await routerApi.deleteRoute(del));
      if (draft?.name === del) setDraft(null);
    } catch (e) {
      onError(String(e instanceof Error ? e.message : e));
    } finally {
      setDeleting(false);
      setDel(null);
    }
  };

  const setDefault = async (name: string) => {
    try {
      onSaved(await routerApi.setOpenAIDefault(name));
    } catch (e) {
      onError(String(e instanceof Error ? e.message : e));
    }
  };

  const hostChanged = !!draft && !draft.isNew && draft.base_url.replace(/\/+$/, '') !== draft.original_base_url.replace(/\/+$/, '');
  const openaiRoutes = routes.filter((r) => r.kind === 'openai');
  const openaiDefault = routes.find((r) => r.openai_default)?.name ?? '';
  const setRow = (i: number, row: [string, string]) => draft && setDraft({ ...draft, headerRows: draft.headerRows.map((x, j) => (j === i ? row : x)) });

  return (
    <>
      <div className="section-bar">
        <div className="section-intro">
          <h2 className="section-title">{t('routes.heading')}</h2>
          <p className="muted">{t('routes.intro')}</p>
        </div>
        <Popover
          label={t('routes.add')}
          trigger={({ open, toggle, id }) => (
            <Button variant="primary" icon="plus" aria-haspopup="menu" aria-expanded={open} aria-controls={id} onClick={toggle}>{t('routes.add')}</Button>
          )}
        >
          {(close) => (
            <div className="preset-menu">
              {(['anthropic', 'openai'] as const).map((g) => (
                <div key={g}>
                  <div className="menu-head">{t(`routes.presets.${g}`)}</div>
                  {PRESETS.filter((p) => p.group === g).map((p) => (
                    <MenuItem key={p.id} onSelect={() => { close(); startNew(p); }} hint={p.draft.auth === 'passthrough' ? t('route.yourLogin') : t('route.gatewayKey')}>
                      <span className="brand-label"><BrandIcon id={p.icon} label={p.label} size={16} />{p.label}</span>
                    </MenuItem>
                  ))}
                </div>
              ))}
            </div>
          )}
        </Popover>
      </div>

      <div className="defaults-row">
        <Card className="default-card">
          <div className="default-card-text">
            <span className="fact-label">{t('routes.defaultAnthropic')}</span>
            <span className="muted small">{t('routes.defaultAnthropicHint')}</span>
          </div>
          <select value={activeRoute ?? ''} aria-label={t('routes.defaultAnthropic')} onChange={(e) => switchRoute(e.target.value).catch((x) => onError(String(x)))}>
            {routes.filter((r) => r.kind !== 'openai').map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
          </select>
        </Card>
        <Card className="default-card">
          <div className="default-card-text">
            <span className="fact-label">{t('routes.defaultOpenAI')}</span>
            <span className="muted small">{t('routes.defaultOpenAIHint')}</span>
          </div>
          <select value={openaiDefault} aria-label={t('routes.defaultOpenAI')} onChange={(e) => setDefault(e.target.value)}>
            <option value="">{t('routes.defaultOpenAINone')}</option>
            {openaiRoutes.map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
          </select>
        </Card>
      </div>

      {routes.length === 0 && <EmptyState icon="routing" title={t('routes.empty')} />}
      <div className="route-grid">
        {routes.map((r) => {
          const p = routeProvider(r);
          const active = r.name === activeRoute || r.active;
          const isDefault = active || r.openai_default;
          return (
            <Card key={r.name} className={isDefault ? 'route-card is-active' : 'route-card'}>
              <div className="route-card-head">
                <span className="route-logo"><BrandIcon id={p === 'custom' ? undefined : p} label={PROVIDER_NAMES[p]} size={22} /></span>
                <div className="route-card-titles">
                  <strong>{r.name}</strong>
                  <span className="muted small">{PROVIDER_NAMES[p]} · <span className="mono">{r.base_url.replace(/^https?:\/\//, '')}</span></span>
                </div>
                <ProtocolBadges protocols={r.protocols ?? []} />
              </div>
              {(active || r.openai_default) && (
                <div className="btn-row">
                  {active && <Badge tone="accent">{t('routes.badge.anthropicDefault')}</Badge>}
                  {r.openai_default && <Badge tone="info">{t('routes.badge.openaiDefault')}</Badge>}
                </div>
              )}
              <dl className="route-facts">
                <div>
                  <dt>{t('routes.model')}</dt>
                  <dd>{r.model ? <ModelLabel model={r.model} /> : <span className="muted">{t('routes.keepsModel')}</span>}</dd>
                </div>
                <div>
                  <dt>{t('routes.credentials')}</dt>
                  <dd>
                    {r.auth === 'passthrough' ? <Badge tone="info">{t('route.yourLogin')}</Badge>
                      : r.has_key ? <Badge tone="good">{t('route.gatewayKey')}{r.api_key_env ? ` · $${r.api_key_env}` : ''}</Badge>
                        : <Badge tone="warn">{t('route.noKey')}{r.api_key_env ? ` · $${r.api_key_env}` : ''}</Badge>}
                  </dd>
                </div>
                <div>
                  <dt>{t('routes.usedBy')}</dt>
                  <dd>{r.used_by.length ? r.used_by.map((u) => <Badge key={u}>{u}</Badge>) : <span className="muted">—</span>}</dd>
                </div>
                {Object.keys(r.headers ?? {}).length > 0 && (
                  <div>
                    <dt>{t('routes.headers')}</dt>
                    <dd className="mono small">{Object.keys(r.headers).join(', ')}</dd>
                  </div>
                )}
              </dl>
              {r.description ? <p className="route-desc">{r.description}</p> : <p className="route-desc muted">{t('routes.noDescription')}</p>}
              <div className="btn-row">
                <Button size="sm" variant="primary" icon="edit" onClick={() => setDraft(fromRoute(r))} aria-label={t('routes.editNamed', { name: r.name })}>{t('routes.edit')}</Button>
                {!active && r.kind !== 'openai' && <Button size="sm" onClick={() => switchRoute(r.name).catch((e) => onError(String(e)))}>{t('routes.makeActive')}</Button>}
                {!r.openai_default && r.kind === 'openai' && <Button size="sm" onClick={() => setDefault(r.name)}>{t('routes.makeOpenAIDefault')}</Button>}
                <Button
                  size="sm"
                  variant="ghost"
                  icon="trash"
                  onClick={() => setDel(r.name)}
                  disabled={isDefault || r.used_by.length > 0}
                  title={isDefault ? t('routes.cantDeleteActive') : r.used_by.length ? t('routes.cantDeleteUsed') : undefined}
                >
                  {t('common.delete')}
                </Button>
              </div>
            </Card>
          );
        })}
      </div>

      <Confirm open={!!del} title={t('routes.deleteTitle', { name: del ?? '' })} confirmLabel={t('common.delete')} tone="danger" busy={deleting} onConfirm={remove} onCancel={() => setDel(null)}>
        {t('routes.deleteBody')}
      </Confirm>

      <Drawer open={!!draft} onClose={() => setDraft(null)} title={draft?.isNew ? t('routes.form.new') : t('routes.form.edit', { name: draft?.name ?? '' })}>
        {draft && (
          <form className="form-stack" onSubmit={(e) => { e.preventDefault(); save(); }}>
            <Field label={t('routes.form.name')}>
              <input value={draft.name} disabled={!draft.isNew} onChange={(e) => setDraft({ ...draft, name: e.target.value })} required />
            </Field>
            <div className="form-grid">
              <Field label={t('routes.form.kind')}>
                <select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value })}>
                  {KINDS.map((k) => <option key={k} value={k}>{t(`routes.kind.${k}`)}</option>)}
                </select>
              </Field>
              <Field label={t('routes.form.credentials')}>
                <select value={draft.auth} onChange={(e) => setDraft({ ...draft, auth: e.target.value })}>
                  <option value="passthrough">{t('routes.form.passthrough')}</option>
                  <option value="key">{t('routes.form.key')}</option>
                </select>
              </Field>
            </div>
            <Field label={t('routes.form.baseUrl')} hint={draft.kind === 'openai' ? t('routes.form.baseUrlOpenAI') : undefined}>
              <input value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} required className="mono" />
            </Field>
            <Field label={t('routes.form.model')} hint={t('routes.form.modelHint')}>
              <input className="mono" value={draft.model} placeholder={draft.kind === 'anthropic' ? t('routes.keepsModel') : 'anthropic/claude-sonnet-4.5'} onChange={(e) => setDraft({ ...draft, model: e.target.value })} />
            </Field>
            <Field label={t('routes.form.description')} hint={t('routes.form.descriptionHint')}>
              <input value={draft.description} placeholder={t('routes.form.descriptionPh')} onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
            </Field>
            {draft.auth === 'key' && (
              <div className="form-grid">
                <Field label={t('routes.form.apiKey')}>
                  <input type="password" autoComplete="off" value={draft.api_key ?? ''} placeholder={draft.has_key && !hostChanged ? t('common.secretKept') : t('common.secretPaste')} onChange={(e) => setDraft({ ...draft, api_key: e.target.value })} />
                </Field>
                <Field label={t('routes.form.env')}>
                  <input className="mono" value={draft.api_key_env} placeholder="OPENROUTER_API_KEY" onChange={(e) => setDraft({ ...draft, api_key_env: e.target.value })} />
                </Field>
              </div>
            )}
            {hostChanged && draft.auth === 'key' && <Callout tone="warn">{t('routes.form.hostChanged')}</Callout>}
            {!draft.isNew && draft.auth === 'key' && draft.has_key && !hostChanged && (
              <Toggle checked={!!draft.clear_key} onChange={(v) => setDraft({ ...draft, clear_key: v })} label={t('routes.form.clearKey')} />
            )}
            <Field label={t('routes.form.provider')} hint={t('routes.form.providerHint')}>
              <select value={draft.provider} onChange={(e) => setDraft({ ...draft, provider: e.target.value })}>
                <option value="">{t('routes.form.providerAuto')}</option>
                {PROVIDERS.map((p) => <option key={p} value={p}>{PROVIDER_NAMES[p as ProviderId] ?? p}</option>)}
              </select>
            </Field>
            <div className="headers">
              <span className="field-label">{t('routes.form.headers')}</span>
              <span className="field-hint">{t('routes.form.headersHint')}</span>
              {draft.headerRows.map(([k, v], i) => (
                <div key={i} className="header-row header-row-2">
                  <input className="mono" value={k} placeholder="Header-Name" aria-label={t('rules.form.headerName')} onChange={(e) => setRow(i, [e.target.value, v])} />
                  <input className="mono" value={v} placeholder="value" aria-label={t('rules.form.headerValue')} onChange={(e) => setRow(i, [k, e.target.value])} />
                  <IconButton icon="x" label={t('rules.form.removeHeader')} onClick={() => setDraft({ ...draft, headerRows: draft.headerRows.filter((_, j) => j !== i) })} />
                </div>
              ))}
              <Button size="sm" variant="ghost" icon="plus" onClick={() => setDraft({ ...draft, headerRows: [...draft.headerRows, ['', '']] })}>{t('routes.form.addHeader')}</Button>
            </div>
            <p className="fine">{t('routes.form.writeOnly')}</p>
            <div className="btn-row">
              <Button variant="primary" type="submit" loading={saving} disabled={!draft.name.trim() || !draft.base_url.trim()}>{t('routes.form.save')}</Button>
              <Button onClick={() => setDraft(null)}>{t('common.cancel')}</Button>
            </div>
          </form>
        )}
      </Drawer>
    </>
  );
}
