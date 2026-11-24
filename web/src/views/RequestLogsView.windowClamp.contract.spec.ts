// RequestLogsView.windowClamp.contract.spec.ts —— 请求日志页的「服务端截窗必须如实显示」
// 前端契约（2026-11-25 第 28 轮 24h 审计 P1）。
//
// ★ 背景：含探测的列表查询在服务端有窗口上限（admin/logs.go 的 logsProbeWindowCap）。
//   所选时间范围超过上限时，服务端只统计最近 N 小时，并以 window_clamped /
//   window_clamp_hours 如实回传 ⇒ 而此前 web/src 里**没有任何调用方读这两个字段**
//   ⇒ 页面默认 24h 选择下会静默显示 12h 的合计（标题说 24h、数字只有 12h）。
//
// ★ 为什么用源码契约而不是 mount：与同族做法一致
//   （RequestLogsView.scanOutcome.contract.spec.ts、deepLinkFilters.contract.spec.ts）。
//   本门钉的是「视图有没有按响应值接线 ＋ 文案有没有走 i18n」，不钉渲染像素。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

/** 去掉注释，只留可执行代码（防「文档写清了缺陷」被误判成「缺陷修好了」）。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map(line => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

const LOCALES = ['zh-CN', 'en-US', 'zh-TW', 'ja-JP', 'de-DE', 'es-ES', 'fr-FR', 'ar-SA'] as const

/** 取出 `probeScope: { … }` 这一个对象字面量的正文（花括号配对）。 */
function probeScopeBlock(src: string): string {
  const at = src.indexOf('probeScope: {')
  if (at === -1) throw new Error('probeScope block not found')
  let depth = 0
  let i = src.indexOf('{', at)
  for (; i < src.length; i++) {
    const c = src[i]
    if (c === '{') depth++
    else if (c === '}') {
      depth--
      if (depth === 0) break
    }
  }
  return src.slice(at, i + 1)
}

describe('P1 含探测窗口被服务端截断时，页面必须披露实际统计窗口', () => {
  it('① 响应类型声明 window_clamped / window_clamp_hours', async () => {
    const api = await read('api/logs.ts')
    expect(api).toMatch(/window_clamped\?: boolean/)
    expect(api).toMatch(/window_clamp_hours\?: number/)
  })

  it('② 视图认响应值置位（不按本地选择推算），失败路径复位', async () => {
    const code = stripComments(await read('views/RequestLogsView.vue'))
    expect(code).toMatch(/windowClamped\.value = resp\.window_clamped === true/)
    // 上限小时数同样认响应值，不写死
    expect(code).toMatch(/windowClampHours\.value = resp\.window_clamp_hours/)
    // catch 分支必须复位（避免上一次的徽标留在错误态上）
    const resetAt = code.indexOf('windowClamped.value = false')
    expect(resetAt, 'catch 分支缺 windowClamped 复位').toBeGreaterThan(-1)
    const catchAt = code.indexOf('} catch (e: unknown) {', code.indexOf('async function load()'))
    expect(catchAt, '找不到 load() 的 catch 分支').toBeGreaterThan(-1)
    expect(resetAt).toBeGreaterThan(catchAt)
    expect(resetAt).toBeLessThan(code.indexOf('loading.value = false', catchAt))
  })

  it('③ 徽标由 windowClamped 门控、带 data-testid、文案走 i18n 且带 hours 实参', async () => {
    const tpl = stripComments(await read('views/RequestLogsView.vue'))
    expect(tpl).toMatch(/v-if="windowClamped"\s+data-testid="logs-window-clamped-badge"/)
    expect(tpl).toContain("t('requests.list.probeScope.windowClamped', { hours: windowClampHours })")
    expect(tpl).toContain(
      "t('requests.list.probeScope.windowClampedHint', { hours: windowClampHours })",
    )
  })

  it('④ 八个语种都定义这两个键，且 {hours} 占位符个数一致（徽标 1 个、提示 3 个）', async () => {
    for (const loc of LOCALES) {
      const block = probeScopeBlock(await read(`locales/${loc}/requests.ts`))
      expect((block.match(/windowClamped: '/g) ?? []).length, loc).toBe(1)
      expect((block.match(/windowClampedHint: '/g) ?? []).length, loc).toBe(1)
      const badge = /windowClamped: '([^\n]*)',\n/.exec(block)?.[1] ?? ''
      const hint = /windowClampedHint: '([^\n]*)',?\n/.exec(block)?.[1] ?? ''
      expect((badge.match(/\{hours\}/g) ?? []).length, `${loc} 徽标占位符`).toBe(1)
      expect((hint.match(/\{hours\}/g) ?? []).length, `${loc} 提示占位符`).toBe(3)
    }
  })
})
