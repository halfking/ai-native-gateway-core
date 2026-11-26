// UserProfileView.undefinedGuard.test.ts —— L11（undefined 防护）回归门。
//
// ★ 本文件的来历：AGENTS.md 登记的 L11＝「`UserProfileView.vue:430` 未防 undefined」。
//   核对结果：**该原点已被上游提交 `acd409a26` 修掉**
//   （`data?.top_tasks.length` → `data?.top_tasks?.length`）——
//   ⇒ 「修复」这一动作本身已完成，**缺的是把它钉住的回归门**：
//   没有门，下次有人把 `?.` 删掉（或写成 `data?.x.length` 这种只护一半的形态），
//   没有任何东西会红 ⇒ 于是它会在某个降级响应上炸成整页白屏。
//
// ★ 本文件钉三件事：
//   ① **组件级**：运行时喂进「字段缺失 / 字段是字符串 / 非有限数」的 payload，
//      断言页面**不崩**且**不显示错值**（这比「不崩」更难：崩是显性的，
//      静默错值是隐性的——9.4% 看起来完全正常）。
//   ② **源码契约级**：钉住 L11 原点的防护形态（`data?.top_tasks?.length`），
//      防止「只护一半」的写法回潮。
//   ③ **算式收敛**：成功率算式必须走 `successRateText`（显式类型判据），
//      不得在模板里出现裸的 `data.total_success + data.total_errors`。
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { ElLoading } from 'element-plus'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserProfileView from './UserProfileView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/UserProfileView.vue'), 'utf8')
// 去掉注释与字符串，避免「注释里写了旧写法」被误判成代码里还在用。
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    /** 覆盖 payload 的字段（用于制造「缺字段 / 类型不对」的响应）。 */
    override: {} as Record<string, unknown>,
    reset() {
      ctrl.override = {}
    },
  },
}))

/**
 * 正常 payload（缺 `top_tasks` 等在测试里单独制造）。
 * `daily_cost_trend` 给空数组 ⇒ `renderCostChart()` 直接 return，
 * 不让 echarts 在 jsdom 的 0 尺寸容器上初始化（那不是本门要验的东西）。
 */
function payload() {
  return {
    owner_user: 'alice',
    session_count: 6,
    total_cost_usd: 1.79,
    total_requests: 20,
    total_success: 18,
    total_errors: 2,
    avg_health_score: 77,
    end_user_count: 2,
    first_seen_at: '2026-10-01T00:00:00Z',
    last_seen_at: '2026-10-06T00:00:00Z',
    health_distribution: { a: 1, b: 1, c: 1, d: 1, f: 1 },
    daily_cost_trend: [],
    top_tasks: [],
    top_end_users: [],
    recent_sessions: [],
    ...ctrl.override,
  }
}

vi.mock('../api/admin', () => ({
  getUserProfile: vi.fn(async () => payload()),
  getUserModelUsage: vi.fn(async () => ({ models: [] })),
  getUserProjects: vi.fn(async () => ({ projects: [], degraded: false })),
}))

vi.mock('../components/ui/KxDateRangePicker.vue', () => ({
  default: {
    props: ['modelValue', 'presets', 'maxSpanDays'],
    emits: ['apply'],
    template: '<div class="kxdaterangepicker-stub" />',
  },
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { owner: 'alice' }, query: {} }),
  useRouter: () => ({ back: vi.fn(), push: vi.fn() }),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      common: { back: '返回' },
      sessions: {
        userProfile: {
          detailTitle: '用户画像详情',
          empty: '暂无用户画像数据',
          loadFailed: '加载失败',
          successRate: '成功率',
          sessionCount: '会话数',
          requestCount: '请求数',
          totalCost: '总成本',
          topTasks: '热门任务',
          topEndUsers: '热门终端用户',
          recentSessions: '最近会话',
          costTrend: '成本趋势',
          lastSeenAt: '最近活跃',
          createdAt: '创建时间',
        },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无数据' } },
    },
  },
})

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches, media: q, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
      }
    },
  })
}

async function factory(cls: 'compact' | 'expanded' = 'expanded') {
  // ★ 视口模式**必须显式指定**：模板里 `:empty="isCompact && (data?.top_tasks?.length ?? 0) === 0"`
  //   在 expanded 模式下 `isCompact` 为 false ⇒ **右侧整个表达式被短路、根本不被求值**
  //   ⇒ 那些「数组字段缺失」的断言在 expanded 下是**假门**（怎么改代码都不红）。
  //   ⇒ 要真正触达那行，必须跑 compact。
  mockWindowClass(cls)
  const w = mount(UserProfileView, {
    global: { plugins: [i18n], directives: { loading: ElLoading.directive } },
  })
  await flushPromises()
  await flushPromises()
  return w
}

