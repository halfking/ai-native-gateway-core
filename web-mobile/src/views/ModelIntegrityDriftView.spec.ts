import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ModelIntegrityDriftView from './ModelIntegrityDriftView.vue'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { setLocale } from '@/i18n'
import {
  fetchModelIntegrityFingerprintDrift,
  driftDaysIsSendable,
  driftLimitIsSendable,
  DRIFT_DAYS_DEFAULT,
  DRIFT_DAYS_MAX,
  DRIFT_LIMIT_DEFAULT,
  DRIFT_LIMIT_MAX,
  DRIFT_ANOMALY_TYPE,
  DRIFT_SEVERITY_TABLE_DEFAULT,
} from '@/api/modelIntegrityDrift'

/**
 * 指纹漂移面回归护栏（2026-10-14，第一百一十批）。
 *
 * ★★ 权限：**superAdmin 档**（`admin/handler.go:942-943` 两条都是 `h.superAdmin`，
 *   且**整个 `/api/admin/model-integrity/` 前缀**都是这一档）
 *   ⇒ 抽屉席**须设** `requiresRole: 'super_admin'`（同步 `AppDrawer.spec.ts` 白名单）。
 *   ⚠️ 同前缀的 `integrity` 席（summary/events/resolve）是**另一张席** ——
 *     所以权限**不得**按前缀判，必须看可执行注册处。
 *
 * ★★★ 租户口径：`:328` 的 `withAllTenantReadOnlyTx`，SQL 选了 `tenant_id` 却
 *   WHERE 无任何租户条件（契约头 (3)）⇒ 与 operational / selfCheck / routing /
 *   formatAnomalies 同属「读路径不隔离」一族。
 *   ★★ **但处置与批 104 / 批 106 相反**：那两族 admin 档 + 不隔离 ⇒ 必须视图内门控；
 *     本族 superAdmin 档 ⇒ **档位本身就是那道门** ⇒ 视图里**不应有任何角色分支**。
 *   ⇒ 第十一组判据用「结构 + 渲染」两层把这件事钉死。
 *
 * 判据钉十一组不变量：
 *  ① ★★★★ `days` / `limit` 是**静默回落**（不 400）：越界与非法值都拿到缺省数据
 *      ⇒ 页面**不得提供越界档位**（桌面那个 1..90 的输入框就是反例，见 :597）。
 *  ② ★★★ `days` 回显的是**生效窗口**，不是请求值；请求值被回落要单独报。
 *  ③ ★★★ `count` **恒等于**本页行数（`:385` 写 `len(items)`），
 *      不等就是载荷被换过 ⇒ 告警判据必须**成对**写（该报 / 不该报）。
 *  ④ ★★ 响应**不回显 limit** ⇒ 只能靠「拿满请求上限」反推可能截断。
 *  ⑤ ★★★★ `provider_id` / `credential_id` 是**指针** + omitempty
 *      ⇒ 键在时值可能是真实的 **0**，真值判断会把 0 误判成「没有」。
 *  ⑥ ★★★★ `context` 经 `jsoncol.Decode`：NULL / JSON `null` / **JSON 解析失败**
 *      三条路都让键被省略 ⇒ ★★ **键缺不能证明「没有上下文」**。
 *  ⑦ ★★★ `resolved` 与 `resolved_at` / `resolution_notes` 之间**没有 DB 约束**
 *      ⇒ 两个方向都不能单独断言，判据取并集。
 *  ⑧ ★★★ 本表 `severity` **表级缺省是 `low`**，另一张表是 `medium`
 *      ⇒ 缺省这件事**绝不能跨表照抄**；四值域只在注释里，表上没有 CHECK。
 *  ⑨ ★★ `request_id` 是**条件键**：键缺（NULL）与空串（存了 `''`）是两件事。
 *  ⑩ ★★ 排序 `ORDER BY ts DESC`；零行窗口；跨租户 / 无租户标记两个信号。
 *  ⑪ ★★★★ 视图内部**不得**有任何角色分支。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function rec(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    detected_at: '2026-10-14T03:00:00Z',
    // ★ 本端点的 anomaly_type 是 SQL 硬编码常量
    anomaly_type: DRIFT_ANOMALY_TYPE,
    severity: DRIFT_SEVERITY_TABLE_DEFAULT,
    resolved: false,
    ...over,
  }
}

function payload(o: { events?: unknown[]; count?: number; days?: number } = {}) {
  const events = o.events ?? [rec()]
  return {
    events,
    // ★★ count 恒等于本页行数（后端写的是 len(items)），不是窗口内总数
    count: o.count ?? events.length,
    days: o.days ?? DRIFT_DAYS_DEFAULT,
  }
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

// ★ 第十一组要用：把 auth store 做成**可变**的，好在同一份夹具下换角色重挂。
const authState = { role: 'super_admin', userInfo: { role: 'super_admin', tenant_id: 't-1' } }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/modelIntegrityDrift', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelIntegrityDrift')>()
  return { ...actual, fetchModelIntegrityFingerprintDrift: vi.fn() }
})

const mFetch = fetchModelIntegrityFingerprintDrift as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(ModelIntegrityDriftView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

/** ★★★ 取某一格的文本 —— 负断言必须与被测对象**同格**，不能放大到整行/整页。 */
function cell(w: W, id: string): string {
  return byId(w, id).text()
}

