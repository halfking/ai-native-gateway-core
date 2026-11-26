/**
 * RequestLogsView.window.contract.spec.ts —— 「页头时间范围选择器显示的窗口 ≡ 实际查询
 * 的窗口」的接线契约门（2026-11-25 request-logs 根修轮）。
 *
 * 存在理由（211 实测，Maestro 第二层探针撞出）：纯函数的边界由
 * utils/requestLogsWindow.spec.ts 钉住；但「视图到底有没有用它们」是另一回事——
 * 视图里曾经有一份本地的 rollingWindowMinute（按天算、被传了小时数 ⇒ 默认档显示成
 * 24 天）、一张把「近 7 天」映射到滚动 d7 的内联映射表、以及一段原样截断日界的
 * custom 分支。本门钉住这三处**都走 utils**，且旧写法不再回潮。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const VIEW_SRC = readFileSync(resolve(process.cwd(), 'src/views/RequestLogsView.vue'), 'utf8')

/** 去掉注释，只留可执行代码（先剥行注释再剥块注释，理由见 loadGuard 契约门头注）。 */
function stripComments(src: string): string {
  return src
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
}

describe('页头时间范围选择器 ⇒ 查询窗口（查询 ≡ 显示）', () => {
  it('滚动小时窗的显示走 utils（本地的 rollingWindowMinute 不再存在）', () => {
    const code = stripComments(VIEW_SRC)
    expect(code).toContain("import {\n  endOfDay,\n  headerPresetById,\n  pickerRangeToQueryWindow,\n  rollingHoursWindow,\n} from '../utils/requestLogsWindow'")
    expect(code).toMatch(/return rollingHoursWindow\(now, hours\[timePreset\.value\]!\)/)
    expect(code).not.toMatch(/function rollingWindowMinute\(/)
    expect(code).not.toContain('rollingWindowMinute(')
  })

  it('档位映射走 utils 的表（不再有把近 7 天映射到 d7 的内联表）', () => {
    const code = stripComments(VIEW_SRC)
    expect(code).toContain('const next = headerPresetById[matched]')
    expect(code).not.toMatch(/last7d:\s*'d7'/)
    expect(code).not.toMatch(/const presetById/)
  })

  it('custom 分支的窗口经 pickerRangeToQueryWindow（日界补齐到 23:59:59.999）', () => {
    const code = stripComments(VIEW_SRC)
    const apply = code.slice(code.indexOf('function onHeaderRangeApply('), code.indexOf('\n}\n', code.indexOf('function onHeaderRangeApply(')))
    expect(apply).toContain('const { start: s, end: e } = pickerRangeToQueryWindow(range)')
    expect(apply).toContain('customDateRange.value = [s.toISOString(), e.toISOString()]')
    expect(apply).not.toContain('parseLocalMinute(range.start)')
  })

  it('「本月」的查询到今天结束（与显示的「~ 今天 23:59」同口径）', () => {
    const code = stripComments(VIEW_SRC)
    const timeRange = code.slice(code.indexOf('function timeRange()'), code.indexOf('\n}\n', code.indexOf('function timeRange()')))
    const thisMonth = timeRange.slice(timeRange.indexOf("case 'thisMonth':"), timeRange.indexOf("case 'thisYear':"))
    expect(thisMonth).toContain('end.setTime(endOfDay(now).getTime())')
  })

  // ★ 2026-11-26 修「手选日期被弹回」（211 报障：选 2026-11-24 无效、点刷新还原）：
  //   本页时间状态链是分钟级（'YYYY-MM-DD HH:mm'），页头选择器却漏传 precision
  //   ⇒ 按 date 精度渲染 ⇒ 手选产出纯日期串 'YYYY-MM-DD' ⇒ parseLocalMinute 按
  //   UTC 解析（parseLocalMinute 对 10 字符串补 T00:00:00Z，datetime.ts:65）⇒
  //   UTC+8 下起=止=08:00 ⇒ onHeaderRangeApply 的 e<=s 守卫把选择静默丢弃 ⇒
  //   面板弹回旧值。⇒ precision="datetime" 必须显式在场；再被删掉时本门转红。
  it('页头选择器显式 precision="datetime"（本页状态链是分钟级；漏传 ⇒ 手选日期被静默丢弃）', () => {
    const code = stripComments(VIEW_SRC)
    const header = code.slice(code.indexOf('<KxDateRangePicker'), code.indexOf('/>', code.indexOf('<KxDateRangePicker')))
    expect(header).toContain('<KxDateRangePicker')
    expect(header).toContain('precision="datetime"')
  })
})
