import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTenantModelPolicies,
  unwrapTenantModelPolicies,
  checkTenantModelPolicy,
  unwrapTenantModelPolicyCheck,
  fetchTenantModelPolicyAudit,
  unwrapTenantModelPolicyAudit,
  modelPolicyIncludeDeletedWire,
  backendSeesIncludeDeleted,
  modelPolicyAuditLimitEffective,
  modelPolicyAuditLimitFellBack,
  modelPolicySoftDeleted,
  modelPolicyActive,
  modelPolicyDeletedBy,
  modelPolicyDeletedCount,
  modelPolicyCountIsDerived,
  modelPolicyCheckIsUnknown,
  modelPolicyCheckHasFamily,
  modelPolicyCheckVendorUnavailable,
  modelPolicyAuditHasPolicyRef,
  modelPolicyAuditHasNoOlderPage,
  modelPolicyAuditKnownActions,
  tenantPolicyTenantMissingIsError,
  TENANT_POLICY_TENANT_MISSING,
  MODEL_POLICY_AUDIT_LIMIT_DEFAULT,
  MODEL_POLICY_AUDIT_LIMIT_MAX,
  MODEL_POLICY_AUDIT_ACTIONS,
  MODEL_POLICY_CHECK_CTX_SECONDS,
  type TenantModelPolicy,
  type TenantModelPolicyList,
  type TenantModelPolicyCheck,
  type TenantModelPolicyAuditRow,
} from './modelPolicies'

/**
 * 租户 model-policies 子树只读面的契约测试（2026-10-08）。
 *
 * 后端逐条对应：
 *   admin/handler.go:926-927        两条注册都是 h.superAdmin(...) ⇒ 整棵子树 superAdmin
 *   admin/tenants.go:188-190        唯一挂载点（isModelPoliciesSubResource 转发）
 *   admin/tenants.go:216-218        前缀匹配（2026-06-23 修：/check /audit 曾全挂）
 *   admin/model_policies.go:87-163   子树分派（405 check/audit/undelete、400 id、404 tail）
 *   admin/model_policies.go:173-178  list 先验租户存在；err != nil 也报 404
 *   admin/model_policies.go:180      include_deleted 只认字面 "true"
 *   admin/model_policies.go:202-211  scan 失败静默 continue
 *   admin/model_policies.go:218-222  {policies, count:len(out), tenant}
 *   admin/model_policies.go:44-54    TenantModelPolicyWire（deleted_at/by 带 omitempty）
 *   admin/model_policies.go:564-570  check 的 SQL **不含 tenant_id**
 *   admin/model_policies.go:572      modality 初值 "text"
 *   admin/model_policies.go:79       Vendor 声明后**从不赋值** ⇒ 键永不存在
 *   admin/model_policies.go:596-601  audit limit 越界**回落 100**
 *   admin/model_policies.go:647-650  {audit, count:len(out)}
 *   admin/model_policies.go:607-616  auditRow（policy_id 带 omitempty）
 *   admin/model_policies.go:659-672  canAdministerTenant 的 tenant_admin/admin_key 分支在本子树是死代码
 *   admin/tenant_ctx.go              withTenantTx 不校验租户存在
 *   sql/objects/tables/tenant_model_policies_audit.sql   action CHECK（insert 非 create）
 *   sql/objects/functions/tenant_model_policies_audit_fn.sql  actor 兜底 'system'
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

function lastInit(): RequestInit {
  return fetchMock.mock.calls.at(-1)![1] as RequestInit
}

/** 抄自 `TenantModelPolicyWire`（model_policies.go:44-54）。 */
function policy(over: Record<string, unknown> = {}): TenantModelPolicy {
  return {
    id: 7,
    tenant_id: 'acme',
    canonical_name: 'gpt-4o',
    reason: 'denylist for cost',
    created_by: 'root',
    created_at: '2026-05-01T10:00:00Z',
    updated_at: '2026-05-01T10:00:00Z',
    ...over,
  }
}

/** 抄自 `writeJSON(w, 200, {policies, count, tenant})`（model_policies.go:218-222）。 */
function policyList(policies: TenantModelPolicy[] = [policy()]): TenantModelPolicyList {
  return { policies, count: policies.length, tenant: 'acme' }
}

