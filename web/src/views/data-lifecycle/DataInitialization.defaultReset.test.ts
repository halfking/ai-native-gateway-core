// DataInitialization.defaultReset.test.ts —— 「数据初始化」面板：默认要重置数据
//
// ## 这道判据钉的是什么
//
// 首次激活的新环境（刚装好、还带着旧数据或压测痕迹）在打开「数据初始化」面板时，
// **默认应当处在「准备重置」的姿态**：全部类别默认选中 ⇒ 点「预览重置」即可看到
// 将被删除的行数，不必先手动勾一遍类别。
//
// 实现落在 `applyDefaultSelection()`：只在**首次**应用一次
// （`defaultSelectionApplied` 标志）⇒
//   · 首次加载 ⇒ 全选（默认姿态）；
//   · 之后的「刷新」**不覆盖**用户手动改过的选择（刷新 ≠ 重置用户意图）；
//   · 执行成功后标志复位 ⇒ 面板回到默认姿态，可接着做下一轮归零。
//
// ## 为什么单独钉「刷新不覆盖」
//
// 若把默认逻辑写成「每次 load 都全选」，那么用户点完「全不选」随手刷新一下，
// 选择就被悄悄改回全选 ⇒ 紧接着点「预览重置」看到的是**用户没选过的范围**
// ⇒ 破坏性操作的默认范围会漂移。这条必须钉死。
//
// ## 门自身的「有牙」证明
//
// 文末 `MutationM1`：把 `applyDefaultSelection()` 的默认全选去掉（改回空选中）
// ⇒ 判据必须判红。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const catalogMock = vi.fn()
const previewMock = vi.fn()
const executeMock = vi.fn()

