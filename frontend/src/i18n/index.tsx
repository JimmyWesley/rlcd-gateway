// A tiny typed i18n layer: one dictionary per locale, t() with {param}
// interpolation and Intl plural rules, and Intl formatters bound to the
// locale. The English dictionary is the source of truth for keys and params;
// the other locales are typed against it, so a missing or extra key fails the
// build.
import { createContext, Fragment, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { load, save } from '../lib/storage';
import { en } from './en';
import type { Dict, MessageKey, Messages, Plural } from './types';

export type Locale = 'en' | 'pt-BR' | 'es';
export const LOCALES: { id: Locale; label: string }[] = [
  { id: 'en', label: 'English' },
  { id: 'pt-BR', label: 'Português (Brasil)' },
  { id: 'es', label: 'Español' },
];

// English ships in the main bundle; the others are loaded when picked.
const LOADERS: Record<Exclude<Locale, 'en'>, () => Promise<Dict>> = {
  'pt-BR': () => import('./pt-BR').then((m) => m.ptBR),
  es: () => import('./es').then((m) => m.es),
};
const DICTS: Partial<Record<Locale, Dict>> = { en };
const STORAGE_KEY = 'rlcd.locale';

/** Loads a locale's dictionary; resolves false (English stays) if it cannot. */
export async function ensureLocale(l: Locale): Promise<boolean> {
  if (DICTS[l]) return true;
  try {
    DICTS[l] = await LOADERS[l as Exclude<Locale, 'en'>]();
    return true;
  } catch {
    return false;
  }
}

// Param names used by a message: "{a} of {b}" -> "a" | "b".
type ExtractParams<S> = S extends `${string}{${infer P}}${infer R}` ? P | ExtractParams<R> : never;
type ParamsOf<M> = M extends string
  ? ExtractParams<M>
  : M extends { one: infer O; other: infer X }
    ? 'count' | ExtractParams<O> | ExtractParams<X>
    : never;
type Value = string | number;
type Args<K extends MessageKey> = [ParamsOf<Messages[K]>] extends [never]
  ? []
  : [params: Record<ParamsOf<Messages[K]>, Value>];
type NodeArgs<K extends MessageKey> = [ParamsOf<Messages[K]>] extends [never]
  ? []
  : [params: Record<ParamsOf<Messages[K]>, ReactNode>];

export function detectLocale(): Locale {
  const saved = load(STORAGE_KEY);
  if (saved && LOCALES.some((l) => l.id === saved)) return saved as Locale;
  const langs = typeof navigator !== 'undefined' ? navigator.languages ?? [navigator.language] : [];
  for (const l of langs) {
    const lower = l.toLowerCase();
    if (lower.startsWith('pt')) return 'pt-BR';
    if (lower.startsWith('es')) return 'es';
    if (lower.startsWith('en')) return 'en';
  }
  return 'en';
}

function pick(msg: string | Plural, count: unknown, rules: Intl.PluralRules): string {
  if (typeof msg === 'string') return msg;
  const n = typeof count === 'number' ? count : Number(count);
  if (n === 0 && msg.zero) return msg.zero;
  const cat = rules.select(Number.isFinite(n) ? n : 0) as keyof Plural;
  return msg[cat] ?? msg.other;
}

const PARAM = /\{(\w+)\}/g;

function makeFormatters(locale: Locale) {
  const nf = new Intl.NumberFormat(locale);
  const compact = new Intl.NumberFormat(locale, { notation: 'compact', maximumFractionDigits: 1 });
  const pct0 = new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 0 });
  const pct1 = new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 1 });
  const dec1 = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
  const dec3 = new Intl.NumberFormat(locale, { minimumFractionDigits: 3, maximumFractionDigits: 3 });
  const usdCache = new Map<number, Intl.NumberFormat>();
  const usdFmt = (digits: number) => {
    let f = usdCache.get(digits);
    if (!f) {
      f = new Intl.NumberFormat(locale, { style: 'currency', currency: 'USD', minimumFractionDigits: digits, maximumFractionDigits: digits });
      usdCache.set(digits, f);
    }
    return f;
  };
  // Log timestamps use a 24-hour clock in every locale: they line up in columns.
  const time = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
  const hm = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' });
  const hour = new Intl.DateTimeFormat(locale, { hour: 'numeric' });
  const day = new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric' });
  const dayTime = new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const rel = new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'short' });
  const dash = '—';
  const f = {
    /** Plain grouped number: 12,345 / 12.345 */
    num: (v: number | null | undefined) => (v == null || !Number.isFinite(v) ? dash : nf.format(v)),
    /** Compact number for big counts: 12.3K / 12,3 mil */
    compact: (v: number | null | undefined) =>
      v == null || !Number.isFinite(v) ? dash : Math.abs(v) < 10_000 ? nf.format(Math.round(v)) : compact.format(v),
    /** Token counts: compact, never fractional below 10k. */
    tokens: (v: number | null | undefined) => f.compact(v),
    dec: (v: number) => dec1.format(v),
    score: (v: number | null | undefined) => (v == null ? dash : dec3.format(v)),
    pct: (share: number | null | undefined, precise = false) =>
      share == null || !Number.isFinite(share) ? dash : (precise ? pct1 : pct0).format(share),
    /** US dollars (prices are USD list prices); small values keep enough digits to read. */
    usd: (v: number | null | undefined) => {
      if (v == null || !Number.isFinite(v)) return dash;
      const a = Math.abs(v);
      const digits = a === 0 ? 2 : a >= 10 ? 2 : a >= 0.1 ? 3 : a >= 0.001 ? 4 : 6;
      return usdFmt(digits).format(v);
    },
    ms: (v: number | null | undefined) => {
      if (v == null || !Number.isFinite(v)) return dash;
      return v >= 1000 ? `${dec1.format(v / 1000)} s` : `${nf.format(Math.round(v))} ms`;
    },
    /** Bytes in binary units (as the gateway's CLI prints them): 726 KB, 8.5 MB, 2 GB. */
    bytes: (v: number | null | undefined) => {
      if (v == null || !Number.isFinite(v)) return dash;
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      let x = Math.abs(v);
      let i = 0;
      while (x >= 1024 && i < units.length - 1) {
        x /= 1024;
        i++;
      }
      return `${v < 0 ? '−' : ''}${i === 0 || x >= 100 ? nf.format(Math.round(x)) : dec1.format(x)} ${units[i]}`;
    },
    /** A ratio like 12.0×. */
    ratio: (v: number | null | undefined) => (v == null || !Number.isFinite(v) ? dash : `${dec1.format(v)}×`),
    time: (iso: string | number | Date) => time.format(new Date(iso)),
    hm: (iso: string | number | Date) => hm.format(new Date(iso)),
    /** Axis label for an hourly tick: "2 PM" / "14". */
    hour: (iso: string | number | Date) => hour.format(new Date(iso)),
    /** Axis ticks in dollars: two decimals, three below ten cents. */
    usdAxis: (v: number) => usdFmt(v !== 0 && Math.abs(v) < 0.1 ? 3 : 2).format(v),
    day: (iso: string | number | Date) => day.format(new Date(iso)),
    dayTime: (iso: string | number | Date) => dayTime.format(new Date(iso)),
    /** "3 min ago", "yesterday"... */
    ago: (iso: string | number | Date) => {
      const s = (new Date(iso).getTime() - Date.now()) / 1000;
      const a = Math.abs(s);
      if (a < 45) return rel.format(Math.round(s), 'second');
      if (a < 2700) return rel.format(Math.round(s / 60), 'minute');
      if (a < 64800) return rel.format(Math.round(s / 3600), 'hour');
      return rel.format(Math.round(s / 86400), 'day');
    },
  };
  return f;
}

