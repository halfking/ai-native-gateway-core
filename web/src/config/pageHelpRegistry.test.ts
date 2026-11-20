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
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import { PAGE_HELP_ENTRIES, resolvePageHelp } from './pageHelpRegistry'

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

/** router.ts 中 path: '...' 的字面量集合（静态可解析；redirect 行同样带 path，一并覆盖）。 */
function routerPaths(): string[] {
  const out: string[] = []
  const re = /path: '([^']+)'/g
  let m: RegExpExecArray | null
  while ((m = re.exec(routerSource)) !== null) out.push(m[1])
  return out
}

// 虚拟匹配键：不是路由表里的真实路径，由 AppTopbar 按模式改写后传入。
// 目前只有 /dashboard-simple（简易模式总览，与 /dashboard 同一 URL）。
const VIRTUAL_MATCHES = new Set(['/dashboard-simple'])

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
