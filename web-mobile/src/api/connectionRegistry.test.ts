import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  CLOSED_HISTORY_LIMIT,
  REGISTRY_NOT_WIRED_MESSAGE,
  MISSING_REQUEST_ID_MESSAGE,
  REQUEST_NOT_REGISTERED_MESSAGE,
  GO_ZERO_TIME,
  SNAPSHOT_ALWAYS_KEYS,
  SNAPSHOT_OPTIONAL_KEYS,
  REGISTRY_LIST_KEYS,
  fetchConnectionRegistryList,
  fetchConnectionByRequestId,
  unwrapConnectionRegistryList,
  unwrapConnectionSnapshot,
  registryClosedIsAbsent,
  registryClosedRows,
  registryClosedIsImpossibleEmpty,
  registryCountAgrees,
  registryUtilization,
  snapshotNeverWroteFrame,
  snapshotHasFrames,
  snapshotFrameFieldsAgree,
  snapshotCloseReason,
  snapshotIsClosed,
  snapshotTenantId,
  snapshotClientType,
  snapshotProtocol,
  registryLiveSortedByRegisteredAt,
  registryClosedPossiblyTruncated,
  registryRowIsNotFetchableById,
  registryNotWired,
} from '@/api/connectionRegistry'
import type { ConnectionSnapshot, ConnectionRegistryListResponse } from '@/api/connectionRegistry'

/**
 * 流式连接注册台两条只读端点的不变量（2026-10-07，第七十二批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **`live` 恒数组，`closed` 恒「null 或非空数组」，绝不会是 `[]`。**
 *      `List()` 用 `make(..., 0, ...)` ⇒ 恒 `[]`；
 *      `ClosedHistory` 空时 `return nil` ⇒ `null`，非空时 `n ≥ 1` ⇒ 非空数组。
 *      ⇒ **同一份载荷里两个数组键的 nil 编码相反**，且 `[]` 后端永不产生。
 *   ② ★★★★★ **4 个 omitempty 条件键 + 1 个恒发布尔** —— 与第七十/七十一批
 *      那两条端点的 `degraded` 恒发**恰好相反** ⇒ 判据不能跨族照抄。
 *   ③ ★★★★★ **注释与实现矛盾**：`SetConnectionRegistry(nil)` 直接 `return`
 *      （connection_registry.go:32-37）⇒ 装配后无法退回 503。
 *   ④ ★★★★ **`Lookup` 只查活跃表** ⇒ `closed` 里的条目用详情端点必然 404。
 *   ⑤ ★★★★ **Go 零值时间会真的出现**：`last_frame_at` 对未发帧连接是
 *      `"0001-01-01T00:00:00Z"` ⇒ 不能当有效时间渲染。
 *   ⑥ ★★★ **`live` 顺序无保证**（`List()` 注释自陈 map walk is unordered）。
 *   ⑦ ★★★ **上限 50 不回显**（`ClosedHistory(50)` 写死）。
 *
 * 桌面对照两处不要抄：`web/src/api/connection-registry.ts:12-23` 把 5 个恒在键
 * 标成可选；`:28` 把可能是 `null` 的 `closed` 标成必定数组。
 *
 * 夹具纪律：数字与字面量一律硬写后端源码值（50、六个恒在键、四个条件键、
 * Go 零值时间），**不用**被测常量造数据。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

function ok(payload: unknown): void {
  reqMock.mockResolvedValue(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：domains/streaming/connection_registry.go:151-163 逐字抄
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * 完整快照：6 个恒在键 + 4 个条件键全在。
 * @param frames 帧计数
 * @param lastFrame 最后帧时间；传 0 表示「从未发帧」⇒ 落 Go 零值时间
 */
