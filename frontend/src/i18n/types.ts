import type { en } from './en';

/** Plural forms, picked with Intl.PluralRules; "other" is the fallback. */
export type Plural = { zero?: string; one: string; two?: string; few?: string; many?: string; other: string };

export type Messages = typeof en;
export type MessageKey = keyof Messages;

/**
 * What every other locale must provide: exactly the English keys, a string
 * where English has a string and plural forms where English has them.
 */
export type Dict = { [K in MessageKey]: Messages[K] extends string ? string : Plural };
