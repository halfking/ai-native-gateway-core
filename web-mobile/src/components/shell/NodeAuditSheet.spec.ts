import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NodeAuditSheet from './NodeAuditSheet.vue'
import { fetchNodeAudit, operationLabel, isCredentialLevelOp } from '@/api/nodeAudit'

/**
 * 节点操作审计 Sheet（2026-10-06）。
 *
 * 钉的是**作用域诚实性**，这是这块最容易出事的地方：
 *   后端 audit_operations.go 的覆盖面**只有供应商级**两类操作
 *   （node_operations_audit.go:35 "test-now"、:63 "enable_toggle"），
 *   而本专题 §11 上移的四个**凭据级**写操作（停用/恢复/检查/强制恢复）**不落这里**。
 *   ⇒ UI 必须显式说明这个缺口（coverageHint），否则用户会以为
 *     「我刚才在节点页做的那次停用应该能查到」而查不到，误判为系统故障。
 *
 * 另钉 operation 取值：注释写 test_now，实际落库是 "test-now"（连字符）。
 * 过滤器照抄注释会永远查不到行。
 */

vi.mock('@/api/nodeAudit', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/nodeAudit')>()
  return { ...actual, fetchNodeAudit: vi.fn() }
})

const ENTRY_TOGGLE = {
  request_id: 'provider:5:toggle:1',
  provider_id: 5,
  operation: 'enable_toggle',
  from_state: 'disabled',
  to_state: 'enabled',
  operator_id: 'admin',
  source: 'admin_api',
  enabled: true,
  created_at: '2026-10-06T13:00:00Z',
}

const ENTRY_PROBE = {
  request_id: 'provider:5:test-now:1',
  provider_id: 5,
  operation: 'test-now',
  status: 'ok',
  latency_ms: 812,
  source: 'admin_api',
  created_at: '2026-10-06T13:05:00Z',
}

async function mountSheet(providerId: number | null = 5) {
  const w = mount(NodeAuditSheet, {
    props: { providerId, providerName: 'Bedrock' },
    attachTo: document.body,
  })
  await flushPromises()
  await flushPromises()
  return w
}

describe('NodeAuditSheet 节点操作审计', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('按 provider_id + limit 拉取，并渲染两类供应商级操作', async () => {
    ;(fetchNodeAudit as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      entries: [ENTRY_TOGGLE, ENTRY_PROBE],
      count: 2,
      limit: 30,
    })
    await mountSheet(5)

    expect(fetchNodeAudit).toHaveBeenCalledWith({ provider_id: 5, limit: 30 })
    const text = document.body.textContent ?? ''
    expect(text).toContain('Enable toggle')
    expect(text).toContain('Trigger probe')
    // from→to 完整拼接
    expect(text).toContain('disabled → enabled')
    // 覆盖面说明必须出现
    expect(text).toContain('provider-level operations only')
  })

  it('★ 空结果与错误是两个不同状态（不是都显示「无记录」）', async () => {
    ;(fetchNodeAudit as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ entries: [], count: 0, limit: 30 })
    await mountSheet(5)
    let text = document.body.textContent ?? ''
    expect(text).toContain('No operations recorded')
    expect(text).not.toContain('Failed')

    document.body.innerHTML = ''
    vi.clearAllMocks()
    ;(fetchNodeAudit as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error('boom'))
    await mountSheet(5)
    text = document.body.textContent ?? ''
    expect(text).toContain('boom')
    expect(text).not.toContain('No operations recorded')
  })

  it('providerId 为 null 时不发请求', async () => {
    await mountSheet(null)
    expect(fetchNodeAudit).not.toHaveBeenCalled()
  })
})

describe('operationLabel', () => {
  const t = (k: string) => k
  it('连字符 test-now 命中（后端真实落库值，:35）', () => {
    expect(operationLabel('test-now', t)).toBe('audit.opTestNow')
  })
  it('下划线 test_now 也容忍（后端注释里的写法，audit_operations.go:14）', () => {
    expect(operationLabel('test_now', t)).toBe('audit.opTestNow')
  })
  it('enable_toggle 命中（:63）', () => {
    expect(operationLabel('enable_toggle', t)).toBe('audit.opEnableToggle')
  })
  it('未知类型原样透传，不臆造中文名', () => {
    expect(operationLabel('brand_new_op', t)).toBe('brand_new_op')
  })
  it('空串回落为「未知」', () => {
    expect(operationLabel('', t)).toBe('common.unknown')
  })
})

describe('凭据级操作识别（审计缺口用）', () => {
  it.each(['manual_disabled', 'clear_manual_disabled', 'force_recover'])('%s 属凭据级', (op) => {
    expect(isCredentialLevelOp(op)).toBe(true)
  })
  it.each(['test-now', 'enable_toggle'])('%s 属供应商级（不在缺口内）', (op) => {
    expect(isCredentialLevelOp(op)).toBe(false)
  })
})
