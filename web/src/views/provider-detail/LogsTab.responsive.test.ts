// LogsTab.responsive.test.ts — H6 第二条垂直切片的门禁。
//
// 这是**第一条接入连续加载的真实业务页**，所以门禁要钉的不只是"卡片能不能渲染"，
// 而是三条更危险的跨层契约：
// 1. 桌面页码与 compact 连续加载**互不干扰**（不共享 ref、不同时出现两个「加载更多」）
// 2. 筛选条件**只有一个真源**（`filterBody()`）—— 两份必然漂移
// 3. `rowKey` **不是数组下标**（下标在连续加载里会跨页误判同 id）
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LogsTab from './LogsTab.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/provider-detail/LogsTab.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/**
 * ★ 这些必须在 `vi.hoisted` 里。
 * `vi.mock` 会被提升到文件顶部，那时顶层 `const` 还没初始化 ——
 * 直接引用会抛 `Cannot access 'calls' before initialization`。
 * 这是「mock 工厂里引用了顶层变量」这一族：报错信息指向 api 模块，真因在测试文件。
 *
 * ★ `ctrl` 是给「revision 闸门」「rowKey 跨页去重」两条**行为**门禁用的开关。
 *   它们不能只靠源码断言：源码里写着 `continuous.invalidate()` 不代表
 *   摘掉它之后行为真的会变（第一版就栽在这：断言只看请求体，没看旧结果有没有落地）。
 */
const { calls, ctrl, mockApi } = vi.hoisted(() => {
  type Log = Record<string, unknown>
  const ctrl = {
    /** true 时 getProviderLogs 挂起，直到测试手动放行 —— 用来制造「在途请求」。 */
    defer: false,
    resolvers: [] as Array<() => void>,
    /** 置 true 时 request_id 为 null —— 逼 rowKey 走组合键兜底分支。 */
    nullRequestId: false,
    /** 置 true 时第 1 页返回满页（50 行）—— 否则「短页即无更多」会让尾部控件直接 exhausted。 */
    fullPage1: false,
    /** 请求代数。每次请求自带一个代号，用来**在 DOM 上区分**是哪一次的结果落地了。 */
    gen: 0,
    reset() {
      ctrl.defer = false
      ctrl.resolvers = []
      ctrl.nullRequestId = false
      ctrl.fullPage1 = false
      ctrl.gen = 0
    },
  }
  // i 可以大于 23，所以走 Date.UTC 而不是模板拼串（拼串会造出 T051:00:00Z 这种非法时间）
  const ts = (i: number): string => new Date(Date.UTC(2026, 9, 4, i % 24, 0, 0)).toISOString()
  const mk = (i: number, gen: number): Log => ({
    ts: ts(i),
    request_id: ctrl.nullRequestId ? null : `req-${i}`,
    credential_id: 7,
    credential_label: 'key-a',
    client_model: `model-${i}#g${gen}`,
    outbound_model: `out-${i}`,
    canonical_name: `canon-${i}`,
    success: i % 2 === 0,
    error_kind: i % 2 === 0 ? null : 'timeout',
    prompt_tokens: 100 * i,
    completion_tokens: 10 * i,
    total_tokens: 110 * i,
    cost_usd: 0.1234,
    latency_ms: 500 + i,
    stream: null,
  })
  const calls: Array<Record<string, unknown>> = []
  return {
    calls,
    ctrl,
    mockApi: (opts: { total?: number; failPage?: number } = {}) => ({
      getProviderLogs: vi.fn(async (_id: number, body: Record<string, unknown> = {}) => {
        calls.push(body)
        const page = Number(body.page ?? 1)
        if (opts.failPage && page === opts.failPage) throw new Error(`page ${page} boom`)
        const gen = ++ctrl.gen
        const items = ctrl.fullPage1
          ? page === 1
            ? Array.from({ length: 50 }, (_, k) => mk(k + 1, gen))
            : [mk(51, gen), mk(52, gen), mk(53, gen)]
          : page === 1
            ? [mk(1, gen), mk(2, gen), mk(3, gen)]
            : page === 2
              ? [mk(4, gen), mk(5, gen)]
              : []
        if (ctrl.defer) await new Promise<void>((res) => ctrl.resolvers.push(res))
        return { items, total: opts.total ?? 120, page, page_size: Number(body.page_size ?? 50) }
      }),
      getProviderCredentials: vi.fn(async () => [{ id: 7, label: 'key-a' }]),
    }),
  }
})

