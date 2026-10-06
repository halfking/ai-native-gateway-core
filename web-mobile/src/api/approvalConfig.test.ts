import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchApprovalConfig,
  unwrapApprovalConfig,
  fetchApprovalConfigStats,
  unwrapApprovalConfigStats,
  fetchApprovalApprovers,
  unwrapApprovalApprovers,
  fetchApprovalRules,
  unwrapApprovalRules,
  approvalConfigIsSynthesized,
  approvalTimeoutMayBeSynthetic,
  approvalAutoRejectMayBeSynthetic,
  approvalModeTone,
  approvalTopApprover,
  approvalTopRule,
  approverListIsEnabledOnly,
  ruleListIsEnabledOnly,
  approverCountIsDerived,
  ruleCountIsDerived,
  channelHasNoConfig,
  ruleHasNoConditions,
  approvalStatsMatchesConfig,
  approverCountGapIsExpected,
  ruleCountGapIsExpected,
  approverHasEmail,
  approverHasPhone,
  APPROVAL_ZERO_TIME,
  APPROVAL_SYNTHETIC_TIMEOUT_SECONDS,
  type ApprovalConfig,
  type ApprovalConfigStats,
  type Approver,
  type ApproverList,
  type ApprovalRule,
  type ApprovalRuleList,
} from './approvalConfig'

/**
 * 租户审批配置面只读端的契约测试（2026-10-08）。
 *
 * 后端逐条对应：
 *   cmd/gateway/main.go:7358               挂载条件：dbConn + Enabled + Redis 都要在
 *   cmd/gateway/main.go:7367               注册前缀 **tenant-approval-config（单数）**
 *   cmd/gateway/main.go:7369-7396          strings.Contains 派发 + default http.NotFound
 *   cmd/gateway/main_admin_wrappers.go:26  wrapAdmin = admin.AdminMiddleware ⇒ **admin 档**
 *   admin/handler.go:926-927               /api/admin/tenants/ 归 superAdmin（复数路径是另一族）
 *   admin/approval_config_handler.go:68    config  ⇒ writeJSON(200, config)  **裸对象**
 *   admin/approval_config_handler.go:141-144 approvers ⇒ {approvers, count}
 *   admin/approval_config_handler.go:294-297 rules ⇒ {rules, count}
 *   admin/approval_config_handler.go:403   stats ⇒ writeJSON(200, stats)     **裸对象**
 *   admin/approval_config_handler.go:413-424 extractTenantID 按**段**匹配两个前缀
 *   admin/approval_config_handler.go:452-490 canAccessTenant/canModifyTenant 额外放行 admin_key
 *   domains/approval/store.go:401-414      无配置行 ⇒ **合成默认配置**（含假 3600）
 *   domains/approval/store.go:389,431      approvers/rules 的 SQL **AND enabled = true**
 *   domains/approval/store.go:390,433      var x []T 是 **nil 切片** ⇒ 空时序列化成 null
 *   domains/approval/store.go:386,428      ORDER BY priority ASC / DESC（方向相反）
 *   domains/approval/store.go:392-393      COALESCE(email,'') + omitempty ⇒ 键不存在
 *   domains/approval/config_manager.go:435-472 stats 是 config 的**纯函数**
 *   domains/approval/types.go:104-176     线格式
 *   domains/approval/types.go:65-68        RiskLevel 四值
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

/** 抄自 `Approver`（types.go:127-135）：email/phone 带 omitempty。 */
function approver(over: Record<string, unknown> = {}): Approver {
  return {
    user_id: 'u1',
    name: 'Alice',
    role: 'admin',
    priority: 1,
    enabled: true,
    ...over,
  } as Approver
}

/** 抄自 `ApprovalRule`（types.go:156-162）。 */
function rule(over: Record<string, unknown> = {}): ApprovalRule {
  return {
    name: 'high-cost',
    enabled: true,
    priority: 10,
    conditions: [{ field: 'cost', operator: 'gt', value: '100' }],
    action: { type: 'require_approval', risk_level: 'HIGH', reason: 'expensive' },
    ...over,
  } as ApprovalRule
}

/** 抄自 `ApprovalConfig`（types.go:104-115）+ store.go 的真实行形态。 */
function config(over: Record<string, unknown> = {}): ApprovalConfig {
  return {
    tenant_id: 'acme',
    enabled: true,
    mode: 'manual',
    approvers: [approver()],
    channels: [{ type: 'feishu', config: { app_id: 'x' }, enabled: true }],
    timeout_seconds: 900,
    auto_reject_on_timeout: false,
    rules: [rule()],
    created_at: '2026-03-01T08:00:00Z',
    updated_at: '2026-09-01T08:00:00Z',
    ...over,
  } as ApprovalConfig
}

