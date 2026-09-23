import { useCallback, useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import type { IconName } from '../../icons/Icon';
import { BrandIcon } from '../../icons/BrandIcon';
import { agentsApi, type AgentSource, type AgentStatus } from '../../lib/agentsApi';
import { useGateway } from '../../state/gateway';
import { Badge, Button, Callout, Card, Confirm, CopyField, Disclosure, Dot, EmptyState, ErrorState, Field, Loading, PageHeader, SubNav, cx } from '../../ui';
import { Recall } from './Recall';

type Tab = 'agents' | 'recall' | 'apps' | 'keys';

export function Integrations({ sub }: { sub: string }) {
  const { t } = useI18n();
  const tab: Tab = (['recall', 'apps', 'keys'] as string[]).includes(sub) ? (sub as Tab) : 'agents';
  return (
    <div className="page">
      <PageHeader
        title={t('nav.integrations')}
        description={t('integrations.desc')}
        tabs={
          <SubNav
            label={t('nav.integrations')}
            active={tab}
            items={[
              { id: 'agents', label: t('integrations.tab.agents'), href: '#/integrations' },
              { id: 'recall', label: t('integrations.tab.recall'), href: '#/integrations/recall' },
              { id: 'apps', label: t('integrations.tab.apps'), href: '#/integrations/apps', badge: <Badge tone="accent">{t('common.soon')}</Badge> },
              { id: 'keys', label: t('integrations.tab.keys'), href: '#/integrations/keys', badge: <Badge tone="accent">{t('common.soon')}</Badge> },
            ]}
          />
        }
      />
      {tab === 'agents' && <Agents />}
      {tab === 'recall' && <Recall />}
      {tab === 'apps' && <Reserved kind="apps" />}
      {tab === 'keys' && <Reserved kind="keys" />}
    </div>
  );
}

/** A reserved place in the navigation for screens that land with F5. */
export function Reserved({ kind }: { kind: 'apps' | 'keys' | 'aliases' }) {
  const { t } = useI18n();
  const icon: IconName = kind === 'apps' ? 'apps' : kind === 'keys' ? 'key' : 'routing';
  return (
    <Card>
      <EmptyState icon={icon} title={t(`reserved.${kind}.title`)}>
        <p>{t(`reserved.${kind}.body`)}</p>
        <p className="fine">{t('reserved.soon')}</p>
      </EmptyState>
    </Card>
  );
}

function Agents() {
  const { t } = useI18n();
  const [agents, setAgents] = useState<AgentStatus[] | null>(null);
  const [project, setProject] = useState('');
  const [checked, setChecked] = useState('');
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((p: string) => {
    agentsApi.list(p).then((a) => {
      setAgents(a);
      setChecked(p);
      setError(null);
    }).catch((e) => setError(String(e instanceof Error ? e.message : e)));
  }, []);
  useEffect(() => load(''), [load]);

  return (
    <>
      <Callout tone="info" icon="shield" title={t('agents.loginKept')}>{t('agents.intro')}</Callout>
      <Card>
        <form className="project-form" onSubmit={(e) => { e.preventDefault(); load(project.trim()); }}>
          <Field label={t('agents.project')} hint={t('agents.projectHint')} wide>
            <input value={project} className="mono" placeholder="/path/to/your/project" onChange={(e) => setProject(e.target.value)} />
          </Field>
          <Button type="submit" icon="search">{t('agents.check')}</Button>
        </form>
      </Card>
      {error && <ErrorState error={error} onRetry={() => load(checked)} />}
      {!agents && !error && <Card><Loading lines={5} /></Card>}
      <div className="agents">
        {agents?.map((a) => <AgentCard key={a.name} agent={a} project={checked} onUpdated={setAgents} />)}
      </div>
    </>
  );
}

function AgentCard({ agent: a, project, onUpdated }: { agent: AgentStatus; project: string; onUpdated: (a: AgentStatus[]) => void }) {
  const { t, tn } = useI18n();
  const { config } = useGateway();
  const [confirm, setConfirm] = useState<'setup' | 'undo' | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);
  const primary = a.name === 'claude';

  const run = async (action: 'setup' | 'undo') => {
    setBusy(true);
    setResult(null);
    try {
      const r = await agentsApi.run(a.name, action, project);
      onUpdated(r.agents);
      setResult({ ok: r.ok, text: r.ok ? (r.message ?? t('common.done')) : (r.error ?? t('common.failed')) });
    } catch (e) {
      setResult({ ok: false, text: String(e instanceof Error ? e.message : e) });
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  };

  const tone = a.effective ? 'good' : a.configured ? 'warn' : 'neutral';
  const state = a.effective ? t('agents.state.using') : a.configured ? t('agents.state.overridden') : t('agents.state.off');

  return (
    <Card className={cx('agent-card', primary && 'is-primary')}>
      <header className="agent-head">
        <span className="agent-logo"><BrandIcon id={a.name === 'claude' ? 'claude-code' : a.name} label={a.title} size={28} /></span>
        <div className="agent-titles">
          <h3>{a.title}</h3>
          <span className="muted small">{a.installed ? (a.version || (a.binary ? t('agents.installed') : t('agents.configFound'))) : t('agents.notFound')}</span>
        </div>
        <Badge tone={tone}><Dot tone={tone} /> {state}</Badge>
      </header>

      {a.error && <Callout tone="bad">{a.error}</Callout>}

      <dl className="agent-facts">
        <div>
          <dt>{t('agents.baseUrl')}</dt>
          <dd className="mono">{a.current || t('agents.providerDefault')}</dd>
          {a.effective_source && <dd className="muted small">{t('agents.from', { source: a.effective_source })}</dd>}
        </div>
        {a.auth && (
          <div>
            <dt>{t('agents.login')}</dt>
            <dd>{a.auth.label} {a.auth.keeps_subscription && <Badge tone="good">{t('agents.subscriptionKept')}</Badge>}</dd>
            {a.auth.source && <dd className="muted small">{a.auth.source}</dd>}
          </div>
        )}
        <div>
          <dt>{t('agents.file')}</dt>
          <dd className="mono small clip" title={a.config_path}>{a.config_path}</dd>
          {!a.config_exists && <dd className="muted small">{t('agents.fileMissing')}</dd>}
        </div>
      </dl>
      {a.auth?.note && <p className="fine">{a.auth.note}</p>}
      {a.warnings?.map((w) => <Callout key={w} tone="warn">{w}</Callout>)}

      <div className="agent-try">
        <span className="field-label">{t('agents.tryOnce')}</span>
        <CopyField text={a.try_command} label={a.title} multiline={a.try_command.length > 90} />
      </div>

      {a.sources && a.sources.length > 0 && (primary || a.sources.some((s) => s.exists)) && (
        <Disclosure title={t('agents.sources')} defaultOpen={primary && !a.effective && a.configured}>
          <Sources sources={a.sources} effective={a.effective_source} />
        </Disclosure>
      )}

      <p className="fine">{a.changes}</p>

      <div className="agent-actions">
        <Button variant="primary" icon="integrations" onClick={() => setConfirm('setup')} disabled={a.configured && a.effective}>{t('agents.setup')}</Button>
        <Button onClick={() => setConfirm('undo')} disabled={!a.configured}>{t('agents.undo')}</Button>
        <code className="mono muted small">{a.setup_command}</code>
      </div>
      {result && <pre className={cx('result', result.ok ? 'is-ok' : 'is-bad')} role="status">{result.text}</pre>}

      {a.notes && a.notes.length > 0 && (
        <Disclosure title={t('agents.notes', { count: a.notes.length })}>
          <ul className="notes">{a.notes.map((n) => <li key={n}>{n}</li>)}</ul>
        </Disclosure>
      )}
      {primary && (
        <p className="fine">{tn('agents.recallHint', { tool: <code>rlcd_recall</code>, link: <a href="#/integrations/recall">{t('integrations.tab.recall')}</a> })}</p>
      )}

      <Confirm
        open={!!confirm}
        title={confirm === 'setup' ? t('agents.confirmSetup', { name: a.title }) : t('agents.confirmUndo', { name: a.title })}
        confirmLabel={confirm === 'setup' ? t('agents.yesSetup') : t('agents.yesUndo')}
        busy={busy}
        onConfirm={() => confirm && run(confirm)}
        onCancel={() => setConfirm(null)}
      >
        <p>{tn('agents.confirmBody', { file: <code>{a.config_path}</code> })}</p>
        {confirm === 'setup' && config && <p className="fine">{t('agents.confirmTarget', { url: config.listen })}</p>}
      </Confirm>
    </Card>
  );
}

function Sources({ sources, effective }: { sources: AgentSource[]; effective?: string }) {
  const { t } = useI18n();
  return (
    <div className="scroll-x">
      <table className="table">
        <thead>
          <tr><th>{t('agents.src.level')}</th><th>{t('agents.src.file')}</th><th>{t('agents.src.baseUrl')}</th></tr>
        </thead>
        <tbody>
          {sources.map((s) => (
            <tr key={s.scope + s.path} className={cx(s.set && s.scope === effective && 'is-win')}>
              <td>{s.scope}</td>
              <td className="mono small clip" title={s.path}>{s.path}{!s.exists && s.scope !== 'environment' && <span className="muted"> ({t('agents.src.none')})</span>}</td>
              <td className="mono small">
                {s.error ? <span className="tone-bad">{s.error}</span> : s.set ? (
                  <>
                    {s.base_url}
                    {s.gateway && <Badge tone="accent">{t('agents.src.gateway')}</Badge>}
                    {s.scope === effective && <Badge tone="good">{t('agents.src.inEffect')}</Badge>}
                  </>
                ) : <span className="muted">—</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