/** 抄自 `TenantModelPolicyCheckResp`（model_policies.go:75-81），**命中**形态。 */
function checkFound(): TenantModelPolicyCheck {
  return { exists: true, canonical_name: 'gpt-4o', family: 'gpt', modality: 'text' }
}

/** 抄自同一结构体的**未命中**形态：`resp := {CanonicalName, Modality:"text"}`，Exists 留 false。 */
function checkMissing(): TenantModelPolicyCheck {
  return { exists: false, canonical_name: 'nope-9000', modality: 'text' }
}

/** 抄自 `auditRow`（model_policies.go:607-616）。 */
function auditRow(over: Record<string, unknown> = {}): TenantModelPolicyAuditRow {
  return {
    id: 101,
    ts: '2026-06-01T08:00:00Z',
    action: 'insert',
    policy_id: 7,
    tenant_id: 'acme',
    canonical_name: 'gpt-4o',
    reason: 'denylist for cost',
    actor: 'root',
    ...over,
  }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ 档位与 URL：整棵子树都是 superAdmin', () => {
  it('★★★★★★ 三条端点都挂在 `/api/admin/tenants/{code}/model-policies` 下', async () => {
    const urls: string[] = []

    fetchMock.mockResolvedValueOnce(jsonResponse(policyList()))
    await fetchTenantModelPolicies('acme')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(checkFound()))
    await checkTenantModelPolicy('acme', 'gpt-4o')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse({ audit: [auditRow()], count: 1 }))
    await fetchTenantModelPolicyAudit('acme')
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/tenants/acme/model-policies',
      '/api/admin/tenants/acme/model-policies/check',
      '/api/admin/tenants/acme/model-policies/audit',
    ])
  })

  it('★★★★★ check 必须是 POST 且带 canonical_name 载荷', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(checkFound()))
    await checkTenantModelPolicy('acme', 'gpt-4o')
    expect(lastInit().method).toBe('POST')
    expect(JSON.parse(String(lastInit().body))).toEqual({ canonical_name: 'gpt-4o' })
  })

  it('★★★★★ 租户码必须 encode（后端 SplitN 按 `/` 切段）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyList()))
    await fetchTenantModelPolicies('a/b')
    expect(lastUrl()).toBe('/api/admin/tenants/a%2Fb/model-policies')
  })
})

describe('★★★★★★ list：`{policies, count, tenant}` 不是裸数组、不是 `{items}`', () => {
  it('★★★★★★ 正确形状可解包', () => {
    expect(() => unwrapTenantModelPolicies(policyList())).not.toThrow()
  })

  it('★★★★★★ 裸数组 ⇒ 抛错（本仓多数 admin 端点才是裸数组）', () => {
    expect(() => unwrapTenantModelPolicies([policy()])).toThrow(/形状不符/)
  })

  it('★★★★★★ `{items}` ⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicies({ items: [policy()], count: 1, tenant: 'acme' })).toThrow(/形状不符/)
  })

  it('★★★★★ null ⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicies(null)).toThrow(/形状不符/)
  })

  it('★★★★★ `count` 缺失 ⇒ 抛错（`count` 是契约的一部分）', () => {
    expect(() => unwrapTenantModelPolicies({ policies: [], tenant: 'acme' })).toThrow(/形状不符/)
  })

  it('★★★★★ 空清单是 `{policies:[], count:0}`，**不是** `{policies:null}`', () => {
    const empty = policyList([])
    expect(unwrapTenantModelPolicies(empty).policies).toEqual([])
  })
})

