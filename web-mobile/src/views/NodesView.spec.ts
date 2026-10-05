import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NodesView from './NodesView.vue'
import type { CredentialMonitorSummary } from '@/api/nodes'

/**
 * 回归护栏：凭据卡**不能**把缺失的模型计数渲染成字面量 "undefined"（2026-10-06）。
 *
 * 缺陷背景（模拟器实测 + 线上 API 核对）：
 *   /api/credentials/monitor-summary 的 model_available / model_total 对一部分
 *   凭据是**整个键不存在**（不是 null）。245 上 mode=core 的 65 条里：
 *       58 条是 int，7 条缺失（canary-cred-A / canary-cred-B 等，
 *       状态 auth_failed、14h 未检查）
 *   模板原先无条件 `{{ t('nodes.modelsAvailable', {available, total}) }}`，
 *   于是那 7 张卡在真机上渲染出：
 *       小米大模型 · canary-cred-A
 *       Models undefined/undefined   Concurrency 5
 *
 * 这里钉两条，缺一不可：
 *   ① 有计数 ⇒ 仍渲染 "Models 8/8"（别把正常的也一起藏了）
 *   ② 无计数 ⇒ 整段不出现，且**整页文本里不含 "undefined"**
 *      ——只钉①会让「全都藏起来」这种过度修法蒙混过关。
 */

const WITH_COUNTS = {
  id: 9,
  provider_id: 3,
  provider_name: '小米大模型',
  label: 'xiaomi-token-plan',
  status: 'ok',
  availability_state: 'ready',
  health_status: 'healthy',
  quota_state: 'ok',
  effective_concurrency: 20,
  concurrency_limit: 20,
  manual_disabled: false,
  consecutive_failures: 0,
  state_reason_code: null,
  state_reason_detail: null,
  health_checked_at: '2026-10-06T04:28:36+08:00',
  total_requests: 0,
  model_total: 8,
  model_available: 8,
  broken_model_count: 0,
} satisfies CredentialMonitorSummary

// 现场实测形态：这两个键**根本不存在**（不是 null）。
const WITHOUT_COUNTS = {
  id: 12,
  provider_id: 3,
  provider_name: '小米大模型',
  label: 'canary-cred-A',
  status: 'auth_failed',
  availability_state: 'down',
  health_status: 'down',
  quota_state: 'unknown',
  effective_concurrency: 5,
  concurrency_limit: 5,
  manual_disabled: false,
  consecutive_failures: 3,
  state_reason_code: 'auth_failed',
  state_reason_detail: 'invalid api key',
  health_checked_at: '2026-10-05T15:02:00+08:00',
  total_requests: 0,
  broken_model_count: 0,
} as CredentialMonitorSummary

vi.mock('@/api/nodes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/nodes')>()
  return {
    ...actual,
    fetchMonitorSummary: vi.fn(async () => [{ ...WITH_COUNTS }, { ...WITHOUT_COUNTS }]),
  }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

async function mountView() {
  const w = mount(NodesView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  return w
}

describe('NodesView 凭据卡的模型计数', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('有计数的凭据照常渲染 "Models 8/8"', async () => {
    const w = await mountView()
    const cards = w.findAll('.node-card')
    expect(cards.length).toBe(2)
    expect(cards[0]?.text()).toContain('8/8')
  })

  it('计数缺失的凭据整段不渲染，且全页不出现 "undefined"', async () => {
    const w = await mountView()
    const cards = w.findAll('.node-card')
    expect(cards.length).toBe(2)

    // 缺计数那张：标签仍在，但它不该带 Models 那一段
    const noCounts = cards[1]
    if (!noCounts) throw new Error('node-card[1] 不存在')
    expect(noCounts.text()).toContain('canary-cred-A')
    expect(noCounts.text()).not.toContain('Models')

    // ★ 关键判据：整页文本里不得出现字面量 undefined
    const whole = w.text()
    expect(whole).not.toContain('undefined')
    expect(whole).not.toContain('null')
  })
})
