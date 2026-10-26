// callSitePlaceholderCoverage.ts — 「调用点传的参数，有没有被任何语种显示出来」的判定层
//
// 目的：关掉 R54 §11.1-6 登记的盲区 ——
//   「**所有语种一致、但都缺**调用点要的那个占位符」⇒ 跨语种平价门抓不到。
//
// 判据（为什么不是「参数必须每个语种都显示」）：
//   · 「**每个语种都显示**」是**平价门**的职责（localePlaceholderParity.ts）。
//     本门刻意**不重复**它，两门互补：
//       平价门管「有的语种显示、有的不显示」（集合不一致）。
//       本门管「**所有语种都不显示**」（集合一致 ⇒ 平价门无不一致可看）。
//   · 「参数没被显示」本身**不等于缺陷**（vue-i18n 忽略未使用的具名参数，
//     作者可能只是多传了一个）。
//     ⇒ 可判的缺陷形态是「**这个参数名在候选消息里一个都没出现过**」
//       ⇒ 它既没被显示，**也不可能**被显示 ⇒ 该参数是**死的**。
//
// 「候选消息」怎么定（按 key 形态分三档）：
//   · literal（`t('a.b', {x})`）⇒ 候选 = 该键在各语种的消息。
//   · template（`t(\`a.${k}.b\`, {x})`）⇒ 候选 = **同时满足静态前缀与静态后缀**的键
//     （`prefix` 前缀 + `suffix` 后缀）⇒ 动态段未知，但两端已知 ⇒ 可枚举。
//   · dynamic（`t(key, {x})`）⇒ 静态不可知 ⇒ 归入「未决」，必须登记。
//
// 未决（unresolved）怎么办：
//   未决**不是**「静默放过」，而是**必须登记在册**（见 UNRESOLVED_REGISTRY）：
//   门断言「实测未决集合」与「登记册」**双向严格相等** ——
//     实测有而册里没有 ⇒ 🔴 新增未决（有人写了 spread/变量键却没人登记）
//     册里有而实测没有 ⇒ 🔴 登记过期（该未决已被修掉，册子必须同步）
//   ⇒ 豁免清单只能变少，不会变成永久藏污纳垢。

import { allLocaleValues, SRC_DIR, type LocaleValues } from './localeValueQuality.ts'
import { placeholderSet } from './localePlaceholderParity.ts'
import {
  scanAllCallSiteParams,
  scanCallSiteParamsInSource,
  type CallSiteKeyKind,
  type CallSiteParamRef,
} from './callSiteParams.ts'

export interface DeadParamViolation {
  /** 调用点 */
  callSite: CallSiteParamRef
  /** 没有任何候选消息显示的参数名 */
  deadParams: string[]
  /** 候选消息键（literal=该键；template=前缀/后缀匹配到的键） */
  candidateKeys: string[]
  /** 候选消息里出现过的全部占位符名 */
  availableParams: string[]
}

export interface UnresolvedSite {
  callSite: CallSiteParamRef
  /** 未决原因 */
  reason: 'spread-params' | 'dynamic-key' | 'no-candidate'
  /** 原因说明（人可读） */
  detail: string
}

export interface CoverageReport {
  /** 参与统计的调用点总数（第二个参数是对象字面量） */
  callSites: number
  /** 带至少一个具名参数的调用点 */
  sitesWithParams: number
  /** 具名参数总数（去重前） */
  paramCount: number
  /** 去重后的参数名数量 */
  distinctParamNames: number
  /** 涉及的不同键数（literal 键 + template 候选键） */
  distinctKeys: number
  /** 按 key 形态分布 */
  byKeyKind: Record<CallSiteKeyKind, number>
  /** 死参数违例（硬闸） */
  deadParams: DeadParamViolation[]
  /** 未决调用点（必须登记在册） */
  unresolved: UnresolvedSite[]
  /** 键在**所有语种**都不存在的调用点（缺失键归 keys_referenced 门管，本门只报不判） */
  missingKeySites: CallSiteParamRef[]
}

/** 未决调用点登记册（实测清单，2026-10-21）。
 *
 *  规则一：**实测未决集合必须与本册双向严格相等**
 *    实测有而册里没有 ⇒ 🔴 新增未决（有人写了 spread/变量键却没人登记）
 *    册里有而实测没有 ⇒ 🔴 登记过期（该未决已被修掉，册子必须同步）
 *    ⇒ 豁免清单只能变少，不会变成永久藏污纳垢。
 *
 *  规则二：**每条登记都自带可自证的「候选键 + 期望占位符」**
 *    ⇒ 登记不是「空口豁免」：测试会断言这些候选键的消息在**全部 8 个语种**里
 *      都真的带上了期望占位符。哪天候选键的消息把占位符吞了，登记照样会红。
 *
 *  规则三：**理由不得为空**（空理由 ⇒ 登记无效）。 */