/**
 * 剥掉块注释与行注释，只留代码。
 * ★ 判「视图有没有角色分支 / 有没有小触控目标」时，**文档不是行为** ——
 *   视图头注释里就写着 `requiresRole: 'super_admin'`，不剥就会把文档当成行为。
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

/** 页面上某一类 chip 的全部取值（从 data-testid 里把数字抠出来）。 */
function chipValues(w: W, prefix: string): number[] {
  return w
    .findAll(`[data-testid^="${prefix}"]`)
    .map((n) => Number(n.attributes('data-testid')!.replace(`${prefix}-`, '')))
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  authState.userInfo = { role: 'super_admin', tenant_id: 't-1' }
  mFetch.mockResolvedValue(payload())
})

// ── ① 档位与错误信封 ──────────────────────────────────────────────────────

describe('① 档位与错误', () => {
  it('①-1 抽屉席的 requiresRole 由 AppDrawer.spec.ts 的白名单把守（此判据只钉路由可达）', () => {
    // ★ 席的白名单不在本文件里；这里只确认本模块常量与后端档位一致，
    //   真正「谁能看」的断言在 AppDrawer.spec.ts（「tenant_admin 少掉的只有这些」）。
    expect(DRIFT_DAYS_MAX).toBe(30)
    expect(DRIFT_LIMIT_MAX).toBe(500)
    expect(DRIFT_DAYS_DEFAULT).toBe(7)
    expect(DRIFT_LIMIT_DEFAULT).toBe(200)
  })

  it('①-2 三条错误文案各不相同，页面原样透出（不吞不改写）', async () => {
    // ★ 405 `method not allowed` / 503 `database not configured` / 500 `drift query failed`
    mFetch.mockRejectedValueOnce(new Error('drift query failed'))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-error')).toBe('drift query failed')
    // ★ 失败后载荷必须清空 —— 否则旧数据会以「本次查询的结果」示人
    expect(hasId(w, 'mid-count')).toBe(false)
  })

  it('①-3 加载中不得把上一份载荷清空成空白页（骨架与数据并存）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ id: 7 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-row-7')).toBe(true)
  })
})

// ── ② 越界档位不得出现在页面上（静默回落族）────────────────────────────────

