// Turns a logged request body back into the conversation it is: messages
// made of parts, each part carrying the same key the gateway's X-ray and
// pruner use ("sys.0", "m3.b1", ...), so pruning decisions can be shown in
// place. Mirrors gateway/internal/ir (ir.go for Anthropic Messages, openai.go
// for Chat Completions and the Responses API).
import type { Block, Protocol } from './api';

export type Role = 'system' | 'user' | 'assistant' | 'tool';
export type PartKind = 'text' | 'thinking' | 'tool_use' | 'tool_result' | 'image' | 'other';

export type Part = {
  /** IR key: joins with X-ray blocks and pruning reports. */
  key: string;
  kind: PartKind;
  text: string;
  /** Tool name (tool_use) or the call behind a tool_result, when known. */
  name?: string;
  /** Tool arguments as text (tool_use). */
  args?: string;
  callId?: string;
  isError?: boolean;
  /** data: or http(s) URL of an image, when the body has one. */
  image?: string;
  /** Short label for parts we cannot render ("document", "web_search_call"). */
  label?: string;
  /** Rendered from the X-ray preview only: bodies were not logged. */
  previewOnly?: boolean;
};

export type Message = { msg: number; role: Role; parts: Part[] };

export type Conversation = {
  system: Part[];
  tools: string[];
  messages: Message[];
};