export const UNRESOLVED_REGISTRY: Array<{
  /** 相对 web/ 的文件路径 */
  file: string
  /** 1-based 行号 */
  line: number
  reason: UnresolvedSite['reason']
  /** 人工解出的候选消息键（该调用点实际可能命中的键） */
  keys: string[]
  /** 期望这些消息显示的占位符名 */
  params: string[]
  /** 可排查的理由（不得为空） */
  why: string
}> = [
  {
    file: 'src/components/NodeStatusMatrix.vue',
    line: 193,
    reason: 'dynamic-key',
    keys: [
      'requestJourneys.matrix.groupDanger',
      'requestJourneys.matrix.groupWarn',
      'requestJourneys.matrix.groupOk',
      'requestJourneys.matrix.groupDisabled',
    ],
    params: ['count'],
    why:
      '键是 `group.titleKey`（循环变量）⇒ 静态不可知。候选键取自同文件 ' +
      'NodeStatusMatrix.vue:61-64 的 healthGroups 字面量数组（4 个 titleKey 值）。',
  },
  {
    file: 'src/components/OperationAgreementDialog.vue',
    line: 66,
    reason: 'dynamic-key',
    keys: ['public.opAgreement.downloadTitle', 'public.opAgreement.activateTitle'],
    params: ['title'],
    why:
      '键是局部变量 `key`，由 `props.scope` 的三元表达式赋值（' +
      'OperationAgreementDialog.vue:62-65）⇒ 静态不可知，候选键取自该三元。',
  },
  {
    file: 'src/views/FreeDiscoveryView.vue',
    line: 296,
    reason: 'spread-params',
    keys: ['freeDiscovery.res.importedToast'],
    params: ['imported', 'skipped', 'conflicted', 'failed'],
    why:
      '第二参数是 `{ ...summary }`（API 返回对象整体展开）⇒ 属性名无法静态枚举。' +
      '其键集合由 `FreeDiscoveryImportSummary`（src/api/free-discovery.ts:122）决定 = ' +
      'imported/skipped/conflicted/failed，与消息 `freeDiscovery.res.importedToast` 的 ' +
      '4 个占位符一致；该一致性由登记的 keys/params 自证断言钉住。',
  },
]

// ─────────────────────────── 判定核心 ───────────────────────────

/** 候选消息键集合：literal=该键；template=前缀+后缀匹配 */
export function candidateKeysFor(
  callSite: CallSiteParamRef,
  allKeys: Iterable<string>,
): string[] {
  if (callSite.keyKind === 'literal') return [callSite.key]
  if (callSite.keyKind === 'template') {
    const out: string[] = []
    for (const k of allKeys) {
      if (k.startsWith(callSite.keyPrefix) && k.endsWith(callSite.keySuffix)) out.push(k)
    }
    return out
  }
  return []
}

/**
 * 量一批调用点的「参数是否被显示」覆盖率。
 * @param callSites 调用点（来自 scanAllCallSiteParams）
 * @param valuesByLocale 语种 → 展平的键值表
 */
export function measureCoverage(
  callSites: CallSiteParamRef[],
  valuesByLocale: Map<string, LocaleValues>,
): CoverageReport {
  const allKeys = new Set<string>()
  for (const values of valuesByLocale.values()) {
    for (const key of Object.keys(values)) allKeys.add(key)
  }

  const byKeyKind: Record<CallSiteKeyKind, number> = { literal: 0, template: 0, dynamic: 0 }
  let sitesWithParams = 0
  let paramCount = 0
  const distinctParamNames = new Set<string>()
  const distinctKeys = new Set<string>()
  const deadParams: DeadParamViolation[] = []
  const unresolved: UnresolvedSite[] = []
  const missingKeySites: CallSiteParamRef[] = []

  for (const callSite of callSites) {
    byKeyKind[callSite.keyKind]++

    if (callSite.unresolvedParams) {
      unresolved.push({
        callSite,
        reason: 'spread-params',
        detail: '第二参数含 spread / 计算属性 ⇒ 属性名无法静态枚举',
      })
      continue
    }
    if (callSite.params.length === 0) continue

    sitesWithParams++
    paramCount += callSite.params.length
    for (const p of callSite.params) distinctParamNames.add(p)

    if (callSite.keyKind === 'dynamic') {
      unresolved.push({
        callSite,
        reason: 'dynamic-key',
        detail: `键是变量（${callSite.key}）⇒ 候选消息无法枚举`,
      })
      continue
    }

    if (callSite.keyKind === 'literal') {
      // 该键在**所有语种**都不存在 ⇒ 归「缺失键」（keys_referenced 门管），
      // 不算死参数（没有消息 ⇒ 谈不上「传了却没显示」）
      const exists = [...valuesByLocale.values()].some((v) =>
        Object.prototype.hasOwnProperty.call(v, callSite.key),
      )
      if (!exists) {
        missingKeySites.push(callSite)
        continue
      }
    }

    const candidates = candidateKeysFor(callSite, allKeys)
    if (candidates.length === 0) {
      unresolved.push({
        callSite,
        reason: 'no-candidate',
        detail:
          `模板键无候选消息（前缀=${JSON.stringify(callSite.keyPrefix)} ` +
          `后缀=${JSON.stringify(callSite.keySuffix)}）`,
      })
      continue
    }
    for (const c of candidates) distinctKeys.add(c)

    // 候选消息里出现过的占位符名（跨语种并集）
    const available = new Set<string>()
    for (const values of valuesByLocale.values()) {
      for (const c of candidates) {
        const v = values[c]
        if (typeof v !== 'string') continue
        for (const p of placeholderSet(v)) available.add(p)
      }
    }

    const dead = callSite.params.filter((p) => !available.has(p))
    if (dead.length > 0) {
      deadParams.push({
        callSite,
        deadParams: dead,
        candidateKeys: candidates,
        availableParams: [...available].sort(),
      })
    }
  }

  return {
    callSites: callSites.length,
    sitesWithParams,
    paramCount,
    distinctParamNames: distinctParamNames.size,
    distinctKeys: distinctKeys.size,
    byKeyKind,
    deadParams,
    unresolved,
    missingKeySites,
  }
}

