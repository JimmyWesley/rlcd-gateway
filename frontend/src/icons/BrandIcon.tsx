// Provider, model-vendor and client logos, by slug. Logos are used
// nominatively, next to the name they identify (see LICENSES.md); anything
// unknown gets a neutral monogram rather than a guessed logo.
import { BRAND_SVGS, type BrandSvgId } from './brands.generated';
import { LogoMark } from './Icon';
import { PROVIDER_NAMES, type ProviderId, type ResolvedClient } from '../lib/brands';

type Props = {
  id: string | undefined;
  /** Name used for the monogram and the accessible label. */
  label?: string;
  size?: number;
  /** Decorative when the name is printed next to it (the default). */
  decorative?: boolean;
};

// Provider slugs whose logo is the model family's rather than the company's.
const ALIASES: Record<string, BrandSvgId> = { gemini: 'gemini' };

export function BrandIcon({ id, label, size = 16, decorative = true }: Props) {
  const a11y = decorative ? { 'aria-hidden': true as const } : { role: 'img' as const, 'aria-label': label ?? id ?? '' };
  const key = (id && (ALIASES[id] ?? id)) as BrandSvgId | undefined;
  const svg = key && key in BRAND_SVGS ? BRAND_SVGS[key] : undefined;
  if (svg) {
    return (
      <svg className="brand-icon" width={size} height={size} viewBox={svg.viewBox} fill="currentColor" fillRule="evenodd" {...a11y}
        dangerouslySetInnerHTML={{ __html: svg.body }} />
    );
  }
  if (id === 'rlcd') return <LogoMark size={size} title={decorative ? undefined : label} />;
  if (id === 'browser') {
    return (
      <svg className="brand-icon" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} {...a11y}>
        <circle cx="12" cy="12" r="9" />
        <path d="M3 12h18M12 3c2.5 2.6 3.8 5.6 3.8 9s-1.3 6.4-3.8 9c-2.5-2.6-3.8-5.6-3.8-9S9.5 5.6 12 3z" />
      </svg>
    );
  }
  return <Monogram text={label || id || '?'} size={size} a11y={a11y} />;
}

function hue(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
  return h;
}

function Monogram({ text, size, a11y }: { text: string; size: number; a11y: Record<string, unknown> }) {
  const words = text.replace(/[^\p{L}\p{N} ]/gu, ' ').trim().split(/\s+/).filter(Boolean);
  const letters = (words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? '?').slice(0, 2)).toUpperCase();
  return (
    <span className="monogram" style={{ width: size, height: size, fontSize: size * 0.42, ['--mono-h' as string]: hue(text) }} {...a11y}>
      {letters}
    </span>
  );
}

/** Logo plus name, for a provider slug. */
export function ProviderLabel({ id, size = 14 }: { id: ProviderId; size?: number }) {
  return (
    <span className="brand-label">
      <BrandIcon id={id} label={PROVIDER_NAMES[id]} size={size} />
      <span>{PROVIDER_NAMES[id]}</span>
    </span>
  );
}

/** A client's logo, with a small language badge for SDKs. */
export function ClientIcon({ client, size = 16 }: { client: ResolvedClient; size?: number }) {
  return (
    <span className="client-icon" style={{ width: size, height: size }}>
      <BrandIcon id={client.icon} label={client.name || '?'} size={size} />
      {client.lang && (
        <span className="client-lang">
          <BrandIcon id={client.lang} size={Math.round(size * 0.6)} />
        </span>
      )}
    </span>
  );
}
