import { useState } from 'react';
import { useI18n } from '../../i18n';
import { routerApi, type Assignment } from '../../lib/routerApi';
import { href } from '../../lib/router';
import { useGateway } from '../../state/gateway';
import { Button, Card, Confirm, EmptyState, Loading, RouteLabel } from '../../ui';

type Props = { convs: Assignment[] | null; sticky: boolean; onReload: () => void; onError: (e: string) => void };

export function ConversationsView({ convs, sticky, onReload, onError }: Props) {
  const { t, f } = useI18n();
  const { config } = useGateway();
  const [confirmAll, setConfirmAll] = useState(false);
  const [busy, setBusy] = useState(false);

  const reset = async (id?: string) => {
    setBusy(true);
    try {
      if (id) await routerApi.resetConversation(id);
      else await routerApi.resetAll();
    } catch (e) {
      onError(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
      setConfirmAll(false);
      onReload();
    }
  };

  return (
    <Card
      title={t('convs.title')}
      subtitle={sticky ? t('convs.sub') : t('convs.subOff')}
      actions={
        <div className="btn-row">
          <Button size="sm" icon="refresh" onClick={onReload}>{t('common.refresh')}</Button>
          <Button size="sm" variant="danger" onClick={() => setConfirmAll(true)} disabled={!convs?.length}>{t('convs.resetAll')}</Button>
        </div>
      }
    >
      {!convs && <Loading />}
      {convs && convs.length === 0 && <EmptyState compact icon="routing" title={t('convs.empty')}>{t('convs.emptyBody')}</EmptyState>}
      {convs && convs.length > 0 && (
        <div className="scroll-x">
          <table className="table">
            <thead>
              <tr>
                <th>{t('convs.col.conversation')}</th>
                <th>{t('convs.col.route')}</th>
                <th>{t('convs.col.decidedBy')}</th>
                <th className="num">{t('convs.col.turns')}</th>
                <th>{t('convs.col.lastSeen')}</th>
                <th><span className="sr-only">{t('common.actions')}</span></th>
              </tr>
            </thead>
            <tbody>
              {convs.map((a) => (
                <tr key={a.conversation_id}>
                  <td><a className="mono small" href={href('traffic', { conv: a.conversation_id })} title={a.conversation_id}>{a.conversation_id.slice(0, 22)}…</a></td>
                  <td>{a.route ? <RouteLabel name={a.route} route={config?.routes.find((r) => r.name === a.route)} /> : <span className="muted">{t('convs.activeRoute', { route: config?.active_route ?? '' })}</span>}</td>
                  <td className="small" title={a.reason}>{a.rule ? t('convs.rule', { name: a.rule }) : <span className="muted">{t('convs.noRule')}</span>}</td>
                  <td className="num mono">{a.turns}</td>
                  <td className="small" title={f.dayTime(a.last_seen)}>{f.ago(a.last_seen)}</td>
                  <td className="num"><Button size="sm" onClick={() => reset(a.conversation_id)} disabled={busy}>{t('convs.reset')}</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Confirm open={confirmAll} title={t('convs.resetAllTitle')} confirmLabel={t('convs.resetAll')} tone="danger" busy={busy} onConfirm={() => reset()} onCancel={() => setConfirmAll(false)}>
        {t('convs.resetAllBody')}
      </Confirm>
    </Card>
  );
}