/** 量本仓当前状态（复用 localeValueQuality 的值表缓存 ⇒ 不重复加载 locale） */
export function measureRepoCoverage(): CoverageReport {
  const { callSites } = scanAllCallSiteParams(SRC_DIR)
  return measureCoverage(callSites, allLocaleValues())
}

// ─────────────────────────── 自证（量具必须有牙） ───────────────────────────
//
// 方向必须**双向**：
//   · 「会触发」—— 死参数存在时必须报违例
//   · 「不误触发」—— 参数被显示 / 键缺失 / spread 未决 时不得报违例
// ⚠️ 缺了「不误触发」这一半，一门永远绿的判定和一门永远红的判定同样没用。

export interface SelfCheckCase {
  id: string
  desc: string
  expected: number
  actual: number
  ok: boolean
}

function site(
  file: string,
  line: number,
  key: string,
  keyKind: CallSiteKeyKind,
  params: string[],
  extra: Partial<CallSiteParamRef> = {},
): CallSiteParamRef {
  return {
    file,
    line,
    column: 1,
    key,
    keyKind,
    keyPrefix: extra.keyPrefix ?? '',
    keySuffix: extra.keySuffix ?? '',
    params,
    unresolvedParams: extra.unresolvedParams ?? false,
    snippet: `t('${key}', { ${params.join(', ')} })`,
  }
}

