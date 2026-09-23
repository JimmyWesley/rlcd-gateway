import { useCallback, useEffect, useMemo, useState } from 'react';
import { useI18n, type PlainKey } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { pruneApi, type Preset, type PresetName, type PruneConfig, type PruneSettings as Settings } from '../../lib/pruneApi';
import { Button, Callout, Card, Disclosure, ErrorState, Field, Loading, Toggle, cx } from '../../ui';

type BoolFlag = 'keep_errors' | 'keep_edits' | 'drop_superseded_reads' | 'always_keep_user_text';
type NumFlag = 'keep_last_n_turns' | 'min_block_tokens' | 'keep_threshold' | 'epoch_tokens' | 'floor_tokens';
// Fields a preset sets; an explicit value overrides it.
const PRESET_FIELDS: (BoolFlag | NumFlag)[] = [
  'keep_errors', 'keep_edits', 'drop_superseded_reads', 'always_keep_user_text',
  'keep_last_n_turns', 'min_block_tokens', 'keep_threshold', 'epoch_tokens', 'floor_tokens',
];
const FLAGS: BoolFlag[] = ['keep_errors', 'keep_edits', 'drop_superseded_reads', 'always_keep_user_text'];

const pricesJSON = (c: PruneConfig) => (c.settings.prices ? JSON.stringify(c.settings.prices, null, 2) : '');

