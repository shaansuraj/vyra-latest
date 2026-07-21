import tailwindcss from '@tailwindcss/vite';
import devtools from 'solid-devtools/vite';
import { defineConfig } from 'vite';
import solidPlugin from 'vite-plugin-solid';

export default defineConfig(({ mode }) => {
  const relayProxyTarget = process.env.VITE_RELAY_PROXY_TARGET || 'http://127.0.0.1:8020';
  const plugins = [solidPlugin(), tailwindcss()];
  if (mode === 'development') {
    plugins.unshift(devtools());
  }
  const allowedHosts = [
    'vyracam.in',
    'www.vyracam.in',
    'app.vyracam.in',
  ];
  const relayProxyPaths = [
    '/flags',
    '/auth',
    '/api',
    '/agents',
    '/camera-groups',
    '/cameras',
    '/nodes',
    '/client',
    '/events',
    '/daily-summary',
    '/timeline',
    '/system',
    '/managed-nodes',
    '/persons',
    '/query',
    '/storage',
  ];

  const proxy = Object.fromEntries(
    relayProxyPaths.map((path) => [
      path,
      {
        target: relayProxyTarget,
        changeOrigin: true,
        ws: path === '/client',
      },
    ]),
  );

  return {
    plugins,
    server: {
      host: '0.0.0.0',
      port: 3000,
      allowedHosts,
      proxy,
    },
    build: {
      target: 'esnext',
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (!id.includes('node_modules')) return undefined;
            if (id.includes('chart.js')) return 'vendor-charts';
            if (id.includes('three')) return 'vendor-three';
            if (id.includes('@ark-ui')) return 'vendor-ark';
            if (id.includes('solid-js')) return 'vendor-solid';
            return undefined;
          },
        },
      },
    },
    appType: 'spa',
  };
});
