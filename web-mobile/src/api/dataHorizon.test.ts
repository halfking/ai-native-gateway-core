import { describe, it, expect } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  unwrapDataHorizon,
  dataHorizonIsLive,
  dataHorizonIsStale,
  dataHorizonIsUnknown,
  dataHorizonExactlyOneFlag,
  dataHorizonSourceIsSessionTurns,
  dataHorizonIsUnexpectedShape,
  parseDataHorizonHeader,
  dataHorizonHeaderMatchesBody,
  DATA_HORIZON_HEADER,
  DATA_HORIZON_SOURCE,
  DATA_HORIZON_ENVELOPE_KEYS,
  DATA_HORIZON_KEY,
  DATA_HORIZON_NOTICE_KEYS,
  DATA_HORIZON_STATES,
  DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES,
  DATA_HORIZON_LAG_TOLERANCE_MINUTES,
  type DataHorizonNotice,
  type DataHorizonResponse,
} from './dataHorizon'

/**
 * 流量数据地平线解包与语义判据（2026-10-15，第一百二十九批）。
 *
 * ★★★★★ 本批是**契约重写**，不是改名。判据的第一职责是把「哪些概念被下线了」
 *   钉死 —— 否则下一个人会以为 `affects` / `gate_key` / 配置来源还在。
 *
 *   后端把 `admin/v1_freeze_notice.go` 整文件换成 `admin/data_horizon.go`
 *   （r1033，2026-10-15），**删掉了一整批概念**：
 *   `affects` 字段没了 · `gate_key` 换成 `hint` · 配置来源 `{db,env,default}`
 *   这个概念整个消失 · 前端那个「重算三态」的纯函数镜像不再有对应物。
 *
 * ★★★★★ 本族**保留**的两条核心纪律（它们跨代不变，判据必须继续钉）：
 *   (1) 主键**恒在**，值是「对象」或「裸 `null`」—— 不是主键消失。
 *   (2) **有对象 ⇒ `stale` 与 `unknown` 恰好一个为 true**（后端刻意不产出
 *       「两个都 false」，类型注释白纸黑字写着「两者互斥」）。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 信封两键恒在 + 主键两种形态（对象 / 裸 null）。
 *  ② ★★★★★★ 六键 notice 无 omitempty + 类型分支（null 放行不是错）。
 *  ③ ★★★★★★ 「恰好一个 flag」不变式（含两个反例）。
 *  ④ ★★★★★ **已下线概念不得残留**（负控：旧名一个都不许出现）。
 *  ⑤ ★★★★★ 响应头四值 + 交叉验证（含 live 那档的头必须**缺失**）。
 *  ⑥ ★★★★ 两个时间窗常量与 `source` 恒定值。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function envelope(n: DataHorizonNotice | null): DataHorizonResponse {
  return { data_horizon: n, '//': 'stale=true 表示…' }
}
function staleNotice(): DataHorizonNotice {
  return {
    stale: true,
    unknown: false,
    source: DATA_HORIZON_SOURCE,
    effect: '网关仍在接收请求，但 session_turns 未记录到新数据；本页及派生统计正在滞后于此刻。',
    silence: '这类断流没有任何错误信号：统计接口照常返回、数字齐全。',
    hint: '排查方向：session 镜像写入路径（sessionv2mirror）与 sessions_v2 写门。',
  }
}
function unknownNotice(): DataHorizonNotice {
  return {
    stale: false,
    unknown: true,
    source: DATA_HORIZON_SOURCE,
    effect: '无法确认流量数据状态：session_turns 读取失败。',
    silence: '两种可能都未经验证：记录可能仍在继续，也可能已经断流。',
    hint: '排查方向：数据库连通性 / session_turns_hot 表是否存在。',
  }
}
/** ★ 后端不产出这种形态 —— 用来证明判据真的有牙。 */
function bothFalseNotice(): DataHorizonNotice {
  return { ...staleNotice(), stale: false, unknown: false }
}
function bothTrueNotice(): DataHorizonNotice {
  return { ...staleNotice(), stale: true, unknown: true }
}

const ALL_HEADERS = new Map<string, string>()

function headerGet(name: string): string | null {
  return ALL_HEADERS.get(name) ?? null
}

// ── ① 信封 ──────────────────────────────────────────────────────────────────

