// BoardDistGrid.pieDegraded.test.ts —— 饼图维度降级不得渲染成「没有数据」
//
// ## 挡住的是什么
//
// 2026-10-03 之前，dashboard_board_fallback.go 的回退路径是：
//
//     items, err := h.fallbackDimPie(...)
//     if err != nil {
//         out[key] = []boardPieItem{}   // ← 静默吞错
//         continue
//     }
//
// 42P01（聚合视图未迁移）与「这个维度真的没有任何客户端」
// 产出**完全相同的载荷**：长度为 0 的数组。前端 `?? []` 之后，
// 排行榜卡照常画出一张空表，顶部分布条停在 0% ——
// 用户读到的是「这段时间没有任何客户端」，而真相是「没算出来」。
//
// 与 T1 那 5 处裸数组降级、credits 降级显示 0、UserProfileList 三态
// 全都是同一个形状：**把「不知道」渲染成「知道」比不显示更糟。**
//
// ## 判据的三态
//
// 本测试刻意**不断言「降级时必须显示某段文字」**，而是断言三件事：
//   ① 降级维度 → 出现降级提示元素，且**不**出现空态元素；
//   ② 正常空数组（真·没有数据）→ 出现空态元素，**不**出现降级提示；
//   ③ 未降级的其他维度不受牵连（部分降级只标被点名的那几个）。
//
// ②③ 是防「恒绿」的关键：如果只测 ①，一个「永远显示降级提示」的
// 实现也能过 —— 那会把「确实没有客户端」误报成降级。
//
// ## 反向对照（已实测）
//
// · 把 v-else-if="card.degraded" 删掉（退回无条件空态）→ ① 红
// · 把 degraded 判定改成恒真（`() => true`）            → ② 红
// · 只断言「错误被捕获」不渲染                          → 保留（下面 ① ② 都断渲染）

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import BoardDistGrid from './BoardDistGrid.vue'
import { isBoardPieDegraded, type BoardPayload } from '../../api/board'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      dashboard: {
        loading: '加载中',
        board: {
          distTitle: '分布分析',
          metricToggle: '指标',
          metricRequests: '请求',
          metricTokens: 'Token',
          distTop: 'Top {n}',
          distOthers: '其他 {n}',
          distDrillHint: '点击下钻',
          empty: '暂无数据',
          pieClients: '客户端类型',
          pieErrors: '错误类型',
          pieIdentity: '身份指纹',
          pieTenants: '租户用量',
          pieClientIp: '来源 IP',
          errorDrill: '错误下钻 {kind}',
          drillModel: '模型',
          drillProvider: '供应商',
          drillClient: '客户端',
          pieDegraded: '不可用 —— {reason}。此处的空排名表示「没算出来」，而不是「没有数据」。',
          pieDegradedGeneric: '该排行查询失败',
        },
        table: { colModel: '模型' },
        providerUsage: { colRequests: '请求' },
        v2: { totalTokensShort: 'Token' },
      },
      common: { button: { close: '关闭' } },
    },
  },
})

function makeBoard(over: Record<string, unknown> = {}): BoardPayload {
  return {
    summary: { total_requests: 10 },
    pies: {
      clients: [],
      client_ips: [],
      identity_hashes: [],
      models: [],
      errors: [],
      tenants: [],
      providers: [],
    },
    trends: [],
    days: 7,
    degraded: false,
    degraded_pies: {},
    degraded_trends: false,
    ...over,
  } as BoardPayload
}

function mountGrid(board: BoardPayload | null) {
  return mount(BoardDistGrid, {
    props: { board, days: 7 },
    global: { plugins: [i18n] },
  })
}

describe('饼图维度降级三态', () => {
  it('① 降级维度显示原因，且同一格内不出现「暂无数据」', () => {
    const w = mountGrid(makeBoard({
      degraded: true,
      degraded_pies: {
        dimensions: ['clients'],
        reason: 'board pie dimension unavailable: missing view request_stats_dim_minute',
      },
    }))

    const deg = w.findAll('.dist-card__degraded')
    expect(deg.length).toBe(1)
    expect(w.text()).toContain('request_stats_dim_minute')

    // 关键断言，且必须**按格**断言，不能断言整页没有空态：
    // 一次只降级一个维度时，其余 4 个格子本来就该显示各自的空态
    // （那些维度确实没有数据）。整页级断言会把正确行为判成缺陷 ——
    // 这是写这条用例时真实踩到的一次：探针实测 degraded=1 / empty=4。
    const degCard = deg[0].element.closest('.dist-card') as HTMLElement
    expect(degCard.querySelector('.dist-card__empty')).toBeNull()

    // 反向确认：确实还有 4 格走的是正常空态路径（证明上面不是恒绿）。
    expect(w.findAll('.dist-card__empty').length).toBe(4)
  })

  it('② 正常空数组（真·没有数据）显示空态，且不显示降级提示', () => {
    const w = mountGrid(makeBoard({ degraded: false, degraded_pies: {} }))

    expect(w.find('.dist-card__empty').exists()).toBe(true)
    expect(w.find('.dist-card__degraded').exists()).toBe(false)
  })

  it('③ 部分降级只标被点名的维度，其余维度照常渲染', () => {
    const item = { key: 'client-a', requests: 5, tokens: 100, credits: 0, cost_usd: 0.1 }
    const w = mountGrid(makeBoard({
      degraded: true,
      degraded_pies: { dimensions: ['errors'], reason: 'query failed' },
      pies: {
        clients: [item],
        client_ips: [],
        identity_hashes: [],
        models: [],
        errors: [],
        tenants: [],
        providers: [],
      },
    }))

    // 只有 errors 一个格是降级态（5 个格子，其余 4 个走正常路径）。
    expect(w.findAll('.dist-card__degraded').length).toBe(1)
    // clients 有数据 → 渲染排行，且不是降级态。
    expect(w.text()).toContain('client-a')
    expect(w.text()).toContain('暂无数据') // 其它空维度仍走空态
  })
})

describe('isBoardPieDegraded 判据本身', () => {
  it('只认服务端点名的维度，不看数组是否为空', () => {
    const degraded = makeBoard({ degraded_pies: { dimensions: ['clients'] } })
    // 空数组但没被点名 ⇒ 不是降级（真·没有数据）
    expect(isBoardPieDegraded(degraded, 'clients')).toBe(true)
    expect(isBoardPieDegraded(degraded, 'models')).toBe(false)

    const healthy = makeBoard()
    expect(isBoardPieDegraded(healthy, 'clients')).toBe(false)
    expect(isBoardPieDegraded(null, 'clients')).toBe(false)
    expect(isBoardPieDegraded(undefined, 'clients')).toBe(false)
  })

  it('degraded_pies 缺失（旧缓存载荷）不会被读成「健康」之外的任何断言', () => {
    // 只断言它不抛：老 payload 没有该字段时必须安全降级而不是崩。
    const legacy = { pies: {}, trends: [], days: 7, summary: {} } as unknown as BoardPayload
    expect(isBoardPieDegraded(legacy, 'clients')).toBe(false)
  })
})
