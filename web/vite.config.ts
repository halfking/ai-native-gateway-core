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
    proxy: (() => {
      // 2026-10-03：本地实测目标端口是 8782（compose 里 gateway-v2 映射），
      // 而 8781 已经没有进程在跑。默认值保持 8781 不动（不破坏既有工作流），
      // 需要对着实际在跑的那个网关做页面审计时用
      // GATEWAY_API_TARGET=http://localhost:8782 npm run dev 覆盖。
      const API = process.env.GATEWAY_API_TARGET || 'http://localhost:8781'
      // 本地 8782 的 ADMIN_PASSWORD 为空 → POST /api/auth/token 登录不可用
      // （见 docs/06-deployment/01-environments/local-8782-env-state-20260905.md §3）。
      // 需要逐页审计时，把自签的 admin JWT 放进这个环境变量，由 dev 代理注入
      // Authorization 头 —— 浏览器侧不需要、也不应该出现任何凭据。
      // 不设这个变量时代理行为与从前完全一致。
      const DEV_TOKEN = process.env.GATEWAY_DEV_AUTH_TOKEN
      const auth = DEV_TOKEN
        ? { headers: { Authorization: `Bearer ${DEV_TOKEN}` } }
        : {}

      // 本地 8782 处于「未激活」态（/api/system/bootstrap/status → activated:false），
      // 于是 router 的 bootstrap 门把每一个业务页都重定向到 /bootstrap，页面根本
      // 到不了。逐页审计需要绕过这道门。
      //
      // 做法：只在 dev 代理层把这个状态端点覆写成已激活。好处是——不动后端真实
      // 状态、不动浏览器 localStorage，而且走的是应用自己的分支：门读到
      // activated:true 后会自己写下 llmgw_activated='1'，后续导航不再打这个端点。
      // 显式 opt-in（GATEWAY_ASSUME_ACTIVATED=1），默认不启用。
      const ASSUME_ACTIVATED = process.env.GATEWAY_ASSUME_ACTIVATED === '1'
      const bootstrapStatusOverride = {
        '/api/system/bootstrap/status': {
          target: API,
          changeOrigin: true,
          ...auth,
          configure(proxy: any) {
            proxy.on('proxyReq', (proxyReq: any) => {
              proxyReq.setHeader('X-GW-Audit-Assume-Activated', '1')
            })
          },
          bypass(_req: any, res: any) {
            res.setHeader('Content-Type', 'application/json')
            res.end(JSON.stringify({ activated: true, registered: true, admin_bootstrap_required: false }))
          },
        },
      }

      return {
        // ★ 顺序要紧：vite 代理按对象键顺序做**前缀**匹配，先命中先处理。
        // 具体的 /api/system/bootstrap/status 必须排在通用的 /api 之前，
        // 否则会被 /api 吃掉，覆写静默失效（实测过一次）。
        ...(ASSUME_ACTIVATED ? bootstrapStatusOverride : {}),
        // 2026-07-09: rewrite cookie Domain so Set-Cookie from `localhost:8781`
        // is accepted by the browser running on `127.0.0.1:5783` (and vice versa).
        // Without this, dev-mode SPA can call /api/* but the auth cookie never
        // sticks, so the browser stays "logged out" even after a successful login.
        '/api': { target: API, changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' }, ...auth },
        '/v1': { target: API, changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' } },
        '/healthz': { target: API, changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' } },
        // 2026-10-03：SystemStatusIndicator 会并发打 /healthz 与 /readyz。
        // /readyz 原来不在白名单里，请求落到 vite 自身 → 返回 index.html →
        // 前端 JSON.parse('<!DOCTYPE ...') 抛错，页面控制台常驻一条 error，
        // 且 readiness 面板永远拿不到 DB/Redis 状态。
        '/readyz': { target: API, changeOrigin: true, cookieDomainRewrite: { '*': '127.0.0.1' } },
        '/maintain-api': {
          target: 'http://127.0.0.1:8082',
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/maintain-api/, '/maintain-api'),
        },
        '/artifacts': { target: 'http://127.0.0.1:8082', changeOrigin: true },
      }
    })(),
  },
  // @ts-ignore - vitest config is valid but not in vite's types
  test: {
    globals: true,
    environment: 'jsdom',
    // 2026-09-13: jsdom lacks window.matchMedia; stub it globally for
    // useBreakpoint / theme detection tests (see src/test/setup.ts).
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.ts'],
    exclude: ['node_modules/**', 'dist/**'],
  },
})