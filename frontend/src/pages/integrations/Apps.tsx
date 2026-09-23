// Quick start for any app: point an SDK's base URL at the gateway.
import { useEffect, useMemo, useState } from 'react';
import { useI18n } from '../../i18n';
import { BrandIcon } from '../../icons/BrandIcon';
import { routerApi, type AliasView, type RouterRoute } from '../../lib/routerApi';
import { useGateway } from '../../state/gateway';
import { Card, CopyField, Field, Loading, Segmented } from '../../ui';

type Lang = 'python' | 'node' | 'curl' | 'anthropic' | 'env';
const LANGS: { id: Lang; icon: string; label: string }[] = [
  { id: 'python', icon: 'openai', label: 'OpenAI Python' },
  { id: 'node', icon: 'openai', label: 'OpenAI Node' },
  { id: 'curl', icon: 'curl', label: 'curl' },
  { id: 'anthropic', icon: 'anthropic', label: 'Anthropic Python' },
  { id: 'env', icon: 'terminal', label: 'env' },
];

export function Apps() {
  const { t, tn } = useI18n();
  const { config } = useGateway();
  const [aliases, setAliases] = useState<AliasView[]>([]);
  const [routes, setRoutes] = useState<RouterRoute[]>([]);
  const [lang, setLang] = useState<Lang>('python');
  const [model, setModel] = useState('');
  const [key, setKey] = useState('');

  useEffect(() => {
    routerApi.aliases().then((a) => {
      setAliases(a);
      setModel((m) => m || a.find((x) => x.protocols.includes('openai-chat'))?.name || '');
    }).catch(() => {});
    routerApi.routes().then(setRoutes).catch(() => {});
  }, []);

  // Beyond loopback, the address this page was opened on is the one other
  // machines use; on loopback, the bound address (the dev server differs).
  const base = config ? (config.exposed ? window.location.origin : `http://${config.listen}`) : '';
  const apiKey = key.trim() || 'rlcd-YOUR-GATEWAY-KEY';
  const oaiModel = model || 'gpt-4.1';
  const anthModel = aliases.find((a) => a.protocols.includes('anthropic-messages'))?.name || 'claude-sonnet-4-5';
  const active = routes.find((r) => r.active);
  const openaiDefault = routes.find((r) => r.openai_default);

  const snippet = useMemo(() => {
    switch (lang) {
      case 'python':
        return `from openai import OpenAI

client = OpenAI(base_url="${base}/v1", api_key="${apiKey}")
reply = client.chat.completions.create(
    model="${oaiModel}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(reply.choices[0].message.content)`;
      case 'node':
        return `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}/v1", apiKey: "${apiKey}" });
const reply = await client.chat.completions.create({
  model: "${oaiModel}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(reply.choices[0].message.content);`;
      case 'curl':
        return `curl ${base}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${oaiModel}", "messages": [{"role": "user", "content": "Hello"}]}'

# ${t('apps.modelsComment')}
curl ${base}/v1/models -H "Authorization: Bearer ${apiKey}"`;
      case 'anthropic':
        return `import anthropic

client = anthropic.Anthropic(base_url="${base}", api_key="${apiKey}")
message = client.messages.create(
    model="${anthModel}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello"}],
)
print(message.content[0].text)`;
      case 'env':
        return `# ${t('apps.envComment')}
export OPENAI_BASE_URL=${base}/v1
export OPENAI_API_KEY=${apiKey}

export ANTHROPIC_BASE_URL=${base}
export ANTHROPIC_API_KEY=${apiKey}`;
    }
  }, [lang, base, apiKey, oaiModel, anthModel, t]);

  if (!config) return <Card><Loading /></Card>;

  return (
    <>
      <div className="grid grid-2-1">
        <Card title={t('apps.title')} subtitle={t('apps.sub')}>
          <div className="form-grid">
            <Field label={t('apps.model')}>
              <select value={model} onChange={(e) => setModel(e.target.value)}>
                <option value="">{t('apps.noAlias')}</option>
                {aliases.map((a) => <option key={a.name} value={a.name}>{a.name} → {a.route}{a.model ? ` · ${a.model}` : ''}</option>)}
              </select>
            </Field>
            <Field label={t('apps.key')} hint={t('apps.keyHint')}>
              <input type="password" autoComplete="off" value={key} placeholder="rlcd-…" onChange={(e) => setKey(e.target.value)} />
            </Field>
          </div>
          <Segmented
            label={t('apps.language')}
            value={lang}
            onChange={setLang}
            options={LANGS.map((l) => ({ id: l.id, label: <span className="brand-label"><BrandIcon id={l.icon === 'terminal' ? undefined : l.icon} label={l.label} size={14} />{l.label}</span> }))}
          />
          <CopyField text={snippet} label={t('apps.snippet')} multiline />
        </Card>
        <Card title={t('apps.endpoints')}>
          <dl className="kv-list kv-list-stack">
            <div><dt>{t('apps.openaiBase')}</dt><dd><CopyField text={`${base}/v1`} label={t('apps.openaiBase')} /></dd></div>
            <div><dt>{t('apps.anthropicBase')}</dt><dd><CopyField text={base} label={t('apps.anthropicBase')} /></dd></div>
            <div><dt>{t('apps.unclaimedOpenAI')}</dt><dd>{openaiDefault ? <strong>{openaiDefault.name}</strong> : <span className="muted">{t('apps.byLogin')}</span>}</dd></div>
            <div><dt>{t('apps.unclaimedAnthropic')}</dt><dd><strong>{active?.name ?? config.active_route}</strong></dd></div>
          </dl>
        </Card>
      </div>
      <Card title={t('apps.notes')}>
        <ul className="notes">
          <li>{tn('apps.note.keys', { header: <code>X-Rlcd-Key</code> })}</li>
          <li>{tn('apps.note.formats', { kind: <code>openai</code> })}</li>
          <li>{config.exposed ? t('apps.note.exposed') : config.require_keys ? t('apps.note.required') : t('apps.note.optional')}</li>
        </ul>
      </Card>
    </>
  );
}
