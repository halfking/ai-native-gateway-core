import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModules,
  unwrapModules,
  fetchModule,
  unwrapModule,
  fetchModuleConfig,
  moduleHasConfigEndpoint,
  moduleEnabledMayBeFallback,
  moduleEnabledWasReallyRead,
  moduleIsBlocked,
  moduleCanToggle,
  moduleMissingConfigKeys,
  moduleConfigCoverage,
  moduleHasNoSettingKey,
  moduleIsAlwaysOn,
  moduleRequiredDepNames,
  moduleDangerTone,
  moduleHasNoCapabilities,
  moduleTestHasSideEffect,
  modulesSettingsNotInitialised,
  moduleConfigKeyState,
  moduleConfigStates,
  moduleConfigStateCounts,
  moduleNullConfigKeys,
  moduleEnabledDisagrees,
  unwrapModuleConfigSummary,
  allowedUserCountMayBeEmptySplit,
  quietHoursWindowIsEmptyArtifact,
  MODULES_REAL_SOURCES,
  modulesSourceIsKnown,
  MODULE_CONFIG_SUMMARY_KEYS,
  MODULES_WITH_CONFIG_ENDPOINT,
  MODULES_FALLBACK_SOURCE,
  type ModuleWithStatus,
  type ModuleList,
  type ModuleDetail,
} from './modules'

/**
 * 功能模块面只读端的契约测试（2026-10-08）。
 *
 * 后端逐条对应：
 *   admin/modules.go:1129-1134   registerModuleRoutes（**不在** handler.go！），两条都 h.admin
 *   admin/modules.go:21-34       ModuleDefinition（integration/dependencies 带 omitempty）
 *   admin/modules.go:55-61       ModuleWithStatus（blocked_reason 带 omitempty）
 *   admin/modules.go:628-648     resolveModuleEnabled：**五条**失败路径都返回 (true,"default")
 *   admin/modules.go:731-738     can_toggle_enabled = (blocked_reason == "")
 *   admin/modules.go:739-746     dependencies 的 enabled 只在第二轮被填上
 *   admin/modules.go:776-789     list ⇒ {items}，items 永不为 null
 *   admin/modules.go:793-840     get ⇒ {module, config}；两处 continue **静默丢配置键**
 *   admin/modules.go:1136-1161   子树分派，其余一律 404 unknown modules endpoint
 *   admin/modules.go:1272-1285   /config 只有 feishu_bot，其余 **501**
 *   admin/modules.go:1169-1180   POST /test 对 feishu_bot **真发一条消息**（有副作用）
 *   settings/spec_modules.go     setting_key / config_keys 的来源
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

/** 抄自 `ModuleDefinition` + `ModuleWithStatus`（modules.go:21-61）。 */
function mod(over: Record<string, unknown> = {}): ModuleWithStatus {
  return {
    key: 'compression',
    name: '会话压缩',
    description: '智能压缩超长对话上下文',
    capabilities: ['多模式压缩'],
    icon: '🗜️',
    category: 'compression',
    setting_key: 'compression.enabled',
    config_keys: ['compression.mode', 'compression.window_fraction'],
    docs_url: '/admin/compression',
    danger_level: 'warning',
    enabled: false,
    source: 'db',
    can_toggle_enabled: true,
    ...over,
  } as ModuleWithStatus
}

/** 抄自 `GET /api/admin/modules`（modules.go:788）。 */
function listOf(items: ModuleWithStatus[] = [mod()]): ModuleList {
  return { items }
}

/**
 * ★ 抄自 `feishuBotConfigSummary`（`admin/modules.go:1327-1357`）——
 *   `summary := map[string]any{}` 上**逐个无条件赋值** ⇒ 这 20 键永远都在。
 *   ⚠️ 这里的取值**都是各 read* 的零值形态**，正是「什么都没配」时后端真会发的样子：
 *   `allowed_user_count: 1`（`Split("", ",")` 的长度）+ `quiet_hours_window: "–"`（空+空）。
 */
