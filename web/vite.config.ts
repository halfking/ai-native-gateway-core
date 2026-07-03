import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// vite.config.ts — extended with vitest `test` field (v6.0 audit T11, 2026-06-22)
// Vitest 1.x auto-detects a `test` field in vite.config.ts, so we keep
// one config file instead of duplicating plugin/env across vite.config.ts
// and vitest.config.ts. This also means `vite build` and `vitest run`
// share the same plugin list (vue), avoiding drift.
export default defineConfig({
  plugins: [vue()],
  define: {
    // 2026-07-03: Enable vue-i18n JIT compilation in production
    // DO NOT set __INTLIFY_DROP_MESSAGE_COMPILER__ to true when using JIT mode
    __INTLIFY_JIT_COMPILATION__: true,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5780,
    proxy: {
      '/api': { target: 'http://localhost:8781', changeOrigin: true },
      '/v1':  { target: 'http://localhost:8781', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8781', changeOrigin: true },
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.{test,spec}.ts'],
    exclude: ['node_modules/**', 'dist/**'],
  },
})