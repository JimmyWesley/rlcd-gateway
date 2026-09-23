import { useCallback, useEffect, useMemo, useState } from 'react';
import { fmt } from '../api';
import {
  pruneApi,
  REASONS,
  usd,
  type Effective,
  type Feedback,
  type Preset,
  type PresetName,
  type PruneConfig,
  type PruneSettings,
  type PruneStats,
  type ReplayReport,
} from './pruneApi';

// Fields a preset sets; an explicit value overrides it.
type BoolFlag = 'keep_errors' | 'keep_edits' | 'drop_superseded_reads' | 'always_keep_user_text';
type NumFlag = 'keep_last_n_turns' | 'min_block_tokens' | 'keep_threshold' | 'epoch_tokens' | 'floor_tokens';
const PRESET_FIELDS: (BoolFlag | NumFlag)[] = [
  'keep_errors', 'keep_edits', 'drop_superseded_reads', 'always_keep_user_text',
  'keep_last_n_turns', 'min_block_tokens', 'keep_threshold', 'epoch_tokens', 'floor_tokens',
];

const FLAGS: { key: BoolFlag; label: string; help: string }[] = [
  { key: 'keep_errors', label: 'Keep errors', help: 'Never drop a tool result marked is_error.' },
  { key: 'keep_edits', label: 'Keep edit results', help: 'Never drop results of Edit, Write, MultiEdit, NotebookEdit and similar.' },
  { key: 'drop_superseded_reads', label: 'Drop superseded reads', help: 'A file read that is read again (or overwritten) later is stale: drop it without asking.' },
  { key: 'always_keep_user_text', label: 'Always keep user text', help: 'Never drop text blocks in user messages (prompts, reminders).' },
];

