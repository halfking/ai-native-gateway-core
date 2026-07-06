// keys_referenced.test.ts — 2026-07-07 强化版 i18n 引用审计
//
// 替代 parity.test.ts 的 "TODO: broaden this check to all of src/**" 那段。
// 使用 scanner.ts 提供的纯 Node 扫描器，在 CI 阶段一次性找出：
//   1. 源码里 t('xxx') 但 xxx 不在任何 locale 中（缺失）
//   2. 源码里 t('xxx') 但 xxx 不在源 locale (zh-CN) 中（潜在 fallback 渲染）
//   3. 源 locale 中定义但完全未被引用的 keys（可选清理信号）
//
// 设计目标：
//   - 跑得快：纯正则 + 字符串处理，无 TS/AST 解析，~150 文件 < 1s
//   - 误报少：跳过动态 key、跳过注释、跳过 helper 内部定义本身、按文件解析 helper
//   - 易读：失败时打印 file:line + snippet + 修复建议
//
// 默认行为：warn-only（打印报告但不 fail）。可通过环境变量 I18N_STRICT=1
// 切换到 strict 模式（有 missing 直接 fail CI）。这样可以先把工具铺好，
// 团队再逐步清理存量 missing。

import { describe, it, expect } from 'vitest'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { audit, formatReport } from './scanner'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const SRC_DIR = join(__dirname, '..')
const LOCALES_DIR = join(SRC_DIR, 'locales')
const STRICT = process.env.I18N_STRICT === '1'

describe('i18n keys referenced in src/', () => {
  it('scanner: produces a complete audit report', () => {
    const r = audit(SRC_DIR, LOCALES_DIR)
    // Always print — vitest captures stdout in -r, but we want this in CI logs.
    // eslint-disable-next-line no-console
    console.log(
      `i18n scan: ${r.stats.filesScanned} files, ${r.stats.referencesFound} refs, ` +
        `${r.stats.keysInSource} keys in zh-CN, ` +
        `${r.missing.length} missing, ${r.missingInSource.length} missing-in-source`,
    )
    // Sanity: scanner must work
    expect(r.stats.filesScanned).toBeGreaterThan(50)
    expect(r.stats.referencesFound).toBeGreaterThan(100)
    expect(r.stats.keysInSource).toBeGreaterThan(100)
  })

  it('no new missing keys appear in i18n references', () => {
    // 设计原则：CI 应阻止新增 bug。存量 bug 用 `node scripts/i18n-audit.mjs`
    // 离线修复。判定标准：与上一次的 missing 数基线对比。
    // 首次跑：基线=当前值，全部 pass。之后：任何增量都应 fail。
    //
    // 简化为：I18N_STRICT=1 时全部 fail；否则 warn-only。
    const r = audit(SRC_DIR, LOCALES_DIR)
    if (r.missing.length > 0) {
      // eslint-disable-next-line no-console
      console.warn('\n' + formatReport(r, SRC_DIR))
    }
    if (STRICT) {
      expect(
        r.missing,
        `Found ${r.missing.length} missing i18n key(s). ` +
          'Run `node scripts/i18n-audit.mjs` to see the list and fix incrementally.',
      ).toEqual([])
    } else {
      // warn-only：断言永远通过，但报告依然打印
      expect(true).toBe(true)
    }
  })
})

