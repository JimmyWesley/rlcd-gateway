// What the Flow editor's drawer shows for each node: the same forms and APIs
// as the rest of the dashboard, one node at a time.
import { useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import { recallApi } from '../../lib/api';
import { keysApi } from '../../lib/keysApi';
import { routerApi, type Alias, type AliasView, type RulesDoc } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Button, Callout, Field, MenuItem, Toggle } from '../../ui';
import { DecisionSettings } from '../decisions/DecisionSettings';
import { KeyForm, type KeyDraft } from '../integrations/Keys';
import { RulesView } from '../routing/RulesView';
import { draftFromPreset, fromRoute, PRESETS, RouteForm, saveDraft, type Draft } from '../routing/RoutesView';
import { PruneSettings } from '../savings/PruneSettings';
import { OverrideEditor } from '../settings/Resilience';
import { EconomyModel } from '../settings/Settings';
import type { ConfigData } from './configGraph';

export type Undoable = { text: string; undo?: () => Promise<unknown> };
type P = { d: ConfigData; onSaved: (u?: Undoable) => void; onError: (e: string) => void };
const msg = (e: unknown) => String(e instanceof Error ? e.message : e);
const strip = (a: AliasView | Alias): Alias => ({ name: a.name, route: a.route, model: a.model || undefined, description: a.description || undefined });