function configSummary(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    enabled: false,
    webhook_url_set: false,
    verify_token_set: false,
    encrypt_key_set: false,
    connection_mode: '',
    notify_on_alert: false,
    notify_on_approval: false,
    allowed_user_count: 1,
    alert_severity_min: '',
    alert_rate_limit_min: 0,
    alert_dedup_window_sec: 0,
    quiet_hours_enabled: false,
    quiet_hours_window: '–',
    card_template: '',
    approval_expiry_min: 0,
    approval_mention_crit: false,
    commands_enabled: false,
    commands_admin_only: false,
    signature_required: false,
    timestamp_window_sec: 0,
    ...over,
  }
}

/** 抄自 `GET /api/admin/modules/{key}`（modules.go:836-839）。 */
function detailOf(over: Record<string, unknown> = {}): ModuleDetail {
  return {
    module: mod(),
    config: {
      'compression.mode': { value: 'auto_threshold', source: 'db', spec: { key: 'compression.mode' } },
      'compression.window_fraction': {
        value: 0.5,
        source: 'db',
        spec: { key: 'compression.window_fraction' },
      },
    },
    ...over,
  } as ModuleDetail
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ 注册在 modules.go 自己的 registerModuleRoutes 里，档位是 admin', () => {
  it('★★★★★★ URL 形态', async () => {
    const urls: string[] = []
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchModules()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(detailOf()))
    await fetchModule('compression')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(configSummary()))
    await fetchModuleConfig('feishu_bot')
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/modules',
      '/api/admin/modules/compression',
      '/api/admin/modules/feishu_bot/config',
    ])
  })

  it('★★★★★ 模块 key 也要 encode', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(detailOf()))
    await fetchModule('a/b')
    expect(lastUrl()).toBe('/api/admin/modules/a%2Fb')
  })
})

describe('★★★★★★★ `enabled:true` 可能是「读不出来」的兜底', () => {
  it('★★★★★★★ `source==="default"` + enabled ⇒ 标成「可能是兜底」', () => {
    const m = mod({ enabled: true, source: MODULES_FALLBACK_SOURCE })
    expect(moduleEnabledMayBeFallback(m)).toBe(true)
    expect(moduleEnabledWasReallyRead(m)).toBe(false)
    expect(MODULES_FALLBACK_SOURCE).toBe('default')
  })

  it('★★★★★★ 真读到某处显式配置（`db`/`env`）⇒ 不标兜底', () => {
    const m = mod({ enabled: true, source: 'env' })
    expect(moduleEnabledMayBeFallback(m)).toBe(false)
    expect(moduleEnabledWasReallyRead(m)).toBe(true)
  })

  // ★★★ 这条是**追到 `settings/spec.go:284` 才查出来的**，我第一版把 source 写成
  //   `!== 'default'`（"不是兜底就是真读到"）—— 那在实现上恰好也成立，
  //   但**没有钉住取值集合**：任何新出现的 source 都会被判成「真读到」。
  //   ⇒ 必须逐个点名 db / env / default 三个值。
  it('★★★★★★★ `source` 的取值集合由 EffectiveValue 写死为 {db, env, default}', () => {
    expect(MODULES_REAL_SOURCES).toEqual(['db', 'env'])
    expect(modulesSourceIsKnown('db')).toBe(true)
    expect(modulesSourceIsKnown('env')).toBe(true)
    expect(modulesSourceIsKnown('default')).toBe(true)
    // ★ 我第一版夹具里编的这两个值，后端**不可能**发出来
    expect(modulesSourceIsKnown('platform')).toBe(false)
    expect(modulesSourceIsKnown('tenant')).toBe(false)
  })

  it('★★★★★★ `source:"default"` ⇒ 无论 enabled 是什么都**不算**真读到', () => {
    // ★★ `EffectiveValue` 第 3 步回落 spec 自身 Default 时也返回 "default"
    //   ⇒ enabled:false + source:"default" 同样可能是「没配」而不是「关了」
    const off = mod({ enabled: false, source: 'default' })
    expect(moduleEnabledMayBeFallback(off)).toBe(false) // 它不是「true 的兜底」
    expect(moduleEnabledWasReallyRead(off)).toBe(false) // 但也**不是**真读到
  })

  it('★★★★★★★ `setting_key` 为空 ⇒ 后端**根本没查**，直接 (true,"default")', () => {
    const m = mod({ setting_key: '', enabled: true, source: 'default' })
    expect(moduleHasNoSettingKey(m)).toBe(true)
    expect(moduleIsAlwaysOn(m)).toBe(true)
  })

  it('★★★★★ 非兜底来源时 `moduleIsAlwaysOn` 为 false', () => {
    expect(moduleIsAlwaysOn(mod({ source: 'db' }))).toBe(false)
  })

  // ★★★★ 这条是**变异 T4 逼出来的**：原实现是
  //   `setting_key === '' && moduleEnabledMayBeFallback(m)`。
  //   把它退化成 `setting_key === ''`（忽略 source）时，上面所有用例**照样全绿** ——
  //   因为没有一条**同时**满足「setting_key 为空」与「source 不是 default」。
  //   ⇒ 缺口是「两个条件从没被拆开测过」。
  it('★★★★★★★ setting_key 为空但**真读到值** ⇒ 不是「常开」（两个条件要拆开测）', () => {
    const m = mod({ setting_key: '', enabled: true, source: 'db' })
    expect(moduleHasNoSettingKey(m)).toBe(true)
    expect(moduleEnabledMayBeFallback(m)).toBe(false)
    expect(moduleIsAlwaysOn(m)).toBe(false) // ★ 退化实现会在这里放行
  })

  // ★★★★ 这条是**变异验证逼出来的**：把 `moduleEnabledWasReallyRead` 的实现退化成
  //   `m.source !== 'default'`，上面所有用例**照样全绿**（它们只喂了 db/env/default）。
  //   ⇒ 缺口是「从没喂过集合外的值」。补上。
  it('★★★★★★★ 集合外的 source ⇒ 不算「真读到」（否则退化成 !== default 也能过）', () => {
    expect(moduleEnabledWasReallyRead(mod({ source: 'platform' }))).toBe(false)
    expect(moduleEnabledWasReallyRead(mod({ source: 'tenant' }))).toBe(false)
    expect(moduleEnabledWasReallyRead(mod({ source: '' }))).toBe(false)
    // 而 db / env 必须仍然算真读到
    expect(moduleEnabledWasReallyRead(mod({ source: 'db' }))).toBe(true)
    expect(moduleEnabledWasReallyRead(mod({ source: 'env' }))).toBe(true)
  })
})