function snap(
  requestId: string,
  frames: number,
  bytes: number,
  closed: boolean,
  over: Partial<ConnectionSnapshot> = {},
): Record<string, unknown> {
  return {
    request_id: requestId,
    protocol: 'sse',
    client_type: 'web',
    tenant_id: 'default',
    registered_at: '2026-10-07T10:00:00Z',
    last_frame_at: frames > 0 ? '2026-10-07T10:05:00Z' : GO_ZERO_TIME,
    frames_written: frames,
    bytes_written: bytes,
    close_reason: closed ? 'stream_end' : '',
    closed,
    ...over,
  }
}

/**
 * 只有 6 个恒在键、4 个条件键**全部缺失**的快照。
 * ★ 这是 `omitempty` 全命中时的真实形状。
 */
function snapMinimal(requestId: string, closed: boolean): Record<string, unknown> {
  return {
    request_id: requestId,
    registered_at: '2026-10-07T10:00:00Z',
    last_frame_at: GO_ZERO_TIME,
    frames_written: 0,
    bytes_written: 0,
    closed,
  }
}

/** `connection_registry.go:54-59` 有注销历史时的形状（`closed` 非空数组）。 */
function listWithClosed(over: Partial<ConnectionRegistryListResponse> = {}): Record<string, unknown> {
  return {
    live: [snap('req-live-1', 5, 512, false)],
    live_count: 1,
    capacity: 500,
    closed: [snap('req-closed-1', 9, 900, true)],
    ...over,
  }
}

