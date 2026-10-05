import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Hyper 移动前端（UI规范 01 §3 硬约束）：iOS 15 WebView / Safari 15 不支持
// Media Queries Level 4 范围语法。Vite 默认 cssTarget 会把传统
// @media (max-width: …) 压成 (width<=…) 导致整层响应式在解析期被丢弃。
// target / cssTarget 必须钉住 safari15；scripts/verify-css-media-syntax.mjs
// 在 build 前兜底扫描范围语法计数（必须为 0）。
export default defineConfig(({ command }) => ({
  plugins: [vue()],
  // 生产（build）：资产走 /m-assets/*（网关 /m 挂载，镜像 maintain 先例）；
  // 开发（serve）：base '/'，应用在根路径跑（router base 运行时探测 appBase()）。
  base: command === 'build' ? '/m-assets/' : '/',
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    target: ['es2020', 'safari15'],
    cssTarget: ['safari15'],
  },
  server: {
    port: 5790,
    proxy: {
      '/api': { target: process.env.GATEWAY_API_TARGET ?? 'http://127.0.0.1:8782', changeOrigin: false },
      '/healthz': { target: process.env.GATEWAY_API_TARGET ?? 'http://127.0.0.1:8782', changeOrigin: false },
      '/readyz': { target: process.env.GATEWAY_API_TARGET ?? 'http://127.0.0.1:8782', changeOrigin: false },
      '/version': { target: process.env.GATEWAY_API_TARGET ?? 'http://127.0.0.1:8782', changeOrigin: false },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    testTimeout: 20000,
    include: ['src/**/*.{spec,test}.ts'],
  },
}))