// ⚠️ vi.mock 的说明符相对**本判据**解析（不是相对被测视图）：
// 本判据在 src/views/data-lifecycle/ ⇒ '../../api/dataInit' 指向 src/api/dataInit，
// 与被测视图里的 '../../api/dataInit' 指向**同一个模块** ⇒ 桩真的挂上了。
//   （写成 '../api/dataInit' 会指向**不存在的** src/views/api/dataInit ⇒
//     桩挂了个空模块、真模块照常发请求 ⇒ 分类表空 ⇒ 0 个复选框。）
vi.mock('../../api/dataInit', () => ({
  dataInitCatalog: (...a: unknown[]) => catalogMock(...a),
  dataInitPreview: (...a: unknown[]) => previewMock(...a),
  dataInitExecute: (...a: unknown[]) => executeMock(...a),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  // 空 messages ⇒ t() 回落到键路径本身（断言打在键路径上，不依赖译文措辞）
  messages: { 'zh-CN': {} },
})

const CATEGORIES = [
  {
    key: 'request_records',
    label_key: 'dataLifecycle.dataInit.categories.requestRecords',
    level: 'operational' as const,
    tables: [{ name: 'request_records', exists: true, rows: 10, bytes: 100, bytes_human: '100 B', protected_rows: 0, target_rows: 10 }],
    rows: 10,
    bytes: 100,
    bytes_human: '100 B',
    protected_rows: 0,
    target_rows: 10,
  },
  {
    key: 'probe_records',
    label_key: 'dataLifecycle.dataInit.categories.probeRecords',
    level: 'operational' as const,
    tables: [{ name: 'probe_records', exists: true, rows: 5, bytes: 50, bytes_human: '50 B', protected_rows: 0, target_rows: 5 }],
    rows: 5,
    bytes: 50,
    bytes_human: '50 B',
    protected_rows: 0,
    target_rows: 5,
  },
  {
    key: 'tenants',
    label_key: 'dataLifecycle.dataInit.categories.tenants',
    level: 'configuration' as const,
    tables: [{ name: 'tenants', exists: true, rows: 3, bytes: 30, bytes_human: '30 B', protected_rows: 1, target_rows: 2 }],
    rows: 3,
    bytes: 30,
    bytes_human: '30 B',
    protected_rows: 1,
    target_rows: 2,
  },
]

async function renderPanel() {
  const View = (await import('./DataInitialization.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

type PanelVm = {
  load: () => Promise<void>
  selectAll: () => void
  selectNone: () => void
  selected: string[]
}

function vmOf(w: Awaited<ReturnType<typeof renderPanel>>) {
  return w.vm as unknown as PanelVm
}

function checkboxes(w: Awaited<ReturnType<typeof renderPanel>>) {
  return w.findAll('input[type="checkbox"]')
}

function buttonByKey(w: Awaited<ReturnType<typeof renderPanel>>, key: string) {
  const btn = w.findAll('button').find(b => b.text().includes(key))
  if (!btn) throw new Error(`找不到按钮：${key}`)
  return btn
}

describe('数据初始化面板：默认要重置数据', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    catalogMock.mockResolvedValue({
      generated_at: '2026-11-16T10:00:00Z',
      categories: CATEGORIES,
    })
    previewMock.mockResolvedValue({
      categories: [],
      total_rows: 0,
      total_protected_rows: 0,
      total_target_rows: 0,
      confirm_token: 'tok',
      token_expires_at: '2026-11-16T10:05:00Z',
      warnings: [],
    })
    executeMock.mockResolvedValue({
      actor: 'admin',
      total_rows_deleted: 0,
      results: [],
      executed_at: '2026-11-16T10:00:00Z',
    })
  })

  it('① 首次加载 ⇒ 全部类别默认选中（默认重置姿态），且「预览重置」立即可用', async () => {
    const w = await renderPanel()
    const boxes = checkboxes(w)
    expect(boxes.length).toBe(CATEGORIES.length)
    // 默认全选
    for (const box of boxes) {
      expect((box.element as HTMLInputElement).checked).toBe(true)
    }
    // 预览按钮可用（不再要求用户先手动勾一遍）
    const previewBtn = buttonByKey(w, 'dataLifecycle.dataInit.preview')
    expect((previewBtn.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('② 首次加载 ⇒ 渲染出「默认重置」提示（defaultHint）', async () => {
    const w = await renderPanel()
    expect(w.text()).toContain('dataLifecycle.dataInit.defaultHint')
  })

  it('③ 「全不选」⇒ 清空选择且预览按钮禁用；「全选」⇒ 恢复默认姿态', async () => {
    const w = await renderPanel()
    await buttonByKey(w, 'dataLifecycle.dataInit.selectNone').trigger('click')
    await flushPromises()
    for (const box of checkboxes(w)) {
      expect((box.element as HTMLInputElement).checked).toBe(false)
    }
    const previewBtn = buttonByKey(w, 'dataLifecycle.dataInit.preview')
    expect((previewBtn.element as HTMLButtonElement).disabled).toBe(true)

    await buttonByKey(w, 'dataLifecycle.dataInit.selectAll').trigger('click')
    await flushPromises()
    for (const box of checkboxes(w)) {
      expect((box.element as HTMLInputElement).checked).toBe(true)
    }
    expect((previewBtn.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('④ 刷新**不覆盖**用户手动改过的选择（刷新 ≠ 重置用户意图）', async () => {
    const w = await renderPanel()
    await buttonByKey(w, 'dataLifecycle.dataInit.selectNone').trigger('click')
    await flushPromises()
    expect(vmOf(w).selected).toEqual([])

    // 父视图的「刷新全部」会调 load()；这里直接驱动它。
    await vmOf(w).load()
    await flushPromises()

    // ★ 仍然是「全不选」：若默认逻辑写成「每次 load 都全选」，这条会红。
    expect(vmOf(w).selected).toEqual([])
    for (const box of checkboxes(w)) {
      expect((box.element as HTMLInputElement).checked).toBe(false)
    }
  })

  it('⑤ 执行成功 ⇒ 面板回到默认姿态（可接着做下一轮归零）', async () => {
    const w = await renderPanel()
    // 先预览（拿到令牌），再执行。
    await buttonByKey(w, 'dataLifecycle.dataInit.preview').trigger('click')
    await flushPromises()
    const input = w.find('#data-init-confirm')
    await input.setValue('RESET')
    await buttonByKey(w, 'dataLifecycle.dataInit.execute').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(executeMock).toHaveBeenCalledTimes(1)
    // 执行后重新统计 ⇒ 选择回到全选（默认姿态）
    expect(vmOf(w).selected.sort()).toEqual(['probe_records', 'request_records', 'tenants'])
  })
})

// ── 门自身的「有牙」证明 ────────────────────────────────────────────────────
// ★ 变异不在本文件里做（那只能证明「全不选」有效，证明不了「默认全选」这条事实
//   有牙）⇒ 变异在**源码层**做：把 `applyDefaultSelection()` 里的默认全选摘掉，
//   重跑本判据必须判红，随后还原。做法与证据见该轮的验证记录。