/** 从无注销记录时的形状（`ClosedHistory` 返回 nil ⇒ JSON null）。 */
function listNoHistory(over: Partial<ConnectionRegistryListResponse> = {}): Record<string, unknown> {
  return {
    live: [],
    live_count: 0,
    capacity: 500,
    closed: null,
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('注销历史上限是 50', () => {
    // connection_registry.go:58 `reg.ClosedHistory(50)` 写死
    expect(CLOSED_HISTORY_LIMIT).toBe(50)
  })

  it('503 的 message 是 connection registry not wired', () => {
    // :50 / :68
    expect(REGISTRY_NOT_WIRED_MESSAGE).toBe('connection registry not wired')
  })

  it('400 与 404 的 message 各有原文', () => {
    // :73 / :78
    expect(MISSING_REQUEST_ID_MESSAGE).toBe('missing request_id')
    expect(REQUEST_NOT_REGISTERED_MESSAGE).toBe('request not registered')
  })

  it('Go 零值时间是 0001-01-01T00:00:00Z', () => {
    expect(GO_ZERO_TIME).toBe('0001-01-01T00:00:00Z')
  })

  it('恒在键恰好六个', () => {
    // ConnectionSnapshot 里不带 omitempty 的那些
    expect(SNAPSHOT_ALWAYS_KEYS).toHaveLength(6)
    expect([...SNAPSHOT_ALWAYS_KEYS].sort()).toEqual([
      'bytes_written', 'closed', 'frames_written', 'last_frame_at',
      'registered_at', 'request_id',
    ])
  })

  it('条件键恰好四个且与恒在键不相交', () => {
    // protocol / client_type / tenant_id / close_reason 都带 omitempty
    expect([...SNAPSHOT_OPTIONAL_KEYS].sort()).toEqual([
      'client_type', 'close_reason', 'protocol', 'tenant_id',
    ])
    const always = new Set<string>(SNAPSHOT_ALWAYS_KEYS)
    expect(SNAPSHOT_OPTIONAL_KEYS.filter((k) => always.has(k))).toEqual([])
  })

  it('list 容器键恰好四个', () => {
    expect([...REGISTRY_LIST_KEYS].sort()).toEqual([
      'capacity', 'closed', 'live', 'live_count',
    ])
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ① live 恒数组
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('list 解包：live 恒数组', () => {
  it('有注销历史时正常通过', () => {
    const r = unwrapConnectionRegistryList(listWithClosed())
    expect(r.live).toHaveLength(1)
    expect(r.closed).toHaveLength(1)
  })

  it('从无注销记录时正常通过', () => {
    const r = unwrapConnectionRegistryList(listNoHistory())
    expect(r.live).toEqual([])
    expect(r.closed).toBeNull()
  })

  it('live 为 null 时抛错', () => {
    // ★ List() 用 make(...,0,...) 永不返回 nil
    expect(() => unwrapConnectionRegistryList(listNoHistory({ live: null as never }))).toThrow(
      /live 不是数组/,
    )
  })

  it('live 不是数组时抛错', () => {
    expect(() => unwrapConnectionRegistryList(listNoHistory({ live: {} as never }))).toThrow(
      /live 不是数组/,
    )
  })

  it('live 项不是对象时抛错', () => {
    expect(() => unwrapConnectionRegistryList(listWithClosed({ live: ['x'] as never }))).toThrow(
      /live\[0\] 不是对象/,
    )
  })

  it('live_count 与 live 长度不一致时抛错', () => {
    // ★ :56 的 len(live) ⇒ 不等就说明载荷被换过
    expect(() => unwrapConnectionRegistryList(listNoHistory({ live_count: 3 }))).toThrow(/不一致/)
  })

  it('live_count 一致时判定为自洽', () => {
    const r = unwrapConnectionRegistryList(listWithClosed())
    expect(registryCountAgrees(r)).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ① closed 只可能是 null 或非空数组
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('list 解包：closed 不会是空数组', () => {
  it('closed 为 null 时通过', () => {
    expect(unwrapConnectionRegistryList(listNoHistory()).closed).toBeNull()
  })

  it('closed 为非空数组时通过', () => {
    const r = unwrapConnectionRegistryList(listWithClosed())
    expect(r.closed).toHaveLength(1)
  })

  it('closed 为空数组时抛错', () => {
    // ★★ 后端 ClosedHistory 走 nil 分支或 n≥1 的非空数组，[] 永不产生
    expect(() => unwrapConnectionRegistryList(listNoHistory({ closed: [] }))).toThrow(/空数组/)
  })

  it('closed 既不是数组也不是 null 时抛错', () => {
    expect(() => unwrapConnectionRegistryList(listNoHistory({ closed: 7 as never }))).toThrow(
      /不是数组也不是 null/,
    )
  })

  it('closed 项不是对象时抛错', () => {
    expect(() =>
      unwrapConnectionRegistryList(listWithClosed({ closed: [null] as never })),
    ).toThrow(/closed\[0\] 不是对象/)
  })

  it('从无注销记录判定为缺历史', () => {
    expect(registryClosedIsAbsent(unwrapConnectionRegistryList(listNoHistory()))).toBe(true)
  })

  it('有注销历史判定为不缺历史', () => {
    expect(registryClosedIsAbsent(unwrapConnectionRegistryList(listWithClosed()))).toBe(false)
  })

  it('closedRows 把 null 兜成空数组', () => {
    expect(registryClosedRows(unwrapConnectionRegistryList(listNoHistory()))).toEqual([])
  })

  it('closedRows 对非空数组原样返回', () => {
    const rows = registryClosedRows(unwrapConnectionRegistryList(listWithClosed()))
    expect(rows).toHaveLength(1)
  })

  it('空数组形态被标为不可能出现', () => {
    // 绕过解包器直接构造，验证二次防御
    const fake = { closed: [] } as unknown as ConnectionRegistryListResponse
    expect(registryClosedIsImpossibleEmpty(fake)).toBe(true)
  })

  it('null 形态不被标为不可能出现', () => {
    const fake = { closed: null } as unknown as ConnectionRegistryListResponse
    expect(registryClosedIsImpossibleEmpty(fake)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ② 恒在键 vs 条件键（与前两族相反）
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('snapshot 恒在键与条件键', () => {
  it('条件键全缺失时正常通过', () => {
    // ★ omitempty 全命中 ⇒ 这是合法形状，不是契约破坏
    const r = unwrapConnectionSnapshot(snapMinimal('req-1', false))
    expect(r.request_id).toBe('req-1')
    expect('protocol' in r).toBe(false)
  })

  it('缺 request_id 时抛错', () => {
    const s = snap('req-1', 1, 10, false)
    delete s.request_id
    expect(() => unwrapConnectionSnapshot(s)).toThrow(/request_id/)
  })

  it('缺 last_frame_at 时抛错', () => {
    // ★ 后端无 omitempty ⇒ 未发帧也给零值时间，而不是缺键
    const s = snap('req-1', 0, 0, false)
    delete s.last_frame_at
    expect(() => unwrapConnectionSnapshot(s)).toThrow(/last_frame_at/)
  })

  it('缺 frames_written 时抛错', () => {
    const s = snap('req-1', 0, 0, false)
    delete s.frames_written
    expect(() => unwrapConnectionSnapshot(s)).toThrow(/frames_written/)
  })

  it('缺 closed 时抛错', () => {
    // ★ closed 无 omitempty ⇒ 恒发
    const s = snap('req-1', 0, 0, false)
    delete s.closed
    expect(() => unwrapConnectionSnapshot(s)).toThrow(/closed/)
  })

  it('closed 不是布尔值时抛错', () => {
    expect(() => unwrapConnectionSnapshot(snap('req-1', 1, 10, 'yes' as never))).toThrow(
      /closed 不是布尔值/,
    )
  })

  it('六个恒在键一起缺失时报缺六', () => {
    expect(() => unwrapConnectionSnapshot(snapMinimal('req-1', false) && { protocol: 'sse' })).toThrow(
      /缺 6 个恒在键/,
    )
  })

  it('closed 为 false 时判为未关闭', () => {
    // ★ 与前两族的 degraded 不同：这一条**不是恒真**
    expect(snapshotIsClosed(unwrapConnectionSnapshot(snap('req-1', 1, 10, false)))).toBe(false)
  })

  it('closed 为 true 时判为已关闭', () => {
    expect(snapshotIsClosed(unwrapConnectionSnapshot(snap('req-1', 1, 10, true)))).toBe(true)
  })

  it('已关闭条目判为不能用详情端点取', () => {
    // ★★ Lookup 只查活跃表 ⇒ closed 的行必然 404
    expect(registryRowIsNotFetchableById(unwrapConnectionSnapshot(snap('r', 1, 1, true)))).toBe(true)
  })

  it('活跃条目判为可以用详情端点取', () => {
    expect(registryRowIsNotFetchableById(unwrapConnectionSnapshot(snap('r', 1, 1, false)))).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑤ Go 零值时间
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('零值时间与帧计数的自洽', () => {
  it('零帧配零值时间时判为自洽', () => {
    const s = unwrapConnectionSnapshot(snap('req-1', 0, 0, false))
    expect(snapshotFrameFieldsAgree(s)).toBe(true)
  })

  it('有帧配真实时间时判为自洽', () => {
    const s = unwrapConnectionSnapshot(snap('req-1', 5, 512, false))
    expect(snapshotFrameFieldsAgree(s)).toBe(true)
  })

  it('零帧配真实时间时判为不自洽', () => {
    // ★ 这是能抓出自相矛盾载荷的判据
    const s = unwrapConnectionSnapshot(snap('req-1', 0, 0, false, { last_frame_at: '2026-10-07T10:05:00Z' }))
    expect(snapshotFrameFieldsAgree(s)).toBe(false)
  })

  it('零值时间判定为从未发帧', () => {
    expect(snapshotNeverWroteFrame(unwrapConnectionSnapshot(snap('req-1', 0, 0, false)))).toBe(true)
  })

  it('真实时间判定为发过帧', () => {
    expect(snapshotNeverWroteFrame(unwrapConnectionSnapshot(snap('req-1', 5, 512, false)))).toBe(false)
  })

  it('帧计数大于零判定为有帧', () => {
    expect(snapshotHasFrames(unwrapConnectionSnapshot(snap('req-1', 5, 512, false)))).toBe(true)
  })

  it('帧计数为零判定为无帧', () => {
    expect(snapshotHasFrames(unwrapConnectionSnapshot(snap('req-1', 0, 0, false)))).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 条件键的取值判定
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('条件键取值判定', () => {
  it('close_reason 有值时返回该值', () => {
    expect(snapshotCloseReason(unwrapConnectionSnapshot(snap('r', 1, 1, true)))).toBe('stream_end')
  })

  it('close_reason 缺键时返回 null', () => {
    expect(snapshotCloseReason(unwrapConnectionSnapshot(snapMinimal('r', false)))).toBeNull()
  })

  it('close_reason 为空串时返回 null', () => {
    // ★ closed: true 也不保证有 reason（snapshot(false, "") 那条路）
    expect(snapshotCloseReason(unwrapConnectionSnapshot(snap('r', 1, 1, true, { close_reason: '' })))).toBeNull()
  })

  it('tenant_id 有值时返回该值', () => {
    expect(snapshotTenantId(unwrapConnectionSnapshot(snap('r', 1, 1, false)))).toBe('default')
  })

  it('tenant_id 缺键时返回 null', () => {
    expect(snapshotTenantId(unwrapConnectionSnapshot(snapMinimal('r', false)))).toBeNull()
  })

  it('tenant_id 为空串时返回 null', () => {
    expect(snapshotTenantId(unwrapConnectionSnapshot(snap('r', 1, 1, false, { tenant_id: '' })))).toBeNull()
  })

  it('client_type 缺键时返回 null', () => {
    expect(snapshotClientType(unwrapConnectionSnapshot(snapMinimal('r', false)))).toBeNull()
  })

  it('protocol 缺键时返回 null', () => {
    expect(snapshotProtocol(unwrapConnectionSnapshot(snapMinimal('r', false)))).toBeNull()
  })

  it('protocol 有值时返回该值', () => {
    expect(snapshotProtocol(unwrapConnectionSnapshot(snap('r', 1, 1, false)))).toBe('sse')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑥ live 顺序无保证
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('live 排序', () => {
  it('按注册时间升序排好', () => {
    const d = listNoHistory({
      live: [
        snap('b', 1, 10, false, { registered_at: '2026-10-07T12:00:00Z' }),
        snap('a', 1, 10, false, { registered_at: '2026-10-07T08:00:00Z' }),
      ] as never,
      live_count: 2,
    })
    const sorted = registryLiveSortedByRegisteredAt(unwrapConnectionRegistryList(d))
    expect(sorted.map((s) => s.request_id)).toEqual(['a', 'b'])
  })

  it('排序不改动原数组', () => {
    const d = listNoHistory({
      live: [
        snap('b', 1, 10, false, { registered_at: '2026-10-07T12:00:00Z' }),
        snap('a', 1, 10, false, { registered_at: '2026-10-07T08:00:00Z' }),
      ] as never,
      live_count: 2,
    })
    const r = unwrapConnectionRegistryList(d)
    registryLiveSortedByRegisteredAt(r)
    expect(r.live[0]!.request_id).toBe('b')
  })

  it('单条序列排序后不变', () => {
    const r = unwrapConnectionRegistryList(listWithClosed())
    expect(registryLiveSortedByRegisteredAt(r)).toHaveLength(1)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * ⑦ 上限 50 静默截断
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('注销历史静默截断', () => {
  it('满 50 条时提示可能截断', () => {
    // 夹具硬写 50 条（后端 ClosedHistory(50) 的字面量）
    const closed = Array.from({ length: 50 }, (_, i) =>
      snap(`req-${i}`, 1, 10, true),
    )
    const r = unwrapConnectionRegistryList(listWithClosed({ closed: closed as never }))
    expect(r.closed).toHaveLength(50)
    expect(registryClosedPossiblyTruncated(r)).toBe(true)
  })

  it('49 条时判定未触上限', () => {
    // ★ 负控
    const closed = Array.from({ length: 49 }, (_, i) => snap(`req-${i}`, 1, 10, true))
    const r = unwrapConnectionRegistryList(listWithClosed({ closed: closed as never }))
    expect(r.closed).toHaveLength(49)
    expect(registryClosedPossiblyTruncated(r)).toBe(false)
  })

  it('缺历史时判定未触上限', () => {
    expect(registryClosedPossiblyTruncated(unwrapConnectionRegistryList(listNoHistory()))).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 水位与 503
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('注册台水位', () => {
  it('capacity 为正时算出利用率', () => {
    const r = unwrapConnectionRegistryList(listWithClosed({ capacity: 500 }))
    expect(registryUtilization(r)).toBeCloseTo(1 / 500)
  })

  it('capacity 为零时返回 null', () => {
    // ★ 分母为 0 时无意义，不是 0%
    const r = unwrapConnectionRegistryList(listWithHistory({ capacity: 0 }))
    expect(registryUtilization(r)).toBeNull()
  })

  it('满载时利用率是一', () => {
    const r = unwrapConnectionRegistryList(listWithClosed({ capacity: 1 }))
    expect(registryUtilization(r)).toBe(1)
  })
})

function listWithHistory(over: Partial<ConnectionRegistryListResponse> = {}) {
  return listWithClosed(over)
}

describe('503 与空列表是两种失败', () => {
  it('503 判为未装配', () => {
    expect(registryNotWired(503)).toBe(true)
  })

  it('200 不判为未装配', () => {
    expect(registryNotWired(200)).toBe(false)
  })

  it('404 不判为未装配', () => {
    expect(registryNotWired(404)).toBe(false)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 形状守卫
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('载荷形状守卫', () => {
  it('数组形状抛错', () => {
    expect(() => unwrapConnectionRegistryList([])).toThrow(/形状不符/)
  })

  it('null 响应抛错', () => {
    expect(() => unwrapConnectionRegistryList(null)).toThrow(/形状不符/)
  })

  it('dashboardapi 信封形状抛错', () => {
    expect(() => unwrapConnectionRegistryList({ success: true, timestamp: 1 })).toThrow(/信封/)
  })

  it('缺 live 但有 live_count 时反向报错', () => {
    expect(() => unwrapConnectionRegistryList({ live_count: 1, capacity: 5, closed: null })).toThrow(
      /片段形状/,
    )
  })

  it('缺 capacity 时抛错', () => {
    const d = listNoHistory()
    delete d.capacity
    expect(() => unwrapConnectionRegistryList(d)).toThrow(/capacity/)
  })

  it('详情端包拿到 list 载荷时反向报错', () => {
    expect(() => unwrapConnectionSnapshot(listWithClosed())).toThrow(/list 的载荷形状/)
  })

  it('详情端包数组形状抛错', () => {
    expect(() => unwrapConnectionSnapshot([])).toThrow(/形状不符/)
  })

  it('详情端包缺恒在键抛错', () => {
    expect(() => unwrapConnectionSnapshot({ request_id: 'r' })).toThrow(/恒在键/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * fetch
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('fetch 打到正确 URL', () => {
  it('列表端点打到无参数的固定路径', async () => {
    ok(listWithClosed())
    await fetchConnectionRegistryList()
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/connection-registry')
  })

  it('详情端点把 request_id 放进路径段并转义', async () => {
    ok(snap('req/1', 1, 10, false))
    await fetchConnectionByRequestId('req/1')
    expect(reqMock.mock.calls[0]![1]).toBe('/api/admin/connection-registry/req%2F1')
  })

  it('列表 fetch 会过解包器', async () => {
    ok(listWithClosed({ closed: [] }))
    await expect(fetchConnectionRegistryList()).rejects.toThrow(/空数组/)
  })

  it('详情 fetch 会过解包器', async () => {
    const s = snap('r', 1, 10, false)
    delete s.closed
    ok(s)
    await expect(fetchConnectionByRequestId('r')).rejects.toThrow(/closed/)
  })
})