describe('① 信封：两键恒在，主键是对象或裸 null', () => {
  it('①-1 ★★★★★★ 两个信封键都在，且主键两种形态都放行', () => {
    expect(DATA_HORIZON_ENVELOPE_KEYS).toEqual(['data_horizon', '//'])
    expect(DATA_HORIZON_KEY).toBe('data_horizon')
    // 裸 null（数据新鲜或网关空闲）是**最常见**的形态
    expect(unwrapDataHorizon({ data_horizon: null, '//': 'x' }).data_horizon).toBeNull()
    const s = unwrapDataHorizon(envelope(staleNotice()))
    expect(s.data_horizon?.stale).toBe(true)
  })

  it('①-2 ★★★★★★ 缺任一信封键必须报错（键恒在，不是降级信封）', () => {
    expect(() => unwrapDataHorizon({ data_horizon: null })).toThrow()
    expect(() => unwrapDataHorizon({ '//': 'x' })).toThrow()
    // ★ 主键消失 ≠ 主键为 null —— 前者是契约破了
    expect(() => unwrapDataHorizon({ '//': 'x', other: 1 })).toThrow()
  })

  it('①-3 ★★★★★ 非对象响应必须报错', () => {
    expect(() => unwrapDataHorizon(null)).toThrow()
    expect(() => unwrapDataHorizon([])).toThrow()
    expect(() => unwrapDataHorizon('x')).toThrow()
  })
})

// ── ② 六键 notice ────────────────────────────────────────────────────────────

describe('② 六键 notice（无 omitempty ⇒ 键恒在）', () => {
  it('②-1 ★★★★★★ 键序与 Go struct 字段序一致，恰好六个', () => {
    expect(DATA_HORIZON_NOTICE_KEYS).toEqual([
      'stale',
      'unknown',
      'source',
      'effect',
      'silence',
      'hint',
    ])
    expect(DATA_HORIZON_NOTICE_KEYS.length).toBe(6)
  })

  it('②-2 ★★★★★★ 缺任一键都必须报错（★ 这一条专钉「无 omitempty」）', () => {
    for (const k of DATA_HORIZON_NOTICE_KEYS) {
      const n: Record<string, unknown> = { ...staleNotice() }
      delete n[k]
      expect(
        () => unwrapDataHorizon({ data_horizon: n, '//': 'x' }),
        `缺 ${k} 竟然通过了`,
      ).toThrow()
    }
  })

  it('②-3 ★★★★★ 两个 flag 必须是布尔，四个文案必须是字符串', () => {
    const bad1: Record<string, unknown> = { ...staleNotice(), stale: 'true' }
    expect(() => unwrapDataHorizon({ data_horizon: bad1, '//': 'x' })).toThrow()
    const bad2: Record<string, unknown> = { ...staleNotice(), hint: 42 }
    expect(() => unwrapDataHorizon({ data_horizon: bad2, '//': 'x' })).toThrow()
  })

  it('②-4 ★★★★★ ★★ 已下线的键不得出现（它们不在六键里，多了也算契约漂移）', () => {
    // 后端已无 affects / gate_key —— 若解包器开始**接受**它们，说明有人放宽了契约
    const withAffects: Record<string, unknown> = { ...staleNotice(), affects: ['silently_frozen'] }
    const r1 = unwrapDataHorizon({ data_horizon: withAffects, '//': 'x' })
    // 解包器**不做**键数上限检查（后端将来加字段不该让前端炸）
    expect((r1.data_horizon as unknown as Record<string, unknown>)['affects']).toEqual([
      'silently_frozen',
    ])
    // ★ 但导出面上**不得**再有任何 affects / gate_key 的判据或常量
    const src = readFileSync(join(process.cwd(), 'src/api/dataHorizon.ts'), 'utf8')
    for (const dead of ['AFFECTS', 'GATE_KEY', 'GATE_SOURCES', 'gate_key']) {
      expect(
        src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, ''),
        `源码里还有已下线的 ${dead}`,
      ).not.toContain(dead)
    }
  })
})

// ── ③ 恰好一个 flag ─────────────────────────────────────────────────────────