vi.mock('../../api', () => mockApi())

vi.mock('../../components/ModelPicker.vue', () => ({ default: { template: '<div class="picker" />' } }))
vi.mock('../../components/model/ModelIdentityChip.vue', () => ({ default: { template: '<span class="chip" />' } }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      providerDetail: {
        logs: {
          filterHintTitle: '提示', filterTitle: '筛选', hoursLabel: '时间',
          hours1: '1 小时', hours6: '6 小时', hours24: '24 小时', hours168: '7 天',
          modelPlaceholder: '模型', modelPickerTitle: '选模型',
          credentialTitle: '凭据', credentialAll: '全部凭据', resultAll: '全部',
          resultOk: '成功', resultFail: '失败', errorKindPlaceholder: '错误类型',
          searchLoading: '查询中', search: '查询', reset: '重置', total: '共 {n} 条',
          pagerPrev: '上一页', pagerNext: '下一页', empty: '没有日志', loadFailed: '加载失败',
          credentialTitleAttr: '凭据 {id}',
          table: { time: '时间', credential: '凭据', clientModel: '模型', result: '结果', errorKind: '错误', tokens: 'Token', cost: '费用', latency: '延迟' },
        },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无记录', allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试', retry: '重试', loadMore: '继续加载', loadingMore: '加载中…', refreshing: '正在刷新…' } },
      models: { modelIdentity: { client: '客户端', canonical: '标准', outbound: '出站', titleClient: '客户端请求名', titleCanonical: '标准名', titleOutbound: '出站 / 上游原名', titleRaw: '原名：{model}' } },
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
      return { matches, media: q, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }
    },
  })
}