describe('★★★★★★ blocked_reason 与 can_toggle_enabled 联动', () => {
  it('★★★★★★ 无阻塞 ⇒ 键**不存在**（omitempty），不是空串', () => {
    const ok = JSON.parse(JSON.stringify(mod())) as Record<string, unknown>
    delete ok.blocked_reason
    const m = ok as unknown as ModuleWithStatus
    expect('blocked_reason' in ok).toBe(false)
    expect(moduleIsBlocked(m)).toBe(false)
    expect(moduleCanToggle(m)).toBe(true)
  })

  it('★★★★★★ 有阻塞 ⇒ 键在且以「需先启用依赖模块」开头', () => {
    const m = mod({
      blocked_reason: '需先启用依赖模块: 会话缓存、任务模式',
      can_toggle_enabled: false,
    })
    expect(moduleIsBlocked(m)).toBe(true)
    expect(moduleCanToggle(m)).toBe(false)
    expect(m.blocked_reason).toContain('需先启用依赖模块')
  })

  it('★★★★★ 空串当**没有**阻塞（omitempty 本来也不会发空串）', () => {
    expect(moduleIsBlocked(mod({ blocked_reason: '' }))).toBe(false)
  })
})

describe('★★★★★★★ config 里的键会静默缺失', () => {
  it('★★★★★★★ 声明了两个、只取到一个 ⇒ 差集就是「读不出来的那部分」', () => {
    const d = detailOf({
      config: {
        'compression.mode': { value: 'auto_threshold', source: 'db', spec: {} },
      },
    })
    expect(moduleMissingConfigKeys(d)).toEqual(['compression.window_fraction'])
    expect(moduleConfigCoverage(d)).toEqual({ declared: 2, resolved: 1 })
  })

  it('★★★★★★ 全部取到 ⇒ 差集为空', () => {
    expect(moduleMissingConfigKeys(detailOf())).toEqual([])
    expect(moduleConfigCoverage(detailOf())).toEqual({ declared: 2, resolved: 2 })
  })

  it('★★★★★★ `config` 为 `{}`（无键）也是**合法响应**，不是 null', () => {
    const d = detailOf({ config: {} })
    expect(d.config).toEqual({})
    expect(moduleMissingConfigKeys(d)).toHaveLength(2)
  })

  it('★★★★★ `config_keys` 为空数组时差集也为空', () => {
    const d = detailOf({ module: mod({ config_keys: [] }), config: {} })
    expect(moduleMissingConfigKeys(d)).toEqual([])
    expect(moduleConfigCoverage(d)).toEqual({ declared: 0, resolved: 0 })
  })
})

