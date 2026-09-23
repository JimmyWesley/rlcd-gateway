import { useCallback, useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { routerApi, type Assignment, type RouterRoute, type RulesDoc } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Badge, Callout, PageHeader, SubNav } from '../../ui';
import { RoutesView } from './RoutesView';
import { RulesView } from './RulesView';
import { DryRunView } from './DryRunView';
import { ConversationsView } from './ConversationsView';
import { Reserved } from '../integrations/Integrations';

type Tab = 'routes' | 'rules' | 'dry-run' | 'conversations' | 'aliases';
const TABS: Tab[] = ['routes', 'rules', 'dry-run', 'conversations', 'aliases'];

export function Routing({ sub }: { sub: string }) {
  const { t } = useI18n();
  const { config, reloadConfig } = useGateway();
  const tab: Tab = TABS.includes(sub as Tab) ? (sub as Tab) : 'routes';
  const [saved, setSaved] = useState<RulesDoc | null>(null);
  const [doc, setDoc] = useState<RulesDoc | null>(null);
  const [routes, setRoutes] = useState<RouterRoute[] | null>(null);
  const [convs, setConvs] = useState<Assignment[] | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadConvs = useCallback(() => routerApi.conversations().then(setConvs).catch((e) => setError(String(e instanceof Error ? e.message : e))), []);
  const loadRoutes = useCallback(() => routerApi.routes().then(setRoutes).catch((e) => setError(String(e instanceof Error ? e.message : e))), []);
  useEffect(() => {
    routerApi.rules().then((d) => { setSaved(d); setDoc(d); }).catch((e) => setError(String(e instanceof Error ? e.message : e)));
    loadRoutes();
    loadConvs();
  }, [loadConvs, loadRoutes]);

  // The active route can be switched from the top bar; keep the list in step.
  useEffect(() => {
    if (config) setRoutes((rs) => rs?.map((r) => ({ ...r, active: r.name === config.active_route })) ?? rs);
  }, [config?.active_route]); // eslint-disable-line react-hooks/exhaustive-deps

  const dirty = !!doc && !!saved && JSON.stringify(doc) !== JSON.stringify(saved);

  // keepDraft saves only the stickiness settings and leaves unsaved rule edits in place.
  const save = async (next?: RulesDoc, keepDraft = false) => {
    const d = next ?? doc;
    if (!d) return;
    setSaving(true);
    setError(null);
    try {
      const out = await routerApi.saveRules(d);
      setSaved(out);
      setDoc((cur) => (keepDraft && cur ? { ...cur, sticky: out.sticky, ttl_hours: out.ttl_hours, background_bypass: out.background_bypass } : out));
      loadRoutes();
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    } finally {
      setSaving(false);
    }
  };

  const setSticky = (patch: Partial<RulesDoc>) => {
    if (!doc || !saved) return;
    setDoc({ ...doc, ...patch });
    save({ ...saved, ...patch }, true);
  };

  const onRoutes = (rs: RouterRoute[]) => {
    setRoutes(rs);
    reloadConfig();
  };

  return (
    <div className="page">
      <PageHeader
        title={t('nav.routing')}
        description={t('routing.desc')}
        tabs={
          <SubNav
            label={t('nav.routing')}
            active={tab}
            items={[
              { id: 'routes', label: t('routing.tab.routes'), href: '#/routing', badge: routes ? <Badge>{routes.length}</Badge> : undefined },
              { id: 'rules', label: t('routing.tab.rules'), href: '#/routing/rules', badge: doc ? <Badge tone={dirty ? 'warn' : 'neutral'}>{doc.rules.length}</Badge> : undefined },
              { id: 'dry-run', label: t('routing.tab.dryRun'), href: '#/routing/dry-run' },
              { id: 'conversations', label: t('routing.tab.conversations'), href: '#/routing/conversations', badge: convs?.length ? <Badge>{convs.length}</Badge> : undefined },
              { id: 'aliases', label: t('routing.tab.aliases'), href: '#/routing/aliases', badge: <Badge tone="accent">{t('common.soon')}</Badge> },
            ]}
          />
        }
      />
      {error && <Callout tone="bad" action={<button type="button" className="linkish" onClick={() => setError(null)}>{t('common.dismiss')}</button>}>{error}</Callout>}
      {tab === 'routes' && <RoutesView routes={routes} activeRoute={config?.active_route} onSaved={onRoutes} onError={setError} />}
      {tab === 'rules' && (
        <RulesView doc={doc} routes={routes ?? []} dirty={dirty} saving={saving} onChange={setDoc} onSave={() => save()} onRevert={() => saved && setDoc(saved)} onSticky={setSticky} />
      )}
      {tab === 'dry-run' && <DryRunView dirty={dirty} />}
      {tab === 'conversations' && <ConversationsView convs={convs} sticky={doc?.sticky ?? true} onReload={loadConvs} onError={setError} />}
      {tab === 'aliases' && <Reserved kind="aliases" />}
    </div>
  );
}
