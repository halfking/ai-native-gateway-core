import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'path'

// vite.config.ts — extended with vitest `test` field (v6.0 audit T11, 2026-06-22)
// Vitest 1.x auto-detects a `test` field in vite.config.ts, so we keep
// one config file instead of duplicating plugin/env across vite.config.ts
// and vitest.config.ts. This also means `vite build` and `vitest run`
// share the same plugin list (vue), avoiding drift.
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  define: {
    // 2026-07-03: Enable vue-i18n JIT compilation in production
    // DO NOT set __INTLIFY_DROP_MESSAGE_COMPILER__ to true when using JIT mode
    __INTLIFY_JIT_COMPILATION__: true,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    reportCompressedSize: false,
    // 2026-09-07: echarts (gzip > 500 kB) and the route manifest push the
    // legacy 500 kB warning limit on every build. The main entry chunk is
    // dominated by __vite__mapDeps — a flat array of lazy-chunk filenames
    // emitted by Rollup; real code is already code-split into vue/element/
    // echarts/chart/i18n vendor chunks and per-route lazy chunks. Raise the
    // warning limit to 1500 kB so deploy logs stay actionable instead of
    // always red. Split echarts into its own chunk so pages that don't
    // render charts (admin tables, login, settings) never load it.
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      maxParallelFileOps: 128,
      output: {
        manualChunks: {
          // Split vendor libraries into separate chunks to improve caching
          'vue-vendor': ['vue', 'vue-router'],
          'element-vendor': ['element-plus', '@element-plus/icons-vue'],
          'i18n-vendor': ['vue-i18n'],
          'chart-vendor': ['chart.js'],
          'echarts-vendor': ['echarts', 'vue-chartjs'],
        },
      },
    },
  },
  server: {
    port: 5780,
    proxy: {
      // 2026-07-09: rewrite cookie Domain so Set-Cookie from `localhost:8781`
      // is accepted by the browser running on `127.0.0.1:5783` (and vice versa).
      // Without this, dev-mode SPA can call /api/* but the auth cookie never
      // sticks, so the browser stays "logged out" even after a successful login.
      '/api': {
        target: 'http://localhost:8781',
        changeOrigin: true,
        cookieDomainRewrite: { '*': '127.0.0.1' },
      },
      '/v1':  { target: 'http://localhost:8781', changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' } },
      '/healthz': { target: 'http://localhost:8781', changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' } },
      '/maintain-api': {
        target: 'http://127.0.0.1:8082',
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/maintain-api/, '/maintain-api'),
      },
      '/artifacts': { target: 'http://127.0.0.1:8082', changeOrigin: true },
    },
  },
  // @ts-ignore - vitest config is valid but not in vite's types
  test: {
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.{test,spec}.ts'],
    exclude: ['node_modules/**', 'dist/**'],
  },
})