export function selfCheckCoverage(): SelfCheckCase[] {
  const cases: SelfCheckCase[] = []
  const probe = (id: string, desc: string, callSites: CallSiteParamRef[], values: Record<string, Record<string, string>>, expected: number) => {
    const map = new Map<string, LocaleValues>()
    for (const [loc, v] of Object.entries(values)) map.set(loc, v)
    const report = measureCoverage(callSites, map)
    cases.push({ id, desc, expected, actual: report.deadParams.length, ok: report.deadParams.length === expected })
  }

  // 判定
  probe('P1', '参数被所有语种显示 ⇒ 不报死参数', [
    site('a.vue', 1, 'k', 'literal', ['op']),
  ], { en: { k: 'run {op}' }, zh: { k: '跑 {op}' } }, 0)

  probe('P2', '★ 盲区本体：所有语种都缺该参数 ⇒ 报死参数（平价门抓不到这一种）', [
    site('a.vue', 1, 'k', 'literal', ['op']),
  ], { en: { k: 'run' }, zh: { k: '跑' } }, 1)

  probe('P3', '部分语种缺、部分不缺 ⇒ 不报死参数（归平价门管，两门不重复）', [
    site('a.vue', 1, 'k', 'literal', ['op']),
  ], { en: { k: 'run {op}' }, zh: { k: '跑' } }, 0)

  probe('P4', '键在所有语种都不存在 ⇒ 归「缺失键」，不报死参数', [
    site('a.vue', 1, 'k', 'literal', ['op']),
  ], { en: {}, zh: {} }, 0)

  probe('P5', '模板键：候选消息里显示了参数 ⇒ 不报死参数', [
    site('a.vue', 1, 'a.${k}.b', 'template', ['size'], { keyPrefix: 'a.', keySuffix: '.b' }),
  ], { en: { 'a.x.b': '{size}' } }, 0)

  probe('P6', '模板键：候选消息里没有该参数 ⇒ 报死参数', [
    site('a.vue', 1, 'a.${k}.b', 'template', ['size'], { keyPrefix: 'a.', keySuffix: '.b' }),
  ], { en: { 'a.x.b': 'none' } }, 1)

  probe('P7', '模板键无候选消息 ⇒ 归未决，不报死参数', [
    site('a.vue', 1, 'zzz.${k}.b', 'template', ['size'], { keyPrefix: 'zzz.', keySuffix: '.b' }),
  ], { en: { 'a.x.b': '{size}' } }, 0)

  probe('P8', 'spread 参数 ⇒ 归未决，不报死参数', [
    site('a.vue', 1, 'k', 'literal', [], { unresolvedParams: true }),
  ], { en: { k: 'run' } }, 0)

  probe('P9', '变量键 ⇒ 归未决，不报死参数', [
    site('a.vue', 1, 'someVar', 'dynamic', ['op']),
  ], { en: { k: 'run' } }, 0)

  probe('P10', '多参数：只报真正死的那一个', [
    site('a.vue', 1, 'k', 'literal', ['ok', 'dead']),
  ], { en: { k: '{ok}' } }, 1)

  // 解析层（scanner 侧）
  const src = [
    `const a = t('k.one', { op, size: n })`,
    `// t('k.commented', { ghost })`,
    `/* t('k.blockComment', { ghost2 }) */`,
    `const s = "t('k.inString', { ghost3 })"`,
    `const b = t(\`k.\${x}.tail\`, { p, c })`,
    `const c = t('k.spread', { ...summary })`,
    `const d = t('k.quoted', { 'q-name': v })`,
  ].join('\n')
  const found = scanCallSiteParamsInSource(src, 'x.ts')
  const byKey = new Map(found.map((f) => [f.key, f]))
  const check = (id: string, desc: string, expected: number, actual: number) => {
    cases.push({ id, desc, expected, actual, ok: expected === actual })
  }
  check('E1', '注释里的调用点不被扫到', 0, byKey.has('k.commented') ? 1 : 0)
  check('E2', '块注释里的调用点不被扫到', 0, byKey.has('k.blockComment') ? 1 : 0)
  check('E3', '字符串字面量里的调用点不被扫到', 0, byKey.has('k.inString') ? 1 : 0)
  check('E4', '普通调用点被扫到', 1, byKey.has('k.one') ? 1 : 0)
  check('E5', 'shorthand 属性名被取到', 1, byKey.get('k.one')?.params.includes('op') ? 1 : 0)
  check('E6', 'key:value 属性名被取到', 1, byKey.get('k.one')?.params.includes('size') ? 1 : 0)
  check('E7', '模板键被扫到且 kind=template', 1, byKey.get('k.${x}.tail')?.keyKind === 'template' ? 1 : 0)
  check('E8', '模板键的静态前后缀正确', 1,
    byKey.get('k.${x}.tail')?.keyPrefix === 'k.' && byKey.get('k.${x}.tail')?.keySuffix === '.tail' ? 1 : 0)
  check('E9', 'spread ⇒ unresolvedParams=true', 1, byKey.get('k.spread')?.unresolvedParams ? 1 : 0)
  check('E10', '引号键的属性名被取到', 1, byKey.get('k.quoted')?.params.includes('q-name') ? 1 : 0)

  // .vue 模板：插值里的调用点是真调用点；自由文本里的撇号不得把代码吃掉
  const vueSrc = [
    '<template>',
    '  <p>it\'s fine</p>',
    "  <span>{{ t('k.inTemplate', { n }) }}</span>",
    "  <div :title=\"t('k.inAttr', { m })\"></div>",
    '</template>',
    '<script setup lang="ts">',
    "const c = t('k.inScript', { q })",
    '</script>',
  ].join('\n')
  const vueFound = scanCallSiteParamsInSource(vueSrc, 'y.vue')
  const vueKeys = new Set(vueFound.map((f) => f.key))
  check('E11', '.vue 模板插值里的调用点被扫到', 1, vueKeys.has('k.inTemplate') ? 1 : 0)
  check('E12', '.vue 属性值表达式里的调用点被扫到', 1, vueKeys.has('k.inAttr') ? 1 : 0)
  check('E13', '.vue script 块里的调用点被扫到', 1, vueKeys.has('k.inScript') ? 1 : 0)
  check('E14', '.vue 自由文本里的撇号不吞掉后续代码（3 处全被扫到）', 3, vueFound.length)

  return cases
}

export function assertSelfCheckCoverage(): SelfCheckCase[] {
  const cases = selfCheckCoverage()
  const failed = cases.filter((c) => !c.ok)
  if (failed.length > 0) {
    const detail = cases
      .map((c) => `  ${c.ok ? 'PASS' : 'FAIL'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
      .join('\n')
    throw new Error(`call-site placeholder coverage self-check failed:\n${detail}`)
  }
  return cases
}