export type Formatters = ReturnType<typeof makeFormatters>;

type I18n = {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: <K extends MessageKey>(key: K, ...args: Args<K>) => string;
  /** Like t(), but params may be React nodes (links, code, bold). */
  tn: <K extends MessageKey>(key: K, ...args: NodeArgs<K>) => ReactNode;
  f: Formatters;
};

const Ctx = createContext<I18n | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  // main.tsx preloads the detected locale, so the first render is already translated.
  const [locale, setLocaleState] = useState<Locale>(() => {
    const l = detectLocale();
    return DICTS[l] ? l : 'en';
  });

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((l: Locale) => {
    save(STORAGE_KEY, l);
    ensureLocale(l).then((ok) => ok && setLocaleState(l));
  }, []);

  const value = useMemo<I18n>(() => {
    const dict = DICTS[locale] ?? en;
    const rules = new Intl.PluralRules(locale);
    const f = makeFormatters(locale);
    const fmtValue = (v: unknown) => (typeof v === 'number' ? f.num(v) : String(v));
    const raw = (key: MessageKey, params?: Record<string, unknown>) =>
      pick((dict[key] ?? en[key]) as string | Plural, params?.count, rules);
    const t = ((key: MessageKey, params?: Record<string, Value>) =>
      raw(key, params).replace(PARAM, (m, name: string) => (params && name in params ? fmtValue(params[name]) : m))) as I18n['t'];
    const tn = ((key: MessageKey, params?: Record<string, ReactNode>) => {
      const s = raw(key, params);
      if (!params) return s;
      const out: ReactNode[] = [];
      let last = 0;
      for (const m of s.matchAll(PARAM)) {
        out.push(s.slice(last, m.index));
        const v = params[m[1]];
        out.push(<Fragment key={out.length}>{typeof v === 'number' ? f.num(v) : v ?? m[0]}</Fragment>);
        last = (m.index ?? 0) + m[0].length;
      }
      out.push(s.slice(last));
      return <>{out}</>;
    }) as I18n['tn'];
    return { locale, setLocale, t, tn, f };
  }, [locale, setLocale]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useI18n(): I18n {
  const v = useContext(Ctx);
  if (!v) throw new Error('useI18n outside I18nProvider');
  return v;
}

export type { MessageKey };
/** Keys whose message takes no params. */
export type PlainKey = { [K in MessageKey]: [ParamsOf<Messages[K]>] extends [never] ? K : never }[MessageKey];