describe('★★★★★★ `/config` 子端点只有 feishu_bot', () => {
  it('★★★★★★ 只有 feishu_bot 有，其余一律 501', () => {
    expect(moduleHasConfigEndpoint('feishu_bot')).toBe(true)
    expect(moduleHasConfigEndpoint('compression')).toBe(false)
    expect(MODULES_WITH_CONFIG_ENDPOINT).toEqual(['feishu_bot'])
  })

  it('★★★★★★ 其它模块 ⇒ **501** `config endpoint not implemented for module: <key>`', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: 'config endpoint not implemented for module: compression' } }, 501),
    )
    await expect(fetchModuleConfig('compression')).rejects.toThrow(/not implemented/)
  })
})

describe('★★★★★★ 形状与错误态', () => {
  // ★★★ 这条是**变异 T12 逼出来的**：把 `Array.isArray(m.items)` 换成
  //   `m.items !== undefined` 时，原来那条用例**照样全绿** ——
  //   裸数组被外层 `!Array.isArray(resp)` 先挡掉，`{}` 也没有 items 键。
  //   ⇒ 缺口是「键在但类型不对」这种形状**从没喂过**。
  it('★★★★★★★ `items` 键在但**不是数组** ⇒ 抛错（键存在性不是充分条件）', () => {
    expect(() => unwrapModules({ items: 'not-an-array' })).toThrow(/形状不符/)
    expect(() => unwrapModules({ items: null })).toThrow(/形状不符/)
    expect(() => unwrapModules({ items: 42 })).toThrow(/形状不符/)
  })

  it('★★★★★★ list 是 `{items}`；裸数组 ⇒ 抛错', () => {
    expect(() => unwrapModules(listOf())).not.toThrow()
    expect(() => unwrapModules([mod()])).toThrow(/形状不符/)
    expect(() => unwrapModules(null)).toThrow(/形状不符/)
    expect(() => unwrapModules({})).toThrow(/形状不符/)
  })

  it('★★★★★★ detail 是 `{module, config}`；缺 config ⇒ 抛错', () => {
    expect(() => unwrapModule(detailOf())).not.toThrow()
    expect(() => unwrapModule({ module: mod() })).toThrow(/形状不符/)
    expect(() => unwrapModule({ config: {} })).toThrow(/形状不符/)
  })

  it('★★★★★★ 两个形状互不包含 ⇒ 互喂抛错', () => {
    expect(() => unwrapModule(listOf())).toThrow(/形状不符/)
    expect(() => unwrapModules(detailOf())).toThrow(/形状不符/)
  })

  it('★★★★★ 404 `unknown module: <key>`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'unknown module: nope' } }, 404))
    await expect(fetchModule('nope')).rejects.toThrow(/unknown module/)
  })

  it('★★★★★ 404 `unknown modules endpoint`（分派不匹配）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'unknown modules endpoint' } }, 404))
    await expect(fetchModule('compression/whatever')).rejects.toThrow(/unknown modules endpoint/)
  })

  it('★★★★★★ 503 `settings registry not initialised`（不是角色问题）', async () => {
    const msg = 'settings registry not initialised'
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: msg } }, 503))
    await expect(fetchModules()).rejects.toThrow(msg)
    expect(modulesSettingsNotInitialised(msg)).toBe(true)
  })

  it('★★★★★ 503 的**两种文案**都能识别', () => {
    expect(modulesSettingsNotInitialised('settings registry not initialised')).toBe(true)
    expect(modulesSettingsNotInitialised('settings not initialised')).toBe(true)
    expect(modulesSettingsNotInitialised('database not configured')).toBe(false)
  })
})

