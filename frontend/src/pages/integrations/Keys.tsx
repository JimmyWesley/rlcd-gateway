// Gateway keys ("virtual keys"): an app holds an rlcd-… key instead of a
// provider key; the gateway checks it, strips it, and the route injects its own.
import { useCallback, useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { api, type Stats } from '../../lib/api';
import { keysApi, type KeyCreated, type KeyLimits, type KeyView } from '../../lib/keysApi';
import { href } from '../../lib/router';
import { routerApi, type AliasView, type RouterRoute } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, Confirm, CopyField, Drawer, EmptyState, ErrorState, Field, Loading, Stat, Toggle, cx } from '../../ui';

export type KeyDraft = KeyLimits & { id?: string; name: string };
type Draft = KeyDraft;
const emptyDraft = (): Draft => ({ name: '', aliases: [], routes: [] });

export function Keys() {
  const { t, f, tn } = useI18n();
  const { config, reloadConfig } = useGateway();
  const [keys, setKeys] = useState<KeyView[] | null>(null);
  const [aliases, setAliases] = useState<AliasView[]>([]);
  const [routes, setRoutes] = useState<RouterRoute[]>([]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [created, setCreated] = useState<KeyCreated | null>(null);
  const [revoking, setRevoking] = useState<KeyView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    keysApi.list().then((k) => { setKeys(k); setLoadErr(null); }).catch((e) => setLoadErr(String(e instanceof Error ? e.message : e)));
    api.stats().then(setStats).catch(() => {});
  }, []);
  useEffect(() => {
    load();
    routerApi.aliases().then(setAliases).catch(() => {});
    routerApi.routes().then(setRoutes).catch(() => {});
  }, [load]);

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    setError(null);
    const limits: KeyLimits = { aliases: draft.aliases, routes: draft.routes, rpm: draft.rpm || undefined, tokens_per_day: draft.tokens_per_day || undefined };
    try {
      if (draft.id) await keysApi.update(draft.id, draft.name, limits);
      else {
        setCreated(await keysApi.create(draft.name, limits));
        reloadConfig();
      }
      setDraft(null);
      load();
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    if (!revoking) return;
    setBusy(true);
    try {
      await keysApi.revoke(revoking.id);
      load();
      reloadConfig();
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
      setRevoking(null);
    }
  };

  const toggleRequire = async (on: boolean) => {
    try {
      await api.setRequireKeys(on);
      reloadConfig();
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    }
  };

  const active = keys?.filter((k) => !k.revoked) ?? [];

  return (
    <>
      <Card
        title={t('keys.title')}
        subtitle={tn('keys.sub', { prefix: <code>rlcd-…</code> })}
        actions={<Button variant="primary" icon="plus" onClick={() => { setDraft(emptyDraft()); setCreated(null); }}>{t('keys.new')}</Button>}
      >
        {config?.exposed ? (
          <Callout tone={active.length ? 'info' : 'warn'} icon="shield" title={t('keys.exposed.title', { listen: config.listen })}>
            {tn('keys.exposed.body', { v1: <code>/v1</code>, openai: <code>/openai</code>, mcp: <code>/mcp</code> })}
            {active.length === 0 && <> {t('keys.exposed.none')}</>}
          </Callout>
        ) : config ? (
          <Toggle
            checked={config.require_keys}
            disabled={!config.require_keys && active.length === 0}
            onChange={toggleRequire}
            label={t('keys.require')}
            description={<>{t('keys.requireHelp')}{!config.require_keys && active.length === 0 && ` ${t('keys.createFirst')}`}</>}
          />
        ) : null}
      </Card>

      {error && <Callout tone="bad">{error}</Callout>}

      {created && (
        <Card className="key-created">
          <Callout tone="good" icon="key" title={t('keys.created', { name: created.view.name })}>{created.note}</Callout>
          <CopyField text={created.key} label={t('keys.theKey')} />
          <p className="fine">{t('keys.createdHint')} <a href="#/integrations/apps">{t('integrations.tab.apps')}</a></p>
          <div className="btn-row"><Button onClick={() => setCreated(null)}>{t('keys.copiedIt')}</Button></div>
        </Card>
      )}

      {loadErr && <ErrorState error={loadErr} onRetry={load} />}
      {!keys && !loadErr && <Card><Loading /></Card>}
      {keys && keys.length === 0 && (
        <Card><EmptyState icon="key" title={t('keys.empty')} actions={<Button variant="primary" icon="plus" onClick={() => setDraft(emptyDraft())}>{t('keys.new')}</Button>}>{t('keys.emptyBody')}</EmptyState></Card>
      )}
      {keys && keys.length > 0 && (
        <div className="key-grid">
          {keys.map((k) => {
            const u = k.usage;
            const tokens = u.input_tokens + u.output_tokens + u.cache_read_input_tokens + u.cache_creation_input_tokens;
            const recent = stats?.by_key?.[k.name];
            const dayShare = k.tokens_per_day ? Math.min(1, k.today_tokens / k.tokens_per_day) : null;
            return (
              <Card key={k.id} className={cx('key-card', k.revoked && 'is-revoked')}>
                <div className="key-head">
                  <span className="route-logo"><Icon name="key" size={18} /></span>
                  <div className="route-card-titles">
                    <strong>{k.name}</strong>
                    <span className="muted small mono">{k.hint} · {k.id}</span>
                  </div>
                  {k.revoked ? <Badge tone="bad">{t('keys.revoked')}</Badge> : <Badge tone="good">{t('keys.active')}</Badge>}
                </div>
                <div className="mini-stats">
                  <Stat label={t('keys.requests')} value={f.compact(u.requests)} sub={u.errors ? <span className="tone-bad">{t('overview.kpi.errors', { count: u.errors })}</span> : recent ? t('keys.recent', { n: recent.requests }) : undefined} />
                  <Stat label={t('keys.tokens')} value={f.compact(tokens)} />
                  <Stat label={t('keys.cost')} value={f.usd(u.est_cost_usd)} sub={t('common.estimated')} />
                </div>
                {dayShare != null && (
                  <div className="meter" title={t('keys.todayOf', { used: f.compact(k.today_tokens), limit: f.compact(k.tokens_per_day) })}>
                    <div className="meter-head small"><span className="muted">{t('keys.today')}</span><span className="mono">{f.compact(k.today_tokens)} / {f.compact(k.tokens_per_day)}</span></div>
                    <div className="meter-track"><span className={cx('meter-fill', dayShare > 0.9 && 'is-bad', dayShare > 0.7 && dayShare <= 0.9 && 'is-warn')} style={{ width: `${dayShare * 100}%` }} /></div>
                  </div>
                )}
                <dl className="route-facts">
                  <div><dt>{t('keys.limits')}</dt><dd>
                    {k.rpm ? <Badge>{t('keys.rpm', { n: k.rpm })}</Badge> : null}
                    {k.tokens_per_day ? <Badge>{t('keys.tpd', { n: f.compact(k.tokens_per_day) })}</Badge> : null}
                    {!k.rpm && !k.tokens_per_day && <span className="muted">{t('keys.noLimits')}</span>}
                  </dd></div>
                  <div><dt>{t('keys.models')}</dt><dd>{k.aliases.length ? k.aliases.map((a) => <Badge key={a} tone="accent">{a}</Badge>) : <span className="muted">{t('keys.anyModel')}</span>}</dd></div>
                  <div><dt>{t('keys.routes')}</dt><dd>{k.routes.length ? k.routes.map((r) => <Badge key={r}>{r}</Badge>) : <span className="muted">{t('keys.anyRoute')}</span>}</dd></div>
                  <div><dt>{t('keys.lastUsed')}</dt><dd>{k.last_used ? <span title={f.dayTime(k.last_used)}>{f.ago(k.last_used)}</span> : <span className="muted">{t('keys.never')}</span>}</dd></div>
                </dl>
                <div className="btn-row">
                  <a className="btn btn-sm btn-ghost" href={href('traffic', { key: k.name })}>{t('keys.traffic')}</a>
                  {!k.revoked && (
                    <>
                      <Button size="sm" icon="edit" onClick={() => setDraft({ id: k.id, name: k.name, aliases: k.aliases, routes: k.routes, rpm: k.rpm, tokens_per_day: k.tokens_per_day })}>{t('keys.editLimits')}</Button>
                      <Button size="sm" variant="danger" onClick={() => setRevoking(k)}>{t('keys.revoke')}</Button>
                    </>
                  )}
                </div>
              </Card>
            );
          })}
        </div>
      )}
      <p className="fine">{t('keys.costNote')}</p>

      <Confirm open={!!revoking} tone="danger" busy={busy} title={t('keys.revokeTitle', { name: revoking?.name ?? '' })} confirmLabel={t('keys.revoke')} onConfirm={revoke} onCancel={() => setRevoking(null)}>
        {t('keys.revokeBody')}
      </Confirm>

      <Drawer open={!!draft} onClose={() => setDraft(null)} title={draft?.id ? t('keys.editTitle', { name: draft.name }) : t('keys.newTitle')}>
        {draft && <KeyForm draft={draft} setDraft={setDraft} aliases={aliases} routes={routes} busy={busy} onSubmit={save} onCancel={() => setDraft(null)} />}
      </Drawer>
    </>
  );
}