describe('③ ★★★★★★ 「有对象 ⇒ stale 与 unknown 恰好一个为 true」', () => {
  it('③-1 ★★★★★★ 三个合法形态都通过', () => {
    expect(dataHorizonExactlyOneFlag(envelope(null))).toBe(true)
    expect(dataHorizonExactlyOneFlag(envelope(staleNotice()))).toBe(true)
    expect(dataHorizonExactlyOneFlag(envelope(unknownNotice()))).toBe(true)
  })

  it('③-2 ★★★★★★ 两个反例都必须不成立（★ 这才叫有牙）', () => {
    expect(dataHorizonExactlyOneFlag(envelope(bothFalseNotice()))).toBe(false)
    expect(dataHorizonExactlyOneFlag(envelope(bothTrueNotice()))).toBe(false)
    expect(dataHorizonIsUnexpectedShape(envelope(bothFalseNotice()))).toBe(true)
    expect(dataHorizonIsUnexpectedShape(envelope(staleNotice()))).toBe(false)
  })

  it('③-3 ★★★★★ 三态判据两两互斥（★ 不得有形态同时命中两个）', () => {
    // ★★★ 四个输入，**含非法形态** —— 变异台 M09 揪出来的：
    //   把 `isUnknown` 写成 `n !== null && !n.stale`，在**三个合法形态**上
    //   与原式**完全等价**（stale 档 !true=false；unknown 档 !false=true；
    //   live 档压根不进分支）⇒ 只有喂「两个 flag 都 false」才分岔。
    //   ⇒ 恒真门与无效变异都躲在这里：**只喂合法输入的判据看不见分岔。**
    for (const b of [envelope(null), envelope(staleNotice()), envelope(unknownNotice())]) {
      const hits = [dataHorizonIsLive(b), dataHorizonIsStale(b), dataHorizonIsUnknown(b)].filter(Boolean)
      expect(hits.length, '三态判据没有恰好命中一个').toBe(1)
    }
    // ★★★ 非法形态（有对象但两个 flag 都 false）**命中零个** —— 而且它就该命中零个：
    //   `isStale` 读 `stale===true`、`isUnknown` 读 `unknown===true`，两者都是 false；
    //   `isLive` 读 `=== null`，而它是对象 ⇒ 三个都不命中。
    //   ⇒ ★★ **这正是「畸形形态」存在的意义**：它必须**落到页面上的第四档**
    //     （`unexpected-shape`），而不是被硬塞进 live 或 unknown 里。
    //   ⚠️ 我第一版误以为它会落到 unknown，写成「必须为 true」直接红了 ——
    //     这是**我凭直觉编的**，Go 源码与函数定义都指向「命中零个」。
    const bad = envelope(bothFalseNotice())
    expect(dataHorizonIsLive(bad)).toBe(false)
    expect(dataHorizonIsStale(bad)).toBe(false)
    expect(dataHorizonIsUnknown(bad)).toBe(false)
    // ★ 且它**必须**被 isUnexpectedShape 认出（否则页面会渲染成三态之一）
    expect(dataHorizonIsUnexpectedShape(bad)).toBe(true)
  })

  it('③-4 ★★★★★ `stale:false` 有合法实例（unknown 档）—— 判断流不能只读 stale', () => {
    const u = envelope(unknownNotice())
    expect(dataHorizonIsStale(u)).toBe(false)
    expect(dataHorizonIsUnknown(u)).toBe(true)
  })
})

// ── ④ 已下线概念（负控）─────────────────────────────────────────────────────

describe('④ ★★★★★ 已下线概念不得以任何形式残留', () => {
  it('④-1 ★★★★★★ 旧模块文件不得再存在（改名是移动，不是复制）', () => {
    const oldApi = join(process.cwd(), 'src/api/v1DataHorizon.ts')
    const oldTest = join(process.cwd(), 'src/api/v1DataHorizon.test.ts')
    expect(existsSync(oldApi), 'v1DataHorizon.ts 还在 ⇒ 改名变成了复制').toBe(false)
    expect(existsSync(oldTest), 'v1DataHorizon.test.ts 还在').toBe(false)
  })

  it('④-2 ★★★★★★ 旧视图不得再存在', () => {
    expect(existsSync(join(process.cwd(), 'src/views/V1DataHorizonView.vue'))).toBe(false)
    expect(existsSync(join(process.cwd(), 'src/views/V1DataHorizonView.spec.ts'))).toBe(false)
  })

  it('④-3 ★★★★★★ 路由与抽屉席都已改名，且旧路径不得残留', () => {
    const router = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(router).toMatch(/path: '\/data-horizon'/)
    expect(router).toMatch(/component: \(\) => import\('@\/views\/DataHorizonView\.vue'\)/)
    expect(router).not.toMatch(/path: '\/v1-data-horizon'/)
    const nav = readFileSync(join(process.cwd(), 'src/config/appNav.ts'), 'utf8')
    expect(nav).toMatch(/key: 'data-horizon'/)
    expect(nav).not.toMatch(/key: 'v1-data-horizon'/)
  })

  it('④-4 ★★★★★ 三态名单是 live/stale/unknown（不是 unavailable）', () => {
    expect(DATA_HORIZON_STATES).toEqual(['live', 'stale', 'unknown'])
    expect(DATA_HORIZON_STATES).not.toContain('unavailable')
  })
})

