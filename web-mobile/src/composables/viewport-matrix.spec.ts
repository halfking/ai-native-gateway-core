import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  BREAKPOINT_MEDIUM_PX,
  BREAKPOINT_EXPANDED_PX,
  BREAKPOINT_LARGE_PX,
} from './useWindowClass'
import { auditEntryUrl, LARGE_HANDOFF_PX } from '../../scripts/audit-entry-url.mjs'

// 视口档位矩阵门禁（UI规范 15 §2.1 ＋ 10 §4.6.61）。
//
// 为什么需要这道门（一次真实的自造缺陷）：§2.1 那张「视口 → 语义档」表
// 最初是**手写**的，而且拿 `01 §2` 的 `768/1024/1440` 当依据 ——
// 那是**桌面 `web/`** 的断点。`/m/*` 走的是 `web-mobile/src/composables/useWindowClass.ts`
// 的 `600/960/1280`（该文件头注明「不要拿一套去改另一套」）。
// ⇒ 14 行里有 4 行贴错档，最要命的一行是 `740×360`：
//   被写成 compact（横屏手机按 compact 设计），实际是 medium
//   —— 而 medium 档**不渲染吸底栏**（`showBottomNav = windowClass === 'compact'`）。
//   实测（§4.6.61）：`914×411` 与 `740×360` 的 fixed 元素只有顶栏，
//   `nav.bottomnav` 不在 DOM 里；只有 `568×320` 在。
//
// 门只钉两件**可机械判定**的事，不碰「这一档专门抓什么」那种设计意图：
//   ① 每一行的语义档 == 用 `useWindowClass` 真实常量算出来的档；
//   ② 表里的视口集合 == `layout-audit-web.mjs` 默认 `--sizes`（双向）。
//      少登记一档 ⇒ 那一档从此没人量，而文档看起来还是齐的。

const DOC = resolve(process.cwd(), '../docs/UI规范/15-真机验收与落地路线.md')
const DRIVER = resolve(process.cwd(), 'scripts/layout-audit-web.mjs')
const ENTRY_SWITCH = resolve(process.cwd(), 'public/entry-switch.js')

/** 与 useWindowClass.classify 同构，但**只依赖导出的常量**（不碰 window）。 */
function classify(w: number): 'compact' | 'medium' | 'expanded' | 'large' {
  if (w >= BREAKPOINT_LARGE_PX) return 'large'
  if (w >= BREAKPOINT_EXPANDED_PX) return 'expanded'
  if (w >= BREAKPOINT_MEDIUM_PX) return 'medium'
  return 'compact'
}

interface Row { w: number; h: number; cls: string; line: number }

/** 抓 §2.1 表的数据行：`| \`320×800\` | compact + small | … |`，容忍 ★ 与 ** 强调。 */
function readTable(): Row[] {
  const lines = readFileSync(DOC, 'utf8').split('\n')
  const rows: Row[] = []
  lines.forEach((line, i) => {
    const m = /^\|\s*`(\d+)×(\d+)`\s*\|\s*([^|]+?)\s*\|/.exec(line)
    if (!m) return
    // `m[3]` 在 noUncheckedIndexedAccess 下是 string | undefined —— 兜成空串，
    // 让它落进下面的「表必须被抓到」自证里，而不是抛 TypeError 把诊断信息吃掉
    const cls = (m[3] ?? '').replace(/★/g, '').replace(/\*/g, '').trim().split(/[\s+]+/)[0] ?? ''
    rows.push({ w: Number(m[1]), h: Number(m[2]), cls, line: i + 1 })
  })
  return rows
}

