import { useState } from 'react';
import { describeMatch, type HeaderCond, type Match, type Rule, type RouterRoute, type RulesDoc } from './types';

type Props = {
  doc: RulesDoc;
  routes: RouterRoute[];
  dirty: boolean;
  saving: boolean;
  onChange: (d: RulesDoc) => void;
  onSave: () => void;
  onRevert: () => void;
};

export function RulesEditor({ doc, routes, dirty, saving, onChange, onSave, onRevert }: Props) {
  const [open, setOpen] = useState<number | null>(null);
  const rules = doc.rules;
  const setRules = (rs: Rule[]) => onChange({ ...doc, rules: rs });
  const update = (i: number, r: Rule) => setRules(rules.map((x, j) => (j === i ? r : x)));
  const move = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= rules.length) return;
    const next = [...rules];
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
    setOpen((o) => (o === i ? j : o === j ? i : o));
  };
  const remove = (i: number) => {
    setRules(rules.filter((_, j) => j !== i));
    setOpen(null);
  };
  const add = (kind: 'match' | 'auto') => {
    const name = uniqueName(rules, kind === 'auto' ? 'auto' : 'rule');
    const r: Rule =
      kind === 'auto'
        ? { name, enabled: true, kind: 'auto', timeout_ms: 1500, when: {} }
        : { name, enabled: true, kind: 'match', route: routes[0]?.name ?? '', when: {} };
    setRules([...rules, r]);
    setOpen(rules.length);
  };
  const addBackground = () => {
    const cheap = routes.find((r) => !r.active)?.name ?? routes[0]?.name ?? '';
    setRules([{ name: uniqueName(rules, 'background'), enabled: true, kind: 'match', route: cheap, when: { background: true } }, ...rules]);
    setOpen(0);
  };

  return (
    <section className="panel rt-section">
      <div className="rt-head">
        <h3>Rules</h3>
        <span className="muted small">Top to bottom, the first enabled rule that matches picks the route. No match: the active route.</span>
        <span className="spacer" />
        <button onClick={() => add('match')}>+ Rule</button>
        <button onClick={() => add('auto')} title="Let the economy model pick among routes that have a description">+ Auto rule</button>
        {!rules.some((r) => r.when.background) && <button onClick={addBackground}>+ Background rule</button>}
      </div>

      {rules.length === 0 && (
        <p className="muted">No rules: every request goes to the active route picked in the top bar, exactly as before.</p>
      )}

      <ol className="rt-rules">
        {rules.map((r, i) => (
          <li key={i} className={`rt-rule ${r.enabled ? '' : 'off'} ${open === i ? 'open' : ''}`}>
            <div className="rt-rule-row">
              <span className="rt-idx mono">{i + 1}</span>
              <label className="rt-switch" title={r.enabled ? 'Enabled' : 'Disabled'}>
                <input type="checkbox" checked={r.enabled} onChange={(e) => update(i, { ...r, enabled: e.target.checked })} />
              </label>
              <button className="rt-rule-name" onClick={() => setOpen(open === i ? null : i)}>
                <strong>{r.name}</strong>
                <span className="muted small">
                  {describeMatch(r.when)}
                  {r.override_sticky && ' · overrides sticky'}
                </span>
              </button>
              <span className="rt-arrow">→</span>
              <span className={`rt-target ${r.kind === 'auto' ? 'auto' : ''}`}>
                {r.kind === 'auto' ? `auto: ${(r.candidates?.length ? r.candidates : ['described routes']).join(' | ')}` : r.route || '—'}
              </span>
              <div className="rt-rule-actions">
                <button onClick={() => move(i, -1)} disabled={i === 0} aria-label="Move up">↑</button>
                <button onClick={() => move(i, 1)} disabled={i === rules.length - 1} aria-label="Move down">↓</button>
                <button onClick={() => remove(i)} aria-label="Delete rule">✕</button>
              </div>
            </div>
            {open === i && <RuleForm rule={r} routes={routes} onChange={(x) => update(i, x)} />}
          </li>
        ))}
      </ol>

      <div className="actions">
        <button className="primary" onClick={onSave} disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save rules'}</button>
        <button onClick={onRevert} disabled={!dirty || saving}>Revert</button>
        {dirty && <span className="note">Unsaved changes</span>}
      </div>
    </section>
  );
}

