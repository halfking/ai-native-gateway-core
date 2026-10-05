import { execSync } from 'node:child_process'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// ── 部署序号构建期注入（UI规范 18 §3 / §10.3 步骤 1）──────────────────
// 取值优先级：LLMGW_DEPLOY_SEQ（显式）→ BUILD_SEQ（scripts/bump-version.sh
// source 后导出，接既有 build_seq 管线，不新造序号源）→ git 短 SHA（构建
// 工具的版本标识）→ 'unknown'（字面量，运行期界面标为「不可判定」）。
// web/vite.config.ts 有同形实现（两项目独立 npm 包，无共享包目标）。
function resolveDeploySeq(): { seq: string; src: 'build-seq' | 'git-sha' | 'unknown' } {
  const explicit = process.env.LLMGW_DEPLOY_SEQ
  if (explicit) return { seq: explicit, src: 'build-seq' }
  const buildSeq = process.env.BUILD_SEQ
  if (buildSeq) return { seq: buildSeq, src: 'build-seq' }
  try {
    const sha = execSync('git rev-parse --short=8 HEAD', {
      stdio: ['ignore', 'pipe', 'ignore'],
    })
      .toString()
      .trim()
    if (sha) return { seq: sha, src: 'git-sha' }
  } catch {
    /* 无 git 可用（如浅克隆丢了 .git）——落 unknown */
  }
  return { seq: 'unknown', src: 'unknown' }
}

function llmgwDeploySeq(): Plugin {
  const apply = (html: string): string => {
    const { seq, src } = resolveDeploySeq()
    return html.replace('__LLMGW_DEPLOY_SEQ__', seq).replace('__LLMGW_DEPLOY_SEQ_SRC__', src)
  }
  return {
    name: 'llmgw-deploy-seq',
    transformIndexHtml(html) {
      return apply(html)
    },
  }
}

// Hyper 移动前端（UI规范 01 §3 硬约束）：iOS 15 WebView / Safari 15 不支持
// Media Queries Level 4 范围语法。Vite 默认 cssTarget 会把传统
// @media (max-width: …) 压成 (width<=…) 导致整层响应式在解析期被丢弃。
// target / cssTarget 必须钉住 safari15；scripts/verify-css-media-syntax.mjs
// 在 build 前兜底扫描范围语法计数（必须为 0）。
export default defineConfig(({ command }) => ({
  plugins: [vue(), llmgwDeploySeq()],
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