describe('★★★★★★ check：裸对象，**没有** `vendor` 键', () => {
  it('★★★★★★ 命中 / 未命中两种形态都可解包', () => {
    expect(() => unwrapTenantModelPolicyCheck(checkFound())).not.toThrow()
    expect(() => unwrapTenantModelPolicyCheck(checkMissing())).not.toThrow()
  })

  it('★★★★★★ `vendor` 键**永不存在**（后端从不赋值 + omitempty）', () => {
    const parsed = JSON.parse(JSON.stringify(checkFound())) as Record<string, unknown>
    expect('vendor' in parsed).toBe(false)
    expect(Object.keys(parsed).sort()).toEqual(['canonical_name', 'exists', 'family', 'modality'])
  })

  it('★★★★★★ 未命中形态：`family` 与 `vendor` 都没有，`modality` 仍是 `"text"`', () => {
    const parsed = JSON.parse(JSON.stringify(checkMissing())) as Record<string, unknown>
    expect(Object.keys(parsed).sort()).toEqual(['canonical_name', 'exists', 'modality'])
    // ★★ modality 是初值，不是「没有模态」
    expect(parsed.modality).toBe('text')
  })

  it('★★★★★ `exists` 非布尔 ⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicyCheck({ exists: 'yes', canonical_name: 'x', modality: 'text' })).toThrow(
      /形状不符/,
    )
  })

  it('★★★★★ `modality` 缺失 ⇒ 抛错（它恒有值，缺了就说明不是这形状）', () => {
    expect(() => unwrapTenantModelPolicyCheck({ exists: true, canonical_name: 'x' })).toThrow(/形状不符/)
  })

  it('★★★★★ 带信封（`{result: …}`）⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicyCheck({ result: checkFound() })).toThrow(/形状不符/)
  })
})

describe('★★★★★★ audit：`{audit, count}`，**没有** `tenant` 键', () => {
  it('★★★★★★ 正确形状可解包', () => {
    expect(() => unwrapTenantModelPolicyAudit({ audit: [auditRow()], count: 1 })).not.toThrow()
  })

  it('★★★★★ 缺 `count` ⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicyAudit({ audit: [] })).toThrow(/形状不符/)
  })

  it('★★★★★ `audit` 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapTenantModelPolicyAudit({ audit: null, count: 0 })).toThrow(/形状不符/)
  })

  it('★★★★★★ 空审计是 `{audit:[], count:0}`，**不是** `{audit:null}`', () => {
    expect(unwrapTenantModelPolicyAudit({ audit: [], count: 0 }).audit).toEqual([])
  })
})

describe('★★★★★★ 互喂：三个形状互不包含，必须互相抛错', () => {
  it('★★★★★★ list 的形状喂 check / audit ⇒ 抛错', () => {
    const listShape = { policies: [policy()], count: 1, tenant: 'acme' }
    expect(() => unwrapTenantModelPolicyCheck(listShape)).toThrow(/形状不符/)
    expect(() => unwrapTenantModelPolicyAudit(listShape)).toThrow(/形状不符/)
  })

  it('★★★★★★ audit 的形状喂 check / list ⇒ 抛错', () => {
    const auditShape = { audit: [auditRow()], count: 1 }
    expect(() => unwrapTenantModelPolicyCheck(auditShape)).toThrow(/形状不符/)
    expect(() => unwrapTenantModelPolicies(auditShape)).toThrow(/形状不符/)
  })

  it('★★★★★★ check 的形状喂 list / audit ⇒ 抛错', () => {
    const checkShape = { exists: true, canonical_name: 'gpt-4o', modality: 'text' }
    expect(() => unwrapTenantModelPolicies(checkShape)).toThrow(/形状不符/)
    expect(() => unwrapTenantModelPolicyAudit(checkShape)).toThrow(/形状不符/)
  })
})

