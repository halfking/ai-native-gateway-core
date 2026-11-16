// DataInitialization.categoryParity.test.ts —— 后端类别注册表 ↔ 前端「默认全选」的跨语言对账
//
// ## 这道门在挡什么
//
// 「默认要重置数据」这个承诺，落到界面上就是一句话：
// **面板一打开，后端下发的每一个类别都默认处于选中态。**
//
// 这句话有两个跨语言前提：
//   ① 后端注册表里的**每一个**类别，前端都得有显示名
//      （否则那一格渲染出原始 key，运维看不懂那一行是什么）；
//   ② 默认全选覆盖的是**后端真实下发的类别集合**，不是前端自己抄的一份清单。
//
// ★ 为什么必须**解析 Go 源码**而不是抄一份 key 清单：
//   抄清单的判据在「后端新增一个类别」时不会自己更新 —— 那正是它要防的回归。
//   （同族先例：web/src/components/board/boardPieDimensionsParity.test.ts）
//
// ★ 这道门同时是「9 类全部勾选」那条**实页证据的可复现替身**：
//   实页截图必须登录态才能拍（访客态拿不到 catalog ⇒ 面板显示
//   「authentication required」，一个类别都渲染不出来），
//   而本判据用**后端真实注册表**构造 catalog ⇒ 断言的正是「真实 9 类全选」，
//   且**任何时候 `npx vitest run` 都能跑**（不依赖线上登录态、不依赖真库）。

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import arSA from '../../locales/ar-SA/dataLifecycle'
import deDE from '../../locales/de-DE/dataLifecycle'
import enUS from '../../locales/en-US/dataLifecycle'
import esES from '../../locales/es-ES/dataLifecycle'
import frFR from '../../locales/fr-FR/dataLifecycle'
import jaJP from '../../locales/ja-JP/dataLifecycle'
import zhCN from '../../locales/zh-CN/dataLifecycle'
import zhTW from '../../locales/zh-TW/dataLifecycle'

const catalogMock = vi.fn()
const previewMock = vi.fn()
const executeMock = vi.fn()

// ⚠️ 说明符相对**本判据**解析（不是相对被测视图）—— 与同目录的
//   DataInitialization.defaultReset.test.ts 同一口径。
vi.mock('../../api/dataInit', () => ({
  dataInitCatalog: (...a: unknown[]) => catalogMock(...a),
  dataInitPreview: (...a: unknown[]) => previewMock(...a),
  dataInitExecute: (...a: unknown[]) => executeMock(...a),
}))

// 从 web/src/views/data-lifecycle 上溯 4 级 = 仓库根。
const REPO = join(import.meta.dirname, '..', '..', '..', '..')
const GO_FILE = join(REPO, 'admin', 'data_lifecycle_data_init.go')

/** 交付给用户的 9 类（owner 原始需求逐条对应；后端注册表的**超集**）。 */
const DELIVERED_CATEGORY_KEYS = [
  'request_records',
  'probe_records',
  'routing_records',
  'credentials',
  'models',
  'providers',
  'api_keys',
  'users',
  'tenants',
] as const

type GoCategory = { key: string; labelKey: string; level: 'operational' | 'configuration' }

/** 取 Go 源码里某个顶层 var 的字面量块（到列 0 的闭合 } 为止）。 */
function goVarBlock(src: string, varName: string): string {
  const start = src.indexOf(`var ${varName} = `)
  if (start < 0) throw new Error(`Go 源码里找不到 ${varName}`)
  const end = src.indexOf('\n}\n', start)
  return src.slice(start, end < 0 ? src.length : end)
}

/** 解析后端类别注册表 dataInitCategories。 */
function goCategories(): GoCategory[] {
  const src = readFileSync(GO_FILE, 'utf8')
  const block = goVarBlock(src, 'dataInitCategories')
  const out: GoCategory[] = []
  const re = /Key:\s*"([a-z0-9_]+)",\s*\n\s*LabelKey:\s*"([^"]+)",\s*\n\s*Level:\s*([A-Za-z0-9_]+),/g
  for (const m of block.matchAll(re)) {
    out.push({
      key: m[1],
      labelKey: m[2],
      level: m[3] === 'dataInitLevelOperational' ? 'operational' : 'configuration',
    })
  }
  if (out.length === 0) throw new Error('没能从 Go 源码解析出任何类别（解析规则失效了？）')
  return out
}

/** 解析后端的执行顺序 dataInitCategoryOrder（外键安全的顺序）。 */
function goCategoryOrder(): string[] {
  const src = readFileSync(GO_FILE, 'utf8')
  const block = goVarBlock(src, 'dataInitCategoryOrder')
  return [...block.matchAll(/"([a-z0-9_]+)"/g)].map(m => m[1])
}

/** 后端 label_key 的约定前缀（与被测视图里的 DATA_INIT_LABEL_PREFIX 一致）。 */
const LABEL_PREFIX = 'dataLifecycle.dataInit.categories.'

/** 按点分路径取语言包里的值。 */
function pick(bundle: unknown, path: string): unknown {
  let cur: any = bundle
  for (const seg of path.split('.')) {
    if (cur === null || cur === undefined || typeof cur !== 'object') return undefined
    cur = cur[seg]
  }
  return cur
}

