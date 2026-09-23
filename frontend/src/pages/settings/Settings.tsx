import { useEffect, useState } from 'react';
import { LOCALES, useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { api, type AdapterSettings, type ProbeResult, type SelectorBackend, type SelectorView } from '../../lib/api';
import { useTheme } from '../../lib/theme';
import { useFetch, useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, ErrorState, Field, Loading, PageHeader, Segmented, SubNav, cx } from '../../ui';

type Tab = 'economy' | 'upstreams' | 'general';
const BACKENDS: SelectorBackend[] = ['open-rlcd-local', 'open-rlcd-cloud', 'jev'];

export function Settings({ sub }: { sub: string; param?: string }) {
  const { t } = useI18n();
  const tab: Tab = sub === 'upstreams' || sub === 'general' ? sub : 'economy';
  return (
    <div className="page">
      <PageHeader
        title={t('nav.settings')}
        description={t('settings.desc')}
        tabs={
          <SubNav
            label={t('nav.settings')}
            active={tab}
            items={[
              { id: 'economy', label: t('settings.tab.economy'), href: '#/settings' },
              { id: 'upstreams', label: t('settings.tab.upstreams'), href: '#/settings/upstreams' },
              { id: 'general', label: t('settings.tab.general'), href: '#/settings/general' },
            ]}
          />
        }
      />
      {tab === 'economy' && <EconomyModel />}
      {tab === 'upstreams' && <Upstreams />}
      {tab === 'general' && <General />}
    </div>
  );
}

function EconomyModel() {
  const { t, f } = useI18n();
  const { config, reloadConfig } = useGateway();
  const presets = useFetch(() => api.presets(), []);
  const [form, setForm] = useState<SelectorView | null>(null);
  const [token, setToken] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [probe, setProbe] = useState<ProbeResult | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (config && !form) setForm(config.selector);
  }, [config, form]);

  if (!config || !form) return <Card><Loading lines={5} /></Card>;

  const pick = (backend: SelectorBackend) => {
    // Switching backend loads its defaults, except what is saved for the current one.
    const p = presets.data?.find((x) => x.backend === backend);
    setForm(backend === config.selector.backend ? config.selector : { ...(p ?? form), backend, has_token: false });
    setProbe(null);
    setSaved(false);
  };

  const save = async () => {
    setSaving(true);
    setSaved(false);
    try {
      const s = await api.setSelector({ ...form, ...(token ? { token } : {}) });
      setToken('');
      setForm(s);
      await reloadConfig();
      setSaved(true);
      return true;
    } catch (e) {
      setProbe({ ok: false, error: String(e instanceof Error ? e.message : e) });
      return false;
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setProbe(null);
    if (await save()) setProbe(await api.testSelector().catch((e) => ({ ok: false as const, error: String(e instanceof Error ? e.message : e) })));
    setTesting(false);
  };

  const needsToken = form.backend !== 'open-rlcd-local';
  const keep = probe?.ok ? probe.result.answers.keep?.noul : undefined;

  return (
    <div className="settings-stack">
      <Card title={t('selector.title')} subtitle={t('selector.intro')}>
        <div className="choice-grid choice-3" role="radiogroup" aria-label={t('selector.title')}>
          {BACKENDS.map((b) => (
            <button key={b} type="button" role="radio" aria-checked={form.backend === b} className={cx('choice', form.backend === b && 'on')} onClick={() => pick(b)}>
              <span className="choice-title">
                <Icon name={b === 'open-rlcd-local' ? 'cpu' : 'globe'} size={16} />
                {t(`selector.backend.${b}.title`)}
                {config.selector.backend === b && <Badge tone="accent">{t('selector.current')}</Badge>}
              </span>
              <span className="choice-body">{t(`selector.backend.${b}.body`)}</span>
            </button>
          ))}
        </div>
        <div className="form-grid">
          <Field label={t('selector.baseUrl')}>
            <input className="mono" value={form.base_url} placeholder={form.backend === 'open-rlcd-cloud' ? 'https://…' : 'http://127.0.0.1:8000'} onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
          </Field>
          <Field label={t('selector.model')}>
            <input className="mono" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} />
          </Field>
          <Field label={needsToken ? t('selector.token') : t('selector.tokenOptional')}>
            <input type="password" autoComplete="off" value={token} placeholder={form.has_token ? t('common.secretKept') : t('common.secretPaste')} onChange={(e) => setToken(e.target.value)} />
          </Field>
          <Field label={t('selector.tokenEnv')}>
            <input className="mono" value={form.token_env ?? ''} placeholder="TYPESAFE_API_KEY" onChange={(e) => setForm({ ...form, token_env: e.target.value })} />
          </Field>
        </div>
        <p className="fine">{t('selector.savedTo', { path: config.config_path })}</p>
        <div className="btn-row">
          <Button variant="primary" onClick={save} loading={saving && !testing}>{t('common.save')}</Button>
          <Button icon="zap" onClick={test} loading={testing} disabled={!form.base_url}>{t('selector.saveTest')}</Button>
          {saved && !probe && <span className="tone-good small" role="status"><Icon name="check" size={12} /> {t('common.saved')}</span>}
        </div>
        {probe && (
          probe.ok ? (
            <Callout tone="good" title={t('selector.connected')}>
              {t('selector.probeTime', { wall: f.ms(probe.result.wall_ms) })}
              {probe.result.forward_ms != null && ` · ${t('selector.probeCompute', { ms: f.ms(probe.result.forward_ms) })}`}
              {keep != null && <div className="fine">{t('selector.probeQuestion', { score: f.score(keep) })}</div>}
            </Callout>
          ) : (
            <Callout tone="bad" title={t('selector.failed')}><span className="mono">{probe.error}</span></Callout>
          )
        )}
      </Card>
    </div>
  );
}

