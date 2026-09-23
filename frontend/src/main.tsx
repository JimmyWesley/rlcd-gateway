import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './lib/theme';
import './styles/tokens.css';
import './styles/base.css';
import './styles/components.css';
import './styles/layout.css';
import './styles/pages.css';
import { App } from './App';
import { I18nProvider } from './i18n';
import { GatewayProvider } from './state/gateway';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
      <GatewayProvider>
        <App />
      </GatewayProvider>
    </I18nProvider>
  </StrictMode>,
);
