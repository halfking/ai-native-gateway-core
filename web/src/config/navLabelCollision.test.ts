/**
 * navLabelCollision —— 顶栏菜单「同名入口」守卫（L9 收口轮）
 *
 * ## 为什么要有这道门
 *
 * 2026-11-20 L9 首次登记的缺陷：**两个菜单项共用同一文案** ⇒ 顶栏出现两个
 * 「会话分析中心」⇒ 用户分不清点哪个。
 *
 * ★ 这道门钉的不是「当时的缺陷」，而是**「同名即缺陷」这条规则本身**：
 *   - 2026-11-20 的修法（给聚合族另起一个 i18n 键 `sessionAnalytics.nav.analyticsCenter`）
 *     **在 zh-CN 下等于没修** —— 新键的值也写着「会话分析中心」⇒ 同名依旧
 *     （那笔提交后来在一次 merge 里成了悬空提交，没进主干）。
 *   - 真正收口的是 2026-11-23 审计轮：**把「/admin/session-analytics」总览
 *     菜单项整个删掉**（路由改为重定向到 users 页）⇒ 顶栏只剩一个入口。
 *   ⇒ 教训：**光靠「加一个新键」防不住同名**，因为新键的值可以被写成一样
 *     ⇒ 只有「**解析后文案**逐语种对拍」才能抓住。
 *
 * ## 判据
 *
 * 对 8 个语种各算一遍：把每个菜单项的 `labelKey` 在该语种的 messages 里
 * **解析成最终文案**（`t()` 看到的那个字符串），若**两个及以上**菜单项解析出
 * **同一文案** ⇒ 判红（已登记的既有项除外，见下）。
 *
 * ★ 为什么按「解析后文案」而不是按「key」判：两个不同的 key 完全可能写着
 *   同一个字符串（zh-CN 的「会话分析中心」就是活例）⇒ 按 key 判会永远绿。
 * ★ 为什么逐语种而不是只看 zh-CN：同名是**用户可见**的问题，英文界面同样
 *   会撞；而某些语种的译文会意外相同（拉丁语种尤其容易）⇒ 必须逐语种算。
 *
 * ## 既有碰撞的登记（不是豁免，是记账）
 *
 * `KNOWN_LABEL_COLLISIONS` 登记**当前已存在**的同名对，并写清成因与归属：
 *   - 每条理由必须**具体**（≥ 20 字符且含可核对的细节）——沿用
 *     `navCoverage.test.ts` 的 `KNOWN_ORPHANS` 纪律：白名单里写「同上」
 *     等于没有约束。
 *   - 登记项**不再碰撞时门会红**（否则白名单会腐烂成一张「什么都豁免」的
 *     废纸，门对已修复项失去约束）。
 *   ⇒ 登记项**只记账、不豁免语义**：它们仍然是顶栏上真实存在的同名入口，
 *     处置归属见每条理由（上游列车 / 已钉在别处）。
 *
 * ## 口径边界（知道才不会误判）
 *
 * - 只覆盖 **appNav 的静态菜单项**（`NAV_PRIMARY_ITEMS` + `NAV_GROUPS[*].items`）。
 *   插件注入的菜单（`/api/v1/plugin-nav` 动态合并）与**远端运维菜单**
 *   （`mergeRemoteOps` 用 ai-native-maintain 下发的组替换本地 `opsplatform`
 *   组）都在运行时才存在 ⇒ 静态门覆盖不到 ⇒ 登记为已知边界。
 * - `label`（字面量文案）与 `labelKey` 都算；两者都缺时该项不进本门。
 */

import { describe, it, expect } from 'vitest'
import { NAV_GROUPS, NAV_PRIMARY_ITEMS } from './appNav'

import zhCN from '../locales/zh-CN'
import zhTW from '../locales/zh-TW'
import enUS from '../locales/en-US'
import jaJP from '../locales/ja-JP'
import deDE from '../locales/de-DE'
import frFR from '../locales/fr-FR'
import esES from '../locales/es-ES'
import arSA from '../locales/ar-SA'

type Messages = Record<string, unknown>

const LOCALES: { code: string; messages: Messages }[] = [
  { code: 'zh-CN', messages: zhCN as unknown as Messages },
  { code: 'zh-TW', messages: zhTW as unknown as Messages },
  { code: 'en-US', messages: enUS as unknown as Messages },
  { code: 'ja-JP', messages: jaJP as unknown as Messages },
  { code: 'de-DE', messages: deDE as unknown as Messages },
  { code: 'fr-FR', messages: frFR as unknown as Messages },
  { code: 'es-ES', messages: esES as unknown as Messages },
  { code: 'ar-SA', messages: arSA as unknown as Messages },
]

/** 菜单项（扁平化）：primary + 各分组。 */
const NAV_ITEMS = [
  ...NAV_PRIMARY_ITEMS.map((it) => ({ group: '(顶栏)', ...it })),
  ...NAV_GROUPS.flatMap((g) => g.items.map((it) => ({ group: g.id, ...it }))),
]

/**
 * 已登记的同名对。
 *
 * 键＝两个菜单项 labelKey 的**字典序拼接**（用 `|` 分隔）⇒ 与语种无关：
 * 同一对 key 在不同语种可能撞（zh-CN 不撞、de-DE 撞），键不变。
 */