const toggleIn = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

/** A key's name, limits and scope; shared with the Flow editor. */
export function KeyForm({ draft, setDraft, aliases, routes, busy, onSubmit, onCancel }: {
  draft: KeyDraft; setDraft: (d: KeyDraft) => void; aliases: AliasView[]; routes: RouterRoute[]; busy: boolean; onSubmit: () => void; onCancel: () => void;
}) {
  const { t } = useI18n();
  const toggle = toggleIn;
  return (
    <form className="form-stack" onSubmit={(e) => { e.preventDefault(); onSubmit(); }}>
      <Field label={t('keys.form.name')}>
        <input value={draft.name} placeholder="support-chatbot" required onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
      </Field>
      <div className="form-grid">
        <Field label={t('keys.form.rpm')} hint={t('keys.form.noLimit')}>
          <input type="number" min={0} value={draft.rpm ?? ''} onChange={(e) => setDraft({ ...draft, rpm: Number(e.target.value) || undefined })} />
        </Field>
        <Field label={t('keys.form.tpd')} hint={t('keys.form.tpdHint')}>
          <input type="number" min={0} value={draft.tokens_per_day ?? ''} onChange={(e) => setDraft({ ...draft, tokens_per_day: Number(e.target.value) || undefined })} />
        </Field>
      </div>
      <div className="field">
        <span className="field-label">{t('keys.form.models')}</span>
        <span className="field-hint">{t('keys.form.modelsHint')}</span>
        <div className="chips">
          {aliases.length === 0 && <span className="muted small">{t('keys.form.noAliases')} <a href="#/routes/aliases">{t('routing.tab.aliases')}</a></span>}
          {aliases.map((a) => (
            <label key={a.name} className={cx('chip', 'chip-check', draft.aliases.includes(a.name) && 'on')}>
              <input type="checkbox" checked={draft.aliases.includes(a.name)} onChange={() => setDraft({ ...draft, aliases: toggle(draft.aliases, a.name) })} />
              <span className="mono">{a.name}</span>
            </label>
          ))}
        </div>
      </div>
      <div className="field">
        <span className="field-label">{t('keys.form.routes')}</span>
        <span className="field-hint">{t('keys.form.routesHint')}</span>
        <div className="chips">
          {routes.map((r) => (
            <label key={r.name} className={cx('chip', 'chip-check', draft.routes.includes(r.name) && 'on')}>
              <input type="checkbox" checked={draft.routes.includes(r.name)} onChange={() => setDraft({ ...draft, routes: toggle(draft.routes, r.name) })} />
              {r.name}
            </label>
          ))}
        </div>
      </div>
      <p className="fine">{t('keys.form.dailyNote')}</p>
      <div className="btn-row">
        <Button variant="primary" type="submit" loading={busy} disabled={!draft.name.trim()}>{draft.id ? t('common.save') : t('keys.form.create')}</Button>
        <Button onClick={onCancel}>{t('common.cancel')}</Button>
      </div>
    </form>
  );
}