/** 成功率卡的显示文本。 */
function successRateText(w: Awaited<ReturnType<typeof factory>>): string {
  return w.find('[data-testid="up-success-rate"]').text()
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('L11 · undefined 防护：组件级（运行时喂畸形响应）', () => {
  it('L11 原点：top_tasks 字段整体缺失 ⇒ 页面不崩（曾因 data?.top_tasks.length 抛 TypeError）', async () => {
    // ★ 这一条钉的是「只护一半」的写法：`data?.top_tasks` 护住了 data，
    //   但 `.length` 仍在 ⇒ data 存在、top_tasks 缺失时抛 TypeError ⇒ 整页白屏。
    // ★ 必须 **compact** 模式：`isCompact &&` 会短路掉右侧（expanded 下那行不被求值）。
    ctrl.override = { top_tasks: undefined }
    const w = await factory('compact')
    expect(w.exists()).toBe(true)
    // 页面仍然渲染出内容（不是崩在半路）
    expect(w.find('[data-testid="up-success-rate"]').exists()).toBe(true)
    expect(successRateText(w)).toBe('90.0%')
  })

  it('三个数组字段同时缺失也不崩（top_tasks / top_end_users / recent_sessions）', async () => {
    ctrl.override = { top_tasks: undefined, top_end_users: undefined, recent_sessions: undefined }
    const w = await factory('compact')
    expect(w.find('[data-testid="up-success-rate"]').exists()).toBe(true)
    expect(w.find('[data-testid="up-top-tasks"]').exists()).toBe(true)
  })

  it('成功率：字段缺失 ⇒ 显示 "-"，不是 NaN%', async () => {
    ctrl.override = { total_success: undefined, total_errors: undefined }
    const w = await factory()
    expect(successRateText(w)).toBe('-')
    expect(successRateText(w)).not.toContain('NaN')
  })

  it('成功率：字段是字符串 ⇒ 显示 "-"（旧写法会给 9.4% 的静默错值）', async () => {
    // ★ 原写法的静默错值路径：'5' + 3 = '53' ⇒ '5' / '53' = 0.0943 ⇒ 9.4%（真值 93.4%）。
    ctrl.override = { total_success: '5', total_errors: 3 }
    const w = await factory()
    expect(successRateText(w)).toBe('-')
    expect(successRateText(w)).not.toContain('9.4')
  })

  it('成功率：非有限数（NaN / Infinity）⇒ 显示 "-"', async () => {
    ctrl.override = { total_success: Number.NaN, total_errors: 2 }
    const w = await factory()
    expect(successRateText(w)).toBe('-')

    ctrl.override = { total_success: 8, total_errors: Number.POSITIVE_INFINITY }
    const w2 = await factory()
    expect(successRateText(w2)).toBe('-')
  })

  it('成功率：真值为 0 不被当成「无数据」（0 成功 / 5 失败 ⇒ 0.0%）', async () => {
    // ★ 「0」与「缺失」是两件事：0 成功 + 5 失败 的真值就是 0.0%，
    //   显示 '-' 等于把一个真值说成「没有」⇒ 同一族里的另一种静默错误答案。
    ctrl.override = { total_success: 0, total_errors: 5 }
    const w = await factory()
    expect(successRateText(w)).toBe('0.0%')
  })

  it('成功率：分母为 0 ⇒ 显示 "-"（真的没有请求）', async () => {
    ctrl.override = { total_success: 0, total_errors: 0 }
    const w = await factory()
    expect(successRateText(w)).toBe('-')
  })

  it('成功率：正常路径不回归（8 成功 / 2 失败 ⇒ 80.0%）', async () => {
    ctrl.override = { total_success: 8, total_errors: 2 }
    const w = await factory()
    expect(successRateText(w)).toBe('80.0%')
  })
})

describe('L11 · undefined 防护：源码契约级（防回潮）', () => {
  it('模板不得出现裸的 data.total_success + data.total_errors 算式', () => {
    // 算式必须走 successRateText（显式类型判据）；模板里内联算式＝判据退化回旧形态。
    expect(CODE_ONLY).not.toMatch(/data\.total_success\s*\+/)
    expect(CODE_ONLY).toContain('successRateText')
  })

  it('数组字段访问必须带双层可选链（data?.x?.length / data?.x ?? []）', () => {
    // 只护一半的写法（data?.x.length）会让「data 在、x 不在」炸掉 ⇒ 钉死形态。
    expect(CODE_ONLY).not.toMatch(/data\?\.[a-z_]+\.length/)
    expect(CODE_ONLY).toMatch(/data\?\.top_tasks\?\.length/)
  })

  it('成功率判据是显式类型判据（typeof），不是算术隐式兜底', () => {
    expect(CODE_ONLY).toMatch(/typeof success !== 'number'/)
    expect(CODE_ONLY).toMatch(/typeof errors !== 'number'/)
  })
})
