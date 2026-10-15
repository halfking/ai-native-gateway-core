import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import RequestAnomaliesView from './RequestAnomaliesView.vue'
import ErrorsTrendView from './ErrorsTrendView.vue'
import ModelHistoryView from './ModelHistoryView.vue'
import SlidingWindowView from './SlidingWindowView.vue'
import ModelIQView from './ModelIQView.vue'
import OpsOverviewView from './OpsOverviewView.vue'
import RequestActionsView from './RequestActionsView.vue'
import SubscriptionTiersView from './SubscriptionTiersView.vue'
import PlatformSettingsView from './PlatformSettingsView.vue'
import UsageEnhancedView from './UsageEnhancedView.vue'
import DataLifecycleStorageView from './DataLifecycleStorageView.vue'
import StorageAndLogConfigView from './StorageAndLogConfigView.vue'
import StorageMigrationStateView from './StorageMigrationStateView.vue'
import TurnsFilterOptionsView from './TurnsFilterOptionsView.vue'
import SessionAnalyticsFilterOptionsView from './SessionAnalyticsFilterOptionsView.vue'

import { setTransport, resetTransport } from '@/api/transport'
import { zhCN } from '@/i18n/zh-CN'

/**
 * 全部带 `meta.requiresRole` 的路由：**错角色深链时的内容兜底**全族普查
 * （2026-10-16，第一百三十五批）。
 *
 * ★★★★★ 本批接着批 134 的线继续往下量。批 134 量化掉的是壳层缺口的
 *   **页面名泄露**（`document.title`）。本批问的是**第二样东西：内容** ——
 *   「无权限用户深链打开一个受限页时，到底会不会把请求发出去」。
 *
 * ★★★★★★ **不 mock api 模块，改 mock 传输接缝。**
 *   第一版想把 15 个 api 模块的导出函数统统换成 `vi.fn` 来数调用 ——
 *   ★★ 那个设计**有一个会毁掉全部读数的坑**：`allowed` 用的正是这些模块里
 *   导出的 gate helper（如 `anomalyRoleIsSuperAdmin`），而「凡导出函数
 *   一律换成 vi.fn」会把 **gate helper 自己**也换成恒返回 `{}` 的桩
 *   ⇒ `allowed` 恒真 ⇒ 「零请求」不是因为兜底，而是因为门被量具自己拆了。
 *   ⇒ 改从 `api/transport` 的 `setTransport` 接缝计数：`client.ts` 的**唯一**
 *     传输出口就在那里（`client.ts:252` 的 `getTransport()({...})`），
 *     15 个模块 15/15 都走 `./client`（本批实测）⇒ **client、api 模块、
 *     gate helper、视图全部真跑**，只有最底层换成计数器。
 *
 * ★★★★★★ **第一版的正控把它自己否掉了 —— 这是本文件最要紧的一段。**
 *   v1 测的是「错角色挂载后零请求」，15/15 全绿，读数漂亮。
 *   但 ③ 正控（放行角色挂载同一个视图）跑出 **14/15 一个请求都没发** ——
 *   ⇒ 「零」根本不是兜底挡的：**那 14 个视图压根不在挂载时取数**，
 *     要用户点查询按钮才会发。⇒ v1 的 ② 是**空门**：它对「兜底被删掉」
 *     与「兜底被删掉且这个视图本来就不自动取数」**给出同样的读数**。
 *   ⇒ 修法：③ 的正控改成**点该席的取数按钮**，并且 ② 增加一条更强的读数：
 *     **无权限时那个按钮压根不在 DOM 里** ⇒ 不是「点了没反应」，
 *     而是**结构上无法把请求发出去**。
 *
 * ★★★★ 判据分六组：
 *   ① ★★★★★★ 召回自证：路由里带 `requiresRole` 的声明**逐条**与本文件
 *     的表配对（**不是只比数量** —— 第一百三十一批实测过数量对账能让
 *     3 条串线的普查绿掉）。
 *   ② ★★★★★★ 请求层：错角色挂载 15 个视图，**零请求**。
 *   ③ ★★★★★★★★ 正控（探针有牙）：放行角色下点取数按钮，**必须**发请求。
 *   ④ ★★★★★★ 结构层：错角色时取数按钮**不在 DOM**。
 *   ⑤ ★★★★ DOM 层：不露出裸 i18n 键。
 *   ⑥ ★★★ 表不许漂移：每个视图的 `allowed` 必须调表里登记的那个 gate。
 *
 * ⚠️ 本文件**不碰**各视图自己的业务判据（那些在各自的 `*View.spec.ts`），
 *   权限与业务不变量分开钉，免得互相拖红。
 *
 * ★★★★★ **读数的宽度**（阴性/阳性结论都要说清边界，见下）：
 *   传输桩固定返回 `{}`，所以一个视图里**链式**取数的第二个请求可能因为
 *   响应形状校验失败而不发。实测：`/usage-enhanced` 的 `load()` 连调
 *   `fetchCacheEconomics()` 与 `fetchPeriodCompare()`（UsageEnhancedView.vue:81-82），
 *   本组只量到 **1** 次 —— 第二个是 `{}` 校验不过抛错、被 catch 吞掉的。
 *   ⇒ **③ 只要求「≥1 次」，不要求「等于真实次数」**；
 *     **② 的 0 不受这条限制**：计数器打在 `getTransport()` 调用那一刻
 *     （`client.ts:252`），请求只要发出去就一定被记下 ⇒ ② 的 0 是**真 0**。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

// ⚠️ mock 掉整个 auth store（与批 130 同）：本组要反复换角色，
//   而 pinia 的 `role` 是 computed，换 store 实例比换普通对象绕。
const A = vi.hoisted(() => ({
  role: 'user' as string | null,
  isAuthenticated: true,
  userInfo: null as unknown,
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => A }))

/**
 * ★ 本表是**判据的输入**，也是 ① 拿去与路由逐条对账的那一份。
 *   `trigger` = 该席的取数按钮 testid；`fill` = 正控点击前要先填的表单项。
 *   ⚠️ `okRole` 只取**争议之外**的角色：`/sliding-window` 的 `admin_key`
 *     口径尚未由属主拍板（席 `super_admin` / 视图放行 `admin_key` /
 *     服务端无下限），所以这里一律用 `super_admin` ——
 *     ★ 无论属主最后怎么裁，`super_admin` 都是放行的 ⇒ 判据不会因为
 *       那条未决口径而变红，也不需要改。
 *   ⚠️ `trigger: null` 只有一条（`/request-anomalies`）：它**挂载即取数**，
 *     本就没有查询按钮。正控对它看的是「挂载就发出请求」。
 *   ★★ `fill` 不是可有可无的润色：mh / sw / ra 三席的取数按钮带
 *     `:disabled="loading || !paramsOk"`，参数不合法时按钮**禁用**，
 *     `trigger('click')` 不会触发 handler（实测：v2 首轮这三席正控失败）。
 *     ⇒ 正控必须先把参数填成合法值，否则量到的是「按钮禁用」而不是「兜底失效」。
 */
