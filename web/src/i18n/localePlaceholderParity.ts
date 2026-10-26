// localePlaceholderParity.ts — 「同一键、不同语种，占位符集合不一致」的质量门
//
// 目的：补上 i18n 既有门合起来的又一条缝（2026-10-21 批判式审计轮实测）：
//   · parity.test.ts             只比**键集合** ⇒ 值里的占位符被吞照样「平」
//   · keys_referenced.test.ts    只查**键是否存在** ⇒ 同上
//   · hardcoded_cjk 棘轮        排除 src/locales/ ⇒ 同上
//   · localeValueQuality.ts      只管「值的语言对不对」⇒ 占位符**数量与名字**不归它管
//   ⇒ 四条门合起来拦不住「键在、值在、但占位符被某个语种吞了」。
//
// 实测到的两族（2026-10-21，修复前）：
//   族1 customer.wizard.messages.deviceLimitBody
//        en-US/zh-CN = "…({active}/{max})"；其余 6 语种 = "…"（两个占位符全丢）
//        ⚠ 2026-10-21（R57 轮）**该键已随死命名空间 `customer.wizard.*` 一起删除**
//          （属主拍板；三口径实测零消费者）⇒ 族1 这个**个案**已不存在。
//          但「同形状的下一个键族」仍在 ⇒ 判据层面的形状覆盖由门与定点回归继续保证。
//   族2 dataLifecycle.storageOverview.rowResult.running
//        6 语种 = "{op} …"；**en-US 与 zh-CN** = "…"（调用点传了 {op} 却没人显示）
//        ⇒ 英文用户与中文用户都看不到「正在跑什么」
//
// ⚠️ **为什么不能拿 en-US 当真值**：族2 里**恰恰是 en-US 缺参数**。
//   若把 en-US 当参照，会把「补 {op}」判成「其他 6 语种多写了 {op}」⇒ 修反方向。
//   ⇒ 本门只判「**平不平**」；**谁对谁错由调用点裁定**（`t('key', { op })` 传了什么，
//   谁就该显示什么）⇒ 修的方向是「补上调用点要的那个占位符」。
//
// 口径：
//   - 键范围 = **全部键**（不只被引用的键）。理由：历史上族1 的键住在**死命名空间**里
//     （`customer.wizard.*` 全仓零消费者）⇒ 若只管被引用的键，族1 这类缺陷
//     永远抓不到，而命名空间一旦复活，缺陷就等着。
//     ★ 2026-10-21（R57 轮）订正：该命名空间已被删除，**但范围决策不变** ——
//       仓里仍有大量死键（严格死键数以 `unusedInSource` 为准），「死键上的不一致」
//       依然是真实风险面 ⇒ 仍按全部键比。
//     代价：死键上的不一致也算违例 ⇒ 但实测死键占绝大多数（unusedInSource 4519 条），
//     而「占位符不一致」在死键上极其罕见（修复前全仓只有 2 个键）⇒ 代价可接受。
//   - 值 = **运行时真值**（vm 求值 locale 模块，见 scanner.ts 的 loadLocale）。
//     理由同 localeValueQuality.ts：JS 对象字面量语义下重复键后者覆盖前者，
//     文件文本口径会把早已被覆盖的值算进来。
//   - 只比**字符串**值。两层机制要分清：
//       ① **值表里根本没有非字符串**（`localeValues` 的 `collectLeafValues` 只收
//          `typeof v === 'string'`）⇒ 数组/数字/布尔值对某个键来说，等价于
//          「**该语种没有这个键**」，而不是「该语种参与了比较但被排除」。
//       ② `measureParity` 另有一道防御：`hasOwnProperty` 通过但值不是字符串时，
//          **该语种不参与该键的比较**（类型不同 ≠ 占位符缺失）——
//          这条分支在真实值表上不会触发，自证 P4 专门钉它。
//   - 占位符语法 = `{name}`，name ∈ [A-Za-z0-9_]+。
//     ⚠️ vue-i18n 的字面量转义 `{'@'}`（8 处）**不匹配**该式 ⇒ 正确地不算占位符。
//     ⚠️ `t(key, { default: ... })` 的 `default` 是 vue-i18n 的**选项**不是占位符
//        （全仓无 `{default}` 字面值，故无歧义）。
//
// 已知盲区（**记在案，不假装没有**）：
//   「**所有语种一致、但都缺**调用点要的那个占位符」—— 平价门抓不到
//   （没有「不一致」可看）。2026-10-21 实测本仓**当前 0 实例**：
//   全仓 336 处 `t(key, {…})` 带对象字面量参数、涉及 301 个键，逐个解掉
//   4 个疑似例（2 个是动态 key 模板 `{tip}.tokenFormat` / `op.${k}.time`，
//   手工解出后 8 语种占位符齐备；1 个 `default` 是 vue-i18n 选项不是占位符；
//   1 个 `sessions.config.${meta.i18nKey}` 同属动态 key）。
//   ⇒ 要彻底关掉这个盲区需要「调用点参数 → 占位符名」的静态解析，
//     本仓的 scanner 还没有 ⇒ 留待后续轮次，本门不假装覆盖它。
//
// 消费方：
//   - src/i18n/localePlaceholderParity.test.ts（pnpm test 强制执行）
//
// 阈值：**硬闸 0**（不是一个棘轮）—— 修复后全仓 0 违例；再出现就是新缺陷，
//   不该被「存量」名额掩盖。⚠️ 硬闸必须配**非空下限**断言（见 test），
//   否则「一个键都没比」也会全绿（假绿）。