describe('§2.1 视口矩阵', () => {
  const rows = readTable()

  it('表必须被抓到（解析器不许空转 —— 空数组会让下面每条断言恒真）', () => {
    expect(rows.length, '§2.1 视口表没抓到任何数据行').toBeGreaterThanOrEqual(14)
  })

  it.each(rows.map((r) => [r, `${r.w}×${r.h}`] as const))(
    '%s 的语义档必须等于按 useWindowClass 常量算出的档',
    (r) => {
      expect(
        r.cls,
        `§2.1 第 ${r.line} 行 ${r.w}×${r.h} 标成 ${r.cls}，` +
        `但 BREAKPOINT_MEDIUM/EXPANDED/LARGE = ${BREAKPOINT_MEDIUM_PX}/${BREAKPOINT_EXPANDED_PX}/${BREAKPOINT_LARGE_PX} ` +
        `算出来是 ${classify(r.w)}（移动端不是 01 §2 的 768/1024/1440）`,
      ).toBe(classify(r.w))
    },
  )

  it('横屏三档必须在表里，且三档覆盖 compact/medium 两种语义', () => {
    const land = rows.filter((r) => r.w > r.h)
    expect(land.map((r) => r.w), '横屏视口不足三档').toEqual(expect.arrayContaining([568, 740, 914]))
    // 少了 568×320 就量不到吸底栏：横握手机宽度多在 568~932，medium 从 600 起
    expect(land.map((r) => classify(r.w)), '横屏三档必须含一个 compact').toContain('compact')
  })

  it('视口集合必须与 layout-audit-web.mjs 的默认 --sizes 完全一致', () => {
    const m = /const SIZES = arg\('sizes',\s*'([^']+)'\)/.exec(readFileSync(DRIVER, 'utf8'))
    expect(m, 'layout-audit-web.mjs 里找不到 SIZES 默认值').not.toBeNull()
    const driverSizes = new Set((m?.[1] ?? '').split(',').filter(Boolean))
    const docSizes = new Set(rows.map((r) => `${r.w}x${r.h}`))
    const onlyDoc = [...docSizes].filter((s) => !driverSizes.has(s))
    const onlyDriver = [...driverSizes].filter((s) => !docSizes.has(s))
    expect(
      { onlyDoc, onlyDriver },
      '文档与审计脚本的视口集合对不上：只在文档里的档从此没人量，只在脚本里的档没人解释',
    ).toEqual({ onlyDoc: [], onlyDriver: [] })
  })

  // ── large 交接阈值的三份镜像（§4.6.75）─────────────────────────────
  // entry-switch.js 自己在文件头要求「改断点必须三处同改」：
  //   ① public/entry-switch.js 的 `(min-width: 1280px)` 字面量
  //   ② useWindowClass.ts 的 BREAKPOINT_LARGE_PX
  //   ③ scripts/audit-entry-url.mjs 的 LARGE_HANDOFF_PX（判据侧）
  // 少改一处的后果不是「不一致」，而是**判据悄悄量错表面**。
  it('large 交接阈值三处镜像一致', () => {
    const entrySwitch = readFileSync(ENTRY_SWITCH, 'utf8')
    const m = /min-width:\s*(\d+)px/.exec(entrySwitch)
    expect(m, 'entry-switch.js 里找不到 min-width 阈值').not.toBeNull()
    expect(Number(m?.[1]), 'entry-switch.js 的 large 阈值与 useWindowClass 不一致').toBe(BREAKPOINT_LARGE_PX)
    expect(LARGE_HANDOFF_PX, '判据侧 LARGE_HANDOFF_PX 与 useWindowClass 不一致').toBe(BREAKPOINT_LARGE_PX)
  })

  it('≥1280 的根入口判据必须显式走 ?mobile（平板显式选移动端是受支持路径）', () => {
    // 行为断言，不是源码 grep：把规则当函数量。
    expect(auditEntryUrl('/m/', 1280)).toEqual({ url: '/m/?mobile', entryMode: 'mobile-forced', forceMobile: true })
    expect(auditEntryUrl('/m/', 1440).forceMobile).toBe(true)
    // 断点之下不该多此一举
    expect(auditEntryUrl('/m/', 1194)).toEqual({ url: '/m/', entryMode: 'auto', forceMobile: false })
    expect(auditEntryUrl('/m/', 411).url).toBe('/m/')
    // 深链不进这条规则（entry-switch 同样只切根入口）
    expect(auditEntryUrl('/m/usage', 1440).url).toBe('/m/usage')
    expect(auditEntryUrl('/m/keys', 1280).forceMobile).toBe(false)
  })

  it('判据真的用上了这条规则（防止模块建了却没接）', () => {
    const src = readFileSync(DRIVER, 'utf8')
    expect(src, 'driver 没有 import auditEntryUrl').toContain("from './audit-entry-url.mjs'")
    expect(src, 'driver 导航仍写死 ORIGIN + route').not.toMatch(/Page\.navigate',\s*\{\s*url:\s*ORIGIN \+ route\s*\}/)
    expect(src, 'driver 没有把入口模式记进行里（报告将无法自证走的是哪条腿）').toContain('entryMode: entry.entryMode')
    // 行对象必须整体带进报告：driver 曾经在这里从零重建 `{ route, reqW, … }`，
    // 把 entryMode 静默丢掉 —— 源码里「写了这个字段」与报告里「有这个字段」是两件事。
    expect(
      src,
      'driver 的 rows.push 从零重建行对象会丢掉 entryMode，必须展开 ...row',
    ).not.toMatch(/rows\.push\(\{\s*route,\s*reqW/)
    expect(src, 'driver 没有展开 ...row').toMatch(/rows\.push\(\{\s*\.\.\.row,/)
  })
})