const TABLE = [
  { path: '/request-anomalies', comp: RequestAnomaliesView, file: 'RequestAnomaliesView.vue', gate: 'anomalyRoleIsSuperAdmin', tier: 'super_admin', okRole: 'super_admin', nope: 'ra-nope', trigger: null, fill: [] },
  { path: '/errors-trend', comp: ErrorsTrendView, file: 'ErrorsTrendView.vue', gate: 'errorsTrendRoleIsAdminTier', tier: 'admin', okRole: 'tenant_admin', nope: 'et-nope', trigger: 'et-load', fill: [] },
  { path: '/model-history', comp: ModelHistoryView, file: 'ModelHistoryView.vue', gate: 'modelHistoryRoleIsAdminTier', tier: 'admin', okRole: 'tenant_admin', nope: 'mh-nope', trigger: 'mh-load', fill: [['mh-credentialId', '1'], ['mh-rawModel', 'gpt-4']] },
  { path: '/sliding-window', comp: SlidingWindowView, file: 'SlidingWindowView.vue', gate: 'slidingWindowRoleIsPrivileged', tier: 'super_admin', okRole: 'super_admin', nope: 'sw-nope', trigger: 'sw-load', fill: [['sw-credentialId', '1'], ['sw-model', 'gpt-4']] },
  { path: '/model-iq', comp: ModelIQView, file: 'ModelIQView.vue', gate: 'modelIQRoleIsAllowed', tier: 'super_admin', okRole: 'super_admin', nope: 'miq-nope', trigger: 'miq-load-all', fill: [] },
  { path: '/ops-overview', comp: OpsOverviewView, file: 'OpsOverviewView.vue', gate: 'opsOverviewRoleIsAllowed', tier: 'super_admin', okRole: 'super_admin', nope: 'opsv-nope', trigger: 'opsv-load', fill: [] },
  { path: '/request-actions', comp: RequestActionsView, file: 'RequestActionsView.vue', gate: 'requestActionsRoleIsAllowed', tier: 'super_admin', okRole: 'super_admin', nope: 'ra-nope', trigger: 'ra-load', fill: [['ra-request-id', 'req-1']] },
  { path: '/subscription-tiers', comp: SubscriptionTiersView, file: 'SubscriptionTiersView.vue', gate: "role.value === 'super_admin'", tier: 'super_admin', okRole: 'super_admin', nope: 'sts-nope', trigger: 'sts-load', fill: [] },
  { path: '/platform-settings', comp: PlatformSettingsView, file: 'PlatformSettingsView.vue', gate: "role.value === 'super_admin'", tier: 'super_admin', okRole: 'super_admin', nope: 'ps-nope', trigger: 'ps-load', fill: [] },
  { path: '/usage-enhanced', comp: UsageEnhancedView, file: 'UsageEnhancedView.vue', gate: '!usageRoleNeedsAdminGate', tier: 'admin', okRole: 'tenant_admin', nope: 'ue-nope', trigger: 'ue-load', fill: [] },
  { path: '/data-lifecycle-storage', comp: DataLifecycleStorageView, file: 'DataLifecycleStorageView.vue', gate: '!storageOverviewRoleNeedsAdminGate', tier: 'admin', okRole: 'tenant_admin', nope: 'dls-nope', trigger: 'dls-load', fill: [] },
  { path: '/storage-log-config', comp: StorageAndLogConfigView, file: 'StorageAndLogConfigView.vue', gate: "role.value === 'super_admin'", tier: 'super_admin', okRole: 'super_admin', nope: 'slc-nope', trigger: 'slc-load', fill: [] },
  { path: '/storage-migration-state', comp: StorageMigrationStateView, file: 'StorageMigrationStateView.vue', gate: '!migrationStateRoleNeedsAdminGate', tier: 'admin', okRole: 'tenant_admin', nope: 'sms-nope', trigger: 'sms-load', fill: [] },
  { path: '/turns-filter-options', comp: TurnsFilterOptionsView, file: 'TurnsFilterOptionsView.vue', gate: '!turnsFilterOptionsRoleNeedsAdminGate', tier: 'admin', okRole: 'tenant_admin', nope: 'tfo-nope', trigger: 'tfo-load', fill: [] },
  { path: '/session-analytics-filter-options', comp: SessionAnalyticsFilterOptionsView, file: 'SessionAnalyticsFilterOptionsView.vue', gate: '!sessionAnalyticsFilterOptionsRoleNeedsAdminGate', tier: 'admin', okRole: 'tenant_admin', nope: 'saf-nope', trigger: 'saf-load', fill: [] },
] as const