describe('★★★★★★ 软删除：`deleted_at` / `deleted_by` 带 omitempty', () => {
  it('★★★★★★ 未软删的策略**没有** `deleted_at` 键（不是 `null`）', () => {
    const active = JSON.parse(JSON.stringify(policy())) as Record<string, unknown>
    expect('deleted_at' in active).toBe(false)
    expect(modelPolicySoftDeleted(active as unknown as TenantModelPolicy)).toBe(false)
    expect(modelPolicyActive(active as unknown as TenantModelPolicy)).toBe(true)
  })

  it('★★★★★★ 已软删：有 `deleted_at` + `deleted_by`', () => {
    const deleted = policy({ deleted_at: '2026-06-01T08:00:00Z', deleted_by: 'root' })
    expect(modelPolicySoftDeleted(deleted)).toBe(true)
    expect(modelPolicyActive(deleted)).toBe(false)
    expect(modelPolicyDeletedBy(deleted)).toBe('root')
  })

  it('★★★★★ `deleted_at` 键缺失 vs `deleted_by` 键缺失**是两回事**', () => {
    // 后端两个字段各自 omitempty，理论上可只缺一个
    const onlyAt = policy({ deleted_at: '2026-06-01T08:00:00Z' })
    expect(modelPolicySoftDeleted(onlyAt)).toBe(true)
    expect(modelPolicyDeletedBy(onlyAt)).toBe(null)

    const onlyBy = policy({ deleted_by: 'root' })
    expect(modelPolicySoftDeleted(onlyBy)).toBe(false)
    expect(modelPolicyDeletedBy(onlyBy)).toBe('root')
  })

  it('★★★★★ `deleted_at` 是空串 ⇒ 不算软删（判据卡长度）', () => {
    expect(modelPolicySoftDeleted(policy({ deleted_at: '' }))).toBe(false)
  })

  it('★★★★★ 软删行计数', () => {
    const list = policyList([
      policy({ id: 1 }),
      policy({ id: 2, deleted_at: '2026-06-01T08:00:00Z', deleted_by: 'root' }),
      policy({ id: 3, deleted_at: '2026-06-02T08:00:00Z', deleted_by: 'root' }),
    ])
    expect(modelPolicyDeletedCount(list)).toBe(2)
  })
})

describe('★★★★★★ include_deleted 只认字面 `"true"`', () => {
  it('★★★★★★ 后端只接受 `=== "true"`：其余写法一律当 false', () => {
    expect(backendSeesIncludeDeleted('true')).toBe(true)
    expect(backendSeesIncludeDeleted('1')).toBe(false)
    expect(backendSeesIncludeDeleted('TRUE')).toBe(false)
    expect(backendSeesIncludeDeleted('True')).toBe(false)
    expect(backendSeesIncludeDeleted('yes')).toBe(false)
    expect(backendSeesIncludeDeleted('')).toBe(false)
    expect(backendSeesIncludeDeleted(undefined)).toBe(false)
    expect(backendSeesIncludeDeleted(null)).toBe(false)
  })

  it('★★★★★ URL 里必须出现字面 `include_deleted=true`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyList()))
    await fetchTenantModelPolicies('acme', { includeDeleted: true })
    expect(lastUrl()).toBe('/api/admin/tenants/acme/model-policies?include_deleted=true')
  })

  it('★★★★★ 显式 false 也会发 `include_deleted=false`（后端当 false，但保持形状可读）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyList()))
    await fetchTenantModelPolicies('acme', { includeDeleted: false })
    expect(lastUrl()).toBe('/api/admin/tenants/acme/model-policies?include_deleted=false')
  })

  it('★★★★★ 不传该参数 ⇒ URL 里**没有**这个 query', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyList()))
    await fetchTenantModelPolicies('acme')
    expect(lastUrl()).toBe('/api/admin/tenants/acme/model-policies')
    expect(modelPolicyIncludeDeletedWire(true)).toBe('true')
    expect(modelPolicyIncludeDeletedWire(false)).toBe('false')
  })
})

