// CredentialHeatmapView.test.ts — 写侧角色门（2026-10-20）
//
// 后端 2026-10-19 补上了凭据写侧的角色门（admin/credential_monitor.go 的
// RequireSuperAdminForWrite）：**tenant_admin 是只读档**，写操作一律 403。
// 本视图「状态修正」的 6 个动作打到的后端端点全部要求 super_admin：
//   · 模型上下线 → POST /api/credentials/model-toggle
//   · 凭据恢复/降级 → POST /api/credentials/promote | demote
//   · 凭据启用/停用 → POST /api/credentials/set-manual-disabled
// 前端没同步 ⇒ tenant_admin 会看到完全可点的写按钮，点下去只拿到 403。
//
// 判据纪律（AGENTS.md）：
//   ① 断言落在**组件真正渲染出来的那层**（按钮的 disabled 态 + 写 API 有没有被调用），
//      不是源码文本匹配；
//   ② **允许侧也要有正向断言**（super_admin 看得见且点得动），
//      否则「因为压根没渲染」也能让阻断侧全绿 ⇒ 假绿；
//   ③ 阻断侧断言「写 API 一个都没被调用」——那才是真正的不变量，
//      按钮 disabled 只是表象。
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CredentialHeatmapView from './CredentialHeatmapView.vue'

const { superAdmin, getCredentialHeatmap, promote, demote, toggleModel, setManualDisabled } = vi.hoisted(() => ({
  superAdmin: vi.fn(() => false),
  getCredentialHeatmap: vi.fn(),
  promote: vi.fn(async () => ({})),
  demote: vi.fn(async () => ({})),
  toggleModel: vi.fn(async () => ({})),
  setManualDisabled: vi.fn(async () => ({})),
}))

vi.mock('../store', () => ({ isSuperAdmin: superAdmin }))

vi.mock('../api', () => ({
  getCredentialHeatmap,
  toggleModelAvailability: toggleModel,
  promoteCredential: promote,
  demoteCredential: demote,
  setManualDisabled,
}))

// useCredentialLabels 内部引用真实 api/credential-monitor → store 模块链，
// 会连带触发本环境 jsdom localStorage 缺失的存量问题；以最小替身隔离。
vi.mock('../composables/useCredentialLabels', () => ({
  useCredentialLabels: () => ({
    credentialDisplayName: (_id: number, fallback: string) => fallback,
    loadCredentialLabels: async () => undefined,
  }),
}))

const BUCKET = {
  // ★ 必须与 meta.time_start 完全对齐：时间轴是 floor(time_start/step)*step 起步，
  //   桶键也是 floor(bucket/step)*step ⇒ 两者相等才会落在同一格上，
  //   否则色块是 blank，@click 里的 `byBucket.get(t) && onCellClick(...)` 短路不触发。
  time_bucket: '2026-10-20T00:00:00Z',
  status: 'ready',
  total_requests: 10,
  success_count: 9,
  failed_count: 1,
  success_rate: 0.9,
  avg_latency_ms: 120,
  p95_latency_ms: 240,
  error_distribution: {},
  sample_request_ids: [],
}

function heatmapResponse() {
  return {
    credentials: [
      {
        credential_id: 7,
        label: 'prod-key',
        provider_name: 'provider-a',
        models: [
          {
            raw_model_name: 'gpt-4o',
            buckets: [BUCKET],
            node_status: { state: 'healthy_confirmed', routable: true },
          },
        ],
      },
    ],
    meta: {
      time_start: '2026-10-20T00:00:00Z',
      time_end: '2026-10-22T00:00:00Z',
      granularity: '1h',
      bucket_count: 24,
      cache_hit: false,
    },
  }
}

// 组件用 useI18n() 取写侧角色门的两条文案 ⇒ 必须装 i18n 插件，
// 否则 useI18n() 直接抛「Need to install with app.use function」。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      credentialMonitor: {
        error: {
          superAdminOnlyHint: '仅超级管理员可执行状态修正；当前为只读。',
          superAdminOnlyError: '仅超级管理员可执行状态修正操作。',
        },
      },
    },
  },
})

