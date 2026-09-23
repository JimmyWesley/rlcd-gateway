// Decisions: the audit of System One calls, from apps and from the gateway's
// own pruning and routing. Overview, calibration, the audit log, mirror
// parity and the backend settings.
import { useI18n } from '../../i18n';
import type { Window } from '../../lib/api';
import { decisionsApi, type Source } from '../../lib/decisionsApi';
import { useQueryParam } from '../../lib/router';
import { useFetch } from '../../state/gateway';
import { Callout, PageHeader, Segmented, SubNav } from '../../ui';
import { useWindowPref, WindowPicker } from '../overview/Overview';
import { DecisionsOverview } from './DecisionsOverview';
import { Calibration } from './Calibration';
import { Audit } from './Audit';
import { Parity } from './Parity';
import { DecisionSettings } from './DecisionSettings';

type Tab = 'overview' | 'calibration' | 'audit' | 'parity' | 'settings';
const TABS: Tab[] = ['overview', 'calibration', 'audit', 'parity', 'settings'];

export const sinceOf = (w: Window) => (w === 'all' ? '' : w);

export function Decisions({ sub }: { sub: string }) {
  const { t } = useI18n();
  const tab: Tab = TABS.includes(sub as Tab) ? (sub as Tab) : 'overview';
  const [win, setWin] = useWindowPref('rlcd.decisions.window', '7d');
  const since = sinceOf(win);
  // Decisions audit external systems. The gateway's own pruning and routing
  // calls (logged only when log_internal is on) are kept apart, never mixed in.
  const [srcParam, setSrc] = useQueryParam('src');
  const source: Source = srcParam === 'prune' || srcParam === 'router' ? srcParam : 'client';
  const all = useFetch(() => decisionsApi.stats({ since }), [since]);
  const internal = Object.entries(all.data?.by_source ?? {}).filter(([k, g]) => k !== 'client' && g.requests > 0);
  return (
    <div className="page">
      <PageHeader
        title={t('nav.decisions')}
        description={t('decisions.desc')}
        actions={tab !== 'settings' ? (
          <div className="btn-row">
            {(internal.length > 0 || source !== 'client') && (
              <Segmented
                size="sm"
                label={t('decisions.scope')}
                value={source}
                onChange={(v) => setSrc(v === 'client' ? null : v)}
                options={(['client', 'prune', 'router'] as const).map((id) => ({ id, label: t(`decision.source.${id}`) }))}
              />
            )}
            <WindowPicker value={win} onChange={setWin} />
          </div>
        ) : undefined}
        tabs={
          <SubNav
            label={t('nav.decisions')}
            active={tab}
            items={TABS.map((id) => ({ id, label: t(`decisions.tab.${id}`), href: id === 'overview' ? '#/decisions' : `#/decisions/${id}` }))}
          />
        }
      />
      {tab !== 'settings' && source !== 'client' && (
        <Callout tone="warn" icon="cpu" title={t('decisions.internalTitle')}>{t('decisions.internalBody')}</Callout>
      )}
      {tab === 'overview' && <DecisionsOverview since={since} win={win} source={source} hidden={source === 'client' ? internal.reduce((n, [, g]) => n + g.requests, 0) : 0} />}
      {tab === 'calibration' && <Calibration since={since} source={source} />}
      {tab === 'audit' && <Audit since={since} source={source} />}
      {tab === 'parity' && <Parity since={since} source={source} />}
      {tab === 'settings' && <DecisionSettings />}
    </div>
  );
}