describe('★★ 其它判读', () => {
  it('★★ 只列**必需**依赖（后端 blocked_reason 也只统计 required 的）', () => {
    const m = mod({
      dependencies: [
        { key: 'a', name: 'A', required: true },
        { key: 'b', name: 'B', required: false },
      ],
    })
    expect(moduleRequiredDepNames(m)).toEqual(['A'])
    expect(moduleRequiredDepNames(mod())).toEqual([])
  })

  it('★★ danger_level 分档；枚举外的按未知', () => {
    expect(moduleDangerTone('danger')).toBe('danger')
    expect(moduleDangerTone('warning')).toBe('warning')
    expect(moduleDangerTone('info')).toBe('muted')
    expect(moduleDangerTone('')).toBe('muted')
  })

  it('★★ capabilities 为空数组', () => {
    expect(moduleHasNoCapabilities(mod({ capabilities: [] }))).toBe(true)
    expect(moduleHasNoCapabilities(mod())).toBe(false)
  })

  it('★★★★★ `POST /{key}/test` 有外部副作用 ⇒ 标记出来，本页绝不调用', () => {
    // ★ 它名字叫 test，但后端真给飞书机器人发消息 ⇒ 不是只读端点
    expect(moduleTestHasSideEffect()).toBe(true)
  })
})

describe('★★★★★★ 配置键是**三**态，不是两态', () => {
  // ★★★ 复核 `handleModulesGet`（modules.go:816-833）查到的：
  //   那个循环**没有** `raw == nil` 判断（对比 `resolveModuleEnabled` 是有的），
  //   所以值为 NULL 的键**不进 continue**，而是 Decode 失败、`v` 保持 nil，
  //   **键照样进 config** ⇒ `value: null` 是第三种状态。
  it('★★★★★★ 缺键 / value 为 null / 真取到 —— 三态互不合并', () => {
    const d = detailOf({
      config: {
        'compression.mode': { value: 'auto_threshold', source: 'db', spec: {} },
        'compression.window_fraction': { value: null, source: 'db', spec: {} },
      },
    })
    // 第 1 态：声明了但**键整个不在** ⇒ spec 不存在 或 EffectiveValue 报错
    expect(moduleConfigKeyState(d, 'compression.nope')).toBe('absent')
    // 第 2 态：键在但 value 是 null
    expect(moduleConfigKeyState(d, 'compression.window_fraction')).toBe('null-value')
    // 第 3 态
    expect(moduleConfigKeyState(d, 'compression.mode')).toBe('resolved')

    const s = moduleConfigStates(d)
    expect(s.absent).toEqual([])
    expect(s.nullValue).toEqual(['compression.window_fraction'])
    expect(s.resolved).toEqual(['compression.mode'])
    expect(moduleConfigStateCounts(d)).toEqual({ declared: 2, absent: 0, nullValue: 1, resolved: 1 })
  })

  it('★★★★★★ ★ `value: null` **不许**被并进「缺键」里（旧判据的洞）', () => {
    const d = detailOf({ config: { 'compression.mode': { value: null, source: 'db', spec: {} } } })
    // moduleMissingConfigKeys 只认「键不在」⇒ 它看不到 null-value 这一态
    expect(moduleMissingConfigKeys(d)).toEqual(['compression.window_fraction'])
    // 但 null-value 键**确实在 config 里**，所以覆盖率算它「已取到」
    expect(moduleConfigCoverage(d)).toEqual({ declared: 2, resolved: 1 })
    // ⇒ 同一个键：覆盖率说「取到了」，缺键表说「没取到」，而它其实是**值为空**
    expect(moduleNullConfigKeys(d)).toEqual(['compression.mode'])
    expect(moduleConfigStateCounts(d)).toEqual({ declared: 2, absent: 1, nullValue: 1, resolved: 0 })
  })

  it('★★★★★ 全部三态各自为空的情形', () => {
    const d = detailOf({ module: mod({ config_keys: [] }), config: {} })
    expect(moduleConfigStates(d)).toEqual({ absent: [], nullValue: [], resolved: [] })
    expect(moduleConfigStateCounts(d)).toEqual({ declared: 0, absent: 0, nullValue: 0, resolved: 0 })
  })
})