async function mountHeatmap() {
  const w = mount(CredentialHeatmapView, {
    global: {
      plugins: [i18n],
      stubs: {
        // KxDateRangePicker 依赖 element-plus（ElDatePicker/ElPopover），
        // 与本判据无关，替身隔离。
        KxDateRangePicker: { name: 'KxDateRangePicker', template: '<div class="stub-date-range" />' },
      },
    },
  })
  await flushPromises()
  // 打开某个色块的详情弹层（「状态修正」区在弹层里）。
  // 汇总行的色块始终渲染（模型行要展开凭据才出现），点汇总行即可进详情。
  const cell = w.find('td.cell.summary-cell')
  expect(cell.exists()).toBe(true)
  await cell.trigger('click')
  await flushPromises()
  return w
}

/** 读「状态修正」区的 6 个写按钮。 */
function writeButtons(w: Awaited<ReturnType<typeof mountHeatmap>>) {
  const wanted = ['模型上线', '模型下线', '恢复凭据', '降级凭据', '启用凭据', '停用凭据']
  return wanted.map((label) => {
    const btn = w.findAll('button').find((b) => b.text().trim() === label)
    if (!btn) throw new Error(`按钮不存在：${label}`)
    return btn
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  superAdmin.mockReturnValue(false)
  getCredentialHeatmap.mockResolvedValue(heatmapResponse())
  promote.mockResolvedValue({})
  demote.mockResolvedValue({})
  toggleModel.mockResolvedValue({})
  setManualDisabled.mockResolvedValue({})
})

describe('★ 写侧角色门 —— tenant_admin 不能触发凭据状态修正', () => {
  it('tenant_admin：6 个写按钮全部禁用，且原因输入框也禁用', async () => {
    superAdmin.mockReturnValue(false)
    const w = await mountHeatmap()

    for (const btn of writeButtons(w)) {
      expect(btn.attributes('disabled')).toBeDefined()
    }
    const reason = w.find('input.reason-input')
    expect(reason.exists()).toBe(true)
    expect(reason.attributes('disabled')).toBeDefined()
  })

  it('tenant_admin：即使按钮的 disabled 态被绕过，第二道守卫仍拦住写端点', async () => {
    superAdmin.mockReturnValue(false)
    const w = await mountHeatmap()

    // ★ 必须绕掉 disabled 态，否则这条断言是**假绿**：
    //   Vue Test Utils 不给 disabled 元素派发事件 ⇒ 事件压根没进 runAction
    //   ⇒ 「端点没被调用」是因为按钮禁用，而不是因为函数里的守卫。
    //   （把守卫删掉，这条用例照样全绿 —— 实测过。）
    //   这里显式模拟「按钮态被绕过」，断言**函数层**的守卫独立成立。
    for (const btn of writeButtons(w)) {
      const el = btn.element as HTMLButtonElement
      el.removeAttribute('disabled')
      el.disabled = false
      await btn.trigger('click')
    }
    await flushPromises()

    expect(promote).not.toHaveBeenCalled()
    expect(demote).not.toHaveBeenCalled()
    expect(toggleModel).not.toHaveBeenCalled()
    expect(setManualDisabled).not.toHaveBeenCalled()
  })

  it('★ 允许侧正向断言：super_admin 看得见且点得动，写端点确实被调用（防假绿）', async () => {
    superAdmin.mockReturnValue(true)
    const w = await mountHeatmap()

    for (const btn of writeButtons(w)) {
      expect(btn.attributes('disabled')).toBeUndefined()
    }
    const reason = w.find('input.reason-input')
    expect(reason.attributes('disabled')).toBeUndefined()

    const restore = writeButtons(w).find((b) => b.text().trim() === '恢复凭据')!
    await restore.trigger('click')
    await flushPromises()

    expect(promote).toHaveBeenCalledTimes(1)
    expect(promote).toHaveBeenCalledWith(7, expect.any(String))
    expect(demote).not.toHaveBeenCalled()
    expect(toggleModel).not.toHaveBeenCalled()
    expect(setManualDisabled).not.toHaveBeenCalled()
  })
})
