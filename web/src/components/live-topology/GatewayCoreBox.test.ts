// GatewayCoreBox.test.ts — 网关核心「大方块」判据（2026-10-18 三带版；
// 2026-10-28 选路层改版：主线 缓存→脱敏→压缩→模型选择 + 选路扇出带 +
// 基础服务侧条）
//
// owner 口径（2026-10-28）：内部直连模型 / auto 按任务选模要表达；压缩层
// 要进主线；路由与节点状态属基础服务放一侧。要钉住的是：
//  1. 主线四格（缓存→脱敏→压缩→模型选择）落在进站链、顺序 = 处理顺序；
//  2. 选路扇出带三张出口卡（auto/内部直连/指定）互斥分桶且直连未测量要标注；
//  3. 基础服务侧条（路由管理/节点状态管理）在右侧、不在主链；
//  4. 插件 chip 按 direction 落带；存储带队列徽标真实深度（未上报不冒充 0）；
//  5. 旁路模块虚线语义保留；点格子 emit 模块 key。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import GatewayCoreBox from './GatewayCoreBox.vue'
import zhCN from '../../locales/zh-CN/liveTopology'
import { GATEWAY_BRANCH_MODULES, GATEWAY_MODULES, type GatewayModuleKey, type ModelSelectionStats, type TopoPluginChip } from '../../types/liveTopology'
import { computeGatewayBox } from '../../lib/liveTopologyLayout'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

const BOX = computeGatewayBox(1200, 700)

function statsOf(values: Partial<Record<GatewayModuleKey, { count: number; errors: number }>> = {}) {
  const out = {} as Record<GatewayModuleKey, { count: number; errors: number }>
  for (const m of GATEWAY_MODULES) out[m.key] = values[m.key] ?? { count: 0, errors: 0 }
  return out
}

function mountBox(
  stats = statsOf(),
  selected: GatewayModuleKey | null = null,
  extra: { plugins?: TopoPluginChip[]; queueDepth?: number | null; selection?: ModelSelectionStats | null; directMeasured?: boolean } = {},
) {
  return mount(GatewayCoreBox, {
    props: {
      box: BOX,
      stats,
      selected,
      plugins: extra.plugins,
      queueDepth: extra.queueDepth,
      selection: extra.selection ?? { auto: 0, autoTasks: [], direct: 0, explicit: 0, total: 0 },
      directMeasured: extra.directMeasured,
    },
    global: { plugins: [i18n] },
  })
}

describe('GatewayCoreBox 四带布局（2026-10-28 选路层改版）', () => {
  it('renders eleven module cells: 4 in-lane + 2 infra + 2 store + 3 out-lane', () => {
    const wrapper = mountBox()
    expect(wrapper.findAll('.gw__cell')).toHaveLength(11)
    expect(wrapper.find('.gw').attributes('aria-label')).toBe('网关核心')
    // 每个模块名都要出现（一个都不能靠色块暗示）
    const text = wrapper.text()
    for (const name of ['缓存', '脱敏', '压缩', '模型选择', '路由管理', '节点状态管理', '会话数据文件存储', '数据会话写入', '错误记录', '探测任务', '任务识别']) {
      expect(text).toContain(name)
    }
  })

  it('★ places every module in its flow lane (in / infra / store / out)', () => {
    // 模块落在哪条带 = 它在真实处理流程中的位置（owner 2026-10-18）。
    // 断言 DOM 归属（不是常量自查）：组件把模块排进错误带会立刻红。
    const wrapper = mountBox()
    const laneOf = (key: GatewayModuleKey) => {
      const laneIn = wrapper.find('[data-testid="gw-lane-in"]').text()
      const laneStore = wrapper.find('[data-testid="gw-lane-store"]').text()
      const laneOut = wrapper.find('[data-testid="gw-lane-out"]').text()
      const infra = wrapper.find('[data-testid="gw-infra"]').text()
      const label = zhCN.gateway[key]
      if (infra.includes(label)) return 'infra'
      if (laneOut.includes(label)) return 'out'
      if (laneStore.includes(label)) return 'store'
      if (laneIn.includes(label)) return 'in'
      return 'missing'
    }
    expect(laneOf('cache')).toBe('in')
    expect(laneOf('sanitize')).toBe('in')
    expect(laneOf('compress')).toBe('in')
    expect(laneOf('modelSelect')).toBe('in')
    // owner 2026-10-28：路由与节点状态是基础服务，放一侧（不在主链）
    expect(laneOf('route')).toBe('infra')
    expect(laneOf('nodeState')).toBe('infra')
    expect(laneOf('sessionWrite')).toBe('store')
    expect(laneOf('sessionFile')).toBe('store')
    expect(laneOf('taskDetect')).toBe('out')
    expect(laneOf('errorRecord')).toBe('out')
    expect(laneOf('probeTask')).toBe('out')
  })

  it('★ in-lane DOM order matches the REAL pipeline (cache → sanitize → modelSelect → compress)', () => {
    // ★ 批判式审计修正（2026-10-28）：会话压缩在**候选解析之后**才跑
    // （SessionCompressor.Prepare 需要目标模型的上下文窗口）—— 压缩必须排
    // 在模型选择之后。早先版本压缩在前，违反「模块按真实位置排布」原则。
    const wrapper = mountBox()
    const cells = wrapper.find('[data-testid="gw-lane-in"]').findAll('.gw__cell')
    expect(cells.map((c) => c.attributes('data-testid'))).toEqual([
      'gw-cell-cache',
      'gw-cell-sanitize',
      'gw-cell-modelSelect',
      'gw-cell-compress',
    ])
    // 主链出口（⬇ 派发）在进站链末尾，不在扇出带（扇出带是模型选择的
    // 出口分解注解，不是主链一步）
    expect(wrapper.find('[data-testid="gw-lane-in"]').text()).toContain('派发供应端')
    expect(wrapper.find('[data-testid="gw-lane-select"]').text()).not.toContain('派发供应端')
  })

  it('labels the main pipeline (进/出), the selection lane, and the infra rail', () => {
    const wrapper = mountBox()
    expect(wrapper.find('[data-testid="gw-flow-in"]').text()).toContain('进')
    expect(wrapper.find('[data-testid="gw-flow-out"]').text()).toContain('出')
    expect(wrapper.find('[data-testid="gw-lane-store"]').text()).toContain('存储转发')
    expect(wrapper.find('[data-testid="gw-lane-select"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="gw-infra"]').text()).toContain('基础服务')
  })

  it('dashes the bypass modules and keeps the main pipeline solid', () => {
    const wrapper = mountBox()
    const branchCells = wrapper.findAll('.gw__cell.is-branch')
    expect(branchCells).toHaveLength(3)
    // 旁路三兄弟全部在出站链上（观测/记录侧），不在请求主链上
    expect(branchCells.map((c) => c.attributes('aria-label')?.split(' ')[0]).sort()).toEqual(
      ['探测任务', '错误记录', '任务识别'].sort(),
    )
    expect(GATEWAY_BRANCH_MODULES.size).toBe(3)
    // 非旁路实线格 = 主线 4 + 存储 2 + 侧条 2 = 8（侧条用虚线样式但不带
    // is-branch —— 它是基础服务，不是旁路观测；11 总格 − 3 旁路 = 8）
    expect(wrapper.findAll('.gw__cell:not(.is-branch)')).toHaveLength(8)
  })
})