/** 抄自 store.go:401-414 的**合成默认**：无配置行时后端返回的东西。 */
function synthesizedConfig(): ApprovalConfig {
  return {
    tenant_id: 'acme',
    enabled: false,
    mode: 'disabled',
    approvers: [],
    channels: [],
    timeout_seconds: 3600,
    auto_reject_on_timeout: true,
    rules: [],
    created_at: APPROVAL_ZERO_TIME,
    updated_at: APPROVAL_ZERO_TIME,
  } as ApprovalConfig
}

/** 抄自 `ConfigStats`（config_manager.go:475-487）。 */
function stats(over: Record<string, unknown> = {}): ApprovalConfigStats {
  return {
    tenant_id: 'acme',
    enabled: true,
    mode: 'manual',
    approver_count: 1,
    enabled_approvers: 1,
    rule_count: 1,
    enabled_rules: 1,
    channel_count: 1,
    enabled_channels: 1,
    timeout_seconds: 900,
    last_updated: '2026-09-01T08:00:00Z',
    ...over,
  } as ApprovalConfigStats
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ 路径是单数 tenant-approval-config，档位是 admin', () => {
  it('★★★★★★ 四条端点都在 `/api/admin/tenant-approval-config/{code}/` 下', async () => {
    const urls: string[] = []

    fetchMock.mockResolvedValueOnce(jsonResponse(config()))
    await fetchApprovalConfig('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse({ approvers: [approver()], count: 1 }))
    await fetchApprovalApprovers('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse({ rules: [rule()], count: 1 }))
    await fetchApprovalRules('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(stats()))
    await fetchApprovalConfigStats('acme')
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/tenant-approval-config/acme/approval-config',
      '/api/admin/tenant-approval-config/acme/approvers',
      '/api/admin/tenant-approval-config/acme/approval-rules',
      '/api/admin/tenant-approval-config/acme/approval-config/stats',
    ])
  })

  it('★★★★★★ ★ 绝不能是复数 `/api/admin/tenants/…`（那一族是 superAdmin 的另一族）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(config()))
    await fetchApprovalConfig('acme')
    expect(lastUrl()).not.toContain('/api/admin/tenants/')
    expect(lastUrl()).toContain('tenant-approval-config')
  })

  it('★★★★★ 租户码必须 encode', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(config()))
    await fetchApprovalConfig('a/b')
    expect(lastUrl()).toBe('/api/admin/tenant-approval-config/a%2Fb/approval-config')
  })
})

