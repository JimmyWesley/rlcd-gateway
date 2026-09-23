// Model aliases: the model name a client asks for picks the route and the
// model sent upstream. GET /v1/models lists them.
import { useEffect, useState } from 'react';
import { useI18n } from '../../i18n';
import { routerApi, type Alias, type AliasView, type RouterRoute } from '../../lib/routerApi';
import { Button, Card, EmptyState, IconButton, Loading, cx } from '../../ui';
import { ProtocolBadges } from './RoutesView';

const strip = (a: AliasView | Alias): Alias => ({ name: a.name, route: a.route, model: a.model ?? '', description: a.description ?? '' });

export function AliasesView({ routes, onError, onSaved }: { routes: RouterRoute[]; onError: (e: string) => void; onSaved?: () => void }) {
  const { t, tn } = useI18n();
  const [saved, setSaved] = useState<AliasView[] | null>(null);
  const [rows, setRows] = useState<Alias[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    routerApi.aliases().then((a) => {
      setSaved(a);
      setRows(a.map(strip));
    }).catch((e) => onError(String(e instanceof Error ? e.message : e)));
  }, [onError]);

  if (!saved) return <Card><Loading /></Card>;

  const dirty = JSON.stringify(rows) !== JSON.stringify(saved.map(strip));
  const set = (i: number, patch: Partial<Alias>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const add = () => {
    const route = routes.find((r) => r.kind === 'openai')?.name ?? routes[0]?.name ?? '';
    let name = 'smart';
    for (let n = 2; rows.some((r) => r.name === name); n++) name = `smart-${n}`;
    setRows([...rows, { name, route, model: '', description: '' }]);
  };
  const save = async () => {
    setSaving(true);
    try {
      const out = await routerApi.saveAliases(rows.map((r) => ({ ...r, model: r.model?.trim() || undefined, description: r.description?.trim() || undefined })));
      setSaved(out);
      setRows(out.map(strip));
      onSaved?.();
    } catch (e) {
      onError(String(e instanceof Error ? e.message : e));
    } finally {
      setSaving(false);
    }
  };
  const routeOf = (name: string) => routes.find((r) => r.name === name);

  return (
    <Card
      title={t('aliases.title')}
      subtitle={tn('aliases.sub', { models: <code>GET /v1/models</code> })}
      actions={<Button size="sm" icon="plus" onClick={add}>{t('aliases.add')}</Button>}
    >
      {rows.length === 0 ? (
        <EmptyState compact icon="routing" title={t('aliases.empty')}>{tn('aliases.emptyBody', { a: <code>smart</code>, b: <code>gpt-4o</code> })}</EmptyState>
      ) : (
        <div className="scroll-x">
          <table className="table table-form">
            <thead>
              <tr>
                <th>{t('aliases.col.name')}</th>
                <th>{t('aliases.col.route')}</th>
                <th>{t('aliases.col.model')}</th>
                <th>{t('aliases.col.serves')}</th>
                <th>{t('aliases.col.description')}</th>
                <th><span className="sr-only">{t('common.actions')}</span></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a, i) => {
                const rt = routeOf(a.route);
                return (
                  <tr key={i}>
                    <td><input className="mono" aria-label={t('aliases.col.name')} value={a.name} onChange={(e) => set(i, { name: e.target.value })} /></td>
                    <td>
                      <select aria-label={t('aliases.col.route')} value={a.route} onChange={(e) => set(i, { route: e.target.value })}>
                        {routes.map((r) => <option key={r.name} value={r.name}>{r.name} ({r.provider})</option>)}
                      </select>
                    </td>
                    <td><input className="mono" aria-label={t('aliases.col.model')} value={a.model ?? ''} placeholder={rt?.model || t('aliases.asIs')} onChange={(e) => set(i, { model: e.target.value })} /></td>
                    <td>{rt ? <ProtocolBadges protocols={rt.protocols} /> : <span className="tone-bad small">{t('aliases.noRoute')}</span>}</td>
                    <td><input aria-label={t('aliases.col.description')} value={a.description ?? ''} placeholder={t('aliases.optional')} onChange={(e) => set(i, { description: e.target.value })} /></td>
                    <td className="num"><IconButton icon="trash" label={t('aliases.remove')} onClick={() => setRows(rows.filter((_, j) => j !== i))} /></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <p className="fine">{tn('aliases.formats', { a: <code>/v1/messages</code>, b: <code>/v1/chat/completions</code>, c: <code>/v1/responses</code> })}</p>
      <div className={cx('savebar', 'savebar-inline', dirty && 'show')}>
        <span className={dirty ? 'tone-warn small' : 'muted small'}>{dirty ? t('common.unsaved') : t('aliases.allSaved')}</span>
        <span className="toolbar-spacer" />
        <Button onClick={() => setRows(saved.map(strip))} disabled={!dirty || saving}>{t('common.revert')}</Button>
        <Button variant="primary" onClick={save} loading={saving} disabled={!dirty}>{t('aliases.save')}</Button>
      </div>
    </Card>
  );
}