function Upstreams() {
  const { t } = useI18n();
  const cur = useFetch(() => api.adapters(), []);
  const [form, setForm] = useState<AdapterSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => {
    if (cur.data && !form) setForm(cur.data);
  }, [cur.data, form]);
  if (cur.error) return <ErrorState error={cur.error} onRetry={cur.reload} />;
  if (!form) return <Card><Loading /></Card>;
  const save = async () => {
    setBusy(true);
    setMsg(null);
    try {
      const s = await api.setAdapters(form);
      setForm(s);
      cur.setData(s);
      setMsg({ ok: true, text: t('common.saved') });
    } catch (e) {
      setMsg({ ok: false, text: String(e instanceof Error ? e.message : e) });
    } finally {
      setBusy(false);
    }
  };
  const dirty = JSON.stringify(form) !== JSON.stringify(cur.data);
  return (
    <Card title={t('upstreams.title')} subtitle={t('upstreams.sub')}>
      <div className="form-stack">
        <Field label={t('upstreams.openai')} hint={t('upstreams.openaiHint')}>
          <input className="mono" value={form.openai_base_url} onChange={(e) => setForm({ ...form, openai_base_url: e.target.value })} />
        </Field>
        <Field label={t('upstreams.chatgpt')} hint={t('upstreams.chatgptHint')}>
          <input className="mono" value={form.chatgpt_base_url} onChange={(e) => setForm({ ...form, chatgpt_base_url: e.target.value })} />
        </Field>
      </div>
      <p className="fine">{t('upstreams.note')}</p>
      <div className="btn-row">
        <Button variant="primary" onClick={save} loading={busy} disabled={!dirty}>{t('common.save')}</Button>
        <Button onClick={() => setForm({ openai_base_url: '', chatgpt_base_url: '' })}>{t('upstreams.defaults')}</Button>
        {msg && <span className={cx('small', msg.ok ? 'tone-good' : 'tone-bad')} role="status">{msg.text}</span>}
      </div>
    </Card>
  );
}

function General() {
  const { t, locale, setLocale } = useI18n();
  const { pref, setPref } = useTheme();
  const { config } = useGateway();
  return (
    <div className="grid grid-2">
      <Card title={t('general.appearance')}>
        <div className="form-stack">
          <Field label={t('theme.label')}>
            <Segmented label={t('theme.label')} value={pref} onChange={setPref}
              options={[{ id: 'system', label: t('theme.system') }, { id: 'light', label: t('theme.light') }, { id: 'dark', label: t('theme.dark') }]} />
          </Field>
          <Field label={t('shell.language')} hint={t('general.languageHint')}>
            <select value={locale} onChange={(e) => setLocale(e.target.value as typeof locale)}>
              {LOCALES.map((l) => <option key={l.id} value={l.id} lang={l.id}>{l.label}</option>)}
            </select>
          </Field>
        </div>
      </Card>
      <Card title={t('general.gateway')}>
        {!config ? <Loading /> : (
          <dl className="kv-list">
            <div><dt>{t('general.listen')}</dt><dd className="mono">{config.listen}</dd></div>
            <div><dt>{t('general.config')}</dt><dd className="mono small">{config.config_path}</dd></div>
            <div><dt>{t('general.dashboard')}</dt><dd className="mono small">{window.location.origin}/ui/</dd></div>
            <div>
              <dt>log_bodies</dt>
              <dd>{config.log_bodies ? <Badge tone="good">{t('general.on')}</Badge> : <Badge tone="warn">{t('general.off')}</Badge>} <span className="muted small">{t('general.logBodies')}</span></dd>
            </div>
            <div>
              <dt>{t('general.access')}</dt>
              <dd>
                {config.exposed ? <Badge tone="warn">{t('general.exposed')}</Badge> : <Badge tone="good">{t('general.loopback')}</Badge>}{' '}
                <span className="muted small">{config.require_keys ? t('general.keysRequired') : t('general.keysOptional')}</span>
              </dd>
            </div>
            {!!config.allowed_hosts?.length && (
              <div><dt>{t('general.allowedHosts')}</dt><dd className="mono small">{config.allowed_hosts.join(', ')}</dd></div>
            )}
          </dl>
        )}
        {config?.exposed && (
          <div className="btn-row">
            <Button icon="x" onClick={() => api.logout().finally(() => window.location.reload())}>{t('general.logout')}</Button>
          </div>
        )}
        <p className="fine">{t('general.privacy')}</p>
      </Card>
    </div>
  );
}
