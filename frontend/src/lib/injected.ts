// Context a harness injects into the user's turn: Claude Code's
// <system-reminder> blocks, slash-command tags and command output. They sit
// inside user text parts but are not what the person typed, so the chat view
// shows them as collapsed chips. Presentation only: keys, tokens and pruning
// decisions are never touched.

const TAGS = [
  'system-reminder', 'command-name', 'command-message', 'command-args', 'command-contents',
  'local-command-stdout', 'local-command-stderr', 'local-command-caveat',
  'user-prompt-submit-hook', 'bash-input', 'bash-stdout', 'bash-stderr', 'user-memory-input',
] as const;
export type InjectedTag = (typeof TAGS)[number];

/** Known kinds of injected context, each with a translated title. */
export type InjectedKind = 'environment' | 'model' | 'agents' | 'skills' | 'date' | 'context' | 'attribution' | 'files' | 'todos' | 'command' | 'output' | 'hook' | 'other';

export type Segment =
  | { type: 'human'; text: string }
  | { type: 'injected'; tag: string; kind: InjectedKind; title: string; body: string; tokens: number };

const BLOCK = new RegExp(`<(${TAGS.join('|')})>([\\s\\S]*?)(?:<\\/\\1>|$)`, 'g');

function classify(tag: string, body: string): { kind: InjectedKind; title: string } {
  const lines = body.split('\n').map((l) => l.trim()).filter(Boolean);
  const first = lines[0] ?? '';
  const heading = /^#+\s*(.+)$/.exec(first)?.[1];
  const title = (heading ?? first).replace(/[:.]$/, '').slice(0, 60);
  if (tag.startsWith('command-')) return { kind: 'command', title: first.slice(0, 60) };
  if (tag.startsWith('local-command') || tag.startsWith('bash-')) return { kind: 'output', title: first.slice(0, 60) };
  if (tag === 'user-prompt-submit-hook') return { kind: 'hook', title };
  const b = body.slice(0, 400).toLowerCase();
  if (heading && /environment/i.test(heading)) return { kind: 'environment', title };
  if (/invoked in the following environment|working directory:/.test(b)) return { kind: 'environment', title };
  if (/you are powered by the model|model id is/.test(b)) return { kind: 'model', title };
  if (/agent types|available agents/.test(b)) return { kind: 'agents', title };
  if (/skills are available|skill tool/.test(b)) return { kind: 'skills', title };
  if (/today's date|current date/.test(b)) return { kind: 'date', title };
  if (/attribution|co-authored-by/.test(b)) return { kind: 'attribution', title };
  if (/todo list|todowrite/.test(b)) return { kind: 'todos', title };
  if (/opened the file|selected the following|file was modified|the user opened/.test(b)) return { kind: 'files', title };
  if (/following context|context:/.test(b)) return { kind: 'context', title };
  return { kind: 'other', title };
}

/** Splits a text part into what the person typed and what was injected. */
export function splitInjected(text: string): Segment[] {
  if (!text.includes('<')) return [{ type: 'human', text }];
  const out: Segment[] = [];
  let last = 0;
  for (const m of text.matchAll(BLOCK)) {
    const before = text.slice(last, m.index);
    if (before.trim()) out.push({ type: 'human', text: before.trim() });
    const body = m[2].replace(/^\n+|\n+$/g, '');
    out.push({ type: 'injected', tag: m[1], ...classify(m[1], body), body, tokens: Math.max(1, Math.round(m[0].length / 4)) });
    last = (m.index ?? 0) + m[0].length;
  }
  const rest = text.slice(last);
  if (rest.trim()) out.push({ type: 'human', text: rest.trim() });
  return out.length ? out : [{ type: 'human', text }];
}

/** For a (possibly truncated) preview that starts with injected context: its kind and title. */
export function injectedPreview(preview: string | undefined): { kind: InjectedKind; title: string; tag: string } | null {
  const p = (preview ?? '').trimStart();
  const m = /^<([a-z-]+)>/.exec(p);
  if (!m || !(TAGS as readonly string[]).includes(m[1])) return null;
  const seg = splitInjected(p)[0];
  return seg.type === 'injected' ? { kind: seg.kind, title: seg.title, tag: seg.tag } : null;
}