describe('★★★★★★ config / stats 是**裸对象**，approvers / rules 是信封', () => {
  it('★★★★★★ config 正确形状可解包，裸数组/信封 ⇒ 抛错', () => {
    expect(() => unwrapApprovalConfig(config())).not.toThrow()
    expect(() => unwrapApprovalConfig([config()])).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfig({ config })).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfig(null)).toThrow(/形状不符/)
  })

  it('★★★★★★ stats 也是裸对象', () => {
    expect(() => unwrapApprovalConfigStats(stats())).not.toThrow()
    expect(() => unwrapApprovalConfigStats({ stats: stats() })).toThrow(/形状不符/)
  })

  it('★★★★★★ `mode` 缺失 ⇒ 抛错（那是契约的一部分）', () => {
    const c = JSON.parse(JSON.stringify(config())) as Record<string, unknown>
    delete c.mode
    expect(() => unwrapApprovalConfig(c)).toThrow(/形状不符/)
  })

  it('★★★★★★★★ ConfigStats 的键是 ApprovalConfig 的**真子集** ⇒ 判别键必须多于三个', () => {
    // ★ 这条不是「顺手多写个断言」：stats 满足 config 的 tenant_id/enabled/mode
    //   三个判别键且类型全对 ⇒ 只按三个判，stats 会被当 config 放行。
    const cfgKeys = Object.keys(config())
    const statKeys = Object.keys(stats())
    // ★ 共享的是四个键（timeout_seconds 两边都有，config_manager.go:485 也带它）
    const shared = statKeys.filter((k) => cfgKeys.includes(k))
    expect(shared.sort()).toEqual(['enabled', 'mode', 'tenant_id', 'timeout_seconds'])
    // ★ 真正各自独有的判别键：config 独有 auto_reject_on_timeout，stats 独有 approver_count
    expect(cfgKeys).toContain('auto_reject_on_timeout')
    expect(statKeys).not.toContain('auto_reject_on_timeout')
    expect(statKeys).toContain('approver_count')
    expect(cfgKeys).not.toContain('approver_count')
    // ★ 两个判别键在 Go 侧都**没有** omitempty ⇒ 真实响应里必然在
    expect(cfgKeys).toContain('timeout_seconds')
    // ★★★ 光比键集只是文档，护不住行为 ⇒ 必须再断言**解包器真的会拒绝**。
    //   只比键集的话，把 `auto_reject_on_timeout` 从判别条件里删掉也照样绿。
    expect(() => unwrapApprovalConfig(stats())).toThrow(/形状不符/)
  })

  it('★★★★★★ ★★ 四个形状两两互不包含，可以互喂判据', () => {
    const cfgShape = config()
    const statShape = stats()
    const appShape = { approvers: [approver()], count: 1 }
    const ruleShape = { rules: [rule()], count: 1 }

    expect(() => unwrapApprovalConfigStats(cfgShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfig(appShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfig(ruleShape)).toThrow(/形状不符/)

    expect(() => unwrapApprovalConfig(statShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfigStats(appShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalConfigStats(ruleShape)).toThrow(/形状不符/)

    expect(() => unwrapApprovalApprovers(cfgShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalApprovers(ruleShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalRules(cfgShape)).toThrow(/形状不符/)
    expect(() => unwrapApprovalRules(appShape)).toThrow(/形状不符/)
  })
})

describe('★★★★★★★★ 空列表序列化成 `null`，解包器不许抛错', () => {
  it('★★★★★★★★ `{"approvers": null, "count": 0}` 是**合法成功响应**', () => {
    // ★ 后端 `var approvers []Approver` 是 nil 切片，不是 make([]T,0)
    const resp = { approvers: null, count: 0 }
    expect(() => unwrapApprovalApprovers(resp)).not.toThrow()
    expect(unwrapApprovalApprovers(resp).approvers).toBe(null)
    expect(unwrapApprovalApprovers(resp).count).toBe(0)
  })

  it('★★★★★★★★ `{"rules": null, "count": 0}` 同样合法', () => {
    const resp = { rules: null, count: 0 }
    expect(() => unwrapApprovalRules(resp)).not.toThrow()
    expect(unwrapApprovalRules(resp).rules).toBe(null)
  })

  it('★★★★★★ 但「键整个不存在」⇒ 抛错（null 与缺键是两回事）', () => {
    expect(() => unwrapApprovalApprovers({ count: 0 })).toThrow(/形状不符/)
    expect(() => unwrapApprovalRules({ count: 0 })).toThrow(/形状不符/)
  })

  it('★★★★★★ 数组是合法数组时照常解包', () => {
    const resp: ApproverList = { approvers: [approver()], count: 1 }
    expect(unwrapApprovalApprovers(resp).approvers).toHaveLength(1)
    const rl: ApprovalRuleList = { rules: [rule()], count: 1 }
    expect(unwrapApprovalRules(rl).rules).toHaveLength(1)
  })

  it('★★★★★★ fetch 层面对 null 数组**不抛错**', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ approvers: null, count: 0 }))
    await expect(fetchApprovalApprovers('acme')).resolves.toEqual({ approvers: null, count: 0 })
    fetchMock.mockResolvedValueOnce(jsonResponse({ rules: null, count: 0 }))
    await expect(fetchApprovalRules('acme')).resolves.toEqual({ rules: null, count: 0 })
  })
})

describe('★★★★★★★★ 从未配置过 ⇒ 后端返回**合成默认配置**', () => {
  it('★★★★★★★★ 零值时间是判别标志', () => {
    expect(approvalConfigIsSynthesized(synthesizedConfig())).toBe(true)
    expect(approvalConfigIsSynthesized(config())).toBe(false)
    expect(APPROVAL_ZERO_TIME).toBe('0001-01-01T00:00:00Z')
  })

  it('★★★★★★★★ 合成出来的 3600 / auto_reject 必须被标成「可能是假的」', () => {
    const s = synthesizedConfig()
    expect(approvalTimeoutMayBeSynthetic(s)).toBe(true)
    expect(approvalAutoRejectMayBeSynthetic(s)).toBe(true)
    // ★★ 真配置里恰好也是 3600 时**不能**被误标
    expect(approvalTimeoutMayBeSynthetic(config({ timeout_seconds: 3600 }))).toBe(false)
    expect(approvalAutoRejectMayBeSynthetic(config({ auto_reject_on_timeout: true }))).toBe(false)
  })

  it('★★★★★★★ 合成默认里 `enabled` 恒 false、`mode` 恒 disabled', () => {
    const s = synthesizedConfig()
    expect(s.enabled).toBe(false)
    expect(s.mode).toBe('disabled')
    expect(s.timeout_seconds).toBe(APPROVAL_SYNTHETIC_TIMEOUT_SECONDS)
  })

  it('★★★★★★ 只有一个时间戳是零值也算合成', () => {
    expect(approvalConfigIsSynthesized(config({ created_at: APPROVAL_ZERO_TIME }))).toBe(true)
    expect(approvalConfigIsSynthesized(config({ updated_at: APPROVAL_ZERO_TIME }))).toBe(true)
  })
})

