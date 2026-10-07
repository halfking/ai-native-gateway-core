import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchSettingsList,
  fetchSetting,
  fetchSettingHistory,
  unwrapSettingsList,
  unwrapSetting,
  unwrapSettingHistory,
  settingsValueIsNullForTenantScope,
  settingsValueIsNullIffTenantScope,
  settingsSourceIsEmptyForTenantScope,
  settingsListItemsAreAlwaysArray,
  settingsHistoryItemsMayBeNull,
  settingsHistoryNullItemsAreAmbiguous,
  settingsHistoryIsNewestFirst,
  settingsHistoryIsWithinLimit,
  settingsAuditKeyIsPresent,
  settingsAuditIsPlatformScoped,
  settingsWriteNeedsSuperAdmin,
  settingsDangerLevelName,
  settingsIsUnknownSettingMessage,
  settingsExtraPathSegmentsAreIgnored,
  settingsEmptyKeyReachesHandler,
  settingsListMayBeIncomplete,
  PLATFORM_SETTINGS_PATH,
  SETTINGS_REGISTRY_UNAVAILABLE_MESSAGE,
  SETTINGS_DB_UNAVAILABLE_MESSAGE,
  SETTINGS_HISTORY_FAILED_MESSAGE,
  SETTINGS_UNKNOWN_ENDPOINT_MESSAGE,
  SETTINGS_UNKNOWN_SETTING_PREFIX,
  SETTINGS_HISTORY_LIMIT,
  SETTINGS_HISTORY_WINDOW_DAYS,
  SETTINGS_AUDIT_LIMIT_MIN,
  SETTINGS_AUDIT_LIMIT_MAX,
  SETTINGS_VALUE_TYPES,
  SETTINGS_SCOPES,
  SETTINGS_SCOPE_PLATFORM,
  SETTINGS_SCOPE_TENANT,
  SETTINGS_CATEGORIES,
  SETTINGS_DANGER_LEVELS,
  SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD,
  SETTINGS_AUDIT_ACTIONS,
  SETTINGS_LIST_ITEM_KEYS,
  SETTINGS_SPEC_KEYS,
  SETTINGS_DETAIL_KEYS,
  SETTINGS_AUDIT_KEYS,
  SETTINGS_AUDIT_REQUIRED_KEYS,
  SETTINGS_AUDIT_OPTIONAL_KEYS,
  type SettingsListItem,
  type SettingsListResponse,
  type SettingsDetailResponse,
  type SettingsSpec,
  type SettingsAuditEntry,
  type SettingsHistoryResponse,
} from './platformSettings'

