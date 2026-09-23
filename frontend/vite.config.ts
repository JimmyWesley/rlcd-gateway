import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The gateway serves the built dashboard at /ui/ and embeds it in the binary.
// In dev, Vite serves the UI and forwards /api to a running gateway.
const gateway = process.env.RLCD_GATEWAY_URL ?? 'http://127.0.0.1:4777';

export default defineConfig({
  base: '/ui/',
  plugins: [react()],
  build: { outDir: '../gateway/internal/web/dist', emptyOutDir: true },
  server: {
    port: 5177,
    proxy: {
      // changeOrigin: the gateway only accepts requests addressed to itself.
      '/api': { target: gateway, changeOrigin: true },
    },
  },
});