describe('★★★★★★ feishu_bot 的 /config 摘要：20 键扁平对象', () => {
  it('★★★★★★ 一个信封都没有，就是那 20 个键', () => {
    expect(MODULE_CONFIG_SUMMARY_KEYS).toHaveLength(20)
    expect(unwrapModuleConfigSummary(configSummary())).toBeTruthy()
    // 形状互喂：detail 的 `{module, config}` 信封喂给它 ⇒ 抛错
    expect(() => unwrapModuleConfigSummary(detailOf())).toThrow(/形状不符/)
    expect(() => unwrapModuleConfigSummary(listOf())).toThrow(/形状不符/)
    expect(() => unwrapModuleConfigSummary(null)).toThrow(/形状不符/)
    expect(() => unwrapModuleConfigSummary(configSummary())).not.toThrow()
  })

  // ★★ 形状互喂只区分**不同形状**；同一形状内部少一个键它一个都抓不到
  //   ⇒ 必须为每个必填键各钉一条「缺它必抛错」（这里用前 3 个 + 抽查尾部 1 个）。
  it.each([
    'enabled',
    'webhook_url_set',
    'verify_token_set',
    'encrypt_key_set',
    'connection_mode',
    'allowed_user_count',
    'quiet_hours_window',
    'signature_required',
    'timestamp_window_sec',
  ])('★★★★★★ 缺 `%s` 必抛错（20 键是逐个无条件赋值的）', (k) => {
    const s = configSummary()
    delete s[k]
    expect(() => unwrapModuleConfigSummary(s)).toThrow(/形状不符/)
  })
})

describe('★★★★★★ 配置摘要里的两个**零值产物**', () => {
  it('★★★★★★★★ `allowed_user_count: 1` 可能只是 `Split("", ",")` 的长度', () => {
    // 后端：len(strings.Split(readString("feishu_bot.allowed_users"), ","))
    // readString 的四条失败路径全返回 "" ⇒ Split("", ",") = [""] ⇒ 长度 1
    expect(allowedUserCountMayBeEmptySplit(1)).toBe(true)
    expect(allowedUserCountMayBeEmptySplit(0)).toBe(false)
    expect(allowedUserCountMayBeEmptySplit(2)).toBe(false)
    // ★ 夹具里「什么都没配」时后端真发的就是 1
    expect(configSummary().allowed_user_count).toBe(1)
  })

  it('★★★★★★★ `quiet_hours_window` 两端空时是字面量 `"–"`，看着像有值', () => {
    // 后端：readString(start) + "–" + readString(end)
    expect(quietHoursWindowIsEmptyArtifact('–')).toBe(true)
    expect(quietHoursWindowIsEmptyArtifact('-')).toBe(true)
    expect(quietHoursWindowIsEmptyArtifact('22:00–07:00')).toBe(false)
    expect(configSummary().quiet_hours_window).toBe('–')
  })
})

describe('★★★★★★★★ 跨端点自相矛盾：同一设置键，两处能对不上', () => {
  it('★★★★★★★★ 模块面 fallback 到 true、配置摘要 fallback 到 false', () => {
    // resolveModuleEnabled 的五条失败路径 ⇒ (true, "default")
    // readBool 的失败路径           ⇒ false（Go 零值）
    const m = mod({ key: 'feishu_bot', enabled: true, source: MODULES_FALLBACK_SOURCE })
    const summary = configSummary() // enabled: false
    expect(m.enabled).toBe(true)
    expect(summary.enabled).toBe(false)
    // ⇒ 两个端点对**同一个** `feishu_bot.enabled` 给出相反答案
    expect(moduleEnabledDisagrees(m.enabled, summary.enabled as boolean)).toBe(true)
  })

  it('★★★★★ 真读到了值时两处一致', () => {
    const m = mod({ key: 'feishu_bot', enabled: true, source: 'env' })
    const summary = configSummary({ enabled: true })
    expect(moduleEnabledDisagrees(m.enabled, summary.enabled as boolean)).toBe(false)
  })
})