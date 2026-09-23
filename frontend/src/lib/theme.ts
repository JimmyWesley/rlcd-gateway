import { useSyncExternalStore } from 'react';
import { load, save } from './storage';

export type ThemePref = 'system' | 'light' | 'dark';
const KEY = 'rlcd.theme';

function readPref(): ThemePref {
  const v = load(KEY);
  return v === 'light' || v === 'dark' ? v : 'system';
}

/** Applies the preference as data-theme on <html>; "system" removes it and CSS follows the OS. */
function apply(pref: ThemePref) {
  const el = document.documentElement;
  if (pref === 'system') el.removeAttribute('data-theme');
  else el.setAttribute('data-theme', pref);
}

// One store for the whole app, applied before React renders so there is no
// flash of the wrong theme.
let pref: ThemePref = readPref();
apply(pref);
const listeners = new Set<() => void>();
const mq = typeof window !== 'undefined' ? window.matchMedia?.('(prefers-color-scheme: dark)') : undefined;
mq?.addEventListener('change', () => listeners.forEach((l) => l()));

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

function setPref(p: ThemePref) {
  pref = p;
  save(KEY, p === 'system' ? null : p);
  apply(p);
  listeners.forEach((l) => l());
}

const snapshot = () => `${pref}|${mq?.matches ? 'dark' : 'light'}`;

export function useTheme() {
  const snap = useSyncExternalStore(subscribe, snapshot);
  const [p, system] = snap.split('|') as [ThemePref, 'light' | 'dark'];
  return { pref: p, setPref, resolved: p === 'system' ? system : p };
}