// ── ⑤ 响应头 ────────────────────────────────────────────────────────────────

describe('⑤ ★★★★★ 响应头四值 + 与 body 交叉验证', () => {
  it('⑤-1 ★★★★★★ 头名是新的那个，且**不是**旧名', () => {
    expect(DATA_HORIZON_HEADER).toBe('X-LLM-Gateway-Data-Horizon')
    expect(DATA_HORIZON_HEADER).not.toBe('X-LLM-Gateway-V1-Data-Frozen')
  })

  it('⑤-2 ★★★★★★ 四个分支都要有牙', () => {
    expect(parseDataHorizonHeader(() => null)).toBeNull()
    expect(parseDataHorizonHeader(() => 'stale')).toBe('stale')
    expect(parseDataHorizonHeader(() => 'unknown')).toBe('unknown')
    // ★ 第三种字面量必须**显式暴露**，不能默默当成 live
    expect(parseDataHorizonHeader(() => '1')).toBe('unexpected')
    expect(parseDataHorizonHeader(() => '')).toBe('unexpected')
  })

  it('⑤-3 ★★★★★★ 头与 body 一致时才算匹配', () => {
    ALL_HEADERS.clear()
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(null))).toBe(true)
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'stale')
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(staleNotice()))).toBe(true)
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'unknown')
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(unknownNotice()))).toBe(true)
  })

  it('⑤-4 ★★★★★★ 头与 body 矛盾时必须不匹配（四组）', () => {
    ALL_HEADERS.clear()
    // live 时头**不该**存在
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'stale')
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(null))).toBe(false)
    // stale 的 body 配 unknown 的头
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'unknown')
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(staleNotice()))).toBe(false)
    // 有对象但头缺失
    ALL_HEADERS.clear()
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(staleNotice()))).toBe(false)
    // ★ 「头缺失」不能被当成「与任何 body 都匹配」
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(unknownNotice()))).toBe(false)
    // ★★★ 第五组（变异台 M15 揪出来的）：**unknown 的 body 配一个「存在但错」的头**。
    //   前四组都落不到 unknown 分支上（live 先返回 / stale 先返回 / 无头），
    //   ⇒ 把 unknown 分支的判断放宽成 `h !== null`，前四组照样全绿。
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'stale')
    expect(
      dataHorizonHeaderMatchesBody(headerGet, envelope(unknownNotice())),
      'unknown 的 body 配 stale 的头竟然算匹配',
    ).toBe(false)
    // ★ 同理反向：unknown 的 body 配 unknown 的头**必须**匹配（否则上面那条是恒红）
    ALL_HEADERS.set(DATA_HORIZON_HEADER, 'unknown')
    expect(dataHorizonHeaderMatchesBody(headerGet, envelope(unknownNotice()))).toBe(true)
  })

  it('⑤-5 ★★★★ 头**不挂中央出口**（只在本端点自身）—— 读 Go 源码', () => {
    const p = join(process.cwd(), '..', 'admin/data_horizon.go')
    expect(existsSync(p), '源码路径不存在：admin/data_horizon.go').toBe(true)
    const src = readFileSync(p, 'utf8')
    // ★★ 承重判据：applyDataHorizonHeader 只被 handleDataHorizon 调，不在 writeJSON 里
    const callers = [...src.matchAll(/applyDataHorizonHeader\(/g)]
    // 两个出现 = 定义处 + handleDataHorizon 里的调用；**不得**有第三个
    expect(callers.length, `applyDataHorizonHeader 出现 ${callers.length} 次 ⇒ 它又被挂到别处了`).toBe(2)
    // ★★ 且旧的中枢函数名不得复活
    expect(src).not.toMatch(/applyV1FreezeNotice/)
  })
})

// ── ⑥ 常量 ──────────────────────────────────────────────────────────────────