import { allLocaleValues, type LocaleValues } from './localeValueQuality.ts'

/** 占位符语法：vue-i18n 的具名/编号插值 `{name}`。 */
const PLACEHOLDER_RE = /\{([A-Za-z0-9_]+)\}/g

export interface ParityGroup {
  /** 占位符集合的规范形式（排序后逗号连接；空集合 = "(无占位符)"） */
  signature: string
  /** 属于这一组的语种 */
  locales: string[]
}

export interface ParityViolation {
  key: string
  groups: ParityGroup[]
}

export interface ParityReport {
  /** 参与比较的语种 */
  locales: string[]
  /** 实际参与比较的键数（出现在 ≥2 个语种里的键） */
  comparedKeys: number
  /** 至少一个语种的值里含占位符的键数（证明提取器真的在触发） */
  keysWithPlaceholder: number
  violations: ParityViolation[]
}

/** 取一个值里的占位符集合（去重、顺序无关）。非字符串 ⇒ 空集。 */
export function placeholderSet(value: unknown): Set<string> {
  const out = new Set<string>()
  if (typeof value !== 'string') return out
  const re = new RegExp(PLACEHOLDER_RE.source, 'g')
  let m: RegExpExecArray | null
  while ((m = re.exec(value)) !== null) out.add(m[1])
  return out
}

function signatureOf(ph: Set<string>): string {
  return ph.size === 0 ? '(无占位符)' : [...ph].sort().join(',')
}

/**
 * 纯函数：量跨语种占位符平价。
 * @param valuesByLocale 语种 → 展平的键值表
 */
export function measureParity(valuesByLocale: Map<string, LocaleValues>): ParityReport {
  const locales = [...valuesByLocale.keys()]
  const allKeys = new Set<string>()
  for (const values of valuesByLocale.values()) {
    for (const key of Object.keys(values)) allKeys.add(key)
  }

  let comparedKeys = 0
  let keysWithPlaceholder = 0
  const violations: ParityViolation[] = []

  for (const key of allKeys) {
    const groups = new Map<string, string[]>()
    let present = 0
    let anyPlaceholder = false
    for (const loc of locales) {
      const values = valuesByLocale.get(loc)
      if (!values) continue
      const v = values[key]
      // 该语种没有这个键 ⇒ 不参与比较
      if (!Object.prototype.hasOwnProperty.call(values, key)) continue
      // 该语种的值不是字符串 ⇒ 该语种不参与该键比较（类型不同 ≠ 占位符缺失）
      if (typeof v !== 'string') continue
      present++
      const ph = placeholderSet(v)
      if (ph.size > 0) anyPlaceholder = true
      const sig = signatureOf(ph)
      if (!groups.has(sig)) groups.set(sig, [])
      groups.get(sig)!.push(loc)
    }
    if (present < 2) continue
    comparedKeys++
    if (anyPlaceholder) keysWithPlaceholder++
    if (groups.size > 1) {
      violations.push({
        key,
        groups: [...groups.entries()].map(([signature, ls]) => ({ signature, locales: ls })),
      })
    }
  }

  return { locales, comparedKeys, keysWithPlaceholder, violations }
}

