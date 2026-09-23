// A block preview that starts with injected context (a <system-reminder>,
// a slash-command tag...) reads as "Injected · Environment" instead of raw tags.
import { useI18n } from '../../i18n';
import { Icon } from '../../icons/Icon';
import { injectedPreview } from '../../lib/injected';

export function PreviewText({ text }: { text: string | undefined }) {
  const { t } = useI18n();
  const inj = injectedPreview(text);
  if (!inj) return <>{text}</>;
  const kind = inj.kind === 'other' ? inj.title || inj.tag : t(`inject.kind.${inj.kind}`);
  return (
    <span className="inject-mini" title={text}>
      <Icon name="layers" size={11} /> {t('inject.short', { kind })}
    </span>
  );
}