/**
 * ★★★ 剥掉注释，只留代码。
 *   ★★★★★ 顺序必须是**先删行注释、再删块注释**（第一百三十二批实测：
 *   反过来时 `appNav.ts` 行注释里的 `/api/maas/**` 会贡献一个**伪块注释起点**，
 *   把后面 450 行真实代码整段当注释吃掉 —— 30924 字节只剩 6118）。
 *   本批 ①-0 就是钉这个顺序的，不靠注释提醒。
 */
function stripComments(src: string): string {
  const noLine = src.replace(/\/\/[^\n]*/g, '')
  return noLine.replace(/\/\*[\s\S]*?\*\//g, '')
}

function routerSrc(): string {
  return readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
}

/** ★ 字典反查：按点分路径取值，取不到就是「字典里没有这个键」。 */
function dictHas(key: string): boolean {
  let cur: unknown = zhCN
  for (const seg of key.split('.')) {
    if (typeof cur !== 'object' || cur === null) return false
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur !== undefined
}

/**
 * ★★★ 从路由源码里**独立地**抽出「path / component / requiresRole」三条。
 *   ★★★ 切分用的是**按 `path:` 分段**而不是一条跨字段的大正则 ——
 *   第一百三十一批实测过：跨字段正则里的 `[\s\S]{0,N}` 会在 N 够大时
 *   跳过本路由的 `component`、配到**下一条**路由的上去（那次 15 条串 3 条）。
 *   ⇒ 按 `path:` 切段后「同段取三字段」**结构上不可能**配错邻。
 */
function extractRestricted(): { path: string; comp: string; role: string }[] {
  const stripped = stripComments(routerSrc())
  const segs = stripped.split(/\n\s*path:\s*'/)
  const out: { path: string; comp: string; role: string }[] = []
  for (const seg of segs.slice(1)) {
    const path = seg.slice(0, seg.indexOf("'"))
    if (!/requiresRole:\s*'/.test(seg)) continue
    const comp = /component:\s*\(\)\s*=>\s*import\('@\/views\/([^']+)'\)/.exec(seg)?.[1]
    const role = /requiresRole:\s*'([^']+)'/.exec(seg)?.[1]
    // ★ 抽不出就抛，而不是静默跳过：跳过会让数量对账变成「两边都少一条」。
    if (!comp || !role) throw new Error(`抽不出 component/role：${path}`)
    out.push({ path, comp, role })
  }
  return out
}

// ── 传输层计数器 ─────────────────────────────────────────────────────────────

let calls: string[] = []

beforeEach(() => {
  calls = []
  setTransport(async (req) => {
    calls.push(req.path)
    // ★ 返回 `{}`：本组只关心**有没有发**，不关心响应形状。
    //   各视图自己的响应形状判据在它们自己的 spec 里。
    return { status: 200, statusText: 'OK', ok: true, text: async () => '{}' }
  })
})

afterEach(() => {
  resetTransport()
})

type Row = (typeof TABLE)[number]

async function mountAs(row: Row, role: string) {
  A.role = role
  const w = mount(row.comp as never)
  await flushPromises()
  await flushPromises()
  return w
}

/**
 * ★★ 正控专用：把该席的查询参数填成合法值。
 *   mh / sw / ra 三席的按钮带 `:disabled="loading || !paramsOk"`，不填就点不动
 *   （实测 v2 首轮这三席正控失败 —— 量到的是「按钮禁用」不是「兜底失效」）。
 */
async function fillParams(row: Row, w: ReturnType<typeof mount>): Promise<void> {
  for (const [id, val] of row.fill) {
    const el = w.find(`[data-testid="${id}"]`)
    if (!el.exists()) throw new Error(`${row.file} 填 ${id} 失败：控件不在 DOM 里`)
    await el.setValue(val)
  }
  await flushPromises()
}

// ── ① 召回自证 ───────────────────────────────────────────────────────────────

describe('① ★★★★★★ 召回自证：本表与路由里的 requiresRole 声明逐条配对', () => {
  it('①-0 ★★★★★★ stripComments 的剥离顺序：先删行注释，再删块注释', () => {
    // ★★★ 这个夹具是**分支条件**，不是示意 —— 第一版（只有两行、后面没有
    //   真正的 `*/`）在**两种**剥离顺序下都通过 ⇒ 它是**恒真的假门**，
    //   而变异 M05（把顺序换回「先块后行」）实测全绿，正是它没咬出来的。
    //   真正的差异只在**后面存在一个真 `*/`** 时才出现：先删块注释时，
    //   行注释里那个 `/**` 被当成块注释起点，一路吃到下一个真 `*/`，
    //   把夹在中间的**真实代码**整段吃掉（第一百三十二批 `appNav.ts` 的原形）。
    //   ⇒ 所以第三行那句真块注释**不能删**，删了这条判据就废了。
    const trap = ['const a = 1 // 路径 /api/maas/**', 'const 真实代码 = a + 1', '/* 真的块注释 */'].join('\n')
    expect(stripComments(trap), '真实代码被伪块注释起点吃掉了（剥离顺序反了）').toContain('const 真实代码')
    expect(stripComments(trap)).not.toContain('/api/maas')
    // 反向也钉：块注释**确实**会被删（否则这个函数是恒等函数，假门）。
    expect(stripComments('/* 注释 */ const 留下 = 1')).not.toContain('注释')
    expect(stripComments('/* 注释 */ const 留下 = 1')).toContain('留下')
  })

  it('①-1 ★★★★★★ 数量对账：路由里带 requiresRole 的条数 == 本表的行数', () => {
    const found = extractRestricted()
    // ★★★ 先把读数**打出来**：阴性结论必须带读数，不能只写一句「对上了」。
    console.log(
      `[①-1] 路由 requiresRole 声明 ${found.length} 条 / 本表 ${TABLE.length} 行\n  ` +
        found.map((f) => `${f.path}:${f.role}`).join('  '),
    )
    expect(found.length, '路由里的 requiresRole 条数变了，本表没跟上').toBe(TABLE.length)
    expect(TABLE.length).toBe(15)
  })

  it('①-2 ★★★★★★★ 逐条配对：path / 档位 / 组件文件三项全对', () => {
    // ★★ 数量对账 ≠ 配对正确（第一百三十一批实测：数量对上了仍有 3 条串线）。
    const found = extractRestricted()
    const byPath = new Map(found.map((f) => [f.path, f]))
    const problems: string[] = []
    for (const row of TABLE) {
      const got = byPath.get(row.path)
      if (!got) {
        problems.push(`${row.path} 路由里没有这条声明`)
        continue
      }
      if (got.role !== row.tier) problems.push(`${row.path} 档位 ${got.role} ≠ 表里 ${row.tier}`)
      if (got.comp !== row.file) problems.push(`${row.path} 组件 ${got.comp} ≠ 表里 ${row.file}`)
    }
    for (const got of found) {
      if (!TABLE.some((r) => r.path === got.path)) problems.push(`${got.path} 路由里有、本表里没有（漏登记）`)
    }
    expect(problems, '逐条配对失败：\n' + problems.join('\n')).toEqual([])
  })
})

// ── ② 请求层兜底（挂载阶段） ─────────────────────────────────────────────────

describe('② ★★★★★★ 错角色深链：挂载阶段一个请求都不发', () => {
  it('②-1 ★★★★★★★★ 15 个受限视图、user 角色（对全部档位都无权）', async () => {
    const report: string[] = []
    const bad: string[] = []
    for (const row of TABLE) {
      calls = []
      const w = await mountAs(row, 'user')
      report.push(`${row.path.padEnd(30)} 挂载 ${calls.length} 次`)
      if (calls.length > 0) bad.push(`${row.path} 挂载就发了 ${calls.length} 次：${calls.join(', ')}`)
      if (!w.find(`[data-testid="${row.nope}"]`).exists()) bad.push(`${row.path} 没渲染 nope 区`)
    }
    console.log(`[②-1] user 角色 · 挂载阶段：\n  ${report.join('\n  ')}`)
    expect(bad, '挂载阶段就发请求了：\n' + bad.join('\n')).toEqual([])
    // ★ 覆盖面证据：确实量到了 15 个视图，而不是量到 3 个。
    expect(report).toHaveLength(15)
  })
})

// ── ③ 正控：探针有牙 ─────────────────────────────────────────────────────────

describe('③ ★★★★★★★★ 正控：放行角色下点取数按钮必须发请求', () => {
  it('③-1 ★★★★★★★★ 15/15 放行角色点按钮都发出了请求 ⇒ ② 的「零」是兜底挡的', async () => {
    const report: string[] = []
    const silent: string[] = []
    let autoFire = 0
    for (const row of TABLE) {
      calls = []
      const w = await mountAs(row, row.okRole)
      const onMount = calls.length
      if (onMount > 0) autoFire++
      if (row.trigger) {
        await fillParams(row, w)
        const btn = w.find(`[data-testid="${row.trigger}"]`)
        if (!btn.exists()) {
          silent.push(`${row.path} 放行角色下连取数按钮 ${row.trigger} 都不在 DOM 里`)
          continue
        }
        await btn.trigger('click')
        await flushPromises()
        await flushPromises()
      }
      report.push(
        `${row.path.padEnd(30)} 挂载 ${String(onMount).padStart(2)} + 点击 ${String(calls.length - onMount).padStart(2)} = ${String(calls.length).padStart(2)} 次  ${calls[0] ?? ''}`,
      )
      if (calls.length === 0) silent.push(`${row.path} 点了按钮也没发请求 ⇒ ② 的 0 说明不了任何事`)
    }
    console.log(
      `[③-1] 放行角色 · 点取数按钮：\n  ${report.join('\n  ')}\n  ★ 挂载即取数的席位：${autoFire}/15`,
    )
    // ★★★ 这是**整份文件承重的一条**：v1 版本它在**挂载**阶段跑，14/15 失败
    //   —— 正是它揭穿了「零请求」是空门。删掉它，② 就退化成装饰。
    expect(silent, '正控失败：\n' + silent.join('\n')).toEqual([])
    expect(report).toHaveLength(15)
  })

  it('③-2 ★★★★ 读数留档：15 席里只有 1 席挂载即取数（其余要用户点按钮）', () => {
    // ★ 这条不是断言行为，而是把**风险面**钉住：挂载即取数的那几席才需要在
    //   `load()` 首行 fail-closed；只靠按钮触发的那些，兜底主要靠「不渲染」。
    //   ⚠️ 数字变了要改这里，所以它断言的是**结构**而不是这个具体值。
    const auto = TABLE.filter((r) => r.path === '/request-anomalies')
    expect(auto, '自动取数席位的集合变了，本文件的读数要重测').toHaveLength(1)
  })
})

// ── ④ 结构层：按钮压根不在 DOM ───────────────────────────────────────────────

describe('④ ★★★★★★ 无权限时取数按钮不在 DOM（结构上无法触发）', () => {
  it('④-1 ★★★★★★ 错角色时 15/15 都找不到那一席的取数按钮', async () => {
    const report: string[] = []
    const bad: string[] = []
    for (const row of TABLE) {
      calls = []
      const w = await mountAs(row, 'user')
      if (!row.trigger) {
        // ★ 自动取数席（`/request-anomalies`）本就没有查询按钮，
        //   它那一半由 ② 的「挂载零请求」量 —— 而它恰好是**唯一**挂载即取数的
        //   席位，也就是风险面最大的一席。
        report.push(`${row.path.padEnd(30)} （自动取数席，无按钮）`)
        continue
      }
      const there = w.find(`[data-testid="${row.trigger}"]`).exists()
      report.push(`${row.path.padEnd(30)} ${row.trigger.padEnd(12)} ${there ? '还在！' : '不存在'}`)
      if (there) bad.push(`${row.path} 无权限时 ${row.trigger} 仍在 DOM 里`)
    }
    console.log(`[④-1] user 角色 · 取数按钮：\n  ${report.join('\n  ')}`)
    expect(bad, '无权限时业务区没藏干净：\n' + bad.join('\n')).toEqual([])
  })
})

// ── ⑤ DOM 层：不露裸键 ───────────────────────────────────────────────────────

describe('⑤ ★★★★ 错角色时不露出裸 i18n 键', () => {
  /**
   * ★★★ 判据按**字典反查**抓裸键，不按「长得像不像键」。
   *   第一版用 `/[a-z]\.[a-z]{2,}$/` 当过滤条件 —— 那个正则要求尾巴**全小写**，
   *   于是 `et.nopeXX`（变异 M04 用的正是它）**整条漏网**。
   *   第二版加了**词边界** `\b`，实测仍然漏 —— 因为真实渲染是
   *   `Error trend` + `et.nope` + `Tenant scope` 拼起来的，
   *   裸键**粘在相邻文本中间、根本没有词边界**（实测 `w.text()` 里那串是
   *   `"Error trendet.nopeXXTenant scope…"`）⇒ 有边界的正则一条都匹不到。
   *   ⇒ 第三版去掉边界，靠**字典反查**兜误报：
   *   形状像键（前缀 2~4 字母 + 点 + 驼峰键名）**且字典里查不到**。
   *   ★ 用 zh-CN 字典查、断言不看具体语言 ⇒ 不押一种语言；
   *     若某一侧缺键，本组不会误报（缺键由 i18n 门负责）。
   */
  function bareKeys(text: string): string[] {
    const cand = text.match(/[a-z]{2,4}\.[a-z][a-zA-Z0-9]+/g) ?? []
    return cand.filter((t) => !dictHas(t))
  }

  it('⑤-1 ★★★★ 15/15 的 nope 区走的是真字典', async () => {
    const bad: string[] = []
    for (const row of TABLE) {
      const w = await mountAs(row, 'user')
      const bare = bareKeys(w.text())
      if (bare.length) bad.push(`${row.path} 露出裸键：${bare.slice(0, 3).join(', ')}`)
    }
    expect(bad, '裸键泄漏：\n' + bad.join('\n')).toEqual([])
  })

  it('⑤-2 ★★★★★ 判据自证：裸键过滤器确实能抓到（恒真的门是装饰）', async () => {
    // ⚠️⚠️ 样本必须**照抄真实渲染的粘接形态**（`错误标题` + 键 + `后续文案`），
    //   **不能**用前后带空格的「友好」样本：第一版正是用带空格的样本自证通过，
    //   而真实 DOM 里根本没有词边界 ⇒ 判据是假门，变异 M04 实测全绿。
    // ⚠️ 断言用**包含**而不是相等：本判据只回答「有没有漏出字典外的点分键」，
    //   **不**回答「这个键被切成了哪一段」—— 粘接文本里前缀会被上一个词吞掉
    //   （实测匹到的是 `ndet.nopeXXTenant`）。要精确分词就得写分词器，
    //   那是另一件事，不该混进「有没有漏裸键」这个问题里。
    const glued = bareKeys('Error trendet.nopeXXTenant scope')
    expect(glued.some((t) => t.includes('nopeXX')), '漏掉了粘在文本中间的驼峰尾巴裸键（M04 的逃逸样本）').toBe(true)
    // 全小写尾巴也要抓到（第一版过滤器的「正常」路径仍在）。
    // ⚠️ 这里**不能**拿 `et.nope` 当样本 —— 它是字典里**真有的**键，
    //   反查正确时就不该被标成裸键（第一版写这个断言时正踩了这个）。
    expect(bareKeys('Error trendzz.nopeTenant scope').some((t) => t.includes('zz.nope'))).toBe(true)
    // 反向：字典里**真有的**键不能被当成裸键（否则会对每个页面误报）。
    expect(bareKeys('范围说明 saf.state'), '把字典里真有的键误判成裸键了').toEqual([])
    // ★ 反向的更强形式：对**真视图**的 nope 区跑一遍，必须**抓不到任何东西**
    //   —— 这是 ⑤-1 不被误报淹没的保证（第二版正是漏在这一步）。
    const w = await mountAs(TABLE[1], 'user')
    expect(bareKeys(w.text()), '真视图的 nope 区被判成了裸键 ⇒ 过滤器过宽').toEqual([])
  })
})

// ── ⑥ 表不许漂移 ─────────────────────────────────────────────────────────────

describe('⑥ ★★★ 本表登记的 gate 必须就是视图里真正调的那个', () => {
  it('⑥-1 ★★★ 逐个视图核对 allowed 的定义', () => {
    const problems: string[] = []
    for (const row of TABLE) {
      const src = readFileSync(join(process.cwd(), 'src/views', row.file), 'utf8')
      const line = src.split('\n').find((l) => /const allowed = computed\(/.test(l))
      if (!line) {
        problems.push(`${row.file} 里找不到 allowed 的定义`)
        continue
      }
      if (!line.includes(row.gate)) problems.push(`${row.file} 的 allowed 是「${line.trim()}」，不含表里登记的 ${row.gate}`)
    }
    expect(problems, '表与视图漂移了：\n' + problems.join('\n')).toEqual([])
  })

  it('⑥-2 ★★★ 表里的 trigger / fill 必须真的存在于视图里（防漂移）', () => {
    const problems: string[] = []
    for (const row of TABLE) {
      const src = readFileSync(join(process.cwd(), 'src/views', row.file), 'utf8')
      if (row.trigger && !src.includes(`data-testid="${row.trigger}"`)) {
        problems.push(`${row.file} 里没有取数按钮 ${row.trigger}`)
      }
      for (const [id] of row.fill) {
        if (!src.includes(`data-testid="${id}"`)) problems.push(`${row.file} 里没有填单项 ${id}`)
      }
    }
    expect(problems, 'trigger / fill 漂移了：\n' + problems.join('\n')).toEqual([])
  })
})
