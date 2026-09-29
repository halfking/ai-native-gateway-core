// dateInputUnification.test.ts — 裸日期输入守卫（2026-09-30 统一日历轮）。
// 全项目日期选择统一走 KxDateRangePicker / KxDatePicker 组件族：
//  - 禁止新增裸 <input type="date" | "datetime-local" | "month">；
//  - el-date-picker 只允许出现在两个 Kx 组件内部（页面一律用封装）。
// 存量待迁移页面记在 PENDING_MIGRATION 白名单中，随迁移清空（ratchet 只收不放）。
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// 定位 web/src：vitest 的 cwd 可能是 web/ 也可能是仓库根（import.meta.url 在 jsdom 下非 file 协议，不可用）
function findSrc(): string {
  for (const c of [resolve(process.cwd(), 'src'), resolve(process.cwd(), 'web/src')]) {
    if (statSync(c, { throwIfNoEntry: false })?.isDirectory()) return c
  }
  throw new Error('dateInputUnification: cannot locate web/src')
}
const SRC = findSrc()

/** 唯一合法宿主：统一组件族自身。 */
const KX_HOSTS = new Set([
  'components/ui/KxDateRangePicker.vue',
  'components/ui/KxDatePicker.vue',
])

/** 存量待迁移白名单（ratchet：迁一处删一行，最终清空）。 */
const PENDING_MIGRATION = new Set<string>([])

const NATIVE_INPUT_RE = /<input[^>]+type=["'](date|datetime-local|month)["'][^>]*>/g
const EL_DATE_RE = /<el-date-picker[\s>]/

function* walkVue(dir: string): Generator<string> {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === '.git' || entry === 'dist') continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) yield* walkVue(full)
    else if (entry.endsWith('.vue')) yield full
  }
}

describe('date input unification guard', () => {
  it('无裸原生日期输入、el-date-picker 仅存在于 Kx 组件族', () => {
    const violations: string[] = []
    let scanned = 0
    for (const file of walkVue(SRC)) {
      scanned++
      const rel = relative(SRC, file).replace(/\\/g, '/')
      if (KX_HOSTS.has(rel)) continue
      if (PENDING_MIGRATION.has(rel)) continue
      const src = readFileSync(file, 'utf8')
      const tpl = src.match(/<template[^>]*>([\s\S]*)<\/template>/)?.[1] ?? src
      const native = tpl.match(NATIVE_INPUT_RE)
      if (native) violations.push(`${rel}: 裸日期输入 ${native.join(' ')}`)
      if (EL_DATE_RE.test(tpl)) violations.push(`${rel}: 模板直用 el-date-picker（应使用 Kx 组件族）`)
    }
    expect(violations, `发现 ${violations.length} 处违规（统一走 KxDateRangePicker/KxDatePicker，或迁毕后从 PENDING_MIGRATION 删除）:\n${violations.join('\n')}`).toEqual([])
    expect(scanned).toBeGreaterThan(0)
  })

  it('PENDING_MIGRATION 白名单自身保持有序（文件存在且仍含日期输入，防止幽灵条目）', () => {
    for (const rel of PENDING_MIGRATION) {
      const file = join(SRC, rel)
      expect(statSync(file, { throwIfNoEntry: false }), `白名单条目不存在: ${rel}`).toBeTruthy()
      const src = readFileSync(file, 'utf8')
      const hasDateInput = NATIVE_INPUT_RE.test(src) || EL_DATE_RE.test(src)
      expect(hasDateInput, `白名单条目已无日期输入，应删除: ${rel}`).toBe(true)
    }
  })
})
