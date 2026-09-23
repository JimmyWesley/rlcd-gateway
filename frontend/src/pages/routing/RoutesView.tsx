import { useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import { PROVIDER_NAMES, routeProvider } from '../../lib/brands';
import { routerApi, type RouteInput, type RouterRoute } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, Confirm, Drawer, EmptyState, Field, Loading, ModelLabel, Toggle } from '../../ui';

type Draft = RouteInput & { name: string; isNew: boolean; has_key: boolean; original_base_url: string };

const PRESETS = {
  openrouter: { kind: 'openrouter', base_url: 'https://openrouter.ai/api', auth: 'key', api_key_env: 'OPENROUTER_API_KEY', model: '' },
  anthropic_key: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'key', api_key_env: 'ANTHROPIC_API_KEY', model: '' },
  anthropic_login: { kind: 'anthropic', base_url: 'https://api.anthropic.com', auth: 'passthrough', api_key_env: '', model: '' },
} as const;
type PresetId = keyof typeof PRESETS;

function fromRoute(r: RouterRoute): Draft {
  return {
    name: r.name, isNew: false, has_key: r.has_key, original_base_url: r.base_url,
    kind: r.kind, base_url: r.base_url, auth: r.auth, model: r.model ?? '',
    api_key_env: r.api_key_env ?? '', description: r.description ?? '', api_key: '',
  };
}

type Props = { routes: RouterRoute[] | null; activeRoute?: string; onSaved: (rs: RouterRoute[]) => void; onError: (e: string) => void };

export function RoutesView({ routes, activeRoute, onSaved, onError }: Props) {
  const { t } = useI18n();
  const { switchRoute } = useGateway();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [del, setDel] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  if (!routes) return <Card><Loading lines={4} /></Card>;

  const startNew = (preset: PresetId) => {
    const taken = new Set(routes.map((r) => r.name));
    let name = preset === 'openrouter' ? 'openrouter-2' : preset === 'anthropic_key' ? 'anthropic-key' : 'claude-login';
    for (let n = 2; taken.has(name); n++) name = `${name.replace(/-\d+$/, '')}-${n}`;
    setDraft({ name, isNew: true, has_key: false, original_base_url: '', description: '', api_key: '', ...PRESETS[preset] });
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const { name, kind, base_url, auth, model, api_key, api_key_env, clear_key, description } = draft;
      const body: RouteInput = { kind, base_url, auth, model, api_key_env, description, ...(api_key ? { api_key } : {}), ...(clear_key ? { clear_key } : {}) };
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

  const hostChanged = !!draft && !draft.isNew && draft.base_url.replace(/\/+$/, '') !== draft.original_base_url.replace(/\/+$/, '');

  return (
    <>
      <div className="section-bar">
        <p className="muted">{t('routes.intro')}</p>
        <div className="btn-row">
          {(Object.keys(PRESETS) as PresetId[]).map((k) => (
            <Button key={k} size="sm" icon="plus" onClick={() => startNew(k)}>{t(`routes.new.${k}`)}</Button>
          ))}
        </div>
      </div>
      {routes.length === 0 && <EmptyState icon="routing" title={t('routes.empty')} />}
      <div className="route-grid">
        {routes.map((r) => {
          const p = routeProvider(r);
          const active = r.name === activeRoute || r.active;
          return (
            <Card key={r.name} className={active ? 'route-card is-active' : 'route-card'}>
              <div className="route-card-head">
                <span className="route-logo"><BrandIcon id={p === 'custom' ? undefined : p} label={PROVIDER_NAMES[p]} size={22} /></span>
                <div className="route-card-titles">
                  <strong>{r.name}</strong>
                  <span className="muted small">{PROVIDER_NAMES[p]} · <span className="mono">{r.base_url.replace(/^https?:\/\//, '')}</span></span>
                </div>
                {active && <Badge tone="accent">{t('route.activeShort')}</Badge>}
              </div>
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
              </dl>
              {r.description ? <p className="route-desc">{r.description}</p> : <p className="route-desc muted">{t('routes.noDescription')}</p>}
              <div className="btn-row">
                {!active && <Button size="sm" variant="primary" onClick={() => switchRoute(r.name).catch((e) => onError(String(e)))}>{t('routes.makeActive')}</Button>}
                <Button size="sm" icon="edit" onClick={() => setDraft(fromRoute(r))}>{t('common.edit')}</Button>
                <Button
                  size="sm"
                  variant="ghost"
                  icon="trash"
                  onClick={() => setDel(r.name)}
                  disabled={active || r.used_by.length > 0}
                  title={active ? t('routes.cantDeleteActive') : r.used_by.length ? t('routes.cantDeleteUsed') : undefined}
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
                  <option value="anthropic">anthropic</option>
                  <option value="openrouter">openrouter</option>
                </select>
              </Field>
              <Field label={t('routes.form.credentials')}>
                <select value={draft.auth} onChange={(e) => setDraft({ ...draft, auth: e.target.value })}>
                  <option value="passthrough">{t('routes.form.passthrough')}</option>
                  <option value="key">{t('routes.form.key')}</option>
                </select>
              </Field>
            </div>
            <Field label={t('routes.form.baseUrl')}>
              <input value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} required className="mono" />
            </Field>
            <Field label={t('routes.form.model')} hint={t('routes.form.modelHint')}>
              <input className="mono" value={draft.model} placeholder={draft.kind === 'openrouter' ? 'anthropic/claude-sonnet-4.5' : t('routes.keepsModel')} onChange={(e) => setDraft({ ...draft, model: e.target.value })} />
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