describe('★★★★★★ audit 的 limit 是**回落 100**，不是 clamp 到 500', () => {
  it('★★★★★★ `501` ⇒ 100（**不是** 500）', () => {
    expect(modelPolicyAuditLimitEffective(501)).toBe(100)
    // ★★ 与 MaaS 的 ClampUsageLimit（>50⇒50）方向相反
    expect(modelPolicyAuditLimitEffective(501)).not.toBe(MODEL_POLICY_AUDIT_LIMIT_MAX)
  })

  it('★★★★★★ `0` / 负数 / 非数字 ⇒ 回落 100', () => {
    expect(modelPolicyAuditLimitEffective(0)).toBe(100)
    expect(modelPolicyAuditLimitEffective(-1)).toBe(100)
    expect(modelPolicyAuditLimitEffective(Number.NaN)).toBe(100)
    expect(modelPolicyAuditLimitEffective('abc')).toBe(100)
    expect(modelPolicyAuditLimitEffective(undefined)).toBe(100)
    expect(modelPolicyAuditLimitEffective(null)).toBe(100)
    expect(MODEL_POLICY_AUDIT_LIMIT_DEFAULT).toBe(100)
  })

  it('★★★★★ 合法区间原样透传（含两端）', () => {
    expect(modelPolicyAuditLimitEffective(1)).toBe(1)
    expect(modelPolicyAuditLimitEffective(100)).toBe(100)
    expect(modelPolicyAuditLimitEffective(MODEL_POLICY_AUDIT_LIMIT_MAX)).toBe(500)
  })

  it('★★★★★ 小数**先截断再判区间**：客户端发的是截断后的整数', () => {
    // ★ 两层：`fetchTenantModelPolicyAudit` 先 `Math.trunc` 再进 URL ⇒ 后端收到 "500" ⇒ 500。
    //   若客户端**不**截断，后端 `strconv.Atoi("500.9")` 会**失败** ⇒ 回落 100。
    //   所以截断本身是在保护越界判定。
    expect(modelPolicyAuditLimitEffective(500.9)).toBe(500)
    expect(modelPolicyAuditLimitEffective(10.9)).toBe(10)
    expect(modelPolicyAuditLimitEffective(-0.5)).toBe(100)
  })

  it('★★★★★ URL 里的小数确实被截断成整数（证明截断发生在入 URL 之前）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ audit: [], count: 0 }))
    await fetchTenantModelPolicyAudit('acme', { limit: 10.9 })
    expect(lastUrl()).toBe('/api/admin/tenants/acme/model-policies/audit?limit=10')
  })

  it('★★★★★ `fellBack` 与 `effective` 必须一致', () => {
    for (const n of [0, -1, 501, 1000, Number.NaN, 'x', undefined]) {
      expect(modelPolicyAuditLimitFellBack(n)).toBe(true)
    }
    for (const n of [1, 50, 100, 500]) {
      expect(modelPolicyAuditLimitFellBack(n)).toBe(false)
    }
  })

  it('★★★★★ URL 里带 `limit` 时原样发（回落由服务端做）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ audit: [], count: 0 }))
    await fetchTenantModelPolicyAudit('acme', { limit: 501 })
    expect(lastUrl()).toBe('/api/admin/tenants/acme/model-policies/audit?limit=501')
  })

  it('★★★★★ 审计没有「更早一页」', () => {
    expect(modelPolicyAuditHasNoOlderPage()).toBe(true)
  })
})

describe('★★★★★★ `count` 是派生值，证明不了扫描没丢行', () => {
  it('★★★★★★ `count === policies.length` 恒成立（后端 `count: len(out)`，同一切片）', () => {
    expect(modelPolicyCountIsDerived(policyList())).toBe(true)
    expect(modelPolicyCountIsDerived(policyList([]))).toBe(true)
    expect(modelPolicyCountIsDerived(policyList([policy(), policy({ id: 8 })]))).toBe(true)
  })

  it('★★★★★ 所以 `count` 恒等于长度 ⇒ 页面不能把它当「全库策略数」', () => {
    // 三行里有一行 scan 失败被 continue 掉时，count 也跟着少 —— 它只反映返回的那部分
    const list = policyList([policy()])
    expect(list.count).toBe(list.policies.length)
  })
})

describe('★★★★★★ 审计行的 `policy_id` 带 omitempty', () => {
  it('★★★★★★ 走触发器的行 `policy_id` 恒在', () => {
    expect(modelPolicyAuditHasPolicyRef(auditRow())).toBe(true)
  })

  it('★★★★★ 键整个不存在（造 Go 实际吐出的形状，不是 `policy_id: null`）⇒ 不挂策略', () => {
    const orphan = JSON.parse(JSON.stringify(auditRow())) as Record<string, unknown>
    delete orphan.policy_id
    expect('policy_id' in orphan).toBe(false)
    expect(modelPolicyAuditHasPolicyRef(orphan as unknown as TenantModelPolicyAuditRow)).toBe(false)
  })

  it('★★★★★ 审计动作集合是闭合的，第一个是 `insert` 不是 `create`', () => {
    expect(modelPolicyAuditKnownActions()).toEqual(['insert', 'update', 'delete', 'undelete'])
    expect([...MODEL_POLICY_AUDIT_ACTIONS]).not.toContain('create')
  })
})