const KNOWN_LABEL_COLLISIONS: Record<string, string> = {
  'nav.item.opsAutoUpdate|nav.item.opsAutoUpdate':
    '上游既有：「自动更新」路径 /maintain/ops/autoupdate 跨 opsplatform 与 guide 两组各挂一次（同一路径、同一 labelKey）。' +
    '该事实已被 navCounts.test.ts 显式钉住（EXPECTED_ITEMS 67 / EXPECTED_PATHS 66，差值 1 即此条）；' +
    '且本地 opsplatform 组在 ai-native-maintain 远端运维菜单可达时被 mergeRemoteOps 整组替换 ⇒ 生产不同时渲染。',
  'nav.item.operationsOverview|nav.item.opsOverview':
    '上游译文撞车：运营总览（/admin/board-operational）与运维总览（/maintain/ops/overview）是两个不同页面，' +
    '但 de-DE 译作「Betriebsübersicht」、ar-SA 译作「نظرة عامة على العمليات」⇒ 同名。' +
    '属上游语种包文案问题（2026-11-23 前既已存在），本轮只登记不改译文（改文案属上游列车口径）。',
  'nav.item.opsAutoUpdate|nav.item.selfUpdate':
    '上游译文撞车：maintain 侧的「自动更新」（/maintain/ops/autoupdate 外链）与本站二进制的「自更新」' +
    '（/admin/self-update）是两个不同功能，但 fr-FR 译作「Mise à jour automatique」、es-ES 译作「Actualización automática」⇒ 同名。' +
    '同属上游语种包文案问题，本轮只登记。',
}

/** `t('nav.item.sessionAnalytics')` 在 messages 里的解析。 */
function resolveKey(messages: Messages, key: string): string | undefined {
  let cur: unknown = messages
  for (const part of key.split('.')) {
    if (cur === null || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[part]
  }
  return typeof cur === 'string' ? cur : undefined
}

/** 两个 labelKey 组成的登记键（字典序）。 */
function pairKey(a: string, b: string): string {
  return [a, b].sort().join('|')
}

/** 某语种下的全部同名对（含涉及的菜单项描述）。 */
function collisionsOf(messages: Messages) {
  const byText = new Map<string, { key: string; desc: string }[]>()
  for (const item of NAV_ITEMS) {
    const text = item.labelKey ? resolveKey(messages, item.labelKey) : item.label
    if (!text) continue
    const key = item.labelKey ?? `label:${item.path}`
    const list = byText.get(text) ?? []
    list.push({ key, desc: `[${item.group}] ${item.path}` })
    byText.set(text, list)
  }
  const out: { text: string; keys: string[]; descs: string[] }[] = []
  for (const [text, list] of byText) {
    if (list.length < 2) continue
    out.push({
      text,
      keys: [...new Set(list.map((x) => x.key))].sort(),
      descs: list.map((x) => x.desc),
    })
  }
  return out
}

describe('顶栏菜单同名守卫（L9）', () => {
  it('菜单项数量未腐化（解析器没坏）', () => {
    expect(NAV_ITEMS.length).toBeGreaterThanOrEqual(40)
  })

  for (const { code, messages } of LOCALES) {
    it(`${code}：没有未登记的同名菜单入口`, () => {
      const collisions = collisionsOf(messages)
      const offenders = collisions.filter((c) => {
        // ★ 登记键按「两个不同 labelKey」算；同一 labelKey 出现两次 = 同一路径
        //   跨组重复挂载（navCounts 已单独钉），也算登记项。
        const k = c.keys.length === 1 ? `${c.keys[0]}|${c.keys[0]}` : pairKey(c.keys[0], c.keys[1])
        return !(k in KNOWN_LABEL_COLLISIONS)
      })

      expect(
        offenders.map((c) => `  "${c.text}" × ${c.descs.length}\n${c.descs.map((d) => `    - ${d}`).join('\n')}`),
        `${code} 顶栏出现未登记的同名菜单入口 ${offenders.length} 组：\n` +
          offenders.map((c) => `"${c.text}"`).join(' / ') +
          `\n处置：给其中一方**换文案**（不要只换 key —— 两个 key 写同一句话仍然是同名）。`,
      ).toEqual([])
    })
  }

  it('登记项都还真的在碰撞（登记不许腐烂）', () => {
    const seen = new Set<string>()
    for (const { messages } of LOCALES) {
      for (const c of collisionsOf(messages)) {
        const k = c.keys.length === 1 ? `${c.keys[0]}|${c.keys[0]}` : pairKey(c.keys[0], c.keys[1])
        seen.add(k)
      }
    }
    const stale = Object.keys(KNOWN_LABEL_COLLISIONS).filter((k) => !seen.has(k))
    expect(
      stale,
      `登记项已不再碰撞（应从 KNOWN_LABEL_COLLISIONS 移除）：\n${stale.join('\n')}`,
    ).toEqual([])
  })

  it('登记理由必须具体（≥20 字符，不接受「同上」式敷衍）', () => {
    const weak = Object.entries(KNOWN_LABEL_COLLISIONS)
      .filter(([, reason]) => reason.trim().length < 20)
      .map(([k]) => k)
    expect(weak, `登记理由太短：${weak.join(', ')}`).toEqual([])
  })

  it('每个 labelKey 在 8 个语种下都能解析出文案（不静默回落成 key 本身）', () => {
    const keys = [...new Set(NAV_ITEMS.map((i) => i.labelKey).filter((k): k is string => !!k))]
    const missing: string[] = []
    for (const { code, messages } of LOCALES) {
      for (const key of keys) {
        if (resolveKey(messages, key) === undefined) missing.push(`${code}:${key}`)
      }
    }
    expect(missing, `以下 labelKey 在对应语种里查不到文案：\n${missing.join('\n')}`).toEqual([])
  })
})