const LOCALES: { name: string; bundle: unknown }[] = [
  { name: 'zh-CN', bundle: zhCN },
  { name: 'zh-TW', bundle: zhTW },
  { name: 'en-US', bundle: enUS },
  { name: 'ja-JP', bundle: jaJP },
  { name: 'de-DE', bundle: deDE },
  { name: 'fr-FR', bundle: frFR },
  { name: 'es-ES', bundle: esES },
  { name: 'ar-SA', bundle: arSA },
]

// ★ 用**真实 zh-CN 文案**装配 i18n（不是空 messages）：
//   只有带上真文案，才能断言「那一格渲染的是显示名，而不是原始 key 路径」。
const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': { dataLifecycle: zhCN } },
})

/** 用后端真实注册表构造 catalog（就是后端 catalog 端点下发的形状）。 */
function catalogFromGo(): unknown[] {
  return goCategories().map(c => ({
    key: c.key,
    label_key: c.labelKey,
    level: c.level,
    tables: [
      {
        name: c.key,
        exists: true,
        rows: 1,
        bytes: 1,
        bytes_human: '1 B',
        protected_rows: 0,
        target_rows: 1,
      },
    ],
    rows: 1,
    bytes: 1,
    bytes_human: '1 B',
    protected_rows: 0,
    target_rows: 1,
  }))
}

async function renderPanel() {
  const View = (await import('./DataInitialization.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

type PanelVm = { selected: string[] }

describe('数据初始化：后端注册表 ↔ 前端默认全选（跨语言对账）', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    catalogMock.mockResolvedValue({
      generated_at: '2026-11-17T00:00:00Z',
      categories: catalogFromGo(),
    })
    previewMock.mockResolvedValue({
      categories: [],
      total_rows: 0,
      total_protected_rows: 0,
      total_target_rows: 0,
      confirm_token: 'tok',
      token_expires_at: '2026-11-17T00:05:00Z',
      warnings: [],
    })
    executeMock.mockResolvedValue({
      actor: 'admin',
      total_rows_deleted: 0,
      results: [],
      executed_at: '2026-11-17T00:00:00Z',
    })
  })

  it('① 后端注册表自洽：dataInitCategoryOrder == dataInitCategories 的 key 顺序', () => {
    const order = goCategoryOrder()
    const keys = goCategories().map(c => c.key)
    expect(order).toEqual(keys)
    // ★ 解析器自身不能悄悄失效（恒真守卫等于没有守卫）。
    expect(keys.length).toBeGreaterThanOrEqual(DELIVERED_CATEGORY_KEYS.length)
  })

  it('② 交付的 9 类都在后端注册表里（owner 原始需求逐条落地）', () => {
    const keys = new Set(goCategories().map(c => c.key))
    for (const k of DELIVERED_CATEGORY_KEYS) {
      expect(keys.has(k), `后端注册表里没有 ${k}`).toBe(true)
    }
  })

  it('③ 每个类别的 label_key 在**全部 8 个语种**里都有非空显示名', () => {
    for (const cat of goCategories()) {
      expect(cat.labelKey.startsWith(LABEL_PREFIX), `${cat.key} 的 label_key 不在约定前缀下：${cat.labelKey}`).toBe(true)
      const tail = cat.labelKey.slice(LABEL_PREFIX.length)
      for (const { name, bundle } of LOCALES) {
        const value = pick(bundle, `dataInit.categories.${tail}`)
        expect(typeof value, `${name} 语种缺 ${cat.labelKey}`).toBe('string')
        expect(String(value).trim().length, `${name} 语种的 ${cat.labelKey} 是空串`).toBeGreaterThan(0)
        // 值不得等于键路径本身（那是「没翻」的形态）。
        expect(String(value)).not.toBe(cat.labelKey)
      }
    }
  })

  it('④ ★ 默认全选覆盖后端**真实**的每一个类别（打开即是「准备重置」姿态）', async () => {
    const w = await renderPanel()
    const cats = goCategories()
    const boxes = w.findAll('input[type="checkbox"]')
    expect(boxes.length).toBe(cats.length)
    for (const box of boxes) {
      expect((box.element as HTMLInputElement).checked).toBe(true)
    }
    const selected = (w.vm as unknown as PanelVm).selected
    expect([...selected].sort()).toEqual(cats.map(c => c.key).sort())
  })

  it('⑤ 「预览重置」在真实 9 类下立即可用（不必先手动勾一遍）', async () => {
    const w = await renderPanel()
    // ★ 按**真实中文文案**找按钮（不是键路径）：本判据装的是真语言包，
    //   界面上的字就是「预览重置」⇒ 顺带证明这条按钮真的有中文文案。
    const label = String(pick(zhCN, 'dataInit.preview'))
    const btn = w.findAll('button').find(b => b.text().includes(label))
    expect(btn, `找不到「${label}」按钮`).toBeTruthy()
    expect((btn!.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('⑥ 每一类都渲染出**显示名**，而不是原始 key 路径', async () => {
    const w = await renderPanel()
    const text = w.text()
    for (const cat of goCategories()) {
      expect(text.includes(cat.labelKey), `界面里漏了 ${cat.labelKey} 的原始 key 路径（说明该类没渲染出显示名）`).toBe(false)
      const shown = pick(zhCN, `dataInit.categories.${cat.labelKey.slice(LABEL_PREFIX.length)}`)
      expect(text.includes(String(shown)), `界面上没有 ${cat.key} 的显示名 ${String(shown)}`).toBe(true)
    }
  })
})
