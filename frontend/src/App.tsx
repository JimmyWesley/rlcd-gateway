import { lazy, Suspense, useEffect, useState } from 'react';
import { LOCALES, useI18n, type PlainKey } from './i18n';
import { Icon, LogoMark, type IconName } from './icons/Icon';
import { useLocation } from './lib/router';
import { useTheme, type ThemePref } from './lib/theme';
import { speaks } from './lib/api';
import { useGateway } from './state/gateway';
import { Badge, cx, Dot, Loading, MenuItem, Popover, RouteLabel } from './ui';
import { Overview } from './pages/overview/Overview';
import { Traffic } from './pages/traffic/Traffic';
import { Savings } from './pages/savings/Savings';
import { Routing } from './pages/routing/Routing';
import { Integrations } from './pages/integrations/Integrations';
import { Settings } from './pages/settings/Settings';
import { Decisions } from './pages/decisions/Decisions';

// React Flow is large; the Flow page loads it on demand.
const Flow = lazy(() => import('./pages/flow/FlowPage'));

export type Section = 'overview' | 'flow' | 'traffic' | 'savings' | 'routes' | 'routing' | 'decisions' | 'integrations' | 'settings';

const NAV: { group: PlainKey | null; items: { id: Section; icon: IconName; label: PlainKey }[] }[] = [
  {
    group: null,
    items: [
      { id: 'overview', icon: 'overview', label: 'nav.overview' },
      { id: 'flow', icon: 'flow', label: 'nav.flow' },
      { id: 'traffic', icon: 'traffic', label: 'nav.traffic' },
    ],
  },
  {
    group: 'nav.group.control',
    items: [
      { id: 'savings', icon: 'savings', label: 'nav.savings' },
      { id: 'routes', icon: 'globe', label: 'nav.routes' },
      { id: 'routing', icon: 'routing', label: 'nav.routing' },
      { id: 'decisions', icon: 'cpu', label: 'nav.decisions' },
    ],
  },
  {
    group: 'nav.group.connect',
    items: [{ id: 'integrations', icon: 'integrations', label: 'nav.integrations' }],
  },
];

const SECTIONS: Section[] = ['overview', 'flow', 'traffic', 'savings', 'routes', 'routing', 'decisions', 'integrations', 'settings'];

export function App() {
  const { t } = useI18n();
  const loc = useLocation();
  let section: Section = SECTIONS.includes(loc.path[0] as Section) ? (loc.path[0] as Section) : 'overview';
  let sub = loc.path[1] ?? '';
  // Routes and aliases used to live under Routing; old links still work.
  if (section === 'routing' && (sub === 'routes' || sub === 'aliases')) {
    section = 'routes';
    sub = sub === 'aliases' ? 'aliases' : '';
  }
  const param = loc.path[2] ?? '';
  const [navOpen, setNavOpen] = useState(false);

  useEffect(() => {
    setNavOpen(false);
    document.title = `${t(`nav.${section}`)} · RLCD Gateway`;
  }, [section, t]);

  return (
    <div className={cx('shell', navOpen && 'nav-open')}>
      <a className="skip-link" href="#main" onClick={(e) => { e.preventDefault(); document.getElementById('main')?.focus(); }}>{t('shell.skip')}</a>
      <Sidebar section={section} />
      <div className="main">
        <Topbar onMenu={() => setNavOpen((o) => !o)} />
        <main className="content" id="main" tabIndex={-1}>
          {section === 'overview' && <Overview />}
          {section === 'flow' && (
            <Suspense fallback={<div className="page"><Loading lines={4} /></div>}>
              <Flow />
            </Suspense>
          )}
          {section === 'traffic' && <Traffic selectedId={sub} />}
          {section === 'savings' && <Savings sub={sub} />}
          {section === 'routes' && <Routing area="routes" sub={sub} />}
          {section === 'routing' && <Routing area="rules" sub={sub} />}
          {section === 'decisions' && <Decisions sub={sub} />}
          {section === 'integrations' && <Integrations sub={sub} />}
          {section === 'settings' && <Settings sub={sub} param={param} />}
        </main>
      </div>
      <div className="nav-scrim" onClick={() => setNavOpen(false)} />
    </div>
  );
}

function Sidebar({ section }: { section: Section }) {
  const { t } = useI18n();
  const { config, live } = useGateway();
  return (
    <aside className="sidebar" aria-label={t('shell.navigation')}>
      <a href="#/overview" className="brand" aria-label="RLCD Gateway">
        <LogoMark size={28} />
        <span className="wordmark">
          <span className="wm-a">RLCD</span>
          <span className="wm-b">Gateway</span>
        </span>
      </a>
      <nav className="nav">
        {NAV.map((g, gi) => (
          <div key={gi} className="nav-group">
            {g.group && <div className="nav-group-label">{t(g.group)}</div>}
            {g.items.map((it) => (
              <a key={it.id} href={`#/${it.id}`} className={cx('nav-item', section === it.id && 'on')} aria-current={section === it.id ? 'page' : undefined}>
                <Icon name={it.icon} size={17} />
                <span className="nav-label">{t(it.label)}</span>
              </a>
            ))}
          </div>
        ))}
      </nav>
      <div className="sidebar-foot">
        <a href="#/settings" className={cx('nav-item', section === 'settings' && 'on')} aria-current={section === 'settings' ? 'page' : undefined}>
          <Icon name="settings" size={17} />
          <span className="nav-label">{t('nav.settings')}</span>
        </a>
        <div className="gw-status" title={live ? t('shell.liveHint') : t('shell.offlineHint')}>
          <Dot tone={live ? 'good' : 'warn'} pulse={live} />
          <span className="nav-label">
            <span className="gw-status-title">{live ? t('shell.live') : t('shell.reconnecting')}</span>
            <span className="gw-status-addr mono">{config?.listen ?? '…'}</span>
          </span>
          {config?.exposed && <span className="nav-label"><Badge tone="warn" title={t('shell.exposedHint')}>{t('shell.exposed')}</Badge></span>}
        </div>
      </div>
    </aside>
  );
}

