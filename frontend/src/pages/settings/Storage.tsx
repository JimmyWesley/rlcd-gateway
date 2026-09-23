// Settings > Storage: how much the logs take, how long they are kept, and a
// clean-up that always previews what it would delete first.
import { useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import {
  durationHours, hoursToDuration, storageApi,
  type BodiesPolicy, type JanitorReport, type StorageSettings, type StorageStats,
} from '../../lib/storageApi';
import { useFetch } from '../../state/gateway';
import { Donut } from '../../charts';
import { Badge, Button, Callout, Card, Confirm, ErrorState, Loading, Stat, cx } from '../../ui';

const GB = 1024 ** 3;
const MIN_CAP = 64 * 1024 * 1024;

const CATEGORIES = [
  { key: 'blobs', color: 'var(--series-1)' },
  { key: 'records', color: 'var(--series-3)' },
  { key: 'index', color: 'var(--series-4)' },
  { key: 'prune_state', color: 'var(--series-2)' },
  { key: 'recall_events', color: 'var(--series-5)' },
  { key: 'router_state', color: 'var(--series-7)' },
] as const;

export function Storage() {
  const { t } = useI18n();
  const st = useFetch(() => storageApi.stats(), []);
  if (st.error && !st.data) return <ErrorState error={st.error} onRetry={st.reload} />;
  if (!st.data) return <Card><Loading lines={6} /></Card>;
  const s = st.data;
  return (
    <div className="settings-stack storage">
      <Usage s={s} />
      <div className="grid grid-2">
        <Pinned s={s} />
        <Janitor s={s} onDone={st.reload} />
      </div>
      <Retention s={s} onSaved={st.reload} />
      <p className="fine">{t('storage.footnote')}</p>
    </div>
  );
}

function Usage({ s }: { s: StorageStats }) {
  const { t, f, tn } = useI18n();
  const cap = s.settings.max_total_bytes;
  const share = cap ? Math.min(1, s.bytes.managed_total / cap) : null;
  return (
    <Card title={t('storage.usage.title')} subtitle={t('storage.usage.sub')}>
      <div className="storage-hero">
        <div className="storage-story">
          <div className="hero-value">
            {tn('storage.story', { logical: <strong>{f.bytes(s.logical_bytes)}</strong>, stored: <span className="hero-num">{f.bytes(s.stored_bytes)}</span> })}
          </div>
          {s.total_ratio > 1 && (
            <p className="tone-good storage-ratio">
              {s.dedup_ratio >= 1.05
                ? t('storage.ratios', { total: f.ratio(s.total_ratio), dedup: f.ratio(s.dedup_ratio), gzip: f.ratio(s.compression_ratio) })
                : t('storage.ratiosGzip', { total: f.ratio(s.total_ratio) })}
            </p>
          )}
          <div className="ratio-steps" aria-hidden>
            <RatioStep label={t('storage.step.logical')} value={s.logical_bytes} max={s.logical_bytes} />
            <RatioStep label={t('storage.step.dedup')} value={s.deduped_bytes} max={s.logical_bytes} />
            <RatioStep label={t('storage.step.stored')} value={s.stored_bytes} max={s.logical_bytes} />
          </div>
        </div>
        <Donut
          label={t('storage.usage.title')}
          size={132}
          format={(v) => f.bytes(v)}
          segments={CATEGORIES.map((c) => ({ key: c.key, label: t(`storage.cat.${c.key}`), value: s.bytes[c.key], color: c.color }))}
          center={<><div className="donut-value">{f.bytes(s.bytes.total)}</div><div className="donut-label">{t('storage.onDisk')}</div></>}
        />
      </div>
      <div className="mini-stats">
        <Stat label={t('storage.stat.records')} value={f.num(s.counts.records)} sub={s.counts.legacy_records ? t('storage.stat.legacy', { count: s.counts.legacy_records }) : t('storage.stat.blobs', { n: f.num(s.counts.blobs) })} />
        <Stat label={t('storage.stat.oldest')} value={s.oldest_record ? f.day(s.oldest_record) : '—'} sub={s.oldest_record ? f.ago(s.oldest_record) : undefined} />
        <Stat label={t('storage.stat.newest')} value={s.newest_record ? f.day(s.newest_record) : '—'} sub={s.newest_record ? f.ago(s.newest_record) : undefined} />
        <Stat label={t('storage.stat.cap')} value={cap ? f.pct(share) : t('storage.noCap')} sub={cap ? t('storage.stat.capSub', { used: f.bytes(s.bytes.managed_total), cap: f.bytes(cap) }) : undefined} />
      </div>
      {share != null && (
        <div className="meter-track" title={t('storage.stat.capSub', { used: f.bytes(s.bytes.managed_total), cap: f.bytes(cap) })}>
          <span className={cx('meter-fill', share > 0.9 && 'is-bad', share > 0.7 && share <= 0.9 && 'is-warn')} style={{ width: `${Math.max(1, share * 100)}%` }} />
        </div>
      )}
    </Card>
  );
}

function RatioStep({ label, value, max }: { label: string; value: number; max: number }) {
  const { f } = useI18n();
  return (
    <div className="ratio-step">
      <span className="ratio-label">{label}</span>
      <span className="ratio-bar"><span style={{ width: `${Math.max(0.6, (value / (max || 1)) * 100)}%` }} /></span>
      <span className="ratio-val mono">{f.bytes(value)}</span>
    </div>
  );
}

function Pinned({ s }: { s: StorageStats }) {
  const { t, f } = useI18n();
  return (
    <Card title={t('storage.pinned.title')} subtitle={t('storage.pinned.sub')}>
      <div className="mini-stats">
        <Stat icon="lock" label={t('storage.pinned.convs')} value={f.num(s.pinned.conversations)} />
        <Stat label={t('storage.pinned.requests')} value={f.num(s.pinned.requests)} />
      </div>
      <p className="fine">{t('storage.pinned.why', { ttl: s.settings.conversation_ttl })}</p>
    </Card>
  );
}

function reportSummary(t: ReturnType<typeof useI18n>['t'], f: ReturnType<typeof useI18n>['f'], r: JanitorReport) {
  const freed = r.purge.total_bytes_before - r.purge.total_bytes_after;
  return {
    deleted: r.purge.details_deleted,
    freed: f.bytes(r.dry_run ? r.purge.detail_bytes + r.purge.blob_bytes + r.purge.index_bytes : freed),
    line: t('storage.report.line', {
      details: r.purge.details_deleted, age: r.purge.details_by_age, size: r.purge.details_by_size,
      blobs: r.purge.blobs_deleted, convs: r.conversations_expired,
    }),
  };
}

function Janitor({ s, onDone }: { s: StorageStats; onDone: () => void }) {
  const { t, f } = useI18n();
  const [preview, setPreview] = useState<JanitorReport | null>(null);
  const [result, setResult] = useState<JanitorReport | null>(null);
  const [busy, setBusy] = useState<'dry' | 'run' | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const last = s.last_janitor_run;

  const dry = async () => {
    setBusy('dry');
    setErr(null);
    setResult(null);
    try {
      setPreview(await storageApi.purge(true));
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(null);
    }
  };
  const run = async () => {
    setBusy('run');
    try {
      setResult(await storageApi.purge(false));
      setPreview(null);
      onDone();
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(null);
    }
  };

  const pv = preview && reportSummary(t, f, preview);
  const nothing = preview && preview.purge.details_deleted === 0 && preview.purge.blobs_deleted === 0 && preview.conversations_expired === 0 && !preview.purge.index_files_deleted?.length;

  return (
    <Card
      title={t('storage.janitor.title')}
      subtitle={t('storage.janitor.sub', { every: hoursToDuration(s.janitor_interval_seconds / 3600) })}
      actions={<Button icon="refresh" loading={busy === 'dry'} onClick={dry}>{t('storage.cleanup')}</Button>}
    >
      {last ? (
        <div className="janitor-last">
          <div className="small">
            <strong>{t('storage.janitor.last', { when: f.ago(last.time) })}</strong>
            <span className="muted"> · {t(`storage.trigger.${(['startup', 'schedule', 'api', 'cli'] as const).find((x) => x === last.trigger) ?? 'other'}`)} · {f.ms(last.duration_ms)}</span>
          </div>
          <div className="muted small">{reportSummary(t, f, last).line} · {t('storage.report.freed', { bytes: reportSummary(t, f, last).freed })}</div>
          {last.errors?.map((e) => <div key={e} className="tone-bad small">{e}</div>)}
        </div>
      ) : <p className="muted small">{t('storage.janitor.never')}</p>}
      {err && <Callout tone="bad">{err}</Callout>}
      {result && <Callout tone="good" title={t('storage.done')}>{reportSummary(t, f, result).line} · {t('storage.report.freed', { bytes: reportSummary(t, f, result).freed })}</Callout>}
      <Confirm
        open={!!preview}
        title={nothing ? t('storage.preview.nothing') : t('storage.preview.title')}
        confirmLabel={nothing ? t('common.close') : t('storage.preview.confirm')}
        tone={nothing ? 'primary' : 'danger'}
        busy={busy === 'run'}
        onConfirm={nothing ? () => setPreview(null) : run}
        onCancel={() => setPreview(null)}
      >
        {preview && pv && (
          <div className="purge-preview">
            <p>{t('storage.preview.body', { count: pv.deleted, bytes: pv.freed })}</p>
            <ul className="purge-facts">
              <li><span>{t('storage.preview.byAge')}</span><strong>{f.num(preview.purge.details_by_age)}</strong></li>
              <li><span>{t('storage.preview.bySize')}</span><strong>{f.num(preview.purge.details_by_size)}</strong></li>
              <li><span>{t('storage.preview.blobs')}</span><strong>{f.num(preview.purge.blobs_deleted)} · {f.bytes(preview.purge.blob_bytes)}</strong></li>
              <li><span>{t('storage.preview.convs')}</span><strong>{f.num(preview.conversations_expired)}</strong></li>
              <li><span>{t('storage.preview.kept')}</span><strong>{f.num(preview.purge.pinned_kept)}</strong></li>
            </ul>
            {(preview.purge.details ?? []).length > 0 && (
              <ul className="purge-list">
                {(preview.purge.details ?? []).slice(0, 6).map((d) => (
                  <li key={d.id}><span className="mono">{f.dayTime(d.time)}</span><Badge tone={d.reason === 'size' ? 'warn' : 'neutral'}>{t(`purged.reason.${d.reason}`)}</Badge><span className="mono muted">{f.bytes(d.bytes)}</span></li>
                ))}
                {(preview.purge.details ?? []).length > 6 && <li className="muted">{t('storage.preview.more', { count: (preview.purge.details ?? []).length - 6 })}</li>}
              </ul>
            )}
            {preview.purge.warnings?.map((w) => <p key={w} className="tone-warn small">{w}</p>)}
            <p className="fine">{t('storage.preview.safe')}</p>
          </div>
        )}
      </Confirm>
    </Card>
  );
}

type Unit = 'd' | 'h';
type Dur = { value: number; unit: Unit };
const toDur = (s: string): Dur => {
  const h = durationHours(s);
  return h && h % 24 !== 0 ? { value: Math.round(h), unit: 'h' } : { value: h / 24, unit: 'd' };
};
const fromDur = (d: Dur) => hoursToDuration(d.unit === 'd' ? d.value * 24 : d.value);

function DurationInput({ value, onChange, label, allowForever }: { value: Dur; onChange: (d: Dur) => void; label: string; allowForever?: boolean }) {
  const { t } = useI18n();
  return (
    <span className="dur-input">
      <input type="number" min={allowForever ? 0 : 1} step={1} className="input-sm" aria-label={label} value={value.value} onChange={(e) => onChange({ ...value, value: Math.max(0, Number(e.target.value) || 0) })} />
      <select className="input-sm" aria-label={t('storage.unit')} value={value.unit} onChange={(e) => onChange({ ...value, unit: e.target.value as Unit })}>
        <option value="d">{t('storage.unit.days')}</option>
        <option value="h">{t('storage.unit.hours')}</option>
      </select>
    </span>
  );
}

function Retention({ s, onSaved }: { s: StorageStats; onSaved: () => void }) {
  const { t, f, tn } = useI18n();
  const init = () => ({
    detail: toDur(s.settings.detail_max_age),
    summary: toDur(s.settings.summary_max_age),
    ttl: toDur(s.settings.conversation_ttl),
    capGB: s.settings.max_total_bytes / GB,
    bodies: s.settings.bodies,
  });
  const [form, setForm] = useState(init);
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => setForm(init()), [s.settings]); // eslint-disable-line react-hooks/exhaustive-deps

  const next: StorageSettings = {
    detail_max_age: fromDur(form.detail),
    summary_max_age: fromDur(form.summary),
    conversation_ttl: fromDur(form.ttl),
    max_total_bytes: Math.round(form.capGB * GB),
    bodies: form.bodies,
  };
  const cur = s.settings;
  const changed = (Object.keys(next) as (keyof StorageSettings)[]).filter((k) => String(next[k]) !== String(cur[k]));
  const hours = (d: Dur) => (d.unit === 'd' ? d.value * 24 : d.value);
  const problems: string[] = [];
  if (hours(form.detail) !== 0 && hours(form.detail) < 1) problems.push(t('storage.err.age'));
  if (hours(form.ttl) < 1) problems.push(t('storage.err.ttl'));
  if (next.max_total_bytes !== 0 && next.max_total_bytes < MIN_CAP) problems.push(t('storage.err.cap', { min: f.bytes(MIN_CAP) }));

  const save = async () => {
    setSaving(true);
    setMsg(null);
    try {
      const patch: Partial<StorageSettings> = {};
      for (const k of changed) (patch as Record<string, unknown>)[k] = next[k];
      await storageApi.saveSettings(patch);
      setMsg({ ok: true, text: t('storage.saved') });
      onSaved();
    } catch (e) {
      setMsg({ ok: false, text: String(e instanceof Error ? e.message : e) });
    } finally {
      setSaving(false);
    }
  };
  const resetDefaults = () => {
    const d = cur.defaults;
    setForm({ detail: toDur(d.detail_max_age), summary: toDur(d.summary_max_age), ttl: toDur(d.conversation_ttl), capGB: d.max_total_bytes / GB, bodies: d.bodies });
  };

  return (
    <Card title={t('storage.retention.title')} subtitle={t('storage.retention.sub')} actions={<Button size="sm" variant="ghost" onClick={resetDefaults}>{t('storage.defaults')}</Button>}>
      <div className="retention-rows">
        <div className="retention-row">
          <div className="retention-text">
            <strong>{t('storage.detail.label')}</strong>
            <span className="fine">{t('storage.detail.help')}</span>
          </div>
          <div className="retention-ctl">
            <span className="small">{t('storage.keepFor')}</span>
            <DurationInput label={t('storage.detail.label')} value={form.detail} onChange={(d) => setForm({ ...form, detail: d })} allowForever />
            {hours(form.detail) === 0 && <Badge>{t('storage.forever')}</Badge>}
          </div>
        </div>
        <div className="retention-row">
          <div className="retention-text">
            <strong>{t('storage.summary.label')}</strong>
            <span className="fine">{t('storage.summary.help')}</span>
          </div>
          <div className="retention-ctl">
            <span className="small">{t('storage.keepFor')}</span>
            <DurationInput label={t('storage.summary.label')} value={form.summary} onChange={(d) => setForm({ ...form, summary: d })} allowForever />
            {hours(form.summary) === 0 && <Badge>{t('storage.forever')}</Badge>}
          </div>
        </div>
        <div className="retention-row">
          <div className="retention-text">
            <strong>{t('storage.cap.label')}</strong>
            <span className="fine">{t('storage.cap.help')}</span>
          </div>
          <div className="retention-ctl">
            <input type="range" min={0} max={20} step={0.5} value={Math.min(20, form.capGB)} aria-label={t('storage.cap.label')} onChange={(e) => setForm({ ...form, capGB: Number(e.target.value) })} />
            <span className="dur-input">
              <input type="number" min={0} step={0.1} className="input-sm" aria-label={t('storage.cap.label')} value={Number(form.capGB.toFixed(2))} onChange={(e) => setForm({ ...form, capGB: Math.max(0, Number(e.target.value) || 0) })} />
              <span className="small">GB</span>
            </span>
            {form.capGB === 0 && <Badge>{t('storage.noCap')}</Badge>}
          </div>
        </div>
        <div className="retention-row retention-col">
          <div className="retention-text">
            <strong>{t('storage.bodies.label')}</strong>
            <span className="fine">{t('storage.bodies.help')}</span>
          </div>
          <div className="choice-grid choice-3">
            {(['full', 'errors_only', 'none'] as BodiesPolicy[]).map((b) => (
              <button key={b} type="button" className={cx('choice', form.bodies === b && 'on')} aria-pressed={form.bodies === b} onClick={() => setForm({ ...form, bodies: b })}>
                <span className="choice-title"><Icon name={b === 'full' ? 'database' : b === 'errors_only' ? 'alert' : 'eye'} size={15} />{t(`storage.bodies.${b}`)}</span>
                <span className="choice-body">{t(`storage.bodies.${b}.body`)}</span>
              </button>
            ))}
          </div>
          {!cur.log_bodies && <Callout tone="warn">{tn('storage.bodies.logOff', { key: <code>log_bodies: false</code> })}</Callout>}
          {form.bodies === 'none' && <Callout tone="warn">{t('storage.bodies.noneWarn')}</Callout>}
        </div>
        <div className="retention-row">
          <div className="retention-text">
            <strong>{t('storage.ttl.label')}</strong>
            <span className="fine">{t('storage.ttl.help')}</span>
          </div>
          <div className="retention-ctl">
            <DurationInput label={t('storage.ttl.label')} value={form.ttl} onChange={(d) => setForm({ ...form, ttl: d })} />
          </div>
        </div>
      </div>
      {problems.map((p) => <Callout key={p} tone="warn">{p}</Callout>)}
      <div className={cx('savebar', 'savebar-inline', changed.length > 0 && 'show')}>
        <span className={cx('small', msg ? (msg.ok ? 'tone-good' : 'tone-bad') : changed.length ? 'tone-warn' : 'muted')} role="status">
          {msg ? msg.text : changed.length ? t('common.unsaved') : t('storage.allSaved')}
        </span>
        <span className="toolbar-spacer" />
        <Button onClick={() => { setForm(init()); setMsg(null); }} disabled={!changed.length || saving}>{t('common.discard')}</Button>
        <Button variant="primary" onClick={save} loading={saving} disabled={!changed.length || problems.length > 0}>{t('common.save')}</Button>
      </div>
    </Card>
  );
}