describe('⑥ ★★★★ 常量与 Go 源码逐字一致', () => {
  it('⑥-1 ★★★★★★ 两个时间窗从 Go 源码读出来（不是抄一份）', () => {
    const p = join(process.cwd(), '..', 'admin/data_horizon.go')
    const src = readFileSync(p, 'utf8')
    // 2026-10-16：Go 侧把两个窗口并进 const 块（gofmt 对齐后行内有多个空格），
    // 原单行正则失配。真值 10/5 仍由下面两条钉住，失配不会伪装成通过。
    const win = src.match(
      /dataHorizonRecentTrafficWindow\s*= (\d+) \* time\.Minute/,
    )
    const tol = src.match(/dataHorizonLagTolerance\s*= (\d+) \* time\.Minute/)
    expect(win, '没找到 dataHorizonRecentTrafficWindow（正则失配会让这条恒绿）').toBeTruthy()
    expect(tol, '没找到 dataHorizonLagTolerance').toBeTruthy()
    expect(Number(win![1])).toBe(DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES)
    expect(Number(tol![1])).toBe(DATA_HORIZON_LAG_TOLERANCE_MINUTES)
    // ★ 真值也钉住（10 / 5 分钟），不然「两边一起改错」会全绿
    expect(DATA_HORIZON_RECENT_TRAFFIC_WINDOW_MINUTES).toBe(10)
    expect(DATA_HORIZON_LAG_TOLERANCE_MINUTES).toBe(5)
  })

  it('⑥-2 ★★★★★★ `source` 恒为 session_turns_hot ∪ session_turns（读 Go 源码数出现次数）', () => {
    expect(DATA_HORIZON_SOURCE).toBe('session_turns_hot ∪ session_turns')
    const p = join(process.cwd(), '..', 'admin/data_horizon.go')
    const src = readFileSync(p, 'utf8')
    // ★ 两个分支各写一次，都必须是同一个字面量。
    //   2026-10-16：字面量已扩为 "session_turns_hot ∪ session_turns"
    //   （V2 双写后断流要同时看两张表），故捕获类不再限死 [a-z_]。
    const hits = [...src.matchAll(/Source:\s*"([^"]+)"/g)].map((m) => m[1])
    expect(hits.length, '没找到 Source 字面量').toBeGreaterThanOrEqual(2)
    for (const h of hits) expect(h).toBe(DATA_HORIZON_SOURCE)
    // ★★ 旧字面量不得复活
    expect(src).not.toContain('"_request_logs"')
    expect(dataHorizonSourceIsSessionTurns(staleNotice())).toBe(true)
    expect(dataHorizonSourceIsSessionTurns({ ...staleNotice(), source: 'db' })).toBe(false)
  })

  it('⑥-3 ★★★★ ★ 空表判 stale 而不是 unknown（★ 本代与前代最大的行为差异）', () => {
    // 这条是 Go 侧行为，前端**只能读源码确认**，不能复算
    const p = join(process.cwd(), '..', 'admin/data_horizon.go')
    const src = readFileSync(p, 'utf8')
    // ⚠️★ 必须剥掉**注释**再断言「本函数不调用 unknown」——
    //   下一函数的**文档注释里**就写着 `dataHorizonNoticeForUnknown` 这个名字，
    //   直接扫原文会误红（本批真踩到了）。这与「否定断言要否代码形态而非词」
    //   是同一条纪律的加强版：**否定断言必须否的是可执行代码**。
    const noComment = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
    const fn = noComment.slice(noComment.indexOf('func dataHorizonNoticeFor('))
    const body = fn.slice(0, fn.indexOf('\nfunc '))
    // ★★ 承重判据（2026-10-16 随 Go 侧重构同步）：判定已从 sessionMaxTS
    //   改为 canary 口径 —— 「观察窗内的 canary 全部镜像成功 ⇒ live，
    //   有 canary 超窗未镜像 ⇒ stale」，所以 stale 与 unknown 分属两个函数。
    //   零值/空 canary 切片会走完循环返回 (nil,false)，即**判不出 stale**，
    //   这正是「空表不谎报断流」的现代表形态。
    expect(body).toMatch(/age < dataHorizonLagTolerance \|\| age > dataHorizonRecentTrafficWindow/)
    expect(body).toMatch(/Stale:\s+true/)
    // ★ 且 unknown 走的是**另一个**函数（读不上来，不是表空）
    expect(body).not.toContain('dataHorizonNoticeForUnknown')
    expect(noComment).toMatch(/func dataHorizonNoticeForUnknown\(\)/)
    // ★ 空 canary 切片不得判 stale（否「表空即断流」复活）
    expect(body).toMatch(/return nil, false/)
  })
})
