import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './lib/theme';
import './styles/tokens.css';
import './styles/base.css';
import './styles/components.css';
import './styles/layout.css';
import './styles/pages.css';
import { detectLocale, ensureLocale, I18nProvider } from './i18n';
import { Root } from './pages/Login';

// Load the viewer's language before the first render (English is built in).
await ensureLocale(detectLocale());

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
      <Root />
    </I18nProvider>
  </StrictMode>,
);