describe('★★★★★★ check 的辅助判读', () => {
  it('★★★★★★ `exists:false` 有多种成因，不许断言「拼错了」', () => {
    expect(modelPolicyCheckIsUnknown(checkMissing())).toBe(true)
    expect(modelPolicyCheckIsUnknown(checkFound())).toBe(false)
  })

  it('★★★★★ `family` 键缺失 ≠ 「无族」（`mc.family IS NULL` 也缺键）', () => {
    const noFamily = JSON.parse(JSON.stringify(checkFound())) as Record<string, unknown>
    delete noFamily.family
    const c = noFamily as unknown as TenantModelPolicyCheck
    expect(c.exists).toBe(true)
    expect(modelPolicyCheckHasFamily(c)).toBe(false)
  })

  it('★★★★★ 厂商后端永不提供 ⇒ 页面必须显式说明，不许渲染空白', () => {
    expect(modelPolicyCheckVendorUnavailable()).toBe(true)
  })

  it('★★★★★ check 的 ctx 预算是 3s（list/audit 都 5s）', () => {
    expect(MODEL_POLICY_CHECK_CTX_SECONDS).toBe(3)
  })
})

describe('★★★★★★★ 「租户不存在」三条端点三种响应', () => {
  it('★★★★★★★ 只有 list 会用状态码报 404；audit/check 静默', () => {
    expect(tenantPolicyTenantMissingIsError('list')).toBe(true)
    expect(tenantPolicyTenantMissingIsError('audit')).toBe(false)
    expect(tenantPolicyTenantMissingIsError('check')).toBe(false)

    expect(TENANT_POLICY_TENANT_MISSING.list).toEqual({ httpStatus: 404, shape: 'error' })
    expect(TENANT_POLICY_TENANT_MISSING.audit).toEqual({ httpStatus: 200, shape: 'empty-200' })
    expect(TENANT_POLICY_TENANT_MISSING.check).toEqual({ httpStatus: 200, shape: 'exists-false-200' })
  })

  it('★★★★★★ list 对 404 `tenant not found` ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'tenant not found' } }, 404))
    await expect(fetchTenantModelPolicies('nope')).rejects.toThrow(/tenant not found/)
  })

  it('★★★★★★★ audit 对**不存在的租户**返回 200 空数组 ⇒ **不抛错**', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ audit: [], count: 0 }))
    await expect(fetchTenantModelPolicyAudit('nope')).resolves.toEqual({ audit: [], count: 0 })
  })

  it('★★★★★★★ check 对**不存在的租户**返回 200 + exists:false ⇒ **不抛错**', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(checkMissing()))
    const c = await checkTenantModelPolicy('nope', 'anything')
    expect(c.exists).toBe(false)
  })
})

describe('★★★ 后端错误态透出', () => {
  it('★★★ 503 `database not configured`（h.db == nil）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'database not configured' } }, 503))
    await expect(fetchTenantModelPolicies('acme')).rejects.toThrow(/database not configured/)
  })

  it('★★★ 405 `check requires POST`（把 check 当 GET 调时的后端答复）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'check requires POST' } }, 405))
    await expect(checkTenantModelPolicy('acme', 'gpt-4o')).rejects.toThrow(/check requires POST/)
  })

  it('★★★ 405 `audit requires GET`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'audit requires GET' } }, 405))
    await expect(fetchTenantModelPolicyAudit('acme')).rejects.toThrow(/audit requires GET/)
  })

  it('★★★ 400 `expected numeric policy id`（子资源头不是数字）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'expected numeric policy id' } }, 400))
    await expect(fetchTenantModelPolicyAudit('acme')).rejects.toThrow(/expected numeric policy id/)
  })

  it('★★★ 400 `canonical_name is required`（TrimSpace 后为空）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'canonical_name is required' } }, 400))
    await expect(checkTenantModelPolicy('acme', '   ')).rejects.toThrow(/canonical_name is required/)
  })

  it('★★★ 404 `unknown sub-path: <tail>`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'unknown sub-path: bogus' } }, 404))
    await expect(fetchTenantModelPolicies('acme')).rejects.toThrow(/unknown sub-path/)
  })
})