describe('② days / limit 的静默回落', () => {
  it('②-1 ★★★ 页面**不得**提供任何越界的 days 档位（后端上限 30）', async () => {
    const w = await mountView()
    const ds = chipValues(w, 'mid-days')
    expect(ds.length).toBeGreaterThan(0)
    // ★★ 桌面的 days 输入框允许 1..90，而后端 >30 静默回落成 7 ——
    //   移动端绝不能复制那个上界。
    expect(Math.max(...ds)).toBeLessThanOrEqual(DRIFT_DAYS_MAX)
    for (const d of ds) expect(driftDaysIsSendable(d)).toBe(true)
  })

  it('②-2 ★★★ 页面**不得**提供任何越界的 limit 档位（后端上限 500）', async () => {
    const w = await mountView()
    const ls = chipValues(w, 'mid-limit')
    expect(Math.max(...ls)).toBeLessThanOrEqual(DRIFT_LIMIT_MAX)
    for (const l of ls) expect(driftLimitIsSendable(l)).toBe(true)
  })

  it('②-3 ★ 选中的 days / limit 真的发得出去（守卫不是摆设）', async () => {
    mFetch.mockResolvedValue(payload({ days: 30 }))
    const w = await mountView()
    await byId(w, 'mid-days-30').trigger('click')
    await flushPromises()
    // ★ 视图只传一个参数（不传 options），且 limit 缺省 200 也发得出去 ⇒ 两个都在。
    //   第一版写成 `toHaveBeenCalledWith({days:30}, expect.anything())`，
    //   错在**假设了一个根本不存在的第二个参数** ⇒ 失败报的是「参数个数不符」，
    //   而我真正要量的是「days 落在请求里」。
    expect(mFetch).toHaveBeenCalledWith({ days: 30, limit: DRIFT_LIMIT_DEFAULT })
  })

  it('②-4 ★ 换 limit 档位后重发，且带上新的 limit', async () => {
    mFetch.mockResolvedValue(payload())
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const before = mFetch.mock.calls.length
    await byId(w, 'mid-limit-500').trigger('click')
    await flushPromises()
    expect(mFetch.mock.calls.length).toBe(before + 1)
    const last = mFetch.mock.calls[mFetch.mock.calls.length - 1]![0]
    expect(last.limit).toBe(DRIFT_LIMIT_MAX)
  })

  it('②-5 ★★★ 非法值不得发得出去（守卫的负方向也要有判据）', () => {
    // ★ 后端 `queryInt` 解析失败直接取缺省 ⇒ 发过去也是白发。
    expect(driftDaysIsSendable(0)).toBe(false)
    expect(driftDaysIsSendable(31)).toBe(false)
    expect(driftDaysIsSendable(DRIFT_DAYS_MAX + 1)).toBe(false)
    expect(driftLimitIsSendable(0)).toBe(false)
    expect(driftLimitIsSendable(DRIFT_LIMIT_MAX + 1)).toBe(false)
    expect(driftDaysIsSendable(undefined)).toBe(false)
    expect(driftLimitIsSendable(null)).toBe(false)
  })

  it('②-6 ★ 边界值发得出去（守卫不能把合法边界一起挡掉）', () => {
    expect(driftDaysIsSendable(1)).toBe(true)
    expect(driftDaysIsSendable(DRIFT_DAYS_MAX)).toBe(true)
    expect(driftLimitIsSendable(1)).toBe(true)
    expect(driftLimitIsSendable(DRIFT_LIMIT_MAX)).toBe(true)
  })
})

// ── ③ days 回显的是生效窗口 ────────────────────────────────────────────────

describe('③ days 回显', () => {
  it('③-1 ★★★ 页面显示的是**响应里的生效值**，不是请求值', async () => {
    mFetch.mockResolvedValue(payload({ days: 30 }))
    const w = await mountView()
    await byId(w, 'mid-days-30').trigger('click')
    await flushPromises()
    // ★ 请求 30、后端回显 30 ⇒ 两者相同，此时**不该**报「被改写」
    expect(cell(w, 'mid-days-echo')).toContain('30')
    expect(hasId(w, 'mid-days-rewritten')).toBe(false)
  })

  it('③-2 ★★★ 请求值与生效值不同 ⇒ 显式报「窗口被改写」', async () => {
    mFetch.mockResolvedValue(payload({ days: DRIFT_DAYS_DEFAULT }))
    const w = await mountView()
    await byId(w, 'mid-days-14').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-days-rewritten')).toBe(true)
    expect(cell(w, 'mid-days-rewritten')).toContain('14')
  })

  it('③-3 ★ 生效回显**永远**呈现（哪怕没被改写）—— 让用户知道后端认的是哪一档', async () => {
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-days-echo')).toBe(true)
  })
})

// ── ④ count 恒等 + 告警成对 ───────────────────────────────────────────────

