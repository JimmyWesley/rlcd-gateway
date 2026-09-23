// Decision backends: which System One servers exist, which models go where,
// the mirror for parity audits, and whether the gateway's own decisions are
// logged. Tokens are write-only: the API only says whether one is stored.
import { useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { decisionsApi, type DecisionSettingsInput, type DecisionSettingsView } from '../../lib/decisionsApi';
import { useFetch } from '../../state/gateway';
import { Badge, Button, Callout, Card, ErrorState, Field, IconButton, Loading, Toggle, cx } from '../../ui';

type BackendRow = { name: string; base_url: string; auth: string; token: string; token_env: string; provider: string; has_token: boolean; clear_token: boolean; implicit?: boolean };
type Form = {
  backends: BackendRow[];
  default_backend: string;
  models: [string, string][];
  mirror: { on: boolean; backend: string; rate: string; model: string };
  log_internal: boolean;
};

function toForm(v: DecisionSettingsView): Form {
  return {
    backends: v.backends.map((b) => ({ name: b.name, base_url: b.base_url, auth: b.auth, token: '', token_env: b.token_env ?? '', provider: b.provider, has_token: b.has_token, clear_token: false, implicit: b.implicit })),
    default_backend: v.default_backend,
    models: Object.entries(v.models),
    mirror: { on: !!v.mirror, backend: v.mirror?.backend ?? '', rate: v.mirror ? String(Math.round(v.mirror.sample_rate * 1000) / 10) : '10', model: v.mirror?.model ?? '' },
    log_internal: v.log_internal,
  };
}

function toInput(fm: Form): DecisionSettingsInput {
  const backends: DecisionSettingsInput['backends'] = {};
  for (const b of fm.backends) {
    if (b.implicit) continue;
    backends[b.name.trim()] = {
      base_url: b.base_url.trim(), auth: b.auth,
      ...(b.token ? { token: b.token } : {}),
      ...(b.token_env.trim() ? { token_env: b.token_env.trim() } : {}),
      ...(b.provider && b.provider !== 'auto' ? { provider: b.provider } : {}),
      ...(b.clear_token ? { clear_token: true } : {}),
    };
  }
  return {
    backends,
    default_backend: fm.default_backend,
    models: Object.fromEntries(fm.models.filter(([m, b]) => m.trim() && b).map(([m, b]) => [m.trim(), b])),
    mirror: fm.mirror.on ? { backend: fm.mirror.backend, sample_rate: Number(fm.mirror.rate) / 100, ...(fm.mirror.model.trim() ? { model: fm.mirror.model.trim() } : {}) } : null,
    log_internal: fm.log_internal,
  };
}

export function DecisionSettings() {
  const { t } = useI18n();
  const cur = useFetch(() => decisionsApi.settings(), []);
  const [form, setForm] = useState<Form | null>(null);
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => { if (cur.data) setForm(toForm(cur.data)); }, [cur.data]);

  const base = useMemo(() => (cur.data ? JSON.stringify(toInput(toForm(cur.data))) : ''), [cur.data]);
  if (cur.error && !cur.data) return <ErrorState error={cur.error} onRetry={cur.reload} />;
  if (!cur.data || !form) return <Card><Loading lines={6} /></Card>;

  const input = toInput(form);
  const dirty = JSON.stringify(input) !== base;
  const names = form.backends.map((b) => b.name.trim()).filter(Boolean);
  const problems: string[] = [];
  const seen = new Set<string>();
  for (const b of form.backends) {
    const n = b.name.trim();
    if (!n) problems.push(t('dset.p.name'));
    else if (seen.has(n)) problems.push(t('dset.p.dup', { name: n }));
    seen.add(n);
    if (!b.implicit && !/^https?:\/\/\S+$/.test(b.base_url.trim())) problems.push(t('dset.p.url', { name: n || '?' }));
  }
  const rate = Number(form.mirror.rate);
  if (form.mirror.on && !form.mirror.backend) problems.push(t('dset.p.mirrorBackend'));
  if (form.mirror.on && !(rate > 0 && rate <= 100)) problems.push(t('dset.p.rate'));

  const set = (patch: Partial<Form>) => { setForm({ ...form, ...patch }); setMsg(null); };
  const setB = (i: number, patch: Partial<BackendRow>) => set({ backends: form.backends.map((b, j) => (j === i ? { ...b, ...patch } : b)) });
  const setM = (i: number, patch: [string, string]) => set({ models: form.models.map((m, j) => (j === i ? patch : m)) });
  const save = async () => {
    setSaving(true);
    try {
      const v = await decisionsApi.saveSettings(input);
      cur.setData(v);
      setMsg({ ok: true, text: t('dset.saved') });
    } catch (e) {
      setMsg({ ok: false, text: String(e instanceof Error ? e.message : e) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      {cur.data.error && <Callout tone="bad" title={t('dset.invalid')}>{cur.data.error}</Callout>}
      <Card title={t('dset.backends')} subtitle={t('dset.backendsSub')}
        actions={<Button size="sm" icon="plus" onClick={() => set({ backends: [...form.backends, { name: '', base_url: 'http://', auth: 'key', token: '', token_env: '', provider: 'auto', has_token: false, clear_token: false }] })}>{t('dset.addBackend')}</Button>}>
        <div className="backend-list">
          {form.backends.map((b, i) => (
            <div key={i} className={cx('backend-row', b.implicit && 'is-implicit')}>
              <div className="backend-head">
                <input className="mono" aria-label={t('dset.name')} value={b.name} disabled={b.implicit} placeholder="open-rlcd" onChange={(e) => setB(i, { name: e.target.value })} />
                {b.implicit && <Badge tone="info">{t('dset.implicit')}</Badge>}
                {b.name && b.name === form.default_backend && <Badge tone="accent">{t('dset.default')}</Badge>}
                {form.mirror.on && b.name === form.mirror.backend && <Badge tone="shadow">{t('dset.mirror')}</Badge>}
                <span className="toolbar-spacer" />
                {!b.implicit && <IconButton icon="trash" label={t('dset.remove', { name: b.name })} onClick={() => set({ backends: form.backends.filter((_, j) => j !== i) })} />}
              </div>
              {b.implicit ? (
                <p className="fine">{t('dset.implicitBody', { url: b.base_url })}</p>
              ) : (
                <div className="form-grid form-grid-4">
                  <Field label={t('dset.url')}><input className="mono" value={b.base_url} onChange={(e) => setB(i, { base_url: e.target.value })} /></Field>
                  <Field label={t('dset.auth')} hint={b.auth === 'key' ? t('dset.auth.keyHint') : t('dset.auth.passHint')}>
                    <select value={b.auth} onChange={(e) => setB(i, { auth: e.target.value })}>
                      <option value="key">{t('dset.auth.key')}</option>
                      <option value="passthrough">{t('dset.auth.pass')}</option>
                    </select>
                  </Field>
                  {b.auth === 'key' && (
                    <Field label={t('dset.token')} hint={b.clear_token ? t('dset.token.clearing') : b.has_token ? t('dset.token.stored') : t('dset.token.none')}>
                      <div className="input-row">
                        <input type="password" autoComplete="off" value={b.token} placeholder={b.has_token && !b.clear_token ? '••••••••' : t('dset.token.ph')} onChange={(e) => setB(i, { token: e.target.value, clear_token: false })} />
                        {b.has_token && <Button size="sm" variant="ghost" aria-pressed={b.clear_token} onClick={() => setB(i, { clear_token: !b.clear_token, token: '' })}>{b.clear_token ? t('common.revert') : t('dset.token.clear')}</Button>}
                      </div>
                    </Field>
                  )}
                  {b.auth === 'key' && (
                    <Field label={t('dset.tokenEnv')} hint={t('dset.tokenEnvHint')}><input className="mono" value={b.token_env} placeholder="OPEN_RLCD_API_KEY" onChange={(e) => setB(i, { token_env: e.target.value })} /></Field>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </Card>

      <div className="grid grid-2">
        <Card title={t('dset.routing')} subtitle={t('dset.routingSub')}>
          <Field label={t('dset.defaultBackend')} hint={t('dset.defaultHint')}>
            <select value={form.default_backend} onChange={(e) => set({ default_backend: e.target.value })}>
              <option value="">{t('dset.economy')}</option>
              {names.filter((n) => n !== 'economy').map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </Field>
          <table className="table table-form">
            <thead><tr><th>{t('dset.model')}</th><th /><th>{t('dset.backend')}</th><th /></tr></thead>
            <tbody>
              {form.models.map(([m, b], i) => (
                <tr key={i}>
                  <td><input className="mono" value={m} placeholder="Open-RLCD-*" aria-label={t('dset.model')} onChange={(e) => setM(i, [e.target.value, b])} /></td>
                  <td className="muted" aria-hidden>→</td>
                  <td>
                    <select value={b} aria-label={t('dset.backend')} onChange={(e) => setM(i, [m, e.target.value])}>
                      <option value="" disabled>—</option>
                      {names.map((n) => <option key={n} value={n}>{n}</option>)}
                    </select>
                  </td>
                  <td className="num"><IconButton icon="trash" label={t('dset.removeModel', { model: m })} onClick={() => set({ models: form.models.filter((_, j) => j !== i) })} /></td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="btn-row">
            <Button size="sm" icon="plus" onClick={() => set({ models: [...form.models, ['', form.default_backend || names[0] || '']] })}>{t('dset.addModel')}</Button>
          </div>
          <p className="fine">{t('dset.matchHint')}</p>
        </Card>

        <Card title={t('dset.mirrorTitle')} subtitle={t('dset.mirrorSub')}>
          <Toggle checked={form.mirror.on} onChange={(on) => set({ mirror: { ...form.mirror, on } })} label={t('dset.mirrorOn')} description={t('dset.mirrorOnHint')} />
          {form.mirror.on && (
            <div className="form-grid">
              <Field label={t('dset.backend')}>
                <select value={form.mirror.backend} onChange={(e) => set({ mirror: { ...form.mirror, backend: e.target.value } })}>
                  <option value="" disabled>—</option>
                  {names.map((n) => <option key={n} value={n}>{n}</option>)}
                </select>
              </Field>
              <Field label={t('dset.rate')} hint={t('dset.rateHint')}>
                <div className="input-suffix"><input type="number" min={0.1} max={100} step={0.1} value={form.mirror.rate} onChange={(e) => set({ mirror: { ...form.mirror, rate: e.target.value } })} /><span>%</span></div>
              </Field>
              <Field label={t('dset.mirrorModel')} hint={t('dset.mirrorModelHint')}>
                <input className="mono" value={form.mirror.model} placeholder={t('dset.sameModel')} onChange={(e) => set({ mirror: { ...form.mirror, model: e.target.value } })} />
              </Field>
            </div>
          )}
          <hr className="sep" />
          <Toggle checked={form.log_internal} onChange={(v) => set({ log_internal: v })} label={t('dset.logInternal')} description={t('dset.logInternalHint')} />
        </Card>
      </div>

      {problems.map((p) => <Callout key={p} tone="warn">{p}</Callout>)}
      <div className={cx('savebar', 'savebar-inline', (dirty || msg) && 'show')}>
        <span className={cx('small', msg ? (msg.ok ? 'tone-good' : 'tone-bad') : dirty ? 'tone-warn' : 'muted')} role="status">
          {msg ? msg.text : dirty ? t('common.unsaved') : t('dset.allSaved')}
        </span>
        <span className="toolbar-spacer" />
        <Button onClick={() => { setForm(toForm(cur.data!)); setMsg(null); }} disabled={!dirty || saving}>{t('common.discard')}</Button>
        <Button variant="primary" onClick={save} loading={saving} disabled={!dirty || problems.length > 0}>{t('common.save')}</Button>
      </div>
    </>
  );
}