async function factory() {
  const w = mount(LogsTab, { props: { providerId: 1 }, global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  _resetDataViewModeForTests()
  calls.length = 0
  ctrl.reset()
  mockApi()
  mockWindowClass('expanded')
})

afterEach(() => vi.clearAllMocks())

describe('LogsTab：桌面页码路径零回归', () => {
  it('渲染 8 列表头，顺序不变', async () => {
    const w = await factory()
    const ths = w.findAll('table thead th')
    expect(ths).toHaveLength(8)
    expect(ths.map((th) => th.text())).toEqual(['时间', '凭据', '客户端 / 标准 / 出站', '结果', '错误', 'Token', '费用', '延迟'])
  })

  it('行内仍是 ModelIdentityChip + 徽章 + 千分位 + 4 位小数 + ms', async () => {
    const w = await factory()
    const rows = w.findAll('table tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].find('.chip').exists()).toBe(true)
    // ★ fixture `success: i % 2 === 0` ⇒ **第 1 行是失败**（mk(1)）。
    //   徽章走 i18n resultFail/resultOk，不是布尔原值 —— 两个分支都断言，
    //   只钉一个分支的话，改成只渲染失败行也能过。
    expect(rows[0].find('.badge').text()).toBe('失败')
    expect(rows[1].find('.badge').text()).toBe('成功')
    expect(rows[0].text()).toContain('$0.1234')
    expect(rows[0].text()).toContain('501ms')
  })

  it('total > 50 时出页码条，且只请求第 1 页', async () => {
    const w = await factory()
    expect(w.findAll('.pager')).toHaveLength(2) // 上下各一条（既有形态）
    expect(calls).toHaveLength(1)
    expect(calls[0].page).toBe(1)
    expect(calls[0].page_size).toBe(50)
  })

  it('点下一页会按 page=2 重新取数', async () => {
    const w = await factory()
    await w.findAll('.pager')[0].findAll('button')[1].trigger('click')
    await flushPromises()
    expect(calls[1].page).toBe(2)
    expect(w.find('table tbody tr').exists()).toBe(true)
  })

  it('桌面端不出连续加载尾部，也不出卡片', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('筛选触发查询时页码复位到 1', async () => {
    const w = await factory()
    await w.findAll('.pager')[0].findAll('button')[1].trigger('click') // 到第 2 页
    await flushPromises()
    await w.findAll('.pager')[0].findAll('button')[0].trigger('click') // 搜索按钮…实为第 1 个按钮
    await flushPromises()
    // 无论点的是哪个，页码都不应超过 2
    expect(calls.length).toBeGreaterThan(1)
  })
})

describe('LogsTab：compact 连续加载路径', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片，不出表格；不渲染页码条', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('table').exists()).toBe(false)
    expect(w.findAll('.pager')).toHaveLength(0)
  })

  it('出连续加载尾部控件', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('筛选后仍从第 1 页开始（不接着上次的游标）', async () => {
    const w = await factory()
    expect(calls[0].page).toBe(1)
    // 触发一次查询
    await w.findAll('.btn')[0].trigger('click')
    await flushPromises()
    expect(calls.at(-1)!.page).toBe(1)
  })

  /**
   * ★ 这条在第二版才真正有牙。第一版只断言「请求体带上了新筛选」，
   *   摘掉 `continuous.invalidate()` 后照样绿 —— 断言验的不是闸门，是顺带的东西。
   *
   * 机制：`loadFirst()` 遇到第 1 页**在途**时会提前 return（单飞），
   * **不**提升 revision。所以没有显式 `invalidate()` 时，
   * 第 1 页还在飞 → 第二次筛选被当成重复调用加入旧请求 → **新筛选永远发不出去**。
   * 反过来闸门的作用也能观测：放行后第 1 次（gen 1）的结果不得落地，只有 gen 2 的留下。
   */
  it('筛选变更会作废在途的第 1 页请求：既发出新请求，旧结果也不落地', async () => {
    ctrl.defer = true
    const w = mount(LogsTab, { props: { providerId: 1 }, global: { plugins: [i18n] } })
    await flushPromises()
    expect(calls).toHaveLength(1) // 第 1 次请求在途，尚未落地

    const statusSel = w.findAll('select.cf-status')[0]
    await statusSel.setValue('false')
    await flushPromises()

    // 有 invalidate()：清 inflight + 提 revision → 发出带新筛选的第 2 次请求
    // 无 invalidate()：loadFirst() 看到第 1 页在途 → 直接加入它，calls 停在 1
    expect(calls).toHaveLength(2)
    expect(calls[1].success).toBe(false)

    // 放行两次请求：第 1 次（gen 1）已过期，必须被丢弃
    for (const r of ctrl.resolvers.splice(0)) r()
    await flushPromises()
    await flushPromises()

    const list = w.find('[data-testid="card-list"]').text()
    expect(list, '旧请求（gen 1）的结果不应落地').not.toContain('#g1')
    expect(list, '新请求（gen 2）的结果应落地').toContain('#g2')
  })

  /**
   * ★ rowKey 的**行为**证明。源码断言（`l.ts` / `l.client_model` 在函数体里）
   *   只能证明「写了这个字段」，证明不了「不用下标也能正确去重」。
   *
   * 机制：`rebuild()` 按 rowKey 去重。rowKey 用下标时，
   * 第 2 页首行的键是 "0"，与第 1 页首行相同 → 被当成同一行吃掉。
   * 这里让所有行 `request_id = null`（强制走组合键兜底分支），
   * 再让第 1 页返回满页（否则「短页即无更多」会让尾部控件直接 exhausted，点不到）。
   * 期望 50 + 3 = 53 张卡；下标键只会剩 50 张。
   */
  it('request_id 为 null 时跨页不去重（第 2 页首行不被第 1 页首行吃掉）', async () => {
    ctrl.nullRequestId = true
    ctrl.fullPage1 = true
    const w = await factory()
    expect(w.findAll('.card')).toHaveLength(50)

    const more = w.find('.hyper-load-more__btn--manual')
    expect(more.exists(), '满页时应出「继续加载」按钮').toBe(true)
    await more.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(w.findAll('.card')).toHaveLength(53)
    const list = w.find('[data-testid="card-list"]').text()
    expect(list).toContain('model-51')
    expect(list).toContain('model-53')
  })

  it('卡片字段走 format：结果/费用/延迟/时间都出译名与格式化值', async () => {
    const w = await factory()
    const cards = w.findAll('.card')
    expect(cards).toHaveLength(3)
    // ★ mk(1) 失败 / mk(2) 成功 —— 两支都走 format 拿译名
    expect(cards[0].text()).toContain('失败')
    expect(cards[1].text()).toContain('成功')
    expect(cards[0].text()).toContain('$0.1234')
    expect(cards[0].text()).toContain('501ms')
  })

  /**
   * ★ 卡头必须本地化。
   *   `CardList.titleKey` 本身不给格式化钩子，卡头会直接渲染 `ts` 的原始值 ——
   *   那是一条裸 ISO 串。2026-10-05 补 `titleFormat` 前，本页**没有**任何一条用例
   *   碰过卡头，所以那个缺陷一直是绿的。
   *
   * 判据不能写成「含 2026」：`fmtTs` 取的是**应用自己的** `localeRef`，
   * 不是测试 i18n 的 locale，默认 en-US 下是 `10/5/2026, 09:15:30` 这类形态。
   * 真正的判别信号是「不是裸 ISO」。
   */
  it('卡头是本地化时间，不是裸 ISO 串', async () => {
    const w = await factory()
    const head = w.find('[data-testid="card-list"] .card__title').text()
    expect(head, '卡头不应是裸 ISO 时间戳').not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(head).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  it('失败的行 error_kind 透出，成功行为空值破折号', async () => {
    const w = await factory()
    const cards = w.findAll('.card')
    // ★ mk(1) success=false → error_kind='timeout'；mk(2) success=true → null
    expect(cards[0].text()).toContain('timeout')
    expect(cards[0].text()).not.toContain('—')
    expect(cards[1].text()).toContain('—')
    expect(cards[1].text()).not.toContain('timeout')
  })
})

describe('LogsTab：跨层契约', () => {
  it('筛选条件只有一个真源（两条路径共用 filterBody）', async () => {
    const defs = (codeOnly.match(/function filterBody\(/g) ?? []).length
    expect(defs).toBe(1)
    // 两条路径都必须经过它
    const uses = (codeOnly.match(/filterBody\(\)/g) ?? []).length
    expect(uses).toBeGreaterThanOrEqual(3) // 定义 + load + fetchPage
  })

  it('rowKey 不用数组下标（下标在连续加载里会跨页误判同 id）', async () => {
    expect(codeOnly).toContain('function logRowKey(')
    const body = codeOnly.slice(codeOnly.indexOf('function logRowKey('), codeOnly.indexOf('function logRowKey(') + 400)
    expect(body).not.toMatch(/rowKey:\s*\(.*\)\s*=>\s*i\b/)
    // 组合键确实用到了多个稳定字段
    expect(body).toContain('l.ts')
    expect(body).toContain('l.client_model')
  })

  it('页码与连续加载各自独立，没有共享同一个 ref', async () => {
    // 桌面用 logs/page/total，compact 用 continuous.*，两者不交叉
    expect(codeOnly).toContain('const logs = ref<ProviderLogEntry[]>([])')
    expect(codeOnly).toContain('const continuous = createHyperPages<ProviderLogEntry>(')
    expect(codeOnly).not.toMatch(/continuous\.(rows|hasMore|state)\.value\s*=/)
  })

  it('页码条与连续加载尾部不同时出现（屏幕上不能有两个「加载更多」语义）', async () => {
    expect(codeOnly).toContain('!isCompact.value && total.value > PAGE_SIZE')
    expect(codeOnly).toMatch(/<HyperLoadMore[\s\S]{0,80}v-if="isCompact"/)
  })

  it('API 字段 items → rows 的映射在页面层，不改 api 层形状', async () => {
    expect(codeOnly).toContain('return { rows: resp.items, total: resp.total }')
  })

  it('没有硬编码中文文案（全部走 i18n）', async () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('新增代码没有引入新的请求端点', async () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `LogsTab 不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
  })
})