type J = Record<string, unknown>;
const obj = (v: unknown): J => (v && typeof v === 'object' && !Array.isArray(v) ? (v as J) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (v: unknown): string => (typeof v === 'string' ? v : v == null ? '' : JSON.stringify(v, null, 2));

function parseJSON(s: string | undefined): unknown {
  if (!s) return undefined;
  try {
    return JSON.parse(s);
  } catch {
    return undefined;
  }
}

/** Pretty-prints a JSON string of tool arguments, or returns it as is. */
export function prettyArgs(a: string): string {
  const v = parseJSON(a);
  return v === undefined ? a : JSON.stringify(v, null, 2);
}

function roleOf(r: unknown): Role {
  return r === 'assistant' ? 'assistant' : r === 'system' || r === 'developer' ? 'system' : r === 'tool' ? 'tool' : 'user';
}

// ---------- Anthropic Messages ----------

function anthropicBlock(key: string, b: J): Part {
  switch (b.type) {
    case 'text':
      return { key, kind: 'text', text: str(b.text) };
    case 'thinking':
      return { key, kind: 'thinking', text: str(b.thinking) };
    case 'redacted_thinking':
      return { key, kind: 'thinking', text: '', label: 'redacted_thinking' };
    case 'tool_use':
    case 'server_tool_use':
      return { key, kind: 'tool_use', text: '', name: str(b.name), args: JSON.stringify(b.input ?? {}, null, 2), callId: str(b.id) };
    case 'tool_result': {
      let text = '';
      let image: string | undefined;
      if (typeof b.content === 'string') text = b.content;
      for (const c of arr(b.content)) {
        const p = obj(c);
        if (p.type === 'text') text += (text ? '\n' : '') + str(p.text);
        if (p.type === 'image') image = imageSrc(p);
      }
      return { key, kind: 'tool_result', text, callId: str(b.tool_use_id), isError: b.is_error === true, image };
    }
    case 'image':
      return { key, kind: 'image', text: '', image: imageSrc(b) };
    default:
      return { key, kind: 'other', text: '', label: str(b.type) || 'block' };
  }
}

function imageSrc(b: J): string | undefined {
  const s = obj(b.source);
  if (s.type === 'base64' && typeof s.data === 'string') return `data:${str(s.media_type) || 'image/png'};base64,${s.data}`;
  if (s.type === 'url' && typeof s.url === 'string') return s.url;
  return undefined;
}

function parseAnthropic(body: J): Conversation {
  const c: Conversation = { system: [], tools: [], messages: [] };
  if (typeof body.system === 'string') c.system.push({ key: 'sys.0', kind: 'text', text: body.system });
  arr(body.system).forEach((p, i) => c.system.push({ key: `sys.${i}`, kind: 'text', text: str(obj(p).text) }));
  c.tools = arr(body.tools).map((t) => str(obj(t).name));
  arr(body.messages).forEach((m, mi) => {
    const mo = obj(m);
    const role = roleOf(mo.role);
    if (typeof mo.content === 'string') {
      c.messages.push({ msg: mi, role, parts: [{ key: `m${mi}.b0`, kind: 'text', text: mo.content }] });
      return;
    }
    c.messages.push({ msg: mi, role, parts: arr(mo.content).map((b, bi) => anthropicBlock(`m${mi}.b${bi}`, obj(b))) });
  });
  return c;
}

// ---------- OpenAI (shared) ----------

function openaiPart(key: string, p: J): Part {
  switch (p.type) {
    case 'text':
    case 'input_text':
    case 'output_text':
    case 'summary_text':
      return { key, kind: 'text', text: str(p.text) };
    case 'refusal':
      return { key, kind: 'text', text: str(p.refusal) };
    case 'image_url':
      return { key, kind: 'image', text: '', image: str(obj(p.image_url).url) || undefined };
    case 'input_image':
      return { key, kind: 'image', text: '', image: typeof p.image_url === 'string' ? p.image_url : undefined };
    default:
      return { key, kind: 'other', text: '', label: str(p.type) || 'part' };
  }
}

/** A message's content: a string (one part) or a list of parts. */
function openaiContent(mi: number, content: unknown): Part[] {
  if (typeof content === 'string') return [{ key: `m${mi}.b0`, kind: 'text', text: content }];
  return arr(content).map((p, bi) => openaiPart(`m${mi}.b${bi}`, obj(p)));
}

function outputText(v: unknown): string {
  if (typeof v === 'string') return v;
  return arr(v).map((p) => str(obj(p).text ?? obj(p).output_text ?? '')).filter(Boolean).join('\n') || (v == null ? '' : str(v));
}

function parseChat(body: J): Conversation {
  const c: Conversation = { system: [], tools: [], messages: [] };
  c.tools = arr(body.tools).map((t) => str(obj(obj(t).function).name) || str(obj(t).name));
  arr(body.messages).forEach((m, mi) => {
    const mo = obj(m);
    const role = roleOf(mo.role);
    if (mo.role === 'tool') {
      c.messages.push({ msg: mi, role: 'tool', parts: [{ key: `m${mi}.b0`, kind: 'tool_result', text: outputText(mo.content), callId: str(mo.tool_call_id) }] });
      return;
    }
    const parts = mo.content == null ? [] : openaiContent(mi, mo.content);
    let next = parts.length;
    for (const tc of arr(mo.tool_calls)) {
      const t = obj(tc);
      const fn = obj(t.function);
      parts.push({ key: `m${mi}.b${next++}`, kind: 'tool_use', text: '', name: str(fn.name), args: prettyArgs(str(fn.arguments)), callId: str(t.id) });
    }
    if (role === 'system') c.system.push(...parts);
    else c.messages.push({ msg: mi, role, parts });
  });
  return c;
}

function parseResponses(body: J): Conversation {
  const c: Conversation = { system: [], tools: [], messages: [] };
  if (typeof body.instructions === 'string' && body.instructions) c.system.push({ key: 'sys.0', kind: 'text', text: body.instructions });
  c.tools = arr(body.tools).map((t) => str(obj(t).name) || str(obj(t).type));
  if (typeof body.input === 'string') {
    c.messages.push({ msg: 0, role: 'user', parts: [{ key: 'm0.b0', kind: 'text', text: body.input }] });
    return c;
  }
  arr(body.input).forEach((raw, mi) => {
    const it = obj(raw);
    const key = `m${mi}.b0`;
    switch (it.type ?? 'message') {
      case 'message': {
        const role = roleOf(it.role);
        const parts = openaiContent(mi, it.content);
        if (role === 'system') c.system.push(...parts);
        else c.messages.push({ msg: mi, role, parts });
        break;
      }
      case 'function_call':
      case 'custom_tool_call':
      case 'local_shell_call': {
        const args = it.type === 'custom_tool_call' ? str(it.input) : it.type === 'local_shell_call' ? str(it.action) : str(it.arguments);
        const name = it.type === 'local_shell_call' ? 'local_shell' : str(it.name);
        c.messages.push({ msg: mi, role: 'assistant', parts: [{ key, kind: 'tool_use', text: '', name, args: prettyArgs(args), callId: str(it.call_id) }] });
        break;
      }
      case 'function_call_output':
      case 'custom_tool_call_output':
      case 'local_shell_call_output':
        c.messages.push({ msg: mi, role: 'tool', parts: [{ key, kind: 'tool_result', text: outputText(it.output), callId: str(it.call_id), isError: it.status === 'failed' || it.status === 'incomplete' }] });
        break;
      case 'reasoning': {
        const text = arr(it.summary).map((s) => str(obj(s).text)).join('\n');
        c.messages.push({ msg: mi, role: 'assistant', parts: [{ key, kind: 'thinking', text, label: !text && it.encrypted_content ? 'encrypted' : undefined }] });
        break;
      }
      default:
        c.messages.push({ msg: mi, role: roleOf(it.role), parts: [{ key, kind: 'other', text: '', label: str(it.type) }] });
    }
  });
  return c;
}

/** The conversation of a request, from its logged body. */
export function parseConversation(protocol: Protocol, body: string | undefined): Conversation | null {
  const v = parseJSON(body);
  if (!v || typeof v !== 'object') return null;
  const b = v as J;
  const c = protocol === 'openai-chat' ? parseChat(b) : protocol === 'openai-responses' ? parseResponses(b) : parseAnthropic(b);
  linkToolNames(c);
  return c;
}

/** Without bodies, rebuild what we can from the X-ray previews. */
export function conversationFromXray(blocks: Block[]): Conversation {
  const c: Conversation = { system: [], tools: [], messages: [] };
  const byMsg = new Map<number, Message>();
  for (const b of blocks) {
    if (b.kind === 'tool') {
      c.tools.push(b.name ?? b.key);
      continue;
    }
    const kind: PartKind = b.kind === 'system' ? 'text' : (['text', 'thinking', 'tool_use', 'tool_result', 'image'].includes(b.kind) ? b.kind : 'other') as PartKind;
    const part: Part = {
      key: b.key, kind, text: b.kind === 'tool_use' ? '' : b.preview, args: b.kind === 'tool_use' ? b.preview : undefined,
      name: b.name, callId: b.tool_use_id, isError: b.is_error, previewOnly: true,
    };
    if (b.kind === 'system' || b.msg < 0) {
      c.system.push(part);
      continue;
    }
    let m = byMsg.get(b.msg);
    if (!m) {
      m = { msg: b.msg, role: roleOf(b.role), parts: [] };
      byMsg.set(b.msg, m);
      c.messages.push(m);
    }
    m.parts.push(part);
  }
  linkToolNames(c);
  return c;
}

/** Tool results get the name of the call they answer. */
function linkToolNames(c: Conversation) {
  const names = new Map<string, string>();
  for (const m of c.messages) for (const p of m.parts) if (p.kind === 'tool_use' && p.callId) names.set(p.callId, p.name ?? '');
  for (const m of c.messages) for (const p of m.parts) if (p.kind === 'tool_result' && p.callId && !p.name) p.name = names.get(p.callId);
}

// ---------- The response ----------

/** SSE "data:" payloads of a stream, parsed; empty when the body is not SSE. */
function sseEvents(body: string): J[] {
  if (!/^\s*(event|data):/m.test(body)) return [];
  const out: J[] = [];
  for (const line of body.split('\n')) {
    const m = /^data:\s?(.*)$/.exec(line.trimEnd());
    if (!m || m[1] === '[DONE]') continue;
    const v = parseJSON(m[1]);
    if (v && typeof v === 'object') out.push(v as J);
  }
  return out;
}

/** The assistant's reply to this request, as parts (keys "r.<i>"). */
export function parseResponse(protocol: Protocol, body: string | undefined): Part[] {
  if (!body) return [];
  const events = sseEvents(body);
  const whole = events.length ? undefined : obj(parseJSON(body.trim()));
  if (protocol === 'anthropic-messages') {
    if (whole) return arr(whole.content).map((b, i) => anthropicBlock(`r.${i}`, obj(b)));
    const parts: Part[] = [];
    const json: string[] = [];
    for (const e of events) {
      const i = Number(e.index ?? 0);
      if (e.type === 'content_block_start') {
        parts[i] = anthropicBlock(`r.${i}`, obj(e.content_block));
        json[i] = '';
      } else if (e.type === 'content_block_delta' && parts[i]) {
        const d = obj(e.delta);
        if (d.type === 'text_delta') parts[i].text += str(d.text);
        if (d.type === 'thinking_delta') parts[i].text += str(d.thinking);
        if (d.type === 'input_json_delta') json[i] += str(d.partial_json);
      }
    }
    parts.forEach((p, i) => {
      if (p?.kind === 'tool_use' && json[i]) p.args = prettyArgs(json[i]);
    });
    return parts.filter(Boolean);
  }
  if (protocol === 'openai-chat') {
    if (whole) {
      const msg = obj(obj(arr(whole.choices)[0]).message);
      const parts: Part[] = [];
      if (msg.reasoning) parts.push({ key: 'r.think', kind: 'thinking', text: str(msg.reasoning) });
      if (msg.content) parts.push({ key: 'r.0', kind: 'text', text: str(msg.content) });
      arr(msg.tool_calls).forEach((tc, i) => {
        const fn = obj(obj(tc).function);
        parts.push({ key: `r.t${i}`, kind: 'tool_use', text: '', name: str(fn.name), args: prettyArgs(str(fn.arguments)), callId: str(obj(tc).id) });
      });
      return parts;
    }
    let text = '';
    let reasoning = '';
    const calls: { id: string; name: string; args: string }[] = [];
    for (const e of events) {
      const d = obj(obj(arr(e.choices)[0]).delta);
      text += typeof d.content === 'string' ? d.content : '';
      reasoning += typeof d.reasoning === 'string' ? d.reasoning : '';
      for (const tc of arr(d.tool_calls)) {
        const t = obj(tc);
        const i = Number(t.index ?? 0);
        calls[i] ??= { id: '', name: '', args: '' };
        const fn = obj(t.function);
        if (t.id) calls[i].id = str(t.id);
        if (fn.name) calls[i].name += str(fn.name);
        if (fn.arguments) calls[i].args += str(fn.arguments);
      }
    }
    const parts: Part[] = [];
    if (reasoning) parts.push({ key: 'r.think', kind: 'thinking', text: reasoning });
    if (text) parts.push({ key: 'r.0', kind: 'text', text });
    calls.forEach((c, i) => c && parts.push({ key: `r.t${i}`, kind: 'tool_use', text: '', name: c.name, args: prettyArgs(c.args), callId: c.id }));
    return parts;
  }
  // Responses API: the final response object carries the output items.
  let output: unknown[] = whole ? arr(whole.output) : [];
  let deltas = '';
  for (const e of events) {
    if (e.type === 'response.completed' || e.type === 'response.done') output = arr(obj(e.response).output);
    if (e.type === 'response.output_text.delta') deltas += str(e.delta);
  }
  if (!output.length) return deltas ? [{ key: 'r.0', kind: 'text', text: deltas }] : [];
  const conv = parseResponses({ input: output });
  return conv.messages.flatMap((m) => m.parts.map((p, i) => ({ ...p, key: `r.${m.msg}.${i}` })));
}

// ---------- Errors ----------

/**
 * The most specific error message in a provider's reply. Gateways nest the
 * upstream error as a JSON string inside another error (OpenRouter puts it
 * in metadata.raw, some backends again in details), so every string that
 * parses as JSON is searched too, and the deepest message wins.
 */
export function providerError(body: string | undefined, fallback?: string): { message: string; provider?: string; type?: string } | null {
  let best: { message: string; depth: number; type?: string } | null = null;
  let provider: string | undefined;
  const walk = (v: unknown, depth: number) => {
    if (typeof v === 'string') {
      const t = v.trim();
      if (t.startsWith('{') || t.startsWith('[')) {
        const p = parseJSON(t);
        if (p !== undefined) walk(p, depth + 1);
      }
      return;
    }
    if (Array.isArray(v)) {
      v.forEach((x) => walk(x, depth));
      return;
    }
    if (!v || typeof v !== 'object') return;
    const o = v as J;
    if (typeof o.provider_name === 'string') provider = o.provider_name;
    if (typeof o.message === 'string' && (!best || depth >= best.depth)) best = { message: o.message, depth, type: typeof o.type === 'string' ? o.type : undefined };
    for (const k of Object.keys(o)) if (k !== 'message') walk(o[k], depth + 1);
  };
  const events = body ? sseEvents(body) : [];
  if (events.length) events.forEach((e) => walk(e, 0));
  else if (body) walk(body, 0);
  const b = best as { message: string; depth: number; type?: string } | null;
  if (!b && !fallback) return null;
  return { message: b?.message ?? fallback ?? '', provider, type: b?.type };
}

// ---------- Recall ----------

const MARKER = /\[rlcd:[^\]]*?key\s+([^\s·\]]+)[^\]]*?req\s+([^\s·\]]+)/;

/** The key and request a rlcd_recall call asked for, from its arguments. */
export function recallTarget(args: string | undefined): { key: string; req?: string } | null {
  const v = obj(parseJSON(args ?? ''));
  if (typeof v.key === 'string' && v.key) return { key: v.key, req: typeof v.req === 'string' ? v.req : undefined };
  const marker = typeof v.marker === 'string' ? v.marker : args ?? '';
  const m = MARKER.exec(marker);
  return m ? { key: m[1].replace(/…$/, ''), req: m[2] } : null;
}

export const isRecallTool = (name: string | undefined) => !!name && /(^|__)rlcd_recall$/.test(name);