export function PruneSettings() {
  const { t, f, tn } = useI18n();
  const [cfg, setCfg] = useState<PruneConfig | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [form, setForm] = useState<Settings>({});
  const [pricesText, setPricesText] = useState('');
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => {
    setLoadErr(null);
    pruneApi.config().then((c) => {
      setCfg(c);
      setForm(c.settings);
      setPricesText(pricesJSON(c));
    }).catch((e) => setLoadErr(String(e instanceof Error ? e.message : e)));
  }, []);
  useEffect(load, [load]);

  const preset = useMemo<Preset | undefined>(() => cfg?.presets.find((p) => p.name === (form.preset || 'balanced')), [cfg, form.preset]);
  const dirty = cfg != null && (JSON.stringify(form) !== JSON.stringify(cfg.settings) || pricesText !== pricesJSON(cfg));

  if (loadErr) return <ErrorState error={loadErr} onRetry={load} />;
  if (!cfg || !preset) return <Card><Loading lines={6} /></Card>;
  const eff = cfg.effective;

  const val = <K extends BoolFlag | NumFlag>(k: K): Preset[K] => (form[k] ?? preset[k]) as Preset[K];
  const set = (patch: Settings) => { setForm({ ...form, ...patch }); setMsg(null); };
  const mode = form.mode || 'shadow';
  const enabled = form.enabled ?? true;

  const pickPreset = (name: PresetName) => {
    // A preset is a bundle: switching drops the overrides it would set.
    const next: Settings = { ...form, preset: name };
    for (const k of PRESET_FIELDS) delete next[k];
    setForm(next);
    setMsg(null);
  };

  const save = async () => {
    let prices: Settings['prices'];
    if (pricesText.trim()) {
      try {
        prices = JSON.parse(pricesText);
      } catch (e) {
        setMsg({ ok: false, text: t('pruneSettings.pricesInvalid', { error: String(e) }) });
        return;
      }
    }
    setSaving(true);
    try {
      const c = await pruneApi.saveConfig({ ...form, prices });
      setCfg(c);
      setForm(c.settings);
      setPricesText(pricesJSON(c));
      setMsg({ ok: true, text: t('pruneSettings.saved') });
    } catch (e) {
      setMsg({ ok: false, text: String(e instanceof Error ? e.message : e) });
    } finally {
      setSaving(false);
    }
  };

  const Override = ({ k }: { k: BoolFlag | NumFlag | 'selector_timeout_ms' }) =>
    form[k] != null ? (
      <button type="button" className="linkish small override" onClick={(e) => { e.preventDefault(); set({ [k]: null }); }}>{t('pruneSettings.reset')}</button>
    ) : (
      <span className="muted small">{t('pruneSettings.fromPreset')}</span>
    );

  const num = (k: NumFlag | 'selector_timeout_ms', label: PlainKey, hint?: PlainKey, step = 1) => (
    <Field label={t(label)} hint={hint && t(hint)} extra={<Override k={k} />}>
      <input
        type="number"
        min={0}
        step={step}
        value={k === 'selector_timeout_ms' ? form.selector_timeout_ms ?? eff.selector_timeout_ms : val(k)}
        onChange={(e) => set({ [k]: e.target.value === '' ? null : Number(e.target.value) })}
      />
    </Field>
  );

  return (
    <div className="settings-stack">
      <Card title={t('pruneSettings.status.title')} subtitle={t('pruneSettings.status.sub')}>
        <Toggle checked={enabled} onChange={(v) => set({ enabled: v })} label={enabled ? t('pruneSettings.enabled') : t('pruneSettings.disabled')} description={t('pruneSettings.enabledHint')} />
        <div className="choice-grid choice-2">
          {(['shadow', 'enforce'] as const).map((m) => (
            <button key={m} type="button" className={cx('choice', mode === m && 'on')} aria-pressed={mode === m} onClick={() => set({ mode: m })} disabled={!enabled}>
              <span className="choice-title"><Icon name={m === 'shadow' ? 'eye' : 'savings'} size={16} />{t(`pruneSettings.mode.${m}`)}</span>
              <span className="choice-body">{t(`pruneSettings.mode.${m}.body`)}</span>
            </button>
          ))}
        </div>
        {mode === 'enforce' && !cfg.log_bodies && <Callout tone="warn">{tn('pruneSettings.needsBodies', { key: <code>log_bodies</code> })}</Callout>}
      </Card>

      <Card title={t('pruneSettings.preset.title')} subtitle={t('pruneSettings.preset.sub')}>
        <div className="choice-grid choice-3">
          {cfg.presets.map((p) => (
            <button key={p.name} type="button" className={cx('choice', preset.name === p.name && 'on')} aria-pressed={preset.name === p.name} onClick={() => pickPreset(p.name)}>
              <span className="choice-title">{t(`preset.${p.name}`)}</span>
              <span className="choice-body">{t(`preset.${p.name}.body`)}</span>
              <span className="choice-meta mono">{t('pruneSettings.preset.meta', { threshold: p.keep_threshold, turns: p.keep_last_n_turns, epoch: f.tokens(p.epoch_tokens) })}</span>
            </button>
          ))}
        </div>
      </Card>

      <Card title={t('pruneSettings.rules.title')} subtitle={t('pruneSettings.rules.sub')}>
        <div className="toggle-list">
          {FLAGS.map((k) => (
            <div key={k} className="toggle-with-extra">
              <Toggle checked={val(k)} onChange={(v) => set({ [k]: v })} label={t(`pruneSettings.flag.${k}`)} description={t(`pruneSettings.flag.${k}.help`)} />
              <Override k={k} />
            </div>
          ))}
        </div>
        <p className="fine">{tn('pruneSettings.protected', { recall: <code>rlcd_recall</code>, id: <code>tool_use_id</code>, err: <code>is_error</code> })}</p>
        <div className="form-grid">
          {num('keep_last_n_turns', 'pruneSettings.keepTurns', 'pruneSettings.keepTurns.hint')}
          {num('min_block_tokens', 'pruneSettings.minBlock', 'pruneSettings.minBlock.hint')}
          {num('keep_threshold', 'pruneSettings.threshold', 'pruneSettings.threshold.hint', 0.01)}
        </div>
      </Card>

      <Card title={t('pruneSettings.criteria.title')} subtitle={t('pruneSettings.criteria.sub')}>
        <textarea
          className="textarea"
          rows={5}
          aria-label={t('pruneSettings.criteria.title')}
          value={form.criteria || cfg.default_criteria}
          onChange={(e) => set({ criteria: e.target.value === cfg.default_criteria ? '' : e.target.value })}
        />
        {form.criteria && <Button size="sm" variant="ghost" onClick={() => set({ criteria: '' })}>{t('pruneSettings.criteria.reset')}</Button>}
      </Card>

      <Card title={t('pruneSettings.advanced.title')} subtitle={t('pruneSettings.advanced.sub')}>
        <Disclosure title={t('pruneSettings.epochs.title')}>
          <p className="fine">{t('pruneSettings.epochs.body')}</p>
          <div className="form-grid">
            {num('epoch_tokens', 'pruneSettings.epochTokens')}
            {num('floor_tokens', 'pruneSettings.floorTokens', 'pruneSettings.floorTokens.hint')}
            {num('selector_timeout_ms', 'pruneSettings.timeout', 'pruneSettings.timeout.hint')}
          </div>
        </Disclosure>
        <Disclosure title={t('pruneSettings.cacheBreakers.title')}>
          <Callout tone="warn" title={t('pruneSettings.cacheBreakers.warnTitle')}>{t('pruneSettings.cacheBreakers.warn')}</Callout>
          <div className="toggle-list">
            <Toggle checked={form.prune_tools ?? false} onChange={(v) => set({ prune_tools: v })} label={t('pruneSettings.pruneTools')} description={t('pruneSettings.pruneTools.help')} />
            <Toggle checked={form.prune_system ?? false} onChange={(v) => set({ prune_system: v })} label={t('pruneSettings.pruneSystem')} description={t('pruneSettings.pruneSystem.help')} />
          </div>
        </Disclosure>
        <Disclosure title={t('pruneSettings.prices.title')} meta={t('pruneSettings.prices.meta', { date: cfg.prices_as_of })}>
          <div className="scroll-x">
            <table className="table">
              <thead>
                <tr>
                  <th>{t('pruneSettings.prices.model')}</th>
                  <th className="num">{t('pruneSettings.prices.input')}</th>
                  <th className="num">{t('usage.cacheRead')}</th>
                  <th className="num">{t('usage.cacheWrite')}</th>
                  <th className="num">{t('usage.output')}</th>
                </tr>
              </thead>
              <tbody>
                {Object.keys(eff.prices).sort().map((k) => {
                  const p = eff.prices[k];
                  return (
                    <tr key={k}>
                      <td className="mono">{k}</td>
                      <td className="num mono">{p.input}</td>
                      <td className="num mono">{p.cache_read}</td>
                      <td className="num mono">{p.cache_write}</td>
                      <td className="num mono">{p.output}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <p className="fine">{tn('pruneSettings.prices.help', { example: <code>{'{"claude-opus-5": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}}'}</code> })}</p>
          <textarea className="textarea mono" rows={4} value={pricesText} placeholder="{}" aria-label={t('pruneSettings.prices.title')}
            onChange={(e) => { setPricesText(e.target.value); setMsg(null); }} />
        </Disclosure>
      </Card>

      <div className={cx('savebar', (dirty || msg) && 'show')} role="region" aria-label={t('common.save')}>
        <span className={cx('small', msg ? (msg.ok ? 'tone-good' : 'tone-bad') : 'muted')} role="status">
          {msg ? msg.text : dirty ? t('common.unsaved') : ''}
        </span>
        <span className="toolbar-spacer" />
        <Button onClick={load} disabled={saving || !dirty}>{t('common.discard')}</Button>
        <Button variant="primary" onClick={save} loading={saving} disabled={!dirty}>{t('common.save')}</Button>
      </div>
    </div>
  );
}