describe('GatewayCoreBox 选路扇出带（2026-10-28）', () => {
  it('★ shows the three parallel exits with counts (auto / direct internal / named)', () => {
    const selection: ModelSelectionStats = {
      auto: 5,
      autoTasks: [{ type: 'code', count: 3 }, { type: 'chat', count: 2 }],
      direct: 2,
      explicit: 9,
      total: 16,
    }
    const wrapper = mountBox(statsOf(), null, { selection })
    expect(wrapper.find('[data-testid="gw-mode-auto"]').text()).toContain('5')
    expect(wrapper.find('[data-testid="gw-mode-auto"]').text()).toContain('code ×3')
    expect(wrapper.find('[data-testid="gw-mode-direct"]').text()).toContain('2')
    expect(wrapper.find('[data-testid="gw-mode-explicit"]').text()).toContain('9')
    // 守恒式必须能从三卡读出来：5+2+9=16
    expect(selection.auto + selection.direct + selection.explicit).toBe(selection.total)
  })

  it('paints zero-count exits as idle (no volume = 0, not hidden)', () => {
    const wrapper = mountBox()
    expect(wrapper.find('[data-testid="gw-mode-auto"]').text()).toContain('0')
    expect(wrapper.find('[data-testid="gw-mode-direct"]').text()).toContain('0')
    expect(wrapper.find('[data-testid="gw-mode-explicit"]').text()).toContain('0')
  })

  it('★ annotates unmeasured direct bucket when the provider list failed to load', () => {
    // 供应商清单未加载 = 「未测量」，不是「没有直连」。0 + 未测量标注同时在场。
    const wrapper = mountBox(statsOf(), null, { selection: { auto: 1, autoTasks: [], direct: 0, explicit: 4, total: 5 }, directMeasured: false })
    expect(wrapper.find('[data-testid="gw-mode-unmeasured"]').exists()).toBe(true)
  })

  it('shows no unmeasured note when direct bucket measurement succeeded', () => {
    const wrapper = mountBox(statsOf(), null, { directMeasured: true })
    expect(wrapper.find('[data-testid="gw-mode-unmeasured"]').exists()).toBe(false)
  })
})