describe('④ count 恒等于本页行数', () => {
  it('④-1 ★★★ count === 本页行数 ⇒ **不得**报违规（正常业务取值下的正控）', async () => {
    const events = [rec({ id: 1 }), rec({ id: 2 }), rec({ id: 3 })]
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-count')).toContain('3')
    expect(hasId(w, 'mid-count-violation')).toBe(false)
  })

  it('④-2 ★★★ count < 本页行数 ⇒ 报违规（载荷被换过，不是「后端口径变了」）', async () => {
    const events = [rec({ id: 1 }), rec({ id: 2 })]
    mFetch.mockResolvedValue(payload({ events, count: 1 }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-count-violation')).toBe(true)
  })

  it('④-3 ★★ count > 本页行数 ⇒ **同样**报违规（这一个方向最容易漏）', async () => {
    // ★★★ 只钉 `count < len` 的告警判据是**半条**：把判定从 `!==` 改成 `>` 时，
    //   「count 比本页多」（最普通的一种载荷）就会**恒不报**，而没有判据拦它。
    //   ⇒ 两个方向都要钉，且正控必须取**正常业务取值**而不是空数组。
    mFetch.mockResolvedValue(payload({ events: [rec({ id: 1 })], count: 137 }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-count-violation')).toBe(true)
  })

  it('④-4 ★ 零行窗口：count=0 与 events=[] 一致 ⇒ 不报违规，只报空', async () => {
    mFetch.mockResolvedValue(payload({ events: [], count: 0 }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-count-violation')).toBe(false)
    expect(hasId(w, 'mid-empty')).toBe(true)
  })
})

// ── ⑤ 拿满上限 = 可能截断（成对）─────────────────────────────────────────

describe('⑤ 截断反推', () => {
  it('⑤-1 ★★ 拿满请求上限 ⇒ 报「可能截断」（响应无字段可证）', async () => {
    const events = Array.from({ length: DRIFT_LIMIT_DEFAULT }, (_, i) => rec({ id: i + 1 }))
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-at-cap')).toBe(true)
  })

  it('⑤-2 ★★ 未拿满 ⇒ **不得**报截断（正常业务取值下的正控）', async () => {
    // ★ 这一条是给「把判定写成 `>= 1`」那种变异准备的：正常的一页数据不该被说成截断。
    const events = Array.from({ length: 37 }, (_, i) => rec({ id: i + 1 }))
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-at-cap')).toBe(false)
  })

  it('⑤-3 ★★ 换 limit 档位后阈值跟着换（不能写死成 200）', async () => {
    const events = Array.from({ length: 50 }, (_, i) => rec({ id: i + 1 }))
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-limit-50').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-at-cap')).toBe(true)
  })
})

// ── ⑥ 指针型数字键：0 是真的 0 ────────────────────────────────────────────

describe('⑥ 指针型数字键', () => {
  it('⑥-1 ★★★★ provider_id 键在且值为 0 ⇒ 显示 0，**不是**「无数据」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ provider_id: 0 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const t = cell(w, 'mid-f-1-provider_id')
    expect(t).toContain('0')
    expect(t).not.toContain('无数据')
  })

  it('⑥-2 ★★★★ credential_id 键在且值为 0 ⇒ 同上（两个键各自都要钉）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ credential_id: 0 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const t = cell(w, 'mid-f-1-credential_id')
    expect(t).toContain('0')
    expect(t).not.toContain('无数据')
  })

  it('⑥-3 ★★★ provider_id 键缺失 ⇒ 显示「无数据（键缺失）」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec()] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-provider_id')).toContain('无数据')
  })

  it('⑥-4 ★★★ 两个数字键的判定互不影响（provider 缺、credential 在）', async () => {
    // ★ 负断言必须与被测对象**同格**：provider 那格说「无数据」，
    //   不代表 credential 那格也该说 —— 误伤就是从这里来的。
    mFetch.mockResolvedValue(payload({ events: [rec({ credential_id: 0 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-provider_id')).toContain('无数据')
    expect(cell(w, 'mid-f-1-credential_id')).toContain('0')
  })

  it('⑥-5 ★ 非零值原样显示', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ provider_id: 42 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-provider_id')).toContain('42')
  })
})

// ── ⑦ context：键缺不能证明「没有上下文」──────────────────────────────────

describe('⑦ context 的三条消失路径', () => {
  it('⑦-1 ★★★★ 键缺失 ⇒ 显示「无法判定」，**不是**「不存在」', async () => {
    // ★★★ jsoncol.Decode（internal/jsoncol/jsoncol.go:57-79）有**三条路**让键消失：
    //   列是 NULL、列是 JSON `null`、**列里的 JSON 解析失败**（只留 Warn、dst 不动）。
    //   ⇒ 说「没有上下文」是在替后端下结论，而那三种情况里只有一种是「真的没有」。
    mFetch.mockResolvedValue(payload({ events: [rec()] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const t = cell(w, 'mid-f-1-context')
    expect(t).toContain('无法判定')
    expect(t).not.toContain('不存在')
  })

  it('⑦-2 ★ 键在且是空对象 ⇒ 显示「存在但为空」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ context: {} })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-context')).toContain('存在但为空')
  })

  it('⑦-3 ★ 键在且非空 ⇒ 显示「存在」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ context: { k: 'v' } })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-context')).toContain('存在')
    expect(cell(w, 'mid-f-1-context')).not.toContain('无法判定')
  })

  it('⑦-4 ★★ 键在但**不是对象**（Go 的 `any`）也要给出确定呈现，不得崩', async () => {
    // ★ `Context any` + omitempty ⇒ 解出来可能是字符串/数组/数字。
    for (const c of ['abc', [1, 2], 7]) {
      mFetch.mockResolvedValue(payload({ events: [rec({ context: c })] }))
      const w = await mountView()
      await byId(w, 'mid-load').trigger('click')
      await flushPromises()
      expect(cell(w, 'mid-f-1-context')).toContain('存在')
    }
  })
})

// ── ⑧ 处置痕迹：两个方向都不单独断言 ─────────────────────────────────────

describe('⑧ resolved 与处置痕迹', () => {
  it('⑧-1 ★★ 完全未处置（resolved=false 且两个痕迹键都缺）⇒ 未解决', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ resolved: false })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('未解决')
  })

  it('⑧-2 ★★ resolved=true 但痕迹键都缺 ⇒ 标成「标记已解决但没有处置痕迹」', async () => {
    // ★★★ 这是「两个方向都不能单独断言」的实证：`resolved=true` **不蕴含** `resolved_at` 在。
    mFetch.mockResolvedValue(payload({ events: [rec({ resolved: true })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('标记已解决但没有处置痕迹')
  })

  it('⑧-3 ★ resolved=false 但 resolved_at 在 ⇒ 标成「标记与痕迹不一致」', async () => {
    // ★ 反方向同样没有 DB 约束保证。
    mFetch.mockResolvedValue(
      payload({ events: [rec({ resolved: false, resolved_at: '2026-10-14T04:00:00Z' })] }),
    )
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('标记与痕迹不一致')
  })

  it('⑧-4 ★ 三者齐全 ⇒ 处置痕迹完整，且两个痕迹行都渲染出来', async () => {
    mFetch.mockResolvedValue(
      payload({
        events: [
          rec({
            resolved: true,
            resolved_at: '2026-10-14T04:00:00Z',
            resolution_notes: '已按指纹回滚',
          }),
        ],
      }),
    )
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('处置痕迹齐全')
    expect(w.text()).toContain('2026-10-14T04:00:00Z')
    expect(w.text()).toContain('已按指纹回滚')
  })

  it('⑧-5 ★ 痕迹键缺失时**不渲染**那两个行（避免出现空行）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ resolved: false })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const labels = w.findAll('.mid__kv-row dt').map((n) => n.text())
    expect(labels).not.toContain('处置时间')
    expect(labels).not.toContain('处置备注')
  })
})

// ── ⑨ severity：表缺省是 low，不是 medium ────────────────────────────────

describe('⑨ severity 的表级缺省', () => {
  it('⑨-1 ★★★★ 本表的缺省是 low —— 绝不能照抄另一张表的 medium', async () => {
    // ★★ 同族不同口径：`response_format_anomalies` 是 DEFAULT 'medium'，
    //   本表 `model_integrity_events` 是 DEFAULT 'low'（462:52）。
    mFetch.mockResolvedValue(payload({ events: [rec({ severity: 'low' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const card = byId(w, 'mid-row-1')
    expect(card.text()).toContain('low')
    expect(card.text()).toContain('表级缺省')
    expect(card.text()).not.toContain('medium')
  })

  it('⑨-2 ★ medium 不带「表级缺省」标注（那是本表专属）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ severity: 'medium' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const card = byId(w, 'mid-row-1')
    expect(card.text()).toContain('medium')
    expect(card.text()).not.toContain('表级缺省')
  })

  it('⑨-3 ★★ 四值域外的取值要标出来，但**不是错误**（表上没有 CHECK）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ severity: 'sev9' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    const card = byId(w, 'mid-row-1')
    expect(card.text()).toContain('sev9')
    expect(card.text()).toContain('四值域外')
    // ★ 不是错误 ⇒ 不得出现任何告警块
    expect(hasId(w, 'mid-count-violation')).toBe(false)
    expect(hasId(w, 'mid-order-bad')).toBe(false)
  })

  it('⑨-4 ★ anomaly_type 恒为 fingerprint_drift（SQL 硬编码）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ anomaly_type: 'fingerprint_drift' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(byId(w, 'mid-row-1').text()).toContain(DRIFT_ANOMALY_TYPE)
  })
})

// ── ⑩ request_id：键缺与空串是两件事 ─────────────────────────────────────

describe('⑩ 条件键的「缺失」与「空值」', () => {
  it('⑩-1 ★★★ request_id 键缺失 ⇒ 「未提供（键缺失）」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec()] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('未提供（键缺失）')
  })

  it('⑩-2 ★★★ request_id 是空串 ⇒ 「空串」，与键缺失**措辞不同**', async () => {
    // ★ 两者都是「没有值」，但成因不同（NULL vs 存了 `''`）⇒ 措辞必须分开。
    mFetch.mockResolvedValue(payload({ events: [rec({ request_id: '' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    // ★★★ 负断言**必须与被测对象同格**：第一版量的是整张卡片，
    //   而同一行里 provider_code / client_model / … 确实都缺键、
    //   正确地渲染成「未提供（键缺失）」⇒ 整卡口径必然误报。
    //   收窄到 request_id 那一格后才真正量的是「这一格没有用错措辞」。
    const t = cell(w, 'mid-f-1-request_id')
    expect(t).toBe('空串（该行没有请求 ID）')
    expect(t).not.toContain('未提供（键缺失）')
  })

  it('⑩-3 ★ actual_value 键缺失 ⇒ 「未提供」，空串 ⇒ 「空串」', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ actual_value: '' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(cell(w, 'mid-f-1-actual_value')).toBe('空串')
  })
})

// ── ⑪ 排序 / 空窗口 / 跨租户 ─────────────────────────────────────────────

describe('⑪ 排序与跨租户信号', () => {
  it('⑪-1 ★★ detected_at 非增（ORDER BY ts DESC）⇒ 不报违例', async () => {
    const events = [
      rec({ id: 1, detected_at: '2026-10-14T05:00:00Z' }),
      rec({ id: 2, detected_at: '2026-10-14T03:00:00Z' }),
      rec({ id: 3, detected_at: '2026-10-13T03:00:00Z' }),
    ]
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-order-bad')).toBe(false)
  })

  it('⑪-2 ★★ 升序 ⇒ 报违例（与后端契约不符）', async () => {
    const events = [
      rec({ id: 1, detected_at: '2026-10-13T03:00:00Z' }),
      rec({ id: 2, detected_at: '2026-10-14T05:00:00Z' }),
    ]
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-order-bad')).toBe(true)
  })

  it('⑪-3 ★★ 时间戳相同（同一毫秒）⇒ **不得**判违例（非增含相等）', async () => {
    const events = [
      rec({ id: 1, detected_at: '2026-10-14T05:00:00Z' }),
      rec({ id: 2, detected_at: '2026-10-14T05:00:00Z' }),
    ]
    mFetch.mockResolvedValue(payload({ events }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-order-bad')).toBe(false)
  })

  it('⑪-4 ★★★ 带 tenant_id ⇒ 报跨租户（读路径不过滤，超管看的是全部租户）', async () => {
    mFetch.mockResolvedValue(payload({ events: [rec({ tenant_id: 'tenant-b' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-foreign-tenants')).toBe(true)
    expect(hasId(w, 'mid-no-tenant-tags')).toBe(false)
  })

  it('⑪-5 ★★★ 一行都没有 tenant_id ⇒ 报「无租户标记」，**不是**跨租户', async () => {
    // ★ 两个信号互斥 —— 它们在 `foreign ? ... : ...` 上是同一条链的两端。
    mFetch.mockResolvedValue(payload({ events: [rec(), rec({ id: 2 })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-foreign-tenants')).toBe(false)
    expect(hasId(w, 'mid-no-tenant-tags')).toBe(true)
  })

  it('⑪-6 ★ 零行时不得报任何跨租户信号（空集说什么都不是）', async () => {
    mFetch.mockResolvedValue(payload({ events: [], count: 0 }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-foreign-tenants')).toBe(false)
    expect(hasId(w, 'mid-no-tenant-tags')).toBe(false)
  })

  it('⑪-7 ★ 零行窗口渲染空态且不渲染任何行卡片', async () => {
    mFetch.mockResolvedValue(payload({ events: [], count: 0 }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-empty')).toBe(true)
    expect(w.findAll('[data-testid^="mid-row-"]').length).toBe(0)
  })
})

// ── ⑫ superAdmin 档 ⇒ 视图内部不得有角色分支 ──────────────────────────────

describe('⑫ superAdmin 档的处置', () => {
  it('⑫-1 ★★★★★ 视图源码**不 import** auth store ⇒ 结构上就不可能有角色分支', () => {
    // ★★ 比批 109 那条「页面上不得出现某段文案」强一个量级：
    //   批 109 量的是**文案**，写文案的人照样能加一个 `v-if`。
    //   这里量的是**依赖**：视图压根没拿身份 ⇒ 渲染不可能随身份变。
    const src = stripComments(
      readFileSync(join(process.cwd(), 'src/views/ModelIntegrityDriftView.vue'), 'utf8'),
    )
    expect(src).not.toMatch(/from\s+['"]@\/stores\/auth['"]/)
    expect(src).not.toMatch(/\buseAuthStore\b/)
    // ★★★ 第一版直接对**原文**匹配 `super_admin`，立刻被视图头注释里那句
    //   「抽屉席须设 requiresRole: 'super_admin'」打红 ——
    //   而那正是**文档**，不是行为。⇒ 角色断言必须先剥注释。
    expect(src).not.toMatch(/super_admin|isSuperAdmin|tenant_admin/)
  })

  it('⑫-2 ★★★★★ 同一份载荷下换角色重挂 ⇒ 渲染逐字节相同', () => {
    // ★ 行为层的复核：即便有人绕过 store 直接读别处，两次渲染也必须完全一致。
    mFetch.mockResolvedValue(payload({ events: [rec({ tenant_id: 'tenant-b' })] }))
    authState.role = 'tenant_admin'
    authState.userInfo = { role: 'tenant_admin', tenant_id: 't-1' }
    const a = mount(ModelIntegrityDriftView)
    authState.role = 'super_admin'
    authState.userInfo = { role: 'super_admin', tenant_id: 't-1' }
    const b = mount(ModelIntegrityDriftView)
    return flushPromises().then(() => {
      expect(a.html()).toBe(b.html())
    })
  })

  it('⑫-3 ★ 跨租户标记照常渲染（判读信号 ≠ 披露门控）', async () => {
    // ★ 页面**要**把「这条记录带租户标记」说出来（超管需要知道自己在看谁的），
    //   但**不因此**改变字段可见性 —— 这正是「档位已挡、视图无分支」的形态。
    mFetch.mockResolvedValue(payload({ events: [rec({ tenant_id: 'tenant-b' })] }))
    const w = await mountView()
    await byId(w, 'mid-load').trigger('click')
    await flushPromises()
    expect(hasId(w, 'mid-foreign-tenants')).toBe(true)
    expect(byId(w, 'mid-row-1').text()).toContain('tenant-b')
  })
})

// ── ⑬ 触控目标 ───────────────────────────────────────────────────────────

describe('⑬ 触控目标', () => {
  it('⑬-1 ★ 新增的每个触控控件都 ≥48 CSS px', () => {
    // ★ R1：触控门只扫 `<style scoped>` 里的 `min-height < 48px` 选择器，
    //   所以这里只需要保证**新增控件**都声明了 48px。
    const src = readFileSync(join(process.cwd(), 'src/views/ModelIntegrityDriftView.vue'), 'utf8')
    const scoped = src.slice(src.indexOf('<style scoped>'))
    // ★★★ 第一版写的是 `not.toMatch(/min-height:\s*(?:0|[1-4]?\d)px/)`，
    //   而 `[1-4]?\d` 会吃掉**两位数**（'4' + '8' + 'px'）⇒ 连 `48px` 自己都被判违规。
    //   ⇒ 改成把数值**解析出来**再比大小：既不误伤，也挡得住真值 47。
    const vals = [...scoped.matchAll(/min-height:\s*(\d+(?:\.\d+)?)px/g)].map((m) => Number(m[1]))
    expect(vals.length).toBeGreaterThan(0)
    for (const v of vals) expect(v).toBeGreaterThanOrEqual(48)
    expect(scoped).toContain('min-height: 48px')
  })
})
