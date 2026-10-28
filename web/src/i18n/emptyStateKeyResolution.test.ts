// emptyStateKeyResolution.test.ts — 「空状态新键在运行时真的解析成译文」的门
//
// ★ 2026-10-27（第 14 增量 · 空状态 5 分）收尾轮补写。
//
// ## 它补的是哪条缝
//
// 第 14 增量新增了 **23 个**按状态种类（kind）拆分的空态键，
// 并新建了 `scripts/i18n-empty-state-guard.mjs`（结构门）。
// 但那道门与既有 i18n 门**都只看「键在语言包里吗」**，
// **没有任何一道门看「这个键在运行时真的渲染成译文」**。
//
// ★★ 而这正是**最阴险的一类缺陷**，原因是本仓 i18n 实例设了
// `missingWarn: false` ⇒ **缺键不报控制台警告** ⇒
// 缺键时 `t(key)` 直接把**键路径本身**返回给界面 ⇒
// 用户看到的是 "sessions.stats.emptyFailedErrorSummary" 这种**原始字符串**，
// 而**界面不报任何错、日志里没有任何痕迹、门全绿**。
//
// ★ 所以「键在语言包里」与「键在运行时解析成译文」是**两件事**：
//   · 键在语言包里但**挂错了命名空间/层级** ⇒ 静态扫描看得见，运行时才炸；
//   · 键在语言包里但**值是空串** ⇒ 静态扫描（集合层面）看不见，运行时才炸。
//
// ★★★ 上一轮留了一个**一次性验证页**（`web/__q14_empty.html`，
//   其自述「临时产物，验完即删」）来人工确认这一点 ——
//   ⇒★ **验证页是人工的、一次性的、且不会被 CI 跑** ⇒
//   ⇒★ 「人工确认过一次」**不是**「每次提交都被门守住」⇒
//   ⇒⇒ **把它沉成一道自动门**，才是这次收口真正该做的事（§424 形态：一次性观察 ≠ 门）。
//
// ## 与既有门的分工（**不重复**）
//
//   · i18n-empty-state-guard.mjs   结构：kind 标记在位 / 值互不相同 / 通用键已退役
//   · localeKeyParity.test.ts       键的**存在性**跨语种平不平
//   · localeValueQuality.test.ts    值的**语言**对不对
//   · **本门**                      **运行时**解析：键 → **译文**（不是键路径）
//
// ## 判据纪律
//
// ① 判据是**运行时行为**（真的 `t(key)` 一次），不是文本匹配
//    ⇒ 一道「grep 一下语言包里有这个字符串」的伪门会被**空命名空间**骗过。
// ② **负控自带**：本门同时断言「一个**故意不存在**的键**确实**解析成键路径」
//    ⇒ 若哪天 `t()` 的行为变了（改成抛错 / 返回空串），负控会红，
//    ⇒⇒ **证明「判红的那条判据真的测到了东西」**（避免恒绿门）。
// ③ 覆盖**全部 8 个语种**（本仓的语言包面）⇒ 缺一个语种就是覆盖面上的洞。
// ④ 登记簿是**单一事实源**（`scripts/i18n-empty-state-registry.json`）
//    ⇒ 新增键时本门自动跟着走，**不需要**在这里再抄一遍键名清单
//    ⇒ ⇒ 抄一遍就会漂移（§424：「抄两份的量具迟早只对一份」）。

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { i18n, setLocale } from './index'

// ★ 登记簿是 SSOT（单一事实源）—— 本门不抄键名清单。
const REGISTRY_REL = 'scripts/i18n-empty-state-registry.json'
const LOCALES = ['zh-CN', 'zh-TW', 'en-US', 'de-DE', 'fr-FR', 'es-ES', 'ja-JP', 'ar-SA'] as const

type Registry = { keys?: Array<{ key?: string; kind?: string }> }

// ★ 仓库根的定位**不依赖 cwd**（vitest 的 cwd 会随运行方式变：
//   `npm test` 从 web/ 起、`vitest --root` 从别处起 ⇒ 用 cwd 拼路径会时绿时红）。
//   ⇒ 走本文件的真实路径向上两级（src/i18n/ → src/ → 仓库根）。
const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

function readRegistry(): Registry {
  const raw = readFileSync(join(REPO_ROOT, REGISTRY_REL), 'utf8')
  return JSON.parse(raw) as Registry
}

const registry = readRegistry()
const REGISTERED_KEYS = (registry.keys ?? [])
  .map((k) => k.key)
  .filter((k): k is string => typeof k === 'string' && k.length > 0)

describe('空状态新键的运行时解析（键 → 译文，不是键路径）', { timeout: 120_000 }, () => {
  it('登记簿非空（负控前提：门要真的量到东西）', () => {
    // ★★ 「一个键都没量」也是绿的 ⇒ 假绿 ⇒ 必须有非空下限。
    // 2026-10-28 头部统计轮 +1（liveTopology.stats.emptyNoResultWindow，NoResult）。
    expect(REGISTERED_KEYS.length).toBeGreaterThan(0)
    expect(REGISTERED_KEYS.length).toBe(24)
  })

  for (const locale of LOCALES) {
    it(`${locale}：${REGISTERED_KEYS.length} 个登记键全部解析成译文（不是键路径）`, async () => {
      await setLocale(locale)
      const bad: string[] = []
      for (const key of REGISTERED_KEYS) {
        const value = i18n.global.t(key) as unknown as string
        // ★ 判据：值必须是**非空字符串**，且**不得**等于/包含键路径本身。
        //   （键路径上屏 ⇒ 用户看到的是 "sessions.stats.emptyFailedErrorSummary"。）
        if (typeof value !== 'string' || value.trim() === '' || value === key || value.includes(key)) {
          bad.push(`${key} => ${JSON.stringify(value)}`)
        }
      }
      expect(bad).toEqual([])
    })
  }

  it('负控：一个故意不存在的键**确实**解析成键路径（证明上面的判据测得到东西）', async () => {
    await setLocale('zh-CN')
    const missing = 'common.empty.__definitely_not_a_real_key__'
    const value = i18n.global.t(missing) as unknown as string
    // ★★ 若这条红了 ⇒ 说明 `t()` 的行为变了（抛错 / 返回空串），
    //   ⇒⇒ 那么上面那条「值不是键路径」的判据就**不再等价于「解析成功」**
    //   ⇒⇒ 必须重新审视判据，而不是把负控改绿。
    expect(typeof value).toBe('string')
    expect(value).toBe(missing)
  })
})
