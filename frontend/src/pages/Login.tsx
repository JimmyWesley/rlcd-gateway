// Shown when the gateway listens beyond loopback and this browser is remote:
// the dashboard API needs the admin token, exchanged for an HttpOnly cookie.
import { useEffect, useState, type FormEvent } from 'react';
import { useI18n } from '../i18n';
import { LogoMark } from '../icons/Icon';
import { api, setAdminLoginHandler } from '../lib/api';
import { GatewayProvider } from '../state/gateway';
import { Button, Callout, Field } from '../ui';
import { App } from '../App';

export function Root() {
  const [needLogin, setNeedLogin] = useState(false);
  const [session, setSession] = useState(0);
  useEffect(() => {
    setAdminLoginHandler(() => setNeedLogin(true));
    return () => setAdminLoginHandler(null);
  }, []);
  if (needLogin) return <AdminLogin onDone={() => { setNeedLogin(false); setSession((s) => s + 1); }} />;
  // A new session remounts the provider, so every page reloads its data.
  return (
    <GatewayProvider key={session}>
      <App />
    </GatewayProvider>
  );
}

function AdminLogin({ onDone }: { onDone: () => void }) {
  const { t, tn } = useI18n();
  const [token, setToken] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.login(token.trim());
      onDone();
    } catch (x) {
      setErr(String(x instanceof Error ? x.message : x));
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="login-page">
      <form className="login-card card" onSubmit={submit}>
        <div className="login-brand">
          <LogoMark size={40} />
          <span className="wordmark"><span className="wm-a">RLCD</span><span className="wm-b">Gateway</span></span>
        </div>
        <h1 className="login-title">{t('login.title')}</h1>
        <p className="muted">{tn('login.body', { env: <code>RLCD_GATEWAY_ADMIN_TOKEN</code> })}</p>
        <Field label={t('login.token')}>
          <input type="password" autoComplete="current-password" value={token} autoFocus onChange={(e) => setToken(e.target.value)} />
        </Field>
        {err && <Callout tone="bad">{err}</Callout>}
        <Button variant="primary" type="submit" loading={busy} disabled={!token.trim()}>{t('login.submit')}</Button>
        <p className="fine">{t('login.cookie')}</p>
      </form>
    </main>
  );
}