export function PrunePanel() {
  const [cfg, setCfg] = useState<PruneConfig | null>(null);
  const [form, setForm] = useState<PruneSettings>({});
  const [stats, setStats] = useState<PruneStats | null>(null);
  const [feedback, setFeedback] = useState<Feedback[]>([]);
  const [replay, setReplay] = useState<ReplayReport | null>(null);
  const [replaying, setReplaying] = useState(false);
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [pricesText, setPricesText] = useState('');

  const load = useCallback(() => {
    pruneApi.config().then((c) => {
      setCfg(c);
      setForm(c.settings);
      setPricesText(c.settings.prices ? JSON.stringify(c.settings.prices, null, 2) : '');
    }).catch((e) => setMsg({ ok: false, text: String(e) }));
    pruneApi.stats().then(setStats).catch(() => {});
    pruneApi.feedback().then(setFeedback).catch(() => {});
  }, []);
  useEffect(load, [load]);

  const preset = useMemo<Preset | undefined>(
    () => cfg?.presets.find((p) => p.name === (form.preset || 'balanced')),
    [cfg, form.preset],
  );
  const dirty = cfg != null && (JSON.stringify(form) !== JSON.stringify(cfg.settings) ||
    pricesText !== (cfg.settings.prices ? JSON.stringify(cfg.settings.prices, null, 2) : ''));

  if (!cfg || !preset) {
    return <main className="panel prune-panel"><h2>Pruning</h2><p className="muted">{msg?.text ?? 'Loading…'}</p></main>;
  }
  const eff: Effective = cfg.effective;

  // What the form resolves to, before saving.
  const val = <K extends BoolFlag | NumFlag>(k: K): Preset[K] => (form[k] ?? preset[k]) as Preset[K];
  const set = (patch: PruneSettings) => { setForm({ ...form, ...patch }); setMsg(null); };
  const mode = form.mode || 'shadow';
  const enabled = form.enabled ?? true;

  const pickPreset = (name: PresetName) => {
    // A preset is a bundle: switching drops the overrides it would set.
    const next: PruneSettings = { ...form, preset: name };
    for (const k of PRESET_FIELDS) delete next[k];
    setForm(next);
    setMsg(null);
  };

  const save = async () => {
    let prices: PruneSettings['prices'];
    if (pricesText.trim()) {
      try {
        prices = JSON.parse(pricesText);
      } catch (e) {
        setMsg({ ok: false, text: `Price overrides are not valid JSON: ${String(e)}` });
        return;
      }
    }
    setSaving(true);
    try {
      const c = await pruneApi.saveConfig({ ...form, prices });
      setCfg(c);
      setForm(c.settings);
      setPricesText(c.settings.prices ? JSON.stringify(c.settings.prices, null, 2) : '');
      setMsg({ ok: true, text: 'Saved. Applies from the next request.' });
    } catch (e) {
      setMsg({ ok: false, text: String(e) });
    } finally {
      setSaving(false);
    }
  };

  const runReplay = async () => {
    setReplaying(true);
    try {
      setReplay(await pruneApi.replay());
    } catch (e) {
      setMsg({ ok: false, text: String(e) });
    } finally {
      setReplaying(false);
    }
  };

  const del = async (id: string) => {
    await pruneApi.deleteFeedback(id).catch(() => {});
    setFeedback((fs) => fs.filter((f) => f.id !== id));
    setReplay((r) => (r ? { ...r, cases: r.cases.filter((c) => c.id !== id) } : r));
  };

  const replayById = new Map(replay?.cases.map((c) => [c.id, c]) ?? []);

  return (
    <main className="panel prune-panel">
      <div className="prune-title">
        <h2>Pruning</h2>
        <label className="prune-switch">
          <input type="checkbox" checked={enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          {enabled ? 'enabled' : 'disabled'}
        </label>
      </div>
      <p className="muted">
        Before each request is forwarded, the economy model looks at old context blocks and answers, per block key, whether
        each should stay. Dropped content is replaced by a marker the model can pass to <code>rlcd_recall</code> to get it
        back. The economy model never writes text. Token counts are estimates (characters / 4).
      </p>

      <Stats stats={stats} />

      <h3>Mode</h3>
      <div className="cards">
        <button className={`card ${mode === 'shadow' ? 'active' : ''}`} onClick={() => set({ mode: 'shadow' })}>
          <strong>Shadow</strong>
          <span>Decide and report everything, but forward the agent’s request untouched. Use it to check the decisions in
            the request view before trusting them.</span>
        </button>
        <button className={`card ${mode === 'enforce' ? 'active' : ''}`} onClick={() => set({ mode: 'enforce' })}>
          <strong>Enforce</strong>
          <span>Forward the pruned request. Decisions are sticky per conversation, so the same markers are sent on every
            later turn and the provider’s prompt cache keeps working.</span>
        </button>
      </div>
      {mode === 'enforce' && !cfg.log_bodies && (
        <p className="note">
          Body logging is off, so dropped content could not be recalled. The gateway keeps running in shadow until
          <code> log_bodies</code> is turned on.
        </p>
      )}

      <h3>Preset</h3>
      <div className="cards">
        {cfg.presets.map((p) => (
          <button key={p.name} className={`card ${preset.name === p.name ? 'active' : ''}`} onClick={() => pickPreset(p.name)}>
            <strong>{p.name}</strong>
            <span>{p.description}</span>
            <span className="small">
              keep if score ≥ {p.keep_threshold} · last {p.keep_last_n_turns} turns · epoch every ~{fmt.n(p.epoch_tokens)}
            </span>
          </button>
        ))}
      </div>

      <h3>Rules <span className="muted small">applied before the economy model, free and deterministic</span></h3>
      <div className="prune-flags">
        {FLAGS.map((f) => (
          <label key={f.key} className="prune-flag">
            <input type="checkbox" checked={val(f.key)} onChange={(e) => set({ [f.key]: e.target.checked })} />
            <span>
              <strong>{f.label}</strong> <Override on={form[f.key] != null} onReset={() => set({ [f.key]: null })} />
              <span className="muted small">{f.help}</span>
            </span>
          </label>
        ))}
      </div>
      <p className="muted small">
        Always protected, whatever the settings: the latest user turn, signed thinking blocks, tool calls themselves,
        and results of <code>rlcd_recall</code>. A dropped tool result keeps its <code>tool_use_id</code> and
        <code> is_error</code>; only its content becomes a marker.
      </p>
      <div className="form prune-numbers">
        <Num label="Keep the last N turns" hint="a turn starts at each user message, tool results included"
          value={val('keep_last_n_turns')} over={form.keep_last_n_turns != null}
          onChange={(v) => set({ keep_last_n_turns: v })} />
        <Num label="Minimum block size (tokens)" hint="smaller blocks are not worth a marker"
          value={val('min_block_tokens')} over={form.min_block_tokens != null}
          onChange={(v) => set({ min_block_tokens: v })} />
        <Num label="Keep threshold (0–1)" hint="the economy model’s keep score must reach this" step={0.01}
          value={val('keep_threshold')} over={form.keep_threshold != null}
          onChange={(v) => set({ keep_threshold: v })} />
      </div>

      <h3>Criteria</h3>
      <p className="muted small">
        The instruction the economy model answers for every block, with the latest user request as the goal and the last
        tool calls as recent activity. Change it, then replay your feedback cases below to see what it fixes or breaks.
      </p>
      <textarea
        className="prune-criteria"
        rows={4}
        value={form.criteria || cfg.default_criteria}
        onChange={(e) => set({ criteria: e.target.value === cfg.default_criteria ? '' : e.target.value })}
      />
      {form.criteria && <button className="linkish" onClick={() => set({ criteria: '' })}>Reset to default</button>}

      <h3>Epochs</h3>
      <p className="muted small">
        New decisions are only taken in an epoch, and only about blocks that appeared since the last one. Between epochs
        the stored decisions are re-applied byte for byte, so the cached prefix is not disturbed. Each epoch changes the
        prompt once, after its first new drop: that part is written to the cache again at 1.25× input.
      </p>
      <div className="form prune-numbers">
        <Num label="Epoch every (tokens of growth)" value={val('epoch_tokens')} over={form.epoch_tokens != null}
          onChange={(v) => set({ epoch_tokens: v })} />
        <Num label="Only above (tokens)" hint="no pruning below this context size" value={val('floor_tokens')}
          over={form.floor_tokens != null} onChange={(v) => set({ floor_tokens: v })} />
        <Num label="Selector timeout (ms)" hint="on timeout everything is kept" value={form.selector_timeout_ms ?? eff.selector_timeout_ms}
          over={form.selector_timeout_ms != null} onChange={(v) => set({ selector_timeout_ms: v })} />
      </div>

      <h3>Tools and system prompt</h3>
      <div className="prune-cache-warning">
        <strong>These break the prompt cache.</strong> Tools and the system prompt come first in the cached prefix, so
        dropping any of them makes the provider re-write the entire prompt to the cache once (1.25× input on every token)
        instead of reading it (0.1×). Only worth it when the definitions are large and the session is long.
      </div>
      <div className="prune-flags">
        <label className="prune-flag">
          <input type="checkbox" checked={form.prune_tools ?? false} onChange={(e) => set({ prune_tools: e.target.checked })} />
          <span>
            <strong>Prune tool definitions</strong>
            <span className="muted small">Removes tools the economy model thinks the goal will not need. A tool that
              appears in any tool call of the history is never removed, nor is rlcd_recall. Decisions are sticky.</span>
          </span>
        </label>
        <label className="prune-flag">
          <input type="checkbox" checked={form.prune_system ?? false} onChange={(e) => set({ prune_system: e.target.checked })} />
          <span>
            <strong>Prune system prompt sections</strong>
            <span className="muted small">Only system blocks after the first one are considered.</span>
          </span>
        </label>
      </div>

      <h3>OpenAI formats <span className="muted small">Chat Completions and Responses</span></h3>
      <p className="muted small">
        The same rules apply: a <code>role: "tool"</code> message or a <code>function_call_output</code> item is a tool result,
        and only its content becomes a marker (its <code>tool_call_id</code> / <code>call_id</code> stays). Tool calls,
        reasoning items, images and tool definitions are never touched. OpenAI caches prompt prefixes on its own, with no
        write premium, so the estimates price an uncached token at plain input.
      </p>
      <div className="prune-flags">
        <label className="prune-flag">
          <input type="checkbox" checked={form.prune_conversation_text ?? false} onChange={(e) => set({ prune_conversation_text: e.target.checked })} />
          <span>
            <strong>Prune old conversation text</strong>
            <span className="muted small">A plain chatbot has no tool output. With this on, long user and assistant messages older
              than the last N turns become candidates too (user text still follows “Always keep user text”). Off by default:
              it changes what the model remembers of the conversation, and a chat app has no recall tool to get it back.</span>
          </span>
        </label>
      </div>

      <details>
        <summary>Prices <span className="muted">(estimates: Anthropic and OpenAI-compatible list prices as of {cfg.prices_as_of}, USD per million tokens)</span></summary>
        <table className="kv prune-prices">
          <thead>
            <tr><th>Model prefix</th><th className="num">Input</th><th className="num">Cache read</th><th className="num">Cache write</th><th className="num">Output</th></tr>
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
        <p className="muted small">
          Overrides by model prefix, as JSON, e.g. <code>{'{"claude-opus-5": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}}'}</code>.
          The longest matching prefix wins. With a subscription login the dollar figures are what the same traffic would cost on the API.
        </p>
        <textarea className="prune-criteria mono" rows={3} value={pricesText} placeholder="{}"
          onChange={(e) => { setPricesText(e.target.value); setMsg(null); }} />
      </details>

      <div className="actions prune-actions">
        <button className="primary" onClick={save} disabled={saving || !dirty}>{saving ? 'Saving…' : 'Save'}</button>
        <button onClick={load} disabled={saving || !dirty}>Discard changes</button>
        {msg && <span className={msg.ok ? 'muted' : 'bad'}>{msg.text}</span>}
      </div>

      <h3>Feedback cases <span className="muted small">{feedback.length}</span></h3>
      <p className="muted small">
        Added from the request view with “should have kept / dropped”, and from every block the model got back with
        <code> rlcd_recall</code> (marked “recall”: the model needed it, so it should have been kept; it is never dropped
        again in that conversation). Replay asks the economy model again with the current settings (saved ones, not unsaved
        edits): a regression suite for criteria changes.
      </p>
      <div className="actions">
        <button onClick={runReplay} disabled={replaying || feedback.length === 0 || dirty}
          title={dirty ? 'Save first: replay uses the saved settings' : undefined}>
          {replaying ? 'Replaying…' : 'Replay all cases'}
        </button>
        {replay && (
          <span className="muted">
            <span className="good">{replay.agree} agree</span> · <span className={replay.disagree ? 'bad' : ''}>{replay.disagree} disagree</span>
            {replay.errors > 0 && <> · <span className="bad">{replay.errors} errors</span></>}
            {' '}· {replay.fixed} fixed, {replay.broken} broken since the feedback · {fmt.ms(replay.selector_ms)}
          </span>
        )}
      </div>
      {feedback.length > 0 && (
        <div className="scroll-x">
          <table className="blocks prune-cases">
            <thead>
              <tr><th>Block</th><th>Verdict</th><th>Then</th><th>Now</th><th className="num">Score</th><th /></tr>
            </thead>
            <tbody>
              {feedback.map((f) => {
                const r = replayById.get(f.id);
                return (
                  <tr key={f.id}>
                    <td className="clip" title={`${f.request_id} · ${f.key}\n${f.preview}`}>
                      <span className="mono">{f.key}</span> <span className="muted">{f.what || f.kind}</span>
                      {f.source === 'recall' && <span className="tag">recall</span>}
                      {f.note && <div className="muted small">{f.note}</div>}
                    </td>
                    <td>{f.verdict === 'should_keep' ? 'should keep' : 'should drop'}</td>
                    <td><span className={`chip ${f.decision}`}>{f.decision}</span> <span className="muted small">{REASONS[f.reason] ?? f.reason}</span></td>
                    <td>
                      {r ? (
                        r.error ? <span className="bad small">{r.error}</span> : (
                          <>
                            <span className={`chip ${r.decision}`}>{r.decision}</span>{' '}
                            <span className={r.agrees ? 'good' : 'bad'}>{r.agrees ? '✓' : '✗'}</span>
                            {r.agrees !== r.then_agreed && <span className="tag">{r.agrees ? 'fixed' : 'broken'}</span>}
                          </>
                        )
                      ) : <span className="muted">—</span>}
                    </td>
                    <td className="num mono">{r?.score != null ? r.score.toFixed(3) : f.score != null ? f.score.toFixed(3) : '—'}</td>
                    <td>{f.source === 'recall'
                      ? <span className="muted small" title="From recall/events.jsonl">from recall</span>
                      : <button className="linkish" onClick={() => del(f.id)}>delete</button>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </main>
  );
}

function Stats({ stats }: { stats: PruneStats | null }) {
  if (!stats) return null;
  const e = stats.enforce, s = stats.shadow;
  const items: [string, string, string?][] = [
    ['Saved (enforced)', `~${fmt.n(e.saved_tokens)}`, `${usd(e.saved_usd)} est. · ${e.pruned}/${e.requests} requests pruned`],
    ['Would save (shadow)', `~${fmt.n(s.saved_tokens)}`, `${usd(s.saved_usd)} est. · ${s.pruned}/${s.requests} requests`],
    ['Epochs', fmt.n(stats.epochs), stats.epochs ? `selector avg ${fmt.ms(stats.avg_selector_ms)}` : undefined],
    ['Cache rewrites', fmt.n(e.cache_invalidations + s.cache_invalidations), 'turns that change the cached prefix'],
    ['Selector errors', fmt.n(stats.selector_errors), 'failed open: everything kept'],
  ];
  return (
    <>
      <section className="stats prune-stats">
        {items.map(([label, value, hint]) => (
          <div key={label} className="stat">
            <span className="label">{label}</span>
            <span className="value">{value}</span>
            {hint && <span className="hint">{hint}</span>}
          </div>
        ))}
      </section>
      <p className="muted small">
        Over the last {stats.window} requests. Token counts are estimates; dollar figures include the extra cache writes of
        epoch turns, so they can be negative early in a session.
      </p>
    </>
  );
}

function Override({ on, onReset }: { on: boolean; onReset: () => void }) {
  if (!on) return <span className="muted small">(preset)</span>;
  return <button className="linkish small" onClick={(e) => { e.preventDefault(); onReset(); }}>override · reset</button>;
}

function Num(props: { label: string; hint?: string; value: number; over: boolean; step?: number; onChange: (v: number | null) => void }) {
  const { label, hint, value, over, step, onChange } = props;
  return (
    <label>
      <span>{label} {over && <button className="linkish small" onClick={(e) => { e.preventDefault(); onChange(null); }}>reset</button>}</span>
      <input
        type="number"
        min={0}
        step={step ?? 1}
        value={value}
        onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))}
      />
      {hint && <span className="small">{hint}</span>}
    </label>
  );
}