// ─────────────────────────── 自证（量具必须有牙） ───────────────────────────
//
// 方向必须**双向**：
//   · 「会触发」—— 占位符真不一样时必须报违例
//   · 「不误触发」—— 占位符一样 / 只在一个语种存在 / 值不是字符串时不得报违例
// ⚠️ 缺了「不误触发」这一半，一门永远绿的平价门和一个永远红的平价门同样没用。

export interface ParitySelfCheckCase {
  id: string
  desc: string
  expected: number
  actual: number
  ok: boolean
}

export function selfCheckParity(): ParitySelfCheckCase[] {
  const cases: ParitySelfCheckCase[] = []
  const probe = (
    id: string,
    desc: string,
    // ⚠️ 入参类型**故意放宽到 unknown**：P4 要喂一个**非字符串**值去测
    //   「该语种不参与比较」的防御分支，而 `LocaleValues` 规定值必须是 string
    //   ⇒ 若把入参写成 `Record<string, LocaleValues>`，P4 本身就编不过
    //   （vue-tsc 实测报 TS2322）。这里的 cast 是**自证专用**的、有意的放宽，
    //   不是绕过真实值表的类型约束（真实值表由 `localeValues` 保证只有字符串）。
    values: Record<string, Record<string, unknown>>,
    expected: number,
  ) => {
    const map = new Map<string, LocaleValues>()
    for (const [loc, v] of Object.entries(values)) map.set(loc, v as LocaleValues)
    const report = measureParity(map)
    cases.push({ id, desc, expected, actual: report.violations.length, ok: report.violations.length === expected })
  }

  // 提取器本身
  const ph = (id: string, desc: string, value: unknown, expected: string[]) => {
    const got = [...placeholderSet(value)].sort().join(',')
    const want = [...expected].sort().join(',')
    cases.push({ id, desc, expected: want === got ? 1 : 0, actual: want === got ? 1 : 0, ok: want === got })
  }

  ph('E1', '提取器：单个占位符', 'Hello {name}', ['name'])
  ph('E2', '提取器：多个占位符顺序无关', '{b} then {a}', ['a', 'b'])
  ph('E3', '提取器：重复占位符去重', '{a} {a}', ['a'])
  ph('E4', '提取器：无占位符 ⇒ 空集', 'plain text', [])
  ph('E5', '提取器：vue-i18n 字面量转义 {\'@\'} 不算占位符', "you{'@'}company.com", [])
  ph('E6', '提取器：非字符串 ⇒ 空集', 42, [])

  // 平价判定
  probe('P1', '两语种占位符不同 ⇒ 报 1 条违例', {
    en: { 'k': 'a {x}' },
    zh: { 'k': 'a' },
  }, 1)
  probe('P2', '两语种占位符相同 ⇒ 不报违例', {
    en: { 'k': 'a {x}' },
    zh: { 'k': 'b {x}' },
  }, 0)
  probe('P3', '键只在一个语种存在 ⇒ 不参与比较、不报违例', {
    en: { 'k': 'a {x}' },
    zh: { 'other': 'b {y}' },
  }, 0)
  probe('P4', '值不是字符串 ⇒ 该语种不参与比较、不报违例', {
    en: { 'k': 'a {x}' },
    zh: { 'k': ['b'] },
  }, 0)
  probe('P5', '三语种两种签名 ⇒ 报 1 条违例（只报键，不按组数报）', {
    en: { 'k': 'a {x}' },
    zh: { 'k': 'a' },
    ja: { 'k': 'a {x}' },
  }, 1)
  probe('P6', '占位符名字不同也算不一致（{x} vs {y}）', {
    en: { 'k': 'a {x}' },
    zh: { 'k': 'a {y}' },
  }, 1)

  return cases
}

export function assertSelfCheckParity(): ParitySelfCheckCase[] {
  const cases = selfCheckParity()
  const failed = cases.filter((c) => !c.ok)
  if (failed.length > 0) {
    const detail = cases
      .map((c) => `  ${c.ok ? 'PASS' : 'FAIL'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
      .join('\n')
    throw new Error(`locale placeholder parity self-check failed:\n${detail}`)
  }
  return cases
}

/** 量本仓当前状态（复用 localeValueQuality 的全语种值表缓存 ⇒ 不重复加载）。 */
export function measureRepoParity(): ParityReport {
  return measureParity(allLocaleValues())
}
