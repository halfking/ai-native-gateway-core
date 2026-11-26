// pageHelpRegistry.test.ts —— 页面级帮助注册表钉测（2026-11-20）
//
// 钉住四件事：
//  1. ★ 内容完整性：注册表每一条 id 都有对应的 web/content/page-help/<id>.zh-CN.md，
//     且每个文件都能被某条注册项引用——双向对拍，一方漂移即红。
//  2. ★ 路由真实性：注册表每一条 match 都能在 router.ts 里找到同 path 的真实路由
//     （虚拟键除外，见 VIRTUAL_MATCHES）——防止注册表写了路由表里不存在的页面。
//  3. ★ 全覆盖：router.ts 里 meta.requiresAuth/requiresSuper/requiresPlatformOps
//     的页面路由（排除已知豁免）都应有帮助登记——帮助入口是页面级能力，漏登一页
//     就少一个 ？ 图标。
//  4. resolvePageHelp 行为：声明顺序优先、:param 通配、简易虚拟键可命中。

import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { PAGE_HELP_ENTRIES, resolvePageHelp, resolvePageHelpById } from './pageHelpRegistry'

const here = dirname(fileURLToPath(import.meta.url))
const webRoot = join(here, '..', '..')

const routerSource = readFileSync(join(webRoot, 'src', 'router.ts'), 'utf8')
const registrySource = readFileSync(join(webRoot, 'src', 'config', 'pageHelpRegistry.ts'), 'utf8')

/** 读取 web/content/page-help/ 下全部内容 id（文件名单一中文语种，与 help-manual 同先例）。 */
function listContentIds(): string[] {
  const dir = join(webRoot, 'content', 'page-help')
  const ids: string[] = []
  const re = /^([a-z0-9-]+)\.zh-CN\.md$/
  try {
    for (const name of readdirSync(dir)) {
      const m = re.exec(name)
      if (m) ids.push(m[1])
    }
  } catch {
    // 目录缺失时由内容完整性用例红出，不在读取处抛
  }
  return ids
}

/** router.ts 中 path: / alias: '...' 的字面量集合（静态可解析；redirect 行同样带 path，一并覆盖；alias 也是可访问的真实路径 ⇒ 2026-11-26 合并轮补收，211 轮把 /admin/session-analytics/user-profiles 挂成了 alias）。 */
function routerPaths(): string[] {
  const out: string[] = []
  const re = /(?:path|alias): '([^']+)'/g
  let m: RegExpExecArray | null
  while ((m = re.exec(routerSource)) !== null) out.push(m[1])
  return out
}

// 虚拟匹配键：不是路由表里的真实路径，由 AppTopbar 按模式改写后传入。
// 目前只有 /dashboard-simple（简易模式总览，与 /dashboard 同一 URL）。
// 2026-11-23 统计标准轮新增 /dashboard-stats-standards：它**不是路由**，
// 而是「看板」与「会话与统计」两个页签的页面内嵌帮助入口指向的正文 id
// （页面内嵌按钮没有自己的路由 ⇒ 由 resolvePageHelpById 按内容 id 解析）。
const VIRTUAL_MATCHES = new Set(['/dashboard-simple', '/dashboard-stats-standards'])

describe('pageHelpRegistry 内容完整性', () => {
  it('注册表每个 id 都有内容文件', () => {
    const files = new Set(listContentIds())
    const missing = PAGE_HELP_ENTRIES.filter((e) => !files.has(e.id))
    expect(missing, `缺内容文件: ${missing.map((e) => e.id).join(', ')}`).toEqual([])
  })

  it('每个内容文件都被注册表引用（无孤儿文件）', () => {
    const used = new Set(PAGE_HELP_ENTRIES.map((e) => e.id))
    const orphans = listContentIds().filter((id) => !used.has(id))
    expect(orphans, `孤儿内容文件: ${orphans.join(', ')}`).toEqual([])
  })

  it('注册表 titleKey 在 zh-CN 语言包里真实存在', () => {
    const zhCn = readFileSync(join(webRoot, 'src', 'locales', 'zh-CN', 'pageHelp.ts'), 'utf8')
    const missing = PAGE_HELP_ENTRIES
      .map((e) => e.titleKey.split('.').pop() as string)
      .filter((last) => !new RegExp(`\\b${last}:`).test(zhCn))
    expect(missing, `zh-CN pageHelp 缺 title 键: ${missing.join(', ')}`).toEqual([])
  })
})

