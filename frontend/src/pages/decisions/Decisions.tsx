// Decisions: an audit of the economy model's calls (System One), proxied
// through the gateway. Reserved in the navigation until the API lands.
import { useI18n } from '../../i18n';
import { Card, EmptyState, PageHeader } from '../../ui';

export function Decisions() {
  const { t } = useI18n();
  return (
    <div className="page">
      <PageHeader title={t('nav.decisions')} description={t('decisions.desc')} />
      <Card>
        <EmptyState icon="cpu" title={t('decisions.soon.title')}>
          <p>{t('decisions.soon.body')}</p>
        </EmptyState>
      </Card>
    </div>
  );
}
