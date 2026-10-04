import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// vite.config.ts — web-mobile 构建配置。
// 挂载在网关同源 /m/*（MobileStaticHandler），base 必须是 '/m/'，
// 否则产物资源引用 /assets/... 会被桌面 SPA 的 static handler 吃掉。
// cssTarget/build.target 含 safari15：UI 规范 01 §3 —— Vite 默认 cssTarget 会把
// 传统 @media (max-width: …) 压成 Media Queries Level 4 范围语法，iOS 15
// WebView/Safari 15 解析期整层丢弃（iPhone 6s 实证）。
export default defineConfig({
  base: '/m/',
  plugins: [vue()],
  build: {
    target: ['es2020', 'safari15'],
    cssTarget: ['safari15'],
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5176,
    proxy: {
      // 本地开发时代理到 deploy-local 的网关（默认 8782）。
      '/api': { target: 'http://127.0.0.1:8782', changeOrigin: false },
      '/healthz': { target: 'http://127.0.0.1:8782', changeOrigin: false },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
  },
})