function Topbar({ onMenu }: { onMenu: () => void }) {
  const { t } = useI18n();
  return (
    <header className="topbar">
      <button type="button" className="icon-btn menu-btn" aria-label={t('shell.menu')} onClick={onMenu}>
        <Icon name="menu" size={18} />
      </button>
      <div className="topbar-spacer" />
      <RouteSwitcher />
      <LanguageMenu />
      <ThemeMenu />
    </header>
  );
}

function RouteSwitcher() {
  const { t } = useI18n();
  const { config, switchRoute } = useGateway();
  const [err, setErr] = useState<string | null>(null);
  if (!config) return null;
  const active = config.routes.find((r) => r.name === config.active_route);
  return (
    <Popover
      label={t('route.switch')}
      trigger={({ open, toggle, id }) => (
        <button type="button" className={cx('topbar-btn', 'route-btn', err && 'is-bad')} aria-haspopup="menu" aria-expanded={open} aria-controls={id} onClick={toggle}
          title={err ?? t('route.switchHint')}>
          <span className="topbar-btn-kicker">{t('route.active')}</span>
          <RouteLabel name={config.active_route} route={active} strong />
          <Icon name="chevronDown" size={14} />
        </button>
      )}
    >
      {(close) => (
        <>
          <div className="menu-head">{t('route.switchHint')}</div>
          {config.routes.filter((r) => speaks(r, 'anthropic-messages')).map((r) => (
            <MenuItem
              key={r.name}
              checked={r.name === config.active_route}
              hint={r.auth === 'passthrough' ? t('route.yourLogin') : r.has_key ? t('route.gatewayKey') : <span className="tone-warn">{t('route.noKey')}</span>}
              onSelect={async () => {
                close();
                try {
                  await switchRoute(r.name);
                  setErr(null);
                } catch (e) {
                  setErr(String(e instanceof Error ? e.message : e));
                }
              }}
            >
              <RouteLabel name={r.name} route={r} />
              {r.model && <span className="menu-sub mono">{r.model}</span>}
            </MenuItem>
          ))}
          <div className="menu-foot">
            <span className="muted">{t('route.openaiDefault')}</span>
            <span className="mono">{config.default_openai_route || t('route.openaiByLogin')}</span>
          </div>
          <a className="menu-link" href="#/routes" onClick={close}><span className="menu-icon-label"><Icon name="edit" size={14} />{t('route.manage')}</span> <Icon name="arrowRight" size={14} /></a>
        </>
      )}
    </Popover>
  );
}

function LanguageMenu() {
  const { t, locale, setLocale } = useI18n();
  return (
    <Popover
      label={t('shell.language')}
      trigger={({ open, toggle, id }) => (
        <button type="button" className="topbar-btn" aria-haspopup="menu" aria-expanded={open} aria-controls={id} onClick={toggle} aria-label={t('shell.language')} title={t('shell.language')}>
          <Icon name="globe" size={16} />
          <span className="topbar-btn-text">{locale === 'pt-BR' ? 'PT' : locale.toUpperCase()}</span>
        </button>
      )}
    >
      {(close) => LOCALES.map((l) => (
        <MenuItem key={l.id} checked={l.id === locale} onSelect={() => { setLocale(l.id); close(); }}>
          <span lang={l.id}>{l.label}</span>
        </MenuItem>
      ))}
    </Popover>
  );
}

function ThemeMenu() {
  const { t } = useI18n();
  const { pref, setPref, resolved } = useTheme();
  const opts: { id: ThemePref; icon: IconName; label: PlainKey }[] = [
    { id: 'system', icon: 'monitor', label: 'theme.system' },
    { id: 'light', icon: 'sun', label: 'theme.light' },
    { id: 'dark', icon: 'moon', label: 'theme.dark' },
  ];
  return (
    <Popover
      label={t('theme.label')}
      trigger={({ open, toggle, id }) => (
        <button type="button" className="topbar-btn" aria-haspopup="menu" aria-expanded={open} aria-controls={id} onClick={toggle} aria-label={t('theme.label')} title={t('theme.label')}>
          <Icon name={pref === 'system' ? 'monitor' : resolved === 'dark' ? 'moon' : 'sun'} size={16} />
        </button>
      )}
    >
      {(close) => opts.map((o) => (
        <MenuItem key={o.id} checked={pref === o.id} onSelect={() => { setPref(o.id); close(); }}>
          <span className="menu-icon-label"><Icon name={o.icon} size={15} />{t(o.label)}</span>
        </MenuItem>
      ))}
    </Popover>
  );
}