/**
 * 平台设置的契约测试（2026-10-08，第九十批）。
 *
 * 后端：`admin/settings.go:22-30` 的 `registerSettingsRoutes`（**第五种注册形态**：
 * `mux.HandleFunc` 在另一个文件的方法里，由 `admin/handler.go:1123` 调用）
 * + `settings/spec.go`（Spec 与枚举）+ `settings/audit.go`（ListAudit/AuditEntry）。
 *
 * 重点是源文件头写明的十五件事 (1)…(15)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量（避免自指恒真）。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `settings/spec.go` 与 `settings.go:176-192` ──

function specOf(over: Partial<SettingsSpec> = {}): SettingsSpec {
  return {
    Key: 'compression.mode',
    EnvName: 'LLM_GATEWAY_COMPRESSION_MODE',
    Type: 'enum',
    Scope: 'platform',
    Category: 'compression',
    Default: 'none',
    Min: null,
    Max: null,
    Options: ['none', 'gzip'],
    Description: '压缩模式',
    DescriptionLong: '流式响应的压缩模式',
    Unit: '',
    DangerLevel: 0,
    HotReload: true,
    Observability: '/api/admin/settings/compression.mode',
    ...over,
  }
}

function itemOf(over: Partial<SettingsListItem> = {}): SettingsListItem {
  return {
    key: 'compression.mode',
    env_name: 'LLM_GATEWAY_COMPRESSION_MODE',
    type: 'enum',
    scope: 'platform',
    category: 'compression',
    default: 'none',
    value: 'gzip',
    source: 'db',
    options: ['none', 'gzip'],
    min: null,
    max: null,
    description: '压缩模式',
    danger_level: 0,
    hot_reload: true,
    observability: '/api/admin/settings/compression.mode',
    ...over,
  }
}

function listOf(over: Partial<SettingsListResponse> = {}): SettingsListResponse {
  return { items: [itemOf()], ...over }
}

function detailOf(over: Partial<SettingsDetailResponse> = {}): SettingsDetailResponse {
  return { spec: specOf(), value: 'gzip', source: 'db', ...over }
}

function auditOf(over: Partial<SettingsAuditEntry> = {}): SettingsAuditEntry {
  return {
    setting_key: 'compression.mode',
    action: 'update',
    old_value: 'none',
    new_value: 'gzip',
    operator_user: 'alice',
    operator_role: 'tenant_admin',
    created_at: '2026-10-07T12:00:00.123456789Z',
    ...over,
  }
}

function historyOf(over: Partial<SettingsHistoryResponse> = {}): SettingsHistoryResponse {
  return { items: [auditOf()], ...over }
}

// ══════════════════════════════════════════════════════════════════════════
// 清单端点解包
// ══════════════════════════════════════════════════════════════════════════

describe('清单端点解包', () => {
  it('★ 满配清单被放行（15 键条目）', () => {
    expect(unwrapSettingsList(listOf())).toBeTruthy()
  })

  it('★ ★★ items 是空数组 ⇒ 放行（后端用 []map[string]any{} 初始化）', () => {
    expect(() => unwrapSettingsList(listOf({ items: [] }))).not.toThrow()
  })

  it('★ ★★ items 是 null ⇒ 抛（清单端点恒为数组）', () => {
    expect(() => unwrapSettingsList(listOf({ items: null as never }))).toThrow(/设置清单 的 items 不是数组/)
  })

  it('★ 顶层缺 items ⇒ 抛（清单端点）', () => {
    expect(() => unwrapSettingsList({})).toThrow(/设置清单 缺 1 个键（items）/)
  })

  it('★ 顶层不是对象 ⇒ 抛', () => {
    expect(() => unwrapSettingsList([])).toThrow(/设置清单 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ ★★ 条目十五个键逐个都要检查', () => {
    const keys = [
      'key', 'env_name', 'type', 'scope', 'category', 'default', 'value', 'source',
      'options', 'min', 'max', 'description', 'danger_level', 'hot_reload', 'observability',
    ]
    for (const k of keys) {
      expect(() => unwrapSettingsList(listOf({ items: [del(itemOf() as never, k) as never] }))).toThrow(
        /items\[0\] 缺 1 个键/,
      )
    }
  })

  it('★ ★★ 八个字符串键逐个都要校验', () => {
    const keys = ['key', 'env_name', 'type', 'scope', 'category', 'source', 'description', 'observability'] as const
    for (const k of keys) {
      expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), [k]: 1 } as never] }))).toThrow(
        new RegExp(`items\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★ danger_level 不是数字 ⇒ 抛（后端是 int，永不为 null）', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), danger_level: null as never }] }))).toThrow(
      /items\[0\] 的 danger_level 不是数字/,
    )
  })

  it('★ ★ hot_reload 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), hot_reload: 'yes' as never }] }))).toThrow(
      /items\[0\] 的 hot_reload 不是布尔/,
    )
  })

  it('★ options 是 null ⇒ 放行（Spec 里无 omitempty，恒在可为 null）', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), options: null }] }))).not.toThrow()
  })

  it('★ ★ options 既不是数组也不是 null ⇒ 抛', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), options: 'x' as never }] }))).toThrow(
      /items\[0\] 的 options 不是数组也不是 null/,
    )
  })

  it('★ min 与 max 是 null ⇒ 放行（*float64 无 omitempty）', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), min: null, max: null }] }))).not.toThrow()
  })

  it('★ ★ min 与 max 逐个都要校验', () => {
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), min: 'x' as never }] }))).toThrow(
      /items\[0\] 的 min 不是数字也不是 null/,
    )
    expect(() => unwrapSettingsList(listOf({ items: [{ ...itemOf(), max: 'x' as never }] }))).toThrow(
      /items\[0\] 的 max 不是数字也不是 null/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 单键端点解包（spec 是 PascalCase）
// ══════════════════════════════════════════════════════════════════════════

describe('单键端点解包', () => {
  it('★ 满配单键响应被放行', () => {
    expect(unwrapSetting(detailOf())).toBeTruthy()
  })

  it('★ spec 不是对象 ⇒ 抛', () => {
    expect(() => unwrapSetting(detailOf({ spec: [] as never }))).toThrow(/单个设置 的 spec 响应形状不符/)
  })

  // ★ 与清单条目相反：Spec 结构体**没有 json tag** ⇒ PascalCase。
  it('★ ★★ spec 的十五个 PascalCase 键逐个都要检查', () => {
    const keys = [
      'Key', 'EnvName', 'Type', 'Scope', 'Category', 'Default', 'Min', 'Max',
      'Options', 'Description', 'DescriptionLong', 'Unit', 'DangerLevel', 'HotReload', 'Observability',
    ]
    for (const k of keys) {
      expect(() => unwrapSetting(detailOf({ spec: del(specOf() as never, k) as never }))).toThrow(
        /单个设置 的 spec 缺 1 个键/,
      )
    }
  })

  it('★ ★★ spec 的九个字符串键逐个都要校验', () => {
    const keys = ['Key', 'EnvName', 'Type', 'Scope', 'Category', 'Description', 'DescriptionLong', 'Unit', 'Observability'] as const
    for (const k of keys) {
      expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), [k]: 1 } as never }))).toThrow(
        new RegExp(`spec 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★ DangerLevel 不是数字 ⇒ 抛', () => {
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), DangerLevel: null as never } }))).toThrow(
      /spec 的 DangerLevel 不是数字/,
    )
  })

  it('★ HotReload 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), HotReload: 1 as never } }))).toThrow(
      /spec 的 HotReload 不是布尔/,
    )
  })

  it('★ ★ Min 与 Max 逐个都要校验', () => {
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), Min: 'x' as never } }))).toThrow(
      /spec 的 Min 不是数字也不是 null/,
    )
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), Max: 'x' as never } }))).toThrow(
      /spec 的 Max 不是数字也不是 null/,
    )
  })

  it('★ Options 既不是数组也不是 null ⇒ 抛', () => {
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), Options: 'x' as never } }))).toThrow(
      /spec 的 Options 不是数组也不是 null/,
    )
  })

  it('★ ★ Options 是 null ⇒ 放行（Spec 里无 omitempty，恒在可为 null）', () => {
    expect(() => unwrapSetting(detailOf({ spec: { ...specOf(), Options: null } }))).not.toThrow()
  })

  it('★ source 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSetting(detailOf({ source: 1 as never }))).toThrow(/单个设置 的 source 不是字符串/)
  })

  it('★ ★ 缺 value ⇒ 抛（键恒在）', () => {
    const d = del(detailOf() as unknown as Record<string, unknown>, 'value')
    expect(() => unwrapSetting(d)).toThrow(/单个设置 缺 1 个键（value）/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 历史端点解包（items 可能是 null）
// ══════════════════════════════════════════════════════════════════════════

describe('历史端点解包', () => {
  it('★ 满配历史被放行', () => {
    expect(unwrapSettingHistory(historyOf())).toBeTruthy()
  })

  // ★★ 本族最关键的一条：与清单端点**相反**，历史端点无行时是 **null**。
  it('★ ★★★ items 是 null ⇒ 放行（ListAudit 里 var out 是 nil slice）', () => {
    expect(() => unwrapSettingHistory(historyOf({ items: null }))).not.toThrow()
  })

  it('★ ★★ items 既不是数组也不是 null ⇒ 抛', () => {
    expect(() => unwrapSettingHistory(historyOf({ items: 'x' as never }))).toThrow(
      /设置变更历史 的 items 不是数组也不是 null/,
    )
  })

  it('★ 顶层缺 items ⇒ 抛（历史端点）', () => {
    expect(() => unwrapSettingHistory({})).toThrow(/设置变更历史 缺 1 个键（items）/)
  })

  it('★ ★★ 五个恒在键逐个都要检查', () => {
    for (const k of ['setting_key', 'action', 'operator_user', 'operator_role', 'created_at']) {
      expect(() => unwrapSettingHistory(historyOf({ items: [del(auditOf() as never, k) as never] }))).toThrow(
        /items\[0\] 缺 1 个键/,
      )
    }
  })

  it('★ ★★ 四个字符串键逐个都要校验', () => {
    const keys = ['setting_key', 'action', 'operator_user', 'operator_role'] as const
    for (const k of keys) {
      expect(() => unwrapSettingHistory(historyOf({ items: [{ ...auditOf(), [k]: 1 } as never] }))).toThrow(
        new RegExp(`items\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ created_at 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSettingHistory(historyOf({ items: [{ ...auditOf(), created_at: 1 as never }] }))).toThrow(
      /items\[0\] 的 created_at 不是字符串/,
    )
  })

  it('★ ★ 四个 omitempty 键全部缺席也是合法形状', () => {
    const bare: SettingsAuditEntry = {
      setting_key: 'compression.mode',
      action: 'update',
      operator_user: 'alice',
      operator_role: 'tenant_admin',
      created_at: '2026-10-07T12:00:00Z',
    }
    expect(() => unwrapSettingHistory(historyOf({ items: [bare] }))).not.toThrow()
  })

  it('★ ★ tenant_id 存在但不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSettingHistory(historyOf({ items: [{ ...auditOf(), tenant_id: 1 as never }] }))).toThrow(
      /items\[0\] 的 tenant_id 不是字符串/,
    )
  })

  it('★ ★ client_ip 存在但不是字符串 ⇒ 抛', () => {
    expect(() => unwrapSettingHistory(historyOf({ items: [{ ...auditOf(), client_ip: 1 as never }] }))).toThrow(
      /items\[0\] 的 client_ip 不是字符串/,
    )
  })

  // ★★★ 见 (7)：同一个「空」概念有两种编码，两种都必须放行。
  it('★ ★★ old_value 键消失 与 键在但为 null 是两种编码，都放行', () => {
    const bare: SettingsAuditEntry = {
      setting_key: 'compression.mode',
      action: 'update',
      operator_user: 'alice',
      operator_role: 'tenant_admin',
      created_at: '2026-10-07T12:00:00Z',
    }
    expect(() => unwrapSettingHistory(historyOf({ items: [bare] }))).not.toThrow()
    expect(() => unwrapSettingHistory(historyOf({ items: [{ ...bare, old_value: null }] }))).not.toThrow()
    expect(() => unwrapSettingHistory(historyOf({ items: [{ ...bare, new_value: null }] }))).not.toThrow()
  })

  it('★ ★★ old_value 是任意 JSON 形状都放行（对象、数组、字符串、数字）', () => {
    for (const v of [{ a: 1 }, [1, 2], 'x', 3, true]) {
      expect(() => unwrapSettingHistory(historyOf({ items: [{ ...auditOf(), old_value: v }] }))).not.toThrow()
    }
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4) tenant 作用域 ⇒ value 为 null
// ══════════════════════════════════════════════════════════════════════════

describe('(4) tenant 作用域的值恒为 null', () => {
  it('★ ★★ tenant 条目的 value 为 null ⇒ 判为成立', () => {
    expect(settingsValueIsNullForTenantScope(itemOf({ scope: 'tenant', value: null, source: '' }))).toBe(true)
  })

  it('★ ★ tenant 条目的 value 不是 null ⇒ 不成立', () => {
    expect(settingsValueIsNullForTenantScope(itemOf({ scope: 'tenant', value: 7 }))).toBe(false)
  })

  it('★ ★ platform 条目的 value 是 null ⇒ 不成立（value 是可空的）', () => {
    expect(settingsValueIsNullIffTenantScope(itemOf({ scope: 'platform', value: null }))).toBe(false)
  })

  it('★ ★★ platform 且 value 非 null ⇒ 互锁判据成立', () => {
    expect(settingsValueIsNullIffTenantScope(itemOf({ scope: 'platform', value: 'gzip' }))).toBe(true)
  })

  it('★ ★ tenant 条目的 source 是空串 ⇒ 成立', () => {
    expect(settingsSourceIsEmptyForTenantScope(itemOf({ scope: 'tenant', value: null, source: '' }))).toBe(true)
  })

  it('★ platform 条目的 source 不是空串 ⇒ 不成立（反向）', () => {
    expect(settingsSourceIsEmptyForTenantScope(itemOf({ scope: 'platform', source: 'db' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5)(6)(10) 历史：nil、顺序、上限
// ══════════════════════════════════════════════════════════════════════════

describe('(5)(6)(10) 历史的 nil、顺序与上限', () => {
  it('★ ★★ 清单的 items 恒为数组 ⇒ 成立', () => {
    expect(settingsListItemsAreAlwaysArray(listOf({ items: [] }))).toBe(true)
  })

  it('★ ★★ 历史的 items 是 null ⇒ 判为「可能是 null」', () => {
    expect(settingsHistoryItemsMayBeNull(historyOf({ items: null }))).toBe(true)
  })

  it('★ 历史有数据时不是 null（反向）', () => {
    expect(settingsHistoryItemsMayBeNull(historyOf())).toBe(false)
  })

  it('★ ★ items 为 null 判为二义（无变更 / 行被静默跳过）', () => {
    expect(settingsHistoryNullItemsAreAmbiguous(historyOf({ items: null }))).toBe(true)
  })

  it('★ ★★ 按 created_at 降序 ⇒ 成立', () => {
    const r = historyOf({
      items: [
        auditOf({ created_at: '2026-10-07T12:00:02Z' }),
        auditOf({ created_at: '2026-10-07T12:00:01Z' }),
        auditOf({ created_at: '2026-10-06T12:00:00Z' }),
      ],
    })
    expect(settingsHistoryIsNewestFirst(r)).toBe(true)
  })

  it('★ ★ 出现时间升序 ⇒ 不成立', () => {
    const r = historyOf({
      items: [auditOf({ created_at: '2026-10-06T12:00:00Z' }), auditOf({ created_at: '2026-10-07T12:00:01Z' })],
    })
    expect(settingsHistoryIsNewestFirst(r)).toBe(false)
  })

  it('★ ★ 同秒并列（无 tiebreak）⇒ 仍判为成立', () => {
    const r = historyOf({
      items: [auditOf({ created_at: '2026-10-07T12:00:00Z' }), auditOf({ created_at: '2026-10-07T12:00:00Z' })],
    })
    expect(settingsHistoryIsNewestFirst(r)).toBe(true)
  })

  it('★ ★ items 为 null 时顺序判据返回 false（没有序可判）', () => {
    expect(settingsHistoryIsNewestFirst(historyOf({ items: null }))).toBe(false)
  })

  it('★ ★★ 条数不超过硬上限 50 ⇒ 成立', () => {
    expect(settingsHistoryIsWithinLimit(historyOf({ items: [] }))).toBe(true)
  })

  it('★ ★ 条数超过 50 ⇒ 不成立（后端不可能产出，但客户端要能发现）', () => {
    const many = Array.from({ length: 51 }, () => auditOf())
    expect(settingsHistoryIsWithinLimit(historyOf({ items: many }))).toBe(false)
  })

  it('★ items 为 null 时上限判据返回 true（无条数可越界）', () => {
    expect(settingsHistoryIsWithinLimit(historyOf({ items: null }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7)(8) omitempty 键
// ══════════════════════════════════════════════════════════════════════════

describe('(7)(8) 审计条目的 omitempty 键', () => {
  it('★ ★★ old_value 存在 ⇒ 判为存在', () => {
    expect(settingsAuditKeyIsPresent(auditOf(), 'old_value')).toBe(true)
  })

  it('★ ★ old_value 缺席（可能就是 SQL NULL 吃掉的）⇒ 判为不存在', () => {
    const bare: SettingsAuditEntry = {
      setting_key: 'k',
      action: 'update',
      operator_user: 'u',
      operator_role: 'r',
      created_at: '2026-10-07T12:00:00Z',
    }
    expect(settingsAuditKeyIsPresent(bare, 'old_value')).toBe(false)
  })

  it('★ ★★ 无 tenant_id 键 ⇒ 判为平台域审计', () => {
    const bare: SettingsAuditEntry = {
      setting_key: 'k',
      action: 'update',
      operator_user: 'u',
      operator_role: 'r',
      created_at: '2026-10-07T12:00:00Z',
    }
    expect(settingsAuditIsPlatformScoped(bare)).toBe(true)
  })

  it('★ 有 tenant_id 键 ⇒ 不判为平台域（反向）', () => {
    expect(settingsAuditIsPlatformScoped(auditOf({ tenant_id: 't-1' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (1) 写权限闸 / (3)(14)(15) 路由与形状
// ══════════════════════════════════════════════════════════════════════════

describe('(1)(3)(14)(15) 权限闸与路由形状', () => {
  it('★ ★ danger_level 2（dangerous）⇒ 写需要 super_admin', () => {
    expect(settingsWriteNeedsSuperAdmin(2)).toBe(true)
  })

  it('★ ★ danger_level 3（breaking）⇒ 写需要 super_admin', () => {
    expect(settingsWriteNeedsSuperAdmin(3)).toBe(true)
  })

  it('★ danger_level 1（warning）⇒ 不需要（反向）', () => {
    expect(settingsWriteNeedsSuperAdmin(1)).toBe(false)
  })

  it('★ ★★ 四个档位名与数值一一对应', () => {
    expect(settingsDangerLevelName(0)).toBe('safe')
    expect(settingsDangerLevelName(3)).toBe('breaking')
  })

  it('★ 越界的档位名回落为 unknown', () => {
    expect(settingsDangerLevelName(9)).toBe('unknown')
  })

  it('★ ★ unknown setting 文案 ⇒ 按前缀判为未知设置', () => {
    expect(settingsIsUnknownSettingMessage('unknown setting compression.mode')).toBe(true)
  })

  it('★ 路由不匹配的文案 ⇒ 不算未知设置（反向）', () => {
    expect(settingsIsUnknownSettingMessage(SETTINGS_UNKNOWN_ENDPOINT_MESSAGE)).toBe(false)
  })

  it('★ ★ 三段以上 ⇒ 多余段被静默忽略', () => {
    expect(settingsExtraPathSegmentsAreIgnored(['k', 'history', 'extra'])).toBe(true)
  })

  it('★ 只有两段 ⇒ 不忽略（反向）', () => {
    expect(settingsExtraPathSegmentsAreIgnored(['k', 'history'])).toBe(false)
  })

  it('★ ★ 空 key ⇒ 会进 handler（不是路由层 404）', () => {
    expect(settingsEmptyKeyReachesHandler('')).toBe(true)
  })

  it('★ 非空 key ⇒ 不进（反向）', () => {
    expect(settingsEmptyKeyReachesHandler('compression.mode')).toBe(false)
  })

  it('★ ★★ 列表条数少于注册表规模 ⇒ 判为可能不完整', () => {
    expect(settingsListMayBeIncomplete(10, 20)).toBe(true)
  })

  it('★ 条数相等 ⇒ 判为完整（反向）', () => {
    expect(settingsListMayBeIncomplete(20, 20)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★ 清单路径不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchSettingsList()
    expect(String(fetchMock.mock.calls[0]![0])).toContain(PLATFORM_SETTINGS_PATH)
    expect(String(fetchMock.mock.calls[0]![0])).not.toContain('?')
  })

  it('★ ★ category 被拼进查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchSettingsList({ category: 'compression' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('category=compression')
  })

  it('★ ★ 单键路径把 key 放在最后一段', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detailOf()))
    await fetchSetting({ key: 'compression.mode' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/settings/compression.mode')
  })

  it('★ ★ 含斜杠的 key 被编码（否则会切错路径）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detailOf()))
    await fetchSetting({ key: 'a/b' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/settings/a%2Fb')
  })

  it('★ ★★ 历史路径挂在 key 之后', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(historyOf()))
    await fetchSettingHistory({ key: 'compression.mode' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/settings/compression.mode/history')
  })

  it('★ ★★★ 端到端：items 为 null 的历史也能解包（200 不带错误）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(historyOf({ items: null })))
    const r = await fetchSettingHistory({ key: 'compression.mode' })
    expect(settingsHistoryItemsMayBeNull(r)).toBe(true)
  })

  it('★ ★ 端到端：spec 缺键 ⇒ 抛错', async () => {
    const bad = del(detailOf() as unknown as Record<string, unknown>, 'spec')
    fetchMock.mockResolvedValueOnce(jsonResponse(bad))
    await expect(fetchSetting({ key: 'k' })).rejects.toThrow(/缺 1 个键（spec）/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ ★ 503 两种文案各不相同（注册表 vs 数据库）', () => {
    expect(SETTINGS_REGISTRY_UNAVAILABLE_MESSAGE).toBe('settings registry not initialised')
    expect(SETTINGS_DB_UNAVAILABLE_MESSAGE).toBe('db not wired')
  })

  it('★ 500 文案 === query failed', () => {
    expect(SETTINGS_HISTORY_FAILED_MESSAGE).toBe('query failed')
  })

  it('★ ★★ 路由不匹配是 404 而不是 405', () => {
    expect(SETTINGS_UNKNOWN_ENDPOINT_MESSAGE).toBe('unknown settings endpoint')
  })

  it('★ ★ 未知设置文案是把 key 拼进去的前缀', () => {
    expect(SETTINGS_UNKNOWN_SETTING_PREFIX).toBe('unknown setting ')
  })

  it('★ ★ 历史窗口 7 天与上限 50', () => {
    expect(SETTINGS_HISTORY_WINDOW_DAYS).toBe(7)
    expect(SETTINGS_HISTORY_LIMIT).toBe(50)
  })

  it('★ ★ ListAudit 的钳制边界是 1 与 500', () => {
    expect(SETTINGS_AUDIT_LIMIT_MIN).toBe(1)
    expect(SETTINGS_AUDIT_LIMIT_MAX).toBe(500)
  })

  it('★ ★ 七种 ValueType 与后端一致', () => {
    expect([...SETTINGS_VALUE_TYPES]).toEqual(['enum', 'int', 'float', 'bool', 'string', 'url', 'duration'])
  })

  it('★ ★ 两种 Scope 与后端一致', () => {
    expect([...SETTINGS_SCOPES]).toEqual(['platform', 'tenant'])
    expect(SETTINGS_SCOPE_PLATFORM).toBe('platform')
    expect(SETTINGS_SCOPE_TENANT).toBe('tenant')
  })

  it('★ ★★ 十一种 Category 与后端一致', () => {
    expect([...SETTINGS_CATEGORIES]).toEqual([
      'compression',
      'rate_limit',
      'timeout',
      'routing',
      'session',
      'security',
      'circuit_breaker',
      'general',
      'integration',
      'attribution',
      'retry',
    ])
  })

  it('★ ★ 四档 danger_level 名与 super_admin 阈值 2', () => {
    expect([...SETTINGS_DANGER_LEVELS]).toEqual(['safe', 'warning', 'dangerous', 'breaking'])
    expect(SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD).toBe(2)
  })

  it('★ 三种审计 action', () => {
    expect([...SETTINGS_AUDIT_ACTIONS]).toEqual(['update', 'rollback', 'delete'])
  })

  it('★ ★★ 清单十五键是 snake_case 且含 value 与 default', () => {
    expect(SETTINGS_LIST_ITEM_KEYS.length).toBe(15)
    expect([...SETTINGS_LIST_ITEM_KEYS]).toContain('value')
    expect([...SETTINGS_LIST_ITEM_KEYS]).toContain('default')
  })

  // ★★ 同族两端点的键风格相反：清单 snake_case、spec PascalCase。
  it('★ ★★ spec 十五键是 PascalCase（Go 结构体没有 json tag）', () => {
    expect(SETTINGS_SPEC_KEYS.length).toBe(15)
    expect([...SETTINGS_SPEC_KEYS]).toEqual([
      'Key', 'EnvName', 'Type', 'Scope', 'Category', 'Default', 'Min', 'Max',
      'Options', 'Description', 'DescriptionLong', 'Unit', 'DangerLevel', 'HotReload', 'Observability',
    ])
  })

  it('★ ★ 清单键里不含 PascalCase 痕迹（两套键名不能混用）', () => {
    expect(SETTINGS_LIST_ITEM_KEYS as readonly string[]).not.toContain('Key')
    expect(SETTINGS_SPEC_KEYS as readonly string[]).not.toContain('key')
  })

  it('★ 单键响应三键', () => {
    expect([...SETTINGS_DETAIL_KEYS]).toEqual(['spec', 'value', 'source'])
  })

  it('★ ★ 审计九键与五个恒在键、四个 omitempty 键', () => {
    expect([...SETTINGS_AUDIT_KEYS]).toEqual([
      'setting_key', 'tenant_id', 'action', 'old_value', 'new_value',
      'operator_user', 'operator_role', 'client_ip', 'created_at',
    ])
    expect([...SETTINGS_AUDIT_REQUIRED_KEYS]).toHaveLength(5)
    expect([...SETTINGS_AUDIT_OPTIONAL_KEYS]).toHaveLength(4)
  })

  it('★ 路径前缀不带尾斜杠（分发靠精确路径 + 尾斜杠前缀两条注册）', () => {
    expect(PLATFORM_SETTINGS_PATH).toBe('/api/admin/settings')
  })
})