describe('★★★★★★★ stats 是 config 的纯函数，客户端独立复算', () => {
  it('★★★★★★★★ 从 config 复算出的 stats 与后端给的**完全一致** ⇒ 判定为一致', () => {
    const c = config()
    expect(approvalStatsMatchesConfig(c, stats())).toBe(true)
  })

  it('★★★★★★★★ 逐个字段：改一个就判不一致', () => {
    const c = config()
    const bad: Array<[string, ApprovalConfigStats]> = [
      ['tenant_id', stats({ tenant_id: 'other' })],
      ['enabled', stats({ enabled: false })],
      ['mode', stats({ mode: 'disabled' })],
      ['approver_count', stats({ approver_count: 9 })],
      ['enabled_approvers', stats({ enabled_approvers: 9 })],
      ['rule_count', stats({ rule_count: 9 })],
      ['enabled_rules', stats({ enabled_rules: 9 })],
      ['channel_count', stats({ channel_count: 9 })],
      ['enabled_channels', stats({ enabled_channels: 9 })],
      ['timeout_seconds', stats({ timeout_seconds: 9 })],
      ['last_updated', stats({ last_updated: '2020-01-01T00:00:00Z' })],
    ]
    for (const [field, s] of bad) {
      expect(approvalStatsMatchesConfig(c, s), field).toBe(false)
    }
  })

  it('★★★★★★★★ `null` 数组按 0 个算（`?? []`），不该崩也不该算成 null', () => {
    const c = config({ approvers: null, rules: null, channels: null })
    const s = stats({ approver_count: 0, enabled_approvers: 0, rule_count: 0, enabled_rules: 0, channel_count: 0, enabled_channels: 0 })
    expect(approvalStatsMatchesConfig(c, s)).toBe(true)
  })

  it('★★★★★★★ 合成默认 config 与它的 stats 也应一致', () => {
    const c = synthesizedConfig()
    const s = stats({
      enabled: false,
      mode: 'disabled',
      approver_count: 0,
      enabled_approvers: 0,
      rule_count: 0,
      enabled_rules: 0,
      channel_count: 0,
      enabled_channels: 0,
      timeout_seconds: 3600,
      last_updated: APPROVAL_ZERO_TIME,
    })
    expect(approvalStatsMatchesConfig(c, s)).toBe(true)
  })
})

describe('★★★★★★★★ 两个数据源：表 vs JSONB，条数可以对不上', () => {
  it('★★★★★★★★ `/approvers` 只回 enabled 行，而 stats 计数含停用的', () => {
    const c = config({ approvers: [approver(), approver({ user_id: 'u2', enabled: false })] })
    // JSONB 里 2 个（含 1 个停用）
    const s = stats({ approver_count: 2, enabled_approvers: 1 })
    expect(approvalStatsMatchesConfig(c, s)).toBe(true)
    // 而 `/approvers` 表查询只回 enabled ⇒ 1 行
    expect(approverCountGapIsExpected(s, 1)).toBe(true)
    expect(approverCountGapIsExpected(s, 2)).toBe(false)
    expect(approverListIsEnabledOnly()).toBe(true)
  })

  it('★★★★★★★★ 规则同理', () => {
    const c = config({ rules: [rule(), rule({ name: 'off', enabled: false })] })
    const s = stats({ rule_count: 2, enabled_rules: 1 })
    expect(approvalStatsMatchesConfig(c, s)).toBe(true)
    expect(ruleCountGapIsExpected(s, 1)).toBe(true)
    expect(ruleListIsEnabledOnly()).toBe(true)
  })
})

describe('★★★ count 是派生值', () => {
  it('★★★ `null` 与 0 条都推出 count=0', () => {
    expect(approverCountIsDerived({ approvers: null, count: 0 })).toBe(true)
    expect(approverCountIsDerived({ approvers: [approver()], count: 1 })).toBe(true)
    expect(ruleCountIsDerived({ rules: null, count: 0 })).toBe(true)
    expect(ruleCountIsDerived({ rules: [rule(), rule({ name: 'b' })], count: 2 })).toBe(true)
    expect(approverCountIsDerived({ approvers: null, count: 3 })).toBe(false)
  })
})

describe('★★★ omitempty / nil map 造成的键缺失', () => {
  it('★★★★★ 空邮箱 / 手机 ⇒ 键整个不存在', () => {
    // ★ COALESCE(email,'') 变空串，再被 omitempty 省掉
    const a = JSON.parse(JSON.stringify(approver())) as Record<string, unknown>
    expect('email' in a).toBe(false)
    expect('phone' in a).toBe(false)
    expect(approverHasEmail(a as unknown as Approver)).toBe(false)
    expect(approverHasPhone(a as unknown as Approver)).toBe(false)
  })

  it('★★★★★ 有值时键在', () => {
    const a = approver({ email: 'a@x.test', phone: '13800000000' })
    expect(approverHasEmail(a)).toBe(true)
    expect(approverHasPhone(a)).toBe(true)
  })

  it('★★★★★ 渠道 config 是 nil map ⇒ `null`，不是 `{}`', () => {
    const c = { type: 'email', config: null, enabled: false }
    expect(channelHasNoConfig(c)).toBe(true)
    expect(channelHasNoConfig({ type: 'email', config: {}, enabled: false })).toBe(false)
  })

  it('★★★★★ 规则 conditions 为 null ⇒ 一条条件都没有', () => {
    expect(ruleHasNoConditions(rule({ conditions: null }))).toBe(true)
    expect(ruleHasNoConditions(rule())).toBe(false)
    expect(ruleHasNoConditions(rule({ conditions: [] }))).toBe(false)
  })
})

describe('★★ 优先级方向相反', () => {
  it('★★ approvers 数小优先 ⇒ 列表首个最高', () => {
    const list = [approver({ user_id: 'a', priority: 1 }), approver({ user_id: 'b', priority: 5 })]
    expect(approvalTopApprover(list)?.user_id).toBe('a')
    expect(approvalTopApprover(null)).toBe(null)
    expect(approvalTopApprover([])).toBe(null)
  })

  it('★★ rules 数大优先 ⇒ 列表首个最高', () => {
    const list = [rule({ name: 'top', priority: 10 }), rule({ name: 'low', priority: 1 })]
    expect(approvalTopRule(list)?.name).toBe('top')
    expect(approvalTopRule(null)).toBe(null)
    expect(approvalTopRule([])).toBe(null)
  })
})

describe('★★ mode 色调', () => {
  it('★★ manual=warning、automatic=success、disabled 与未知=muted', () => {
    expect(approvalModeTone('manual')).toBe('warning')
    expect(approvalModeTone('automatic')).toBe('success')
    expect(approvalModeTone('disabled')).toBe('muted')
    // ★ 枚举来自注释不是数据库 CHECK ⇒ 未知值不许猜
    expect(approvalModeTone('weird')).toBe('muted')
  })
})

describe('★★ 错误态透出', () => {
  it('★★ 403 `access denied`（handler 内那道 canAccessTenant）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'access denied' } }, 403))
    await expect(fetchApprovalConfig('other')).rejects.toThrow(/access denied/)
  })

  it('★★ 400 `missing tenant_id`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'missing tenant_id' } }, 400))
    await expect(fetchApprovalConfig('')).rejects.toThrow(/missing tenant_id/)
  })

  it('★★★★★★ 404 `unknown sub-resource: approval-config`（误打复数路径时的答复）', async () => {
    // ★ 这正是打 /api/admin/tenants/{code}/approval-config 会收到的
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'unknown sub-resource: approval-config' } }, 404),
    )
    await expect(fetchApprovalConfig('acme')).rejects.toThrow(/unknown sub-resource/)
  })

  it('★★★★★★ 裸文本 404（`http.NotFound`，整族未注册时）', async () => {
    // ★ default 分支是 http.NotFound ⇒ text/plain「404 page not found」，不是 JSON 信封
    fetchMock.mockResolvedValueOnce(
      jsonResponse('404 page not found\n', 404),
    )
    await expect(fetchApprovalConfig('acme')).rejects.toThrow(/404 page not found/)
  })

  it('★★ 500 `failed to get config`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'failed to get config' } }, 500))
    await expect(fetchApprovalConfig('acme')).rejects.toThrow(/failed to get config/)
  })
})