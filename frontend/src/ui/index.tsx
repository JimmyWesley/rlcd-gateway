// Design-system primitives. Styling lives in styles/components.css; these
// only own structure, accessibility and small behaviors.
import {
  useCallback, useEffect, useId, useRef, useState,
  type ButtonHTMLAttributes, type ReactNode,
} from 'react';
import { useI18n } from '../i18n';
import { Icon, type IconName } from '../icons/Icon';
import { BrandIcon } from '../icons/BrandIcon';
import { modelVendor, PROVIDER_NAMES, routeProvider, type ProviderId } from '../lib/brands';
import type { RouteView } from '../lib/api';

export const cx = (...xs: (string | false | null | undefined)[]) => xs.filter(Boolean).join(' ');

type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  size?: 'sm' | 'md';
  icon?: IconName;
  loading?: boolean;
};

export function Button({ variant = 'secondary', size = 'md', icon, loading, className, children, disabled, ...rest }: BtnProps) {
  return (
    <button
      type="button"
      className={cx('btn', `btn-${variant}`, size === 'sm' && 'btn-sm', className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {loading ? <span className="spinner" aria-hidden /> : icon && <Icon name={icon} size={size === 'sm' ? 14 : 16} />}
      {children}
    </button>
  );
}

export function IconButton({ icon, label, className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return (
    <button type="button" className={cx('icon-btn', className)} aria-label={label} title={label} {...rest}>
      <Icon name={icon} size={16} />
    </button>
  );
}

export function Card({ title, subtitle, actions, children, className, id, flush }: {
  title?: ReactNode; subtitle?: ReactNode; actions?: ReactNode; children?: ReactNode; className?: string; id?: string; flush?: boolean;
}) {
  return (
    <section className={cx('card', flush && 'card-flush', className)} id={id}>
      {(title || actions) && (
        <header className="card-head">
          <div className="card-titles">
            {title && <h3 className="card-title">{title}</h3>}
            {subtitle && <p className="card-sub">{subtitle}</p>}
          </div>
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export type Tone = 'neutral' | 'accent' | 'good' | 'warn' | 'bad' | 'info' | 'kept' | 'dropped' | 'shadow' | 'pending';

export function Badge({ tone = 'neutral', icon, children, title, className }: {
  tone?: Tone; icon?: IconName; children: ReactNode; title?: string; className?: string;
}) {
  return (
    <span className={cx('badge', `badge-${tone}`, className)} title={title}>
      {icon && <Icon name={icon} size={12} />}
      {children}
    </span>
  );
}

export function Dot({ tone = 'neutral', pulse, label }: { tone?: Tone; pulse?: boolean; label?: string }) {
  return <span className={cx('dot', `dot-${tone}`, pulse && 'dot-pulse')} role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true} />;
}

export function Stat({ label, value, sub, tone, trend, hint, icon, big }: {
  label: ReactNode; value: ReactNode; sub?: ReactNode; tone?: 'good' | 'bad' | 'warn' | 'accent'; trend?: ReactNode;
  hint?: string; icon?: IconName; big?: boolean;
}) {
  return (
    <div className={cx('stat', big && 'stat-big')}>
      <div className="stat-label">
        {icon && <Icon name={icon} size={14} />}
        <span>{label}</span>
        {hint && <InfoTip text={hint} />}
      </div>
      <div className={cx('stat-value', tone && `tone-${tone}`)}>{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
      {trend && <div className="stat-trend">{trend}</div>}
    </div>
  );
}

export function Segmented<T extends string>({ options, value, onChange, label, size = 'md' }: {
  options: { id: T; label: ReactNode; title?: string }[]; value: T; onChange: (v: T) => void; label: string; size?: 'sm' | 'md';
}) {
  return (
    <div className={cx('segmented', size === 'sm' && 'segmented-sm')} role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          className={cx(value === o.id && 'on')}
          title={o.title}
          onClick={() => onChange(o.id)}
          onKeyDown={(e) => {
            if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
            e.preventDefault();
            const i = options.findIndex((x) => x.id === value);
            const next = options[(i + (e.key === 'ArrowRight' ? 1 : options.length - 1)) % options.length];
            onChange(next.id);
            (e.currentTarget.parentElement?.querySelectorAll('button')[options.indexOf(next)] as HTMLElement | undefined)?.focus();
          }}
          tabIndex={value === o.id ? 0 : -1}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export type SubNavItem = { id: string; label: ReactNode; href: string; badge?: ReactNode };

export function SubNav({ items, active, label }: { items: SubNavItem[]; active: string; label: string }) {
  return (
    <nav className="subnav" aria-label={label}>
      {items.map((it) => (
        <a key={it.id} href={it.href} className={cx('subnav-item', active === it.id && 'on')} aria-current={active === it.id ? 'page' : undefined}>
          {it.label}
          {it.badge}
        </a>
      ))}
    </nav>
  );
}

export function PageHeader({ title, description, actions, tabs }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; tabs?: ReactNode }) {
  return (
    <div className="page-head">
      <div className="page-head-row">
        <div className="page-titles">
          <h1 className="page-title">{title}</h1>
          {description && <p className="page-desc">{description}</p>}
        </div>
        {actions && <div className="page-actions">{actions}</div>}
      </div>
      {tabs}
    </div>
  );
}

export function Toggle({ checked, onChange, label, description, disabled }: {
  checked: boolean; onChange: (v: boolean) => void; label: ReactNode; description?: ReactNode; disabled?: boolean;
}) {
  const id = useId();
  return (
    <div className={cx('toggle-row', disabled && 'is-disabled')}>
      <button
        id={id}
        type="button"
        role="switch"
        aria-checked={checked}
        className={cx('switch', checked && 'on')}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        aria-describedby={description ? `${id}-d` : undefined}
      >
        <span className="switch-knob" />
      </button>
      <div className="toggle-text">
        <label htmlFor={id} className="toggle-label">{label}</label>
        {description && <div className="toggle-desc" id={`${id}-d`}>{description}</div>}
      </div>
    </div>
  );
}

export function Field({ label, hint, children, wide, extra }: { label: ReactNode; hint?: ReactNode; children: ReactNode; wide?: boolean; extra?: ReactNode }) {
  return (
    <label className={cx('field', wide && 'field-wide')}>
      <span className="field-label">
        {label}
        {extra}
      </span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

export function Callout({ tone = 'info', title, children, icon, action }: {
  tone?: 'info' | 'warn' | 'bad' | 'good' | 'accent'; title?: ReactNode; children?: ReactNode; icon?: IconName; action?: ReactNode;
}) {
  const ic: IconName = icon ?? (tone === 'bad' || tone === 'warn' ? 'alert' : tone === 'good' ? 'check' : 'info');
  return (
    <div className={cx('callout', `callout-${tone}`)} role={tone === 'bad' ? 'alert' : undefined}>
      <Icon name={ic} size={16} />
      <div className="callout-body">
        {title && <div className="callout-title">{title}</div>}
        {children && <div className="callout-text">{children}</div>}
      </div>
      {action && <div className="callout-action">{action}</div>}
    </div>
  );
}

export function EmptyState({ icon = 'info', title, children, actions, compact }: {
  icon?: IconName; title: ReactNode; children?: ReactNode; actions?: ReactNode; compact?: boolean;
}) {
  return (
    <div className={cx('empty', compact && 'empty-compact')}>
      <div className="empty-icon"><Icon name={icon} size={compact ? 18 : 22} /></div>
      <div className="empty-title">{title}</div>
      {children && <div className="empty-body">{children}</div>}
      {actions && <div className="empty-actions">{actions}</div>}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: string; onRetry?: () => void }) {
  const { t } = useI18n();
  return (
    <Callout tone="bad" title={t('common.loadFailed')} action={onRetry && <Button size="sm" icon="refresh" onClick={onRetry}>{t('common.retry')}</Button>}>
      <span className="mono">{error}</span>
    </Callout>
  );
}

export function Skeleton({ h = 16, w = '100%', className }: { h?: number | string; w?: number | string; className?: string }) {
  return <span className={cx('skeleton', className)} style={{ height: h, width: w }} aria-hidden />;
}

export function Loading({ lines = 3 }: { lines?: number }) {
  const { t } = useI18n();
  return (
    <div className="loading" role="status" aria-live="polite">
      <span className="sr-only">{t('common.loading')}</span>
      {Array.from({ length: lines }, (_, i) => <Skeleton key={i} h={14} w={`${90 - i * 18}%`} />)}
    </div>
  );
}

export function useCopy(): [(text: string) => Promise<boolean>, boolean] {
  const [copied, setCopied] = useState(false);
  const timer = useRef(0);
  const copy = useCallback(async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), 1600);
      return true;
    } catch {
      return false;
    }
  }, []);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return [copy, copied];
}

/** A command or URL with a copy button. */
export function CopyField({ text, label, multiline }: { text: string; label?: string; multiline?: boolean }) {
  const { t } = useI18n();
  const [copy, copied] = useCopy();
  const [failed, setFailed] = useState(false);
  return (
    <div className={cx('copy-field', multiline && 'copy-multi')}>
      <code className="copy-text" aria-label={label}>{text}</code>
      <button
        type="button"
        className={cx('copy-btn', copied && 'on')}
        onClick={async () => setFailed(!(await copy(text)))}
        aria-label={copied ? t('common.copied') : t('common.copyLabel', { what: label ?? t('common.command') })}
      >
        <Icon name={copied ? 'check' : 'copy'} size={14} />
        <span>{copied ? t('common.copied') : failed ? t('common.copyFailed') : t('common.copy')}</span>
      </button>
      <span className="sr-only" aria-live="polite">{copied ? t('common.copied') : ''}</span>
    </div>
  );
}

export function InfoTip({ text }: { text: string }) {
  return (
    <span className="infotip" tabIndex={0} role="note" aria-label={text} data-tip={text}>
      <Icon name="info" size={13} />
    </span>
  );
}

/** A side panel over the page. Esc closes it; focus moves in on open and back on close. */
export function Drawer({ open, onClose, title, children, actions, wide }: {
  open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; actions?: ReactNode; wide?: boolean;
}) {
  const { t } = useI18n();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.focus();
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      prev?.focus?.();
    };
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="drawer-layer">
      <div className="drawer-scrim" onClick={onClose} />
      <div className={cx('drawer', wide && 'drawer-wide')} role="dialog" aria-modal="true" aria-label={typeof title === 'string' ? title : undefined} tabIndex={-1} ref={ref}>
        <header className="drawer-head">
          <div className="drawer-title">{title}</div>
          <div className="drawer-actions">
            {actions}
            <IconButton icon="x" label={t('common.close')} onClick={onClose} />
          </div>
        </header>
        <div className="drawer-body">{children}</div>
      </div>
    </div>
  );
}

/** A confirmation dialog. */
export function Confirm({ open, title, children, confirmLabel, tone = 'primary', busy, onConfirm, onCancel }: {
  open: boolean; title: ReactNode; children?: ReactNode; confirmLabel: string; tone?: 'primary' | 'danger'; busy?: boolean;
  onConfirm: () => void; onCancel: () => void;
}) {
  const { t } = useI18n();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>('.btn-primary, .btn-danger')?.focus();
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onCancel();
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      prev?.focus?.();
    };
  }, [open, onCancel]);
  if (!open) return null;
  return (
    <div className="modal-layer">
      <div className="drawer-scrim" onClick={onCancel} />
      <div className="modal" role="alertdialog" aria-modal="true" aria-label={typeof title === 'string' ? title : undefined} ref={ref}>
        <h2 className="modal-title">{title}</h2>
        {children && <div className="modal-body">{children}</div>}
        <div className="modal-actions">
          <Button onClick={onCancel} disabled={busy}>{t('common.cancel')}</Button>
          <Button variant={tone} onClick={onConfirm} loading={busy}>{confirmLabel}</Button>
        </div>
      </div>
    </div>
  );
}

/** A small popover menu anchored to its trigger. */
export function Popover({ trigger, children, align = 'end', label }: {
  trigger: (p: { open: boolean; toggle: () => void; id: string }) => ReactNode; children: (close: () => void) => ReactNode;
  align?: 'start' | 'end'; label: string;
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        (ref.current?.querySelector('[aria-haspopup]') as HTMLElement | null)?.focus();
      }
    };
    window.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey);
    requestAnimationFrame(() => ref.current?.querySelector<HTMLElement>('[role="menuitemradio"][aria-checked="true"], [role="menuitemradio"], [role="menuitem"]')?.focus());
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [open]);
  return (
    <div className="popover-wrap" ref={ref}>
      {trigger({ open, toggle: () => setOpen((o) => !o), id })}
      {open && (
        <div className={cx('popover', `popover-${align}`)} role="menu" id={id} aria-label={label}
          onKeyDown={(e) => {
            if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
            e.preventDefault();
            const items = [...(ref.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]') ?? [])];
            const i = items.indexOf(document.activeElement as HTMLElement);
            items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
          }}>
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}

export function MenuItem({ checked, onSelect, children, hint }: { checked?: boolean; onSelect: () => void; children: ReactNode; hint?: ReactNode }) {
  return (
    <button type="button" role={checked === undefined ? 'menuitem' : 'menuitemradio'} aria-checked={checked} className={cx('menu-item', checked && 'on')} onClick={onSelect}>
      <span className="menu-check">{checked && <Icon name="check" size={14} />}</span>
      <span className="menu-text">{children}</span>
      {hint && <span className="menu-hint">{hint}</span>}
    </button>
  );
}

export function Disclosure({ title, children, defaultOpen, meta }: { title: ReactNode; children: ReactNode; defaultOpen?: boolean; meta?: ReactNode }) {
  return (
    <details className="disclosure" open={defaultOpen}>
      <summary>
        <Icon name="chevronRight" size={14} />
        <span className="disclosure-title">{title}</span>
        {meta && <span className="disclosure-meta">{meta}</span>}
      </summary>
      <div className="disclosure-body">{children}</div>
    </details>
  );
}

/** A model id with its vendor's logo. */
export function ModelLabel({ model, muted }: { model: string | undefined; muted?: boolean }) {
  const v: ProviderId | undefined = modelVendor(model);
  if (!model) return <span className="muted">—</span>;
  return (
    <span className={cx('brand-label', 'mono', muted && 'muted')} title={model}>
      <BrandIcon id={v === 'google' ? 'gemini' : v === 'anthropic' ? 'claude' : v} label={v ? PROVIDER_NAMES[v] : model} size={14} />
      <span className="clip">{model}</span>
    </span>
  );
}

/** A route name with its provider's logo. */
export function RouteLabel({ name, route, strong }: { name: string; route?: Pick<RouteView, 'base_url' | 'kind' | 'provider'>; strong?: boolean }) {
  const p = route ? routeProvider(route) : undefined;
  return (
    <span className={cx('brand-label', strong && 'strong')}>
      {p ? <BrandIcon id={p} label={PROVIDER_NAMES[p]} size={14} /> : <Icon name="routing" size={14} />}
      <span className="clip">{name}</span>
    </span>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{children}</kbd>;
}
