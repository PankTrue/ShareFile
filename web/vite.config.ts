import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => {
  const target = mode === 'admin' ? 'http://127.0.0.1:8081' : 'http://127.0.0.1:8080';
  return {
    plugins: [react()],
    server: {
      strictPort: true,
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
          configure(proxy) {
            // Development only: the browser uses Vite's same-origin endpoint.
            proxy.on('proxyReq', (request) => request.setHeader('Origin', target));
          },
        },
        '/s/': {
          target,
          changeOrigin: true,
          configure(proxy) {
            proxy.on('proxyReq', (request) => request.setHeader('Origin', target));
          },
        },
      },
    },
    build: { assetsInlineLimit: 0, sourcemap: false },
  };
});