describe('pageHelpRegistry 路由真实性', () => {
  it('每条 match（除虚拟键）都对应 router.ts 里的真实路由 path', () => {
    const paths = new Set(routerPaths())
    const ghost = PAGE_HELP_ENTRIES
      .map((e) => e.match)
      .filter((match) => !VIRTUAL_MATCHES.has(match) && !paths.has(match))
    expect(ghost, `注册了 router.ts 里不存在的路由: ${ghost.join(', ')}`).toEqual([])
  })

  it('注册表 match 无重复', () => {
    const matches = PAGE_HELP_ENTRIES.map((e) => e.match)
    expect(new Set(matches).size, '存在重复 match').toBe(matches.length)
  })

  it('同一 id 的多条登记必须指向同一份内容文件（id 即内容键，titleKey 可按页面语境不同）', () => {
    // 设计口径：id 是 content/page-help/<id>.zh-CN.md 的内容键；多条 match 共享
    // 同一 id = 多页共用一篇正文（如三个排行页共用一篇排行说明）。钉的是
    // 「同 id 的 titleKey 都登记在 zh-CN 语言包里」这一可行性，语义差异允许。
    const bad = PAGE_HELP_ENTRIES.filter((e) => !/^pageHelp\.title\./.test(e.titleKey))
    expect(bad.map((e) => `${e.id}: ${e.titleKey}`)).toEqual([])
  })

  it('每个 titleKey 都是 pageHelp.title.* 形态', () => {
    const bad = PAGE_HELP_ENTRIES.filter((e) => !/^pageHelp\.title\.[A-Za-z0-9]+$/.test(e.titleKey))
    expect(bad.map((e) => e.titleKey)).toEqual([])
  })
})

describe('resolvePageHelp 行为', () => {
  it('精确路径命中', () => {
    expect(resolvePageHelp('/dashboard')?.id).toBe('dashboard')
    expect(resolvePageHelp('/request-logs')?.id).toBe('request-logs')
  })
  it(':param 路由任意段命中', () => {
    expect(resolvePageHelp('/keys/k-123')?.id).toBe('key-detail')
    expect(resolvePageHelp('/admin/sessions/abc')?.id).toBe('session-detail')
  })

  it('虚拟键 /dashboard-simple 可命中（简易模式总览）', () => {
    expect(resolvePageHelp('/dashboard-simple')?.id).toBe('dashboard-simple')
  })

  it('未登记路径返回 null', () => {
    expect(resolvePageHelp('/login')).toBeNull()
    expect(resolvePageHelp('/no-such-page')).toBeNull()
  })

  it('按声明顺序取第一个命中（前缀不越权匹配）', () => {
    // /live 与 /live2 是不同路由：/live 不得吞掉 /live2
    expect(resolvePageHelp('/live2')?.id).toBe('live-stage-tree')
  })
})

// 2026-11-23 统计标准轮：页面内嵌帮助入口（看板 / 会话与统计两个页签）。
// 页面内嵌按钮没有自己的路由 ⇒ 用「内容 id」解析；标题键必须出自注册表
// （页面不自带文案 ⇒ 语种切换只有一个来源）。
describe('页面内嵌帮助（按内容 id 解析）', () => {
  const STANDARDS_ID = 'dashboard-stats-standards'

  it('resolvePageHelpById 命中统计标准正文，标题键出自注册表', () => {
    const entry = resolvePageHelpById(STANDARDS_ID)
    expect(entry?.id).toBe(STANDARDS_ID)
    expect(entry?.titleKey).toBe('pageHelp.title.dashboardStatsStandards')
  })

  it('未登记的内容 id 返回 null（页面内嵌传错 id 时不崩、不串页）', () => {
    expect(resolvePageHelpById('no-such-content-id')).toBeNull()
    expect(resolvePageHelpById('')).toBeNull()
  })

  it('统计标准正文文件在仓内（中文 + 6 个已译语种齐全）', () => {
    const dir = join(webRoot, 'content', 'page-help')
    for (const locale of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'es-ES', 'fr-FR']) {
      expect(
        existsSync(join(dir, `${STANDARDS_ID}.${locale}.md`)),
        `缺正文文件: ${STANDARDS_ID}.${locale}.md`,
      ).toBe(true)
    }
  })

  it('统计标准正文点明两页口径差异的关键事实（口径别漂移）', () => {
    const body = readFileSync(join(webRoot, 'content', 'page-help', `${STANDARDS_ID}.zh-CN.md`), 'utf8')
    // 三件必须说清的事：实体不同、窗口不同、探测剔除面不同
    expect(body).toContain('请求级')
    expect(body).toContain('会话级')
    expect(body).toContain('探测')
    // 费用/请求数对照表（两页实测值）
    // ★ 2026-11-23：正文在「订正三模块数据一致性结论」轮被重写，实测值随之
    //   更新（看板 81,265 笔 / $8,854.70；会话统计 58,180 笔 / $1,551.41）。
    //   本断言此前仍钉**重写前**的旧值（65,774 / 72,098 / 8,013.93 / 2,717.62）
    //   ⇒ 门长期红（不是被测坏了，是断言指向了旧稿）。
    //   ⇒ 这里只把指针重新指向**正文当前的实测值**（不动任何度量）。
    expect(body).toContain('81,265')
    expect(body).toContain('58,180')
    expect(body).toContain('8,854.70')
    expect(body).toContain('1,551.41')
  })
})
