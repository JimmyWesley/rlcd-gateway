// Decisions: the audit of System One calls, from apps and from the gateway's
// own pruning and routing. Overview, calibration, the audit log, mirror
// parity and the backend settings.
import { useI18n } from '../../i18n';
import type { Window } from '../../lib/api';
import { PageHeader, SubNav } from '../../ui';
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
  return (
    <div className="page">
      <PageHeader
        title={t('nav.decisions')}
        description={t('decisions.desc')}
        actions={tab !== 'settings' ? <WindowPicker value={win} onChange={setWin} /> : undefined}
        tabs={
          <SubNav
            label={t('nav.decisions')}
            active={tab}
            items={TABS.map((id) => ({ id, label: t(`decisions.tab.${id}`), href: id === 'overview' ? '#/decisions' : `#/decisions/${id}` }))}
          />
        }
      />
      {tab === 'overview' && <DecisionsOverview since={since} win={win} />}
      {tab === 'calibration' && <Calibration since={since} />}
      {tab === 'audit' && <Audit since={since} />}
      {tab === 'parity' && <Parity since={since} />}
      {tab === 'settings' && <DecisionSettings />}
    </div>
  );
}
