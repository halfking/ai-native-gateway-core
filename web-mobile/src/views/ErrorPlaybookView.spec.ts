import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import ErrorPlaybookView from './ErrorPlaybookView.vue'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  getStats,
  listEntries,
  listExecutions,
  PLAYBOOK_PLAN_KINDS,
  PLAYBOOK_SOURCES,
  EXECUTION_STATUSES,
  TRIGGER_SOURCES,
} from '@/api/errorPlaybook'

/**
 * 错误处理手册（移动只读席）回归护栏（2026-11-25 菜单 PC 对齐轮）。
 *
 * 判据钉六组：
 *  ① 权限档 super_admin（第三类过度限制）：注册处 AdminMiddleware 不检 role +
 *     entries/executions 无 tenant_id 列 ⇒ 平台级全局事实；抽屉对 tenant_admin 隐藏、
 *     对 super_admin 可见；**不得**落进 admin 档语义。
 *  ② 只读：模块与视图源码里**不得出现**任何写方法 —— execute/revert/analyze/PUT/DELETE
 *     全家（这是本席的立身之本：脚本/API/LLM 分析都有真实副作用）。
 *  ③ 失败安全：请求失败 ⇒ error 披露、不发崩溃；null entries ⇒ 空列表而非崩溃。
 *  ④ 后端枚举闭合：本模块四个枚举常量与 schema CHECK / engine 写入侧逐字同集。
 *  ⑤ i18n 两语种键存在且非「值 === 键名」。
 *  ⑥ 菜单接线：席位在 data-ops 组（与 PC 同组）、routeAllowedFor 按角色放行/拦截。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/errorPlaybook', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/errorPlaybook')>()
  return {
    ...actual,
    getStats: vi.fn(),
    listEntries: vi.fn(),
    listExecutions: vi.fn(),
  }
})

const mStats = getStats as unknown as ReturnType<typeof vi.fn>
const mEntries = listEntries as unknown as ReturnType<typeof vi.fn>
const mExecs = listExecutions as unknown as ReturnType<typeof vi.fn>

function entryFixture(over: Partial<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    id: 1,
    error_type: 'http_502_upstream',
    match_pattern: '',
    title: '上游 502 重试方案',
    description: '',
    root_cause: '',
    plan_kind: 'script',
    confidence: 0.9,
    source: 'pi_analysis',
    source_queue_id: 0,
    enabled: true,
    auto_execute: false,
    hit_count: 5,
    execution_count: 3,
    success_count: 2,
    failure_count: 1,
    last_hit_at: '2026-11-25T10:00:00Z',
    last_executed_at: '2026-11-25T11:00:00Z',
    last_status: 'success',
    refinement_count: 0,
    created_by: 'seed',
    created_at: '2026-11-21T00:00:00Z',
    updated_at: '2026-11-21T00:00:00Z',
    ...over,
  }
}

function execFixture(over: Partial<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    id: 10,
    entry_id: 1,
    error_type: 'http_502_upstream',
    match_pattern: '',
    trigger_source: 'standard',
    status: 'success',
    duration_ms: 120,
    executed_by: 'worker',
    created_at: '2026-11-25T11:00:00Z',
    ...over,
  }
}

const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const w = mount(ErrorPlaybookView)
  await flushPromises()
  await flushPromises()
  // ★ 视图按全族普查（RestrictedPageGuard ③-2）不自动取数 ⇒ 点「刷新」触发。
  const btn = w.find('[data-testid="ep-load"]')
  if (btn.exists()) {
    await btn.trigger('click')
    await flushPromises()
    await flushPromises()
  }
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  mStats.mockResolvedValue({
    engine_wired: true,
    llm_wired: false,
    total_entries: 3,
    enabled_entries: 2,
    auto_execute_entries: 1,
    executions: { last_24h: {}, total: 7 },
    pending_unknown: 0,
    provisioned: true,
  })
  mEntries.mockResolvedValue({ entries: [entryFixture()], total: 3 })
  mExecs.mockResolvedValue({ executions: [execFixture()], total: 7 })
})

// ── ① 权限档 ────────────────────────────────────────────────────────────────

describe('① 权限档 super_admin（第三类过度限制）', () => {
  it('1-1 ★★★ 注册处行号订正：wrap = AdminMiddleware 的形态仍在这两行', () => {
    // ★ 读真源码而不是背读数：admin/error_playbook_handler.go 的 RegisterRoutes
    //   里 wrap 必须仍是 AdminMiddleware（只认证不检 role）。若后端哪天补了
    //   角色下限，这条会红 ⇒ 那时抽屉席可以降档（过度限制就该跟着放开）。
    const p = join(__dirname, '../../../admin/error_playbook_handler.go')
    expect(existsSync(p)).toBe(true)
    const lines = readFileSync(p, 'utf8').split('\n')
    const at = lines.findIndex((l) => l.includes('wrap := func(fn http.HandlerFunc) http.HandlerFunc'))
    expect(at).toBeGreaterThanOrEqual(0)
    expect(lines[at + 1]).toMatch(/AdminMiddleware\(fn, h\.pool, h\.secret\)/)
  })

  it('1-2 ★★ schema 钉：entries/executions 两表无 tenant_id 列（平台级事实的物证）', () => {
    const p = join(__dirname, '../../../sql/migrations/startup/995_error_playbook.sql')
    expect(existsSync(p)).toBe(true)
    const sql = readFileSync(p, 'utf8')
    for (const table of ['error_playbook_entries', 'error_playbook_executions']) {
      const at = sql.indexOf(`CREATE TABLE IF NOT EXISTS public.${table}`)
      expect(at, `${table} 建表语句应在迁移里`).toBeGreaterThanOrEqual(0)
      const body = sql.slice(at, sql.indexOf(');', at))
      expect(body.includes('tenant_id'), `${table} 不应含 tenant_id 列`).toBe(false)
    }
  })

  it('1-3 ★★ 抽屉：tenant_admin 看不到这一席；super_admin 看得到', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'error-playbook')
    expect(seat, '席位应在 DRAWER_NAV 里').toBeTruthy()
    expect(seat!.requiresRole).toBe('super_admin')
    expect(navItemsFor([seat!], 'tenant_admin')).toEqual([])
    expect(navItemsFor([seat!], 'super_admin')).toHaveLength(1)
  })

  it('1-4 ★ 席位归 data-ops 组（与 PC 端同组）', async () => {
    const { DRAWER_GROUPS } = await import('@/config/appNav')
    const group = DRAWER_GROUPS.find((g) => g.items.some((i) => i.key === 'error-playbook'))
    expect(group?.id).toBe('data-ops')
  })
})

// ── ② 只读 ──────────────────────────────────────────────────────────────────

describe('② 只读（写端点全家不碰）', () => {
  it('2-1 ★★★★ API 模块只 export GET 三个函数；请求方法字面量里无 POST/PUT/DELETE', async () => {
    const mod = await import('@/api/errorPlaybook')
    const fns = Object.entries(mod).filter(([, v]) => typeof v === 'function')
    expect(fns.map(([n]) => n).sort()).toEqual(['getStats', 'listEntries', 'listExecutions'])
    const src = readFileSync(join(__dirname, '../api/errorPlaybook.ts'), 'utf8')
    const methods = [...src.matchAll(/'(GET|POST|PUT|PATCH|DELETE)'/g)].map((m) => m[1])
    expect(methods.every((m) => m === 'GET'), `应只有 GET，实际 ${methods}`).toBe(true)
  })

  it('2-2 ★★★ 视图源码不出现写端点路径片段（注释里的「不碰清单」除外）', async () => {
    const src = readFileSync(join(__dirname, './ErrorPlaybookView.vue'), 'utf8')
    // 写端点片段只允许出现在**说明不碰什么**的注释里；strip 注释后必须为零。
    // （stripComments 从 keyScan 导入 —— 与 i18n 门同一条剥离实现。）
    const { stripComments } = await import('@/i18n/keyScan')
    const code = stripComments(src)
    for (const bad of ['/execute', '/revert', '/analyze', 'createEntry', 'deleteEntry']) {
      expect(code.includes(bad), `视图代码（剥注释后）不应出现 ${bad}`).toBe(false)
    }
  })

  it('2-3 ★★ 模块头写明写端点清单（纪律：不碰什么要写下来）', () => {
    const src = readFileSync(join(__dirname, '../api/errorPlaybook.ts'), 'utf8')
    expect(src).toMatch(/execute/)
    expect(src).toMatch(/revert/)
    expect(src).toMatch(/analyze/)
  })
})

// ── ③ 失败安全 ──────────────────────────────────────────────────────────────

describe('③ 失败安全', () => {
  it('3-1 ★★ 请求失败 ⇒ error 披露、不崩溃', async () => {
    mStats.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    expect(w.find('[data-testid="ep-error"]').text()).toContain('boom')
  })

  it('3-2 ★★ null entries / null executions ⇒ 空列表而非崩溃（后端 continue 丢行形态）', async () => {
    mEntries.mockResolvedValue({ entries: null, total: 3 })
    mExecs.mockResolvedValue({ executions: null, total: 7 })
    const w = await mountView()
    expect((w.find('[data-testid="ep-entries"]').element as HTMLElement).children.length).toBe(0)
    expect((w.find('[data-testid="ep-execs"]').element as HTMLElement).children.length).toBe(0)
  })

  it('3-3 ★ Go 零值时间按「从未」披露（last_executed_at = 0001-01-01 形态；en 环境断言 never）', async () => {
    mEntries.mockResolvedValue({ entries: [entryFixture({ last_executed_at: '0001-01-01T00:00:00Z' })], total: 1 })
    const w = await mountView()
    // 测试环境 locale=en-US ⇒ 断言 en 文案（zh 文案同一 key，真机同代码路径）。
    expect(w.text().toLowerCase()).toContain('never')
  })

  it('3-4 ★ 列表计数披露「本页 n · 总数 total」差额语义', async () => {
    mEntries.mockResolvedValue({ entries: [entryFixture()], total: 30 })
    const w = await mountView()
    expect(w.find('[data-testid="ep-entries-count"]').text()).toContain('30')
  })
})

// ── ④ 枚举闭合 ──────────────────────────────────────────────────────────────

describe('④ 后端枚举闭合', () => {
  it('4-1 ★★ plan/source/trigger 与 schema CHECK 逐字同集', () => {
    const p = join(__dirname, '../../../sql/migrations/startup/995_error_playbook.sql')
    const sql = readFileSync(p, 'utf8')
    expect(sql).toContain(`'script', 'api', 'prompt'`)
    expect(sql).toContain(`'seed', 'manual', 'pi_analysis', 'refinement'`)
    expect(sql).toContain(`'standard', 'auto_analyze', 'manual', 'refinement'`)
    expect([...PLAYBOOK_PLAN_KINDS]).toEqual(['script', 'api', 'prompt'])
    expect([...PLAYBOOK_SOURCES]).toEqual(['seed', 'manual', 'pi_analysis', 'refinement'])
    expect([...TRIGGER_SOURCES]).toEqual(['standard', 'auto_analyze', 'manual', 'refinement'])
  })

  it('4-2 ★ status 集与 engine 写入侧同集（running/success/failed/skipped）', () => {
    expect([...EXECUTION_STATUSES]).toEqual(['running', 'success', 'failed', 'skipped'])
  })
})

// ── ⑤ i18n ──────────────────────────────────────────────────────────────────

describe('⑤ i18n 两语种', () => {
  const KEYS = [
    'ep.title',
    'ep.reload',
    'ep.readonlyNote',
    'ep.never',
    'ep.enabled',
    'ep.disabled',
    'ep.filter.all',
    'ep.stats.title',
    'ep.stats.totalEntries',
    'ep.stats.engineOn',
    'ep.entries.title',
    'ep.entries.autoExecute',
    'ep.execs.title',
  ]

  it('5-1 ★★ 关键键在两侧词典都存在且非「值 === 键名」', () => {
    for (const locale of ['zh-CN', 'en-US'] as const) {
      for (const key of KEYS) {
        const v = dictLookup(DICT[locale], key)
        expect(v, `${locale}:${key} 应存在`).toBeDefined()
        expect(typeof v).toBe('string')
        expect(v, `${locale}:${key} 值不应等于键名`).not.toBe(key)
      }
    }
  })

  it('5-2 ★ 枚举文案四族在两侧都齐（source/plan/status/trigger）', () => {
    for (const locale of ['zh-CN', 'en-US'] as const) {
      for (const s of PLAYBOOK_SOURCES) expect(dictLookup(DICT[locale], `ep.source.${s}`)).toBeDefined()
      for (const k of PLAYBOOK_PLAN_KINDS) expect(dictLookup(DICT[locale], `ep.plan.${k}`)).toBeDefined()
      for (const s of EXECUTION_STATUSES) expect(dictLookup(DICT[locale], `ep.status.${s}`)).toBeDefined()
      for (const s of TRIGGER_SOURCES) expect(dictLookup(DICT[locale], `ep.trigger.${s}`)).toBeDefined()
    }
  })

  it("5-3 ★★ nav.errorPlaybook 与 PC 端同文案（zh: 错误处理手册 / en: Error Playbook）", () => {
    expect(dictLookup(zhCN, 'nav.errorPlaybook')).toBe('错误处理手册')
    expect(dictLookup(enUS, 'nav.errorPlaybook')).toBe('Error Playbook')
  })
})

// ── ⑥ 接线 ──────────────────────────────────────────────────────────────────

describe('⑥ 菜单与路由接线', () => {
  it('6-1 ★★ 路由 meta 带 requiresRole: super_admin（hyper-wiring routeAllowedFor 消费）', async () => {
    const src = readFileSync(join(__dirname, '../router/index.ts'), 'utf8')
    const at = src.indexOf("path: '/error-playbook'")
    expect(at, '/error-playbook 路由应存在').toBeGreaterThanOrEqual(0)
    const block = src.slice(at, src.indexOf('},', at))
    expect(block).toMatch(/requiresRole: 'super_admin'/)
    expect(block).toMatch(/requiresAuth: true/)
  })

  it('6-2 ★ routeAllowedFor：super_admin 放行、tenant_admin 拦截', async () => {
    const { routeAllowedFor } = await import('@/config/appNav')
    expect(routeAllowedFor('/error-playbook', 'super_admin', null)).toBe(true)
    expect(routeAllowedFor('/error-playbook', 'tenant_admin', null)).toBe(false)
  })
})