describe('GatewayCoreBox 插件带', () => {
  const plugins: TopoPluginChip[] = [
    { id: 'builtin:prompt_injection', name: 'prompt_injection', direction: 'input', status: 'builtin', external: false },
    { id: 'builtin:sensitive_word_output', name: 'sensitive_word_output', direction: 'output', status: 'builtin', external: false },
    { id: 'ext:ai-session-manager', name: 'ai-session-manager', direction: 'input', status: 'ready', external: true },
    { id: 'ext:broken-one', name: 'broken-one', direction: 'input', status: 'failed', external: true },
  ]

  it('★ routes plugin chips by direction: input/both on the in-lane, output on the out-lane', () => {
    const wrapper = mountBox(statsOf(), null, { plugins })
    const inChips = wrapper.find('[data-testid="gw-plugins-in"]').findAll('.gw__plugin')
    const outChips = wrapper.find('[data-testid="gw-plugins-out"]').findAll('.gw__plugin')
    // input ×2（prompt_injection + ready 外部）+ failed 外部也必须在场：
    // failed 恰恰是最需要被看到的插件状态，藏掉它 = 假绿。
    expect(inChips.map((c) => c.text()).sort()).toEqual(['ai-session-manager', 'broken-one', 'prompt_injection'].sort())
    expect(outChips.map((c) => c.text())).toEqual(['sensitive_word_output'])
  })

  it('★ colors external plugin chips by process status', () => {
    const wrapper = mountBox(statsOf(), null, { plugins })
    const failed = wrapper.find('[data-testid="gw-plugins-in"]').findAll('.gw__plugin').find((c) => c.text() === 'broken-one')
    const ready = wrapper.find('[data-testid="gw-plugins-in"]').findAll('.gw__plugin').find((c) => c.text() === 'ai-session-manager')
    expect(failed?.classes()).toContain('is-failed')
    expect(ready?.classes()).toContain('is-ready')
  })

  it('renders no plugin strip when the source is empty (empty = really none, not hidden)', () => {
    const wrapper = mountBox(statsOf(), null, { plugins: [] })
    expect(wrapper.find('[data-testid="gw-plugins-in"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="gw-plugins-out"]').exists()).toBe(false)
  })
})

describe('GatewayCoreBox 存储转发带', () => {
  it('★ shows the real forward-queue depth from the queue snapshot', () => {
    const wrapper = mountBox(statsOf(), null, { queueDepth: 3 })
    expect(wrapper.find('[data-testid="gw-queue-depth"]').text()).toContain('3')
    expect(wrapper.find('[data-testid="gw-queue-depth"]').classes()).toContain('is-busy')
  })

  it('shows depth 0 as idle (zero is a real value for an empty queue, not missing)', () => {
    const wrapper = mountBox(statsOf(), null, { queueDepth: 0 })
    expect(wrapper.find('[data-testid="gw-queue-depth"]').text()).toContain('0')
    expect(wrapper.find('[data-testid="gw-queue-depth"]').classes()).not.toContain('is-busy')
  })

  it('★ hides the queue badge when the pipeline did not report (null ≠ 0)', () => {
    const wrapper = mountBox(statsOf(), null, { queueDepth: null })
    expect(wrapper.find('[data-testid="gw-queue-depth"]').exists()).toBe(false)
  })

  it('renders store cells with the store visual (⇄ between write and file storage)', () => {
    const wrapper = mountBox()
    const storeLane = wrapper.find('[data-testid="gw-lane-store"]')
    expect(storeLane.find('[data-testid="gw-cell-sessionWrite"]').exists()).toBe(true)
    expect(storeLane.find('[data-testid="gw-cell-sessionFile"]').exists()).toBe(true)
    expect(storeLane.find('.gw__store-arrow').text()).toContain('⇄')
  })
})

describe('GatewayCoreBox 命中统计', () => {
  it('shows zero as idle rather than painting it as an active or failed stage', () => {
    const wrapper = mountBox(statsOf({ route: { count: 0, errors: 0 } }))
    const routeCell = wrapper.find('[data-testid="gw-cell-route"]')
    expect(routeCell?.classes()).toContain('is-idle')
    expect(routeCell?.classes()).not.toContain('is-error')
    expect(routeCell?.text()).toContain('0')
  })

  it('turns a cell red only when it has both volume and errors', () => {
    const wrapper = mountBox(statsOf({ errorRecord: { count: 3, errors: 2 }, cache: { count: 5, errors: 0 } }))
    const errorCell = wrapper.find('[data-testid="gw-cell-errorRecord"]')
    const cacheCell = wrapper.find('[data-testid="gw-cell-cache"]')
    expect(errorCell?.classes()).toContain('is-error')
    expect(cacheCell?.classes()).toContain('is-active')
    expect(cacheCell?.classes()).not.toContain('is-error')
  })

  it('marks the focused module without recoloring it', () => {
    const wrapper = mountBox(statsOf(), 'route')
    const routeCell = wrapper.find('[data-testid="gw-cell-route"]')
    expect(routeCell?.classes()).toContain('is-selected')
    expect(routeCell?.classes()).not.toContain('is-error')
  })

  it('emits the module key when a cell is clicked', async () => {
    const wrapper = mountBox()
    await wrapper.find('[data-testid="gw-cell-cache"]').trigger('click')
    expect(wrapper.emitted('select-module')?.[0]).toEqual(['cache'])
  })
})