function uniqueName(rules: Rule[], base: string): string {
  const names = new Set(rules.map((r) => r.name));
  if (!names.has(base)) return base;
  let n = 2;
  while (names.has(`${base}-${n}`)) n++;
  return `${base}-${n}`;
}

type Tri = 'any' | 'yes' | 'no';
const toTri = (v: boolean | undefined): Tri => (v === undefined ? 'any' : v ? 'yes' : 'no');
const fromTri = (t: Tri): boolean | undefined => (t === 'any' ? undefined : t === 'yes');
const num = (s: string): number | undefined => {
  const n = parseInt(s, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

function RuleForm({ rule, routes, onChange }: { rule: Rule; routes: RouterRoute[]; onChange: (r: Rule) => void }) {
  const m = rule.when;
  const setWhen = (patch: Partial<Match>) => {
    const next: Match = { ...m, ...patch };
    // Drop unset keys so the stored JSON stays readable.
    for (const k of Object.keys(next) as (keyof Match)[]) {
      const v = next[k];
      if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) delete next[k];
    }
    onChange({ ...rule, when: next });
  };
  const headers = m.headers ?? [];
  const setHeader = (i: number, h: HeaderCond) => setWhen({ headers: headers.map((x, j) => (j === i ? h : x)) });
  const auto = rule.kind === 'auto';
  const described = routes.filter((r) => r.description);

  const tri = (label: string, key: 'has_tools' | 'has_images' | 'has_thinking' | 'background', hint?: string) => (
    <label title={hint}>
      {label}
      <select value={toTri(m[key])} onChange={(e) => setWhen({ [key]: fromTri(e.target.value as Tri) })}>
        <option value="any">any</option>
        <option value="yes">yes</option>
        <option value="no">no</option>
      </select>
    </label>
  );

  return (
    <div className="rt-rule-form">
      <div className="form rt-grid">
        <label>
          Name
          <input value={rule.name} onChange={(e) => onChange({ ...rule, name: e.target.value })} />
        </label>
        {auto ? (
          <label>
            Timeout (ms)
            <input
              type="number"
              min={100}
              max={10000}
              value={rule.timeout_ms ?? ''}
              placeholder="1500"
              onChange={(e) => onChange({ ...rule, timeout_ms: num(e.target.value) })}
            />
          </label>
        ) : (
          <label>
            Route
            <select value={rule.route ?? ''} onChange={(e) => onChange({ ...rule, route: e.target.value })}>
              {!routes.some((r) => r.name === rule.route) && <option value={rule.route ?? ''}>{rule.route || 'pick a route'}</option>}
              {routes.map((r) => (
                <option key={r.name} value={r.name}>{r.name}{r.model ? ` · ${r.model}` : ''}</option>
              ))}
            </select>
          </label>
        )}
      </div>

      {auto && (
        <div className="rt-auto">
          <p className="muted small">
            At the start of a conversation the economy model reads the first message and picks one of these routes by its
            description. It only chooses a key; it never writes text. On a timeout or an unusable answer the next rule
            decides. With stickiness on, the choice then holds for the whole conversation.
          </p>
          <div className="rt-chips">
            {described.length < 2 && <span className="note">Give at least two routes a description (Routes, below).</span>}
            {described.map((r) => {
              const on = !rule.candidates?.length || rule.candidates.includes(r.name);
              return (
                <label key={r.name} className={`rt-chip ${on ? 'on' : ''}`} title={r.description}>
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={(e) => {
                      const cur = rule.candidates?.length ? rule.candidates : described.map((d) => d.name);
                      const next = e.target.checked ? [...cur, r.name] : cur.filter((c) => c !== r.name);
                      onChange({ ...rule, candidates: next.length === described.length ? undefined : next });
                    }}
                  />
                  {r.name}
                </label>
              );
            })}
          </div>
          <label className="rt-inline">
            Minimum confidence
            <input
              type="number"
              step={0.05}
              min={0}
              max={1}
              value={rule.min_confidence ?? 0}
              onChange={(e) => onChange({ ...rule, min_confidence: parseFloat(e.target.value) || undefined })}
            />
          </label>
        </div>
      )}

      <h4>Conditions <span className="muted small">all that are set must hold</span></h4>
      <div className="form rt-grid">
        <label>
          Protocol
          <select
            value={m.protocol ?? ''}
            onChange={(e) => setWhen({ protocol: (e.target.value || undefined) as Match['protocol'] })}
          >
            <option value="">any</option>
            <option value="anthropic-messages">Anthropic Messages</option>
            <option value="openai">OpenAI (Chat or Responses)</option>
            <option value="openai-chat">OpenAI Chat Completions</option>
            <option value="openai-responses">OpenAI Responses</option>
          </select>
        </label>
        <label>
          Client model (regex)
          <input value={m.model ?? ''} placeholder="e.g. haiku|sonnet" onChange={(e) => setWhen({ model: e.target.value })} />
        </label>
        <label>
          max_tokens ≤
          <input
            type="number"
            min={0}
            value={m.max_tokens_lte ?? ''}
            placeholder="any"
            onChange={(e) => setWhen({ max_tokens_lte: num(e.target.value) })}
          />
        </label>
        <label>
          Context ≥ (est. tokens)
          <input
            type="number"
            min={0}
            value={m.min_context_tokens ?? ''}
            placeholder="any"
            onChange={(e) => setWhen({ min_context_tokens: num(e.target.value) })}
          />
        </label>
        <label>
          Context ≤ (est. tokens)
          <input
            type="number"
            min={0}
            value={m.max_context_tokens ?? ''}
            placeholder="any"
            onChange={(e) => setWhen({ max_context_tokens: num(e.target.value) })}
          />
        </label>
        {tri('Has tools', 'has_tools')}
        {tri('Has images', 'has_images')}
        {tri('Has thinking', 'has_thinking', 'Thinking enabled on the request, or thinking blocks in the history')}
        {tri(
          'Background request',
          'background',
          'Side requests: quota probes (max_tokens 1) and one-shot queries with no tools, no thinking, one message and no prompt caching (titles, summaries)',
        )}
        <label className="rt-wide">
          Conversation id
          <input
            className="mono"
            value={m.conversation ?? ''}
            placeholder="cc-… (exact)"
            onChange={(e) => setWhen({ conversation: e.target.value.trim() })}
          />
        </label>
      </div>

      <div className="rt-headers">
        <span className="label">Headers</span>
        {headers.map((h, i) => {
          const op = h.equals !== undefined ? 'equals' : h.contains !== undefined ? 'contains' : 'present';
          return (
            <div key={i} className="rt-header-row">
              <input value={h.name} placeholder="header name" onChange={(e) => setHeader(i, { ...h, name: e.target.value })} />
              <select
                value={op}
                onChange={(e) => {
                  const v = h.equals ?? h.contains ?? '';
                  setHeader(i, e.target.value === 'equals' ? { name: h.name, equals: v } : e.target.value === 'contains' ? { name: h.name, contains: v } : { name: h.name });
                }}
              >
                <option value="present">is present</option>
                <option value="equals">equals</option>
                <option value="contains">contains</option>
              </select>
              {op !== 'present' && (
                <input
                  value={(op === 'equals' ? h.equals : h.contains) ?? ''}
                  onChange={(e) => setHeader(i, op === 'equals' ? { name: h.name, equals: e.target.value } : { name: h.name, contains: e.target.value })}
                />
              )}
              <button onClick={() => setWhen({ headers: headers.filter((_, j) => j !== i) })} aria-label="Remove header">✕</button>
            </div>
          );
        })}
        <button className="rt-link" onClick={() => setWhen({ headers: [...headers, { name: '' }] })}>+ header condition</button>
      </div>

      {!auto && (
        <label className="rt-check">
          <input
            type="checkbox"
            checked={!!rule.override_sticky}
            onChange={(e) => onChange({ ...rule, override_sticky: e.target.checked || undefined })}
          />
          <span>
            Override stickiness <span className="muted small">— may move an already pinned conversation (e.g. the context outgrew the
            pinned model). Costs that conversation its prompt cache and signed thinking.</span>
          </span>
        </label>
      )}
    </div>
  );
}