export function RouterPanel({ d, onSaved, onError }: P) {
  const { t } = useI18n();
  const { switchRoute } = useGateway();
  const [doc, setDoc] = useState<RulesDoc>(d.rules);
  const [saving, setSaving] = useState(false);
  useEffect(() => setDoc(d.rules), [d.rules]);
  const dirty = JSON.stringify(doc) !== JSON.stringify(d.rules);
  const save = async (next = doc) => {
    setSaving(true);
    const prev = d.rules;
    try {
      await routerApi.saveRules(next);
      onSaved({ text: t('fedit.saved.rules'), undo: () => routerApi.saveRules(prev) });
    } catch (e) {
      onError(msg(e));
    } finally {
      setSaving(false);
    }
  };
  const openaiDefault = d.routes.find((r) => r.openai_default)?.name ?? '';
  return (
    <div className="stack-lg">
      <div className="form-grid">
        <Field label={t('routes.defaultAnthropic')} hint={t('routes.defaultAnthropicHint')}>
          <select value={d.config.active_route} onChange={(e) => {
            const prev = d.config.active_route;
            switchRoute(e.target.value).then(() => onSaved({ text: t('fedit.saved.default'), undo: () => switchRoute(prev) })).catch((x) => onError(msg(x)));
          }}>
            {d.routes.filter((r) => r.kind !== 'openai').map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
          </select>
        </Field>
        <Field label={t('routes.defaultOpenAI')} hint={t('routes.defaultOpenAIHint')}>
          <select value={openaiDefault} onChange={(e) => {
            const prev = openaiDefault;
            routerApi.setOpenAIDefault(e.target.value).then(() => onSaved({ text: t('fedit.saved.default'), undo: () => routerApi.setOpenAIDefault(prev) })).catch((x) => onError(msg(x)));
          }}>
            <option value="">{t('routes.defaultOpenAINone')}</option>
            {d.routes.filter((r) => r.kind === 'openai').map((r) => <option key={r.name} value={r.name}>{r.name}</option>)}
          </select>
        </Field>
      </div>
      <RulesView doc={doc} routes={d.routes} dirty={dirty} saving={saving} onChange={setDoc} onSave={() => save()} onRevert={() => setDoc(d.rules)}
        onSticky={(patch) => { const next = { ...d.rules, ...patch }; setDoc({ ...doc, ...patch }); void save(next); }} />
    </div>
  );
}

export function AliasPanel({ d, name, onSaved, onError }: P & { name: string | null }) {
  const { t } = useI18n();
  const cur = name ? d.aliases.find((a) => a.name === name) : undefined;
  const [form, setForm] = useState<Alias>(() => cur ? strip(cur) : { name: '', route: d.routes.find((r) => r.kind === 'openai')?.name ?? d.routes[0]?.name ?? '', model: '', description: '' });
  const [saving, setSaving] = useState(false);
  const prev = d.aliases.map(strip);
  const put = async (next: Alias[], text: string) => {
    setSaving(true);
    try {
      await routerApi.saveAliases(next);
      onSaved({ text, undo: () => routerApi.saveAliases(prev) });
    } catch (e) {
      onError(msg(e));
    } finally {
      setSaving(false);
    }
  };
  const clean = { ...form, name: form.name.trim(), model: form.model?.trim() || undefined, description: form.description?.trim() || undefined };
  const route = d.routes.find((r) => r.name === form.route);
  return (
    <form className="form-stack" onSubmit={(e) => { e.preventDefault(); void put(cur ? prev.map((a) => (a.name === cur.name ? clean : a)) : [...prev, clean], t('fedit.saved.alias', { name: clean.name })); }}>
      <Field label={t('aliases.col.name')} hint={t('fedit.alias.nameHint')}>
        <input className="mono" value={form.name} disabled={!!cur} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
      </Field>
      <Field label={t('aliases.col.route')}>
        <select value={form.route} onChange={(e) => setForm({ ...form, route: e.target.value })}>
          {d.routes.map((r) => <option key={r.name} value={r.name}>{r.name} ({r.protocols.map((p) => t(`protocol.short.${p}`)).join(', ')})</option>)}
        </select>
      </Field>
      <Field label={t('aliases.col.model')}>
        <input className="mono" value={form.model ?? ''} placeholder={route?.model || t('aliases.asIs')} onChange={(e) => setForm({ ...form, model: e.target.value })} />
      </Field>
      <Field label={t('aliases.col.description')}>
        <input value={form.description ?? ''} placeholder={t('aliases.optional')} onChange={(e) => setForm({ ...form, description: e.target.value })} />
      </Field>
      <div className="btn-row">
        <Button variant="primary" type="submit" loading={saving} disabled={!form.name.trim()}>{t('common.save')}</Button>
        {cur && <Button variant="danger" onClick={() => void put(prev.filter((a) => a.name !== cur.name), t('fedit.saved.aliasRemoved', { name: cur.name }))}>{t('common.delete')}</Button>}
      </div>
    </form>
  );
}

export function RoutePanel({ d, name, onSaved, onError }: P & { name: string | null }) {
  const { t } = useI18n();
  const cur = name ? d.routes.find((r) => r.name === name) : undefined;
  const [draft, setDraft] = useState<Draft | null>(() => (cur ? fromRoute(cur) : null));
  const [saving, setSaving] = useState(false);
  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      await saveDraft(draft);
      onSaved({ text: t('fedit.saved.route', { name: draft.name }) });
    } catch (e) {
      onError(msg(e));
    } finally {
      setSaving(false);
    }
  };
  if (!draft) {
    return (
      <div className="preset-menu">
        {(['anthropic', 'openai'] as const).map((g) => (
          <div key={g}>
            <div className="menu-head">{t(`routes.presets.${g}`)}</div>
            {PRESETS.filter((p) => p.group === g).map((p) => (
              <MenuItem key={p.id} onSelect={() => setDraft(draftFromPreset(p, d.routes))} hint={p.draft.auth === 'passthrough' ? t('route.yourLogin') : t('route.gatewayKey')}>
                <span className="brand-label"><BrandIcon id={p.icon} label={p.label} size={16} />{p.label}</span>
              </MenuItem>
            ))}
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="stack-lg">
      <RouteForm draft={draft} setDraft={setDraft} saving={saving} onSubmit={save} onCancel={() => (cur ? setDraft(fromRoute(cur)) : setDraft(null))} />
      {cur && (
        <section className="stack drawer-section">
          <h3>{t('fedit.route.resilience')}</h3>
          <OverrideEditor kind="route" name={cur.name} v={d.res} routes={d.config.routes} aliases={d.aliases}
            onSaved={() => onSaved({ text: t('fedit.saved.fallbacks', { name: cur.name }) })} />
        </section>
      )}
    </div>
  );
}

export function KeyPanel({ d, id, onSaved, onError }: P & { id: string }) {
  const { t } = useI18n();
  const k = d.keys.find((x) => x.id === id);
  const init = (): KeyDraft => ({ id: k?.id, name: k?.name ?? '', aliases: k?.aliases ?? [], routes: k?.routes ?? [], rpm: k?.rpm, tokens_per_day: k?.tokens_per_day });
  const [draft, setDraft] = useState<KeyDraft>(init);
  const [busy, setBusy] = useState(false);
  if (!k) return null;
  const save = async () => {
    setBusy(true);
    const prev = init();
    try {
      await keysApi.update(k.id, draft.name, { aliases: draft.aliases, routes: draft.routes, rpm: draft.rpm || undefined, tokens_per_day: draft.tokens_per_day || undefined });
      onSaved({ text: t('fedit.saved.key', { name: draft.name }), undo: () => keysApi.update(k.id, prev.name, { aliases: prev.aliases, routes: prev.routes, rpm: prev.rpm, tokens_per_day: prev.tokens_per_day }) });
    } catch (e) {
      onError(msg(e));
    } finally {
      setBusy(false);
    }
  };
  return <KeyForm draft={draft} setDraft={setDraft} aliases={d.aliases} routes={d.routes} busy={busy} onSubmit={save} onCancel={() => setDraft(init())} />;
}

export function NoKeyPanel({ d }: { d: ConfigData }) {
  const { t } = useI18n();
  return (
    <div className="stack">
      <p>{d.config.exposed ? t('apps.note.exposed') : t('fedit.nokey.body')}</p>
      <div className="btn-row">
        <a className="btn btn-secondary" href="#/settings/general">{t('fedit.nokey.require')}</a>
        <a className="btn btn-secondary" href="#/integrations/keys">{t('fedit.nokey.keys')}</a>
      </div>
    </div>
  );
}

export function RecallPanel({ d, onSaved, onError }: P) {
  const { t } = useI18n();
  const [kb, setKb] = useState(String(Math.round(d.recall.max_bytes / 1000)));
  const set = async (patch: { enabled?: boolean; max_bytes?: number }) => {
    const prev = { enabled: d.recall.enabled, max_bytes: d.recall.max_bytes };
    try {
      await recallApi.setSettings(patch);
      onSaved({ text: t('fedit.saved.recall'), undo: () => recallApi.setSettings(prev) });
    } catch (e) {
      onError(msg(e));
    }
  };
  return (
    <div className="stack">
      <Toggle checked={d.recall.enabled} onChange={(v) => void set({ enabled: v })} label={t('fedit.recall.enabled')} description={t('fedit.recall.hint')} />
      <Field label={t('fedit.recall.maxKB')}>
        <div className="input-suffix">
          <input type="number" min={1} value={kb} onChange={(e) => setKb(e.target.value)} onBlur={() => Number(kb) > 0 && Number(kb) * 1000 !== d.recall.max_bytes && void set({ max_bytes: Number(kb) * 1000 })} />
          <span className="muted small">KB</span>
        </div>
      </Field>
      {!d.config.log_bodies && <Callout tone="warn">{t('fedit.recall.bodies')}</Callout>}
      <a className="small" href="#/integrations/recall">{t('fedit.recall.more')}</a>
    </div>
  );
}

export { DecisionSettings, EconomyModel, PruneSettings };
