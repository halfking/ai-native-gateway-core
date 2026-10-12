import { req, type RequestOptions } from './client'

/**
 * connectionRegistry.ts — 流式连接注册台两条**只读**端点（2026-10-07，第七十二批）。
 *
 * GET /api/admin/connection-registry           → admin/handler.go:1024
 * GET /api/admin/connection-registry/{id}      → admin/handler.go:1025
 *
 * 注册在 `admin(connection_registry.go:47 / :65)` ⇒ **admin 档**，tenant_admin 可用
 * ⇒ 抽屉席**不设** `requiresRole`。
 * ⚠️ 但桌面 `web/src/router.ts:284` 与 `web/src/config/appNav.ts:195` 把这条路由
 *   标成了 `requiresSuper: true` / `super: true` —— **前端比后端严**。
 *   按纪律**以可执行的注册为准**（`admin(...)`），不按前端标记判权限。
 *
 * 数据源是进程内的 `domains/streaming.ConnectionRegistry`
 * （`cmd/gateway/main.go` 经 `SetConnectionRegistry` 装配），
 * 只暴露元数据，**不含** SSE 帧正文或凭据（`connection_registry.go:17-19` §13.5）。
 *
 * ## ★★★★★ 本族最要紧的七件事
 *
 * (1) ★★★★★ **`live` 恒是数组，`closed` 恒不是 `null` 就是非空数组 —— 两者都不会是 `[]`。**
 *      ```go
 *      // List()：domains/streaming/connection_registry.go:473
 *      out := make([]ConnectionSnapshot, 0, len(entries))   // ⇒ 恒非 nil ⇒ JSON 恒 []
 *
 *      // ClosedHistory()：:483-487
 *      if n <= 0 || len(r.closed) == 0 {
 *          return nil                                      // ⇒ JSON 是 null，不是 []
 *      }
 *      ...
 *      out := make([]ConnectionSnapshot, n)                // n ≥ 1 ⇒ 恒非空数组
 *      ```
 *      ⇒ **`closed: []` 后端永不产生** ⇒ 客户端收到空数组就是契约漂了。
 *      ⇒ ★★ 这是本仓**第五种** nil 编码（已见：恒数组 / 键缺失 / 裸 null /
 *        omitempty 条件键 / 恒发布尔），而且**同一份载荷里两个数组键的编码相反**。
 *
 * (2) ★★★★★ **snapshot 有 4 个 omitempty 条件键 + 1 个恒发布尔，恰好是本仓的反面写法。**
 *      `domains/streaming/connection_registry.go:151-163`：
 *      ```go
 *      RequestID     string    `json:"request_id"`              // 恒在
 *      Protocol      string    `json:"protocol,omitempty"`      // ★ 条件键
 *      ClientType    string    `json:"client_type,omitempty"`   // ★ 条件键
 *      TenantID      string    `json:"tenant_id,omitempty"`     // ★ 条件键
 *      RegisteredAt  time.Time `json:"registered_at"`           // 恒在
 *      LastFrameAt   time.Time `json:"last_frame_at"`           // 恒在
 *      FramesWritten uint64    `json:"frames_written"`          // 恒在
 *      BytesWritten  uint64    `json:"bytes_written"`           // 恒在
 *      CloseReason   string    `json:"close_reason,omitempty"`  // ★ 条件键
 *      Closed        bool      `json:"closed"`                  // ★ 恒发（无 omitempty）
 *      ```
 *      ⇒ 第七十/七十一批那两条端点的 `degraded` 是**恒发**，本族的 `protocol` /
 *        `client_type` / `tenant_id` / `close_reason` 是**条件键** ⇒ **判据不能跨族照抄**。
 *      ⇒ `closed` 恒发布尔 ⇒ 客户端**能**判别 live vs closed（条件是恒真的）。

 * (3) ★★★★★ **注释与实现矛盾：`SetConnectionRegistry(nil)` 不能解绑。**
 *      `connection_registry.go:32-37`：
 *      ```go
 *      // SetConnectionRegistry wires ... Pass nil to disable (endpoints return 503).
 *      func SetConnectionRegistry(reg *streaming.ConnectionRegistry) {
 *          if reg == nil {
 *              return          // ← 注释说「传 nil 可禁用」，代码是「传 nil 什么也不做」
 *          }
 *          connectionRegistry.Store(reg)
 *      }
 *      ```
 *      ⇒ 装配后**没有任何 API 能把端点退回 503** ⇒ 注释是错的，以实现为准。
 *
 * (4) ★★★★ **`Lookup` 只查活跃条目，已注销的取不到 —— 但「已注销」不能只看 `closed`。**
 *      `domains/streaming/connection_registry.go:454-465` 只看 `r.entries`（活跃表），
 *      不看 `r.closed`（历史环）⇒ **`closed` 列表里的条目用详情端点必然 404**。
 *      ⇒ 「列表里看得到、点进去 404」是**契约行为**，不是 bug。
 *      ⚠️★ 2026-10-08（第一百零七批）补充：这条**只对 `closed` 段成立**。
 *      `closed === true` 的行**也可能出现在 `live` 段**（见 (8)），
 *      而那一类**仍然留在 `r.entries` 里 ⇒ 详情端点查得到**。
 *      ⇒ ★★★ 单看 `closed` 布尔**分不出这两格**（这正是
 *      `registryRowIsNotFetchableById` 的盲区）⇒ 调用方必须**按行来自哪一段**判断，
 *      不能按 `closed` 字段判断。
 *
 * (5) ★★★★ **零值时间戳会真的出现。**
 *      `LastFrameAt` 是 `time.Time` 且无 omitempty ⇒ 从未写过帧的连接
 *      `last_frame_at` 是 **`"0001-01-01T00:00:00Z"`**（Go 零值时间的 RFC3339）。
 *      ⇒ 客户端**不能**把「键存在」当「有值」，也不能把它渲染成「1970 年」或异常。
 *
 * (6) ★★★ **`live` 的顺序没有保证 —— 而 `closed` 的顺序是保证的（两段相反）。**
 *      `List()` 的注释自陈「map walk is unordered, so callers sort as needed」（:467-471）
 *      ⇒ `live` 数组顺序随进程内的 map 迭代变化 ⇒ **不能靠顺序判稳定**，也不能靠它做 diff。
 *      ★★ 但 `ClosedHistory(50)` 是 `copy` + 显式 reverse（`:483-497`）⇒ **newest first，稳定**。
 *      ⇒ **同一份载荷里两段的顺序保证相反。**
 *
 * (7) ★★★★ **上限 50 不回显，而且它**不是**历史上限。**
 *      `connection_registry.go:58` 写死 `reg.ClosedHistory(50)` ⇒ 端点只暴露最近 50 条，
 *      响应里**没有任何字段**能说明「还有更早的」。
 *      ★★★★ 但**环形缓冲本身**是 `DefaultClosedHistoryDepth = 256`
 *      （`:65` / `:254`，`:355-356` 裁剪）⇒ **256 > 50，端点永远取不满缓冲区**。
 *      ⇒ ★★★ 所以「看到 50 行」的准确含义是「**缓冲区里至少还有 50 条，
 *        且可能还有更早的没被端点暴露**」，**不是**「更早的已经被丢了」。
 *      ⇒ ★★ UI 文案必须按这个口径写：**不能**说「仅显示最近 50 条，更早的已丢弃」。
 *      （真正的丢弃点在环形缓冲的 256 条，那是另一个阈值。）
 *
 * (8) ★★★★★★ **`live` 段里可能出现 `closed: true` 的行 —— 一个真实竞态窗口。**
 *      `snapshot(false, "")`（`:476-491`）看着像「List 里恒 false」，但它内部有覆写分支：
 *      ```go
 *      s := ConnectionSnapshot{ ..., Closed: closed, CloseReason: reason }
 *      if e.closed {            // ★ 会覆写上面两行（原码两字段行尾另有行内注释，
 *                               //   原样写进本块注释会被提前终止，故从略）
 *          s.Closed = true
 *          s.CloseReason = e.closeReason
 *      }
 *      ```
 *      而 `WriteFrame` 超时分支（`:417-441`）是**先标 closed、放开 entry 锁、再从 map 摘除**：
 *      ```go
 *      entry.closed = true                 // :419  先标
 *      entry.closeReason = "write_deadline"
 *      entry.mu.Unlock()                   // :421  锁放开  ← 窗口在这里
 *      r.mu.Lock()
 *      cur, ok := r.entries[requestID]
 *      if ok && cur == entry { delete(r.entries, requestID) }   // :433  才摘除
 *      ```
 *      ⇒ 在 `:421` 与 `:433` 之间，`List()` / `Lookup()` 拿得到这个 entry
 *      ⇒ **返回一行 `closed=true` + `close_reason="write_deadline"`，却出现在 `live` 段里。**
 *      ⇒ 推论三条：
 *        · `live_count` 会把它算进去（`:56` 的 `len(live)`）
 *          ⇒ 「`live_count === live.length`」恒真**不蕴含**「live 里都是活连接」。
 *        · **不能**按 `closed` 字段把行从 live 段搬到 closed 段 ——
 *          它还没进环形缓冲（`detach` 还没跑），搬走就是凭空造出「已归档」。
 *        · 判据**不能**断言「live 段行的 `closed` 恒 false」；
 *          要断言的话夹具必须发得出来（后端确实发得出来）⇒ 这是一条**要判**的格子。
 *      ★ 顺带：`Lookup` 走同一份 `snapshot`、同一个窗口 ⇒ 同理**可达**。

 *
 * ## 另注
 *
 * · 503 的 message 是 `"connection registry not wired"`（`:50`/`:68`）——
 *   「没装配」与「没数据」是**两种不同的失败**，不能都当成空列表渲染。
 * · `live_count` 是服务端算的 `len(live)`（`:56`）⇒ 与 `live.length` 恒等
 *   ⇒ 客户端可以**自查**（若不等说明中间有人改过载荷）。
 * · `capacity` 是配置上界，可能远大于 `live_count` ⇒ 两者之比是注册台水位。
 *   ★★ 2026-10-08（第一百零七批）补：`capacity` **恒 ≥ 4096，永不为 0** ——
 *     `NewConnectionRegistry(0, 0, 0)`（`cmd/gateway/main.go:818`）的三个 0
 *     都被默认值兜底（`connection_registry.go:241-243`）⇒ 装配处写的 0 不会变成 0。
 *     且 `r.capacity` 是构造期常量、进程内从不写 ⇒ 水位是有意义的。
 * · Get 端点的 `request_id` 来自 `r.PathValue`（`:71`）⇒ 路径段要 encodeURIComponent。
 *
 * ## 桌面对照：两处**不要抄**
 *
 * - `web/src/api/connection-registry.ts:12-23` 把 `registered_at` / `last_frame_at` /
 *   `frames_written` / `bytes_written` / `closed` 五个**恒在**键标成了可选（`?:`）
 *   ⇒ 暗示可能缺键，但后端五个都不带 omitempty。
 * - 同文件 `:28` 把 `closed: ConnectionSnapshot[]` 标成**必定是数组**
 *   ⇒ 而后端无历史时给的是 **`null`** ⇒ 按那个类型直接 `.map()`/`.length`
 *   会在「从无注销记录」时抛 `Cannot read properties of null`。
 */

/* ═══════════════════════════════════════════════════════════════════════════
 * 类型
 * ═══════════════════════════════════════════════════════════════════════════ */

/**
 * `domains/streaming/connection_registry.go:151-163` `ConnectionSnapshot`。
 *
 * ★ 恒在的 6 个键与条件键的 4 个键必须分开对待：
 * 恒在 ⇒ `frames_written` 之类的 0 是**真的 0 帧**，不是「没上报」。
 */
export interface ConnectionSnapshot {
  /** 恒在（无 omitempty）。 */
  request_id: string
  /** ★ 条件键：`RegistrationMetadata.Protocol` 为空串时**键不存在**。 */
  protocol?: string
  /** ★ 条件键。 */
  client_type?: string
  /** ★ 条件键；老连接可能没带租户。 */
  tenant_id?: string
  /** 恒在。零值时间是 `"0001-01-01T00:00:00Z"`。 */
  registered_at: string
  /** 恒在。★ 从未发帧时是 Go 零值时间，不是有效时间。 */
  last_frame_at: string
  /** 恒在。0 是真的「一帧都没写」。 */
  frames_written: number
  /** 恒在。 */
  bytes_written: number
  /** ★ 条件键。注释自陈「Closed entries only」，但 `closed: true` **不保证**它有值。 */
  close_reason?: string
  /** ★ 恒发布尔（无 omitempty）⇒ live/closed 能直接判别。 */
  closed: boolean
}

/** `connection_registry.go:54-59`。 */
export interface ConnectionRegistryListResponse {
  /** ★ 恒为数组（含空数组）——`List()` 用 `make(..., 0, ...)`。 */
  live: ConnectionSnapshot[]
  /** `len(live)`（`:56`）⇒ 与 `live.length` 恒等，可自查。 */
  live_count: number
  /** `reg.Capacity()`，配置上界。 */
  capacity: number
  /**
   * ★★★ 后端**只会给两种形状**：`null`（从无注销记录）或**非空数组**。
   *   **绝不会给 `[]`**（`ClosedHistory` 里 `n = min(n, len(r.closed))` 且返回 nil 分支）。
   *   ⇒ 渲染前必须兜 `null`；收到 `[]` 说明契约已漂。
   */
  closed: ConnectionSnapshot[] | null
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量（逐字来自后端源码）
 * ═══════════════════════════════════════════════════════════════════════════ */

/** `connection_registry.go:58` 写死的 `ClosedHistory(50)`。★ 响应不回显这个上限。 */
export const CLOSED_HISTORY_LIMIT = 50

/** `:50` / `:68` 的 503 message。 */
export const REGISTRY_NOT_WIRED_MESSAGE = 'connection registry not wired'

/** `:73` 的 400 message。 */
export const MISSING_REQUEST_ID_MESSAGE = 'missing request_id'

/** `:78` 的 404 message。 */
export const REQUEST_NOT_REGISTERED_MESSAGE = 'request not registered'

/**
 * ★ Go `time.Time` 零值的 RFC3339（`time.Time{}.Format(time.RFC3339)`）。
 * `last_frame_at` 对「从未写过帧」的连接就是这个值
 * ⇒ **不能**把它当成有效时间点渲染。
 */
export const GO_ZERO_TIME = '0001-01-01T00:00:00Z'

/** ★ 恒在的 6 个键（`ConnectionSnapshot` 里不带 omitempty 的那些）。 */
export const SNAPSHOT_ALWAYS_KEYS = [
  'request_id', 'registered_at', 'last_frame_at',
  'frames_written', 'bytes_written', 'closed',
] as const

/** ★ 4 个 omitempty 条件键。 */
export const SNAPSHOT_OPTIONAL_KEYS = [
  'protocol', 'client_type', 'tenant_id', 'close_reason',
] as const

export const REGISTRY_LIST_KEYS = ['live', 'live_count', 'capacity', 'closed'] as const

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

export function fetchConnectionRegistryList(
  options?: RequestOptions,
): Promise<ConnectionRegistryListResponse> {
  return req<unknown>('GET', '/api/admin/connection-registry', undefined, options).then(
    unwrapConnectionRegistryList,
  )
}

/**
 * `connection_registry.go:65-81`。
 *
 * ★ `Lookup` 只查**活跃**表（`domains/streaming/connection_registry.go:454-465`）
 *   ⇒ 列表 `closed` 里的条目用这个端点**必然 404**。
 * ★ `request_id` 来自 `r.PathValue` ⇒ 是**路径段**，必须 encodeURIComponent。
 */
export function fetchConnectionByRequestId(
  requestId: string,
  options?: RequestOptions,
): Promise<ConnectionSnapshot> {
  return req<unknown>(
    'GET',
    `/api/admin/connection-registry/${encodeURIComponent(requestId)}`,
    undefined,
    options,
  ).then(unwrapConnectionSnapshot)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

export function unwrapConnectionRegistryList(resp: unknown): ConnectionRegistryListResponse {
  const d = requireObject(resp, '连接注册台')
  // 反向检测：详情端点的载荷是**裸 snapshot**，没有 list 的容器键。
  if ('live_count' in d && !('live' in d)) {
    throw new Error('连接注册台 拿到的是 list 的片段形状（缺 live）')
  }
  requireKeys(d, REGISTRY_LIST_KEYS, '连接注册台')

  // ★ `live` 恒为数组；`null` 是契约破坏（`List()` 永不返回 nil）。
  // ★ 只写 `!Array.isArray(d.live)`：`Array.isArray(null)` 本身就是 false，
  //   单独再判一次 `=== null` 是**被蕴含的冗余**（变异实测删掉它用例照样全绿）。
  if (!Array.isArray(d.live)) {
    throw new Error('连接注册台 live 不是数组（后端 List() 恒返回非 nil 切片）')
  }
  d.live.forEach((row, i) => {
    if (!isPlainObject(row)) throw new Error(`live[${i}] 不是对象`)
    requireSnapshotShape(row, `live[${i}]`)
  })

  // ★★ `closed` 只能是 `null` 或**非空**数组；`[]` 后端永不产生。
  if (d.closed !== null) {
    if (!Array.isArray(d.closed)) throw new Error('closed 不是数组也不是 null')
    if (d.closed.length === 0) {
      throw new Error('closed 是空数组，但后端 ClosedHistory 只给 null 或非空数组')
    }
    d.closed.forEach((row, i) => {
      if (!isPlainObject(row)) throw new Error(`closed[${i}] 不是对象`)
      requireSnapshotShape(row, `closed[${i}]`)
    })
  }

  // ★ `live_count` 恒等于 `live.length`（`:56` 的 `len(live)`）
  //   ⇒ 不等就是载荷被换过/被代理改过，不是「后端口径变了」。
  if (Number(d.live_count) !== (d.live as unknown[]).length) {
    throw new Error(
      `live_count(${String(d.live_count)}) 与 live.length(${(d.live as unknown[]).length}) 不一致`,
    )
  }
  return d as unknown as ConnectionRegistryListResponse
}

export function unwrapConnectionSnapshot(resp: unknown): ConnectionSnapshot {
  const d = requireObject(resp, '连接详情')
  // 反向检测：list 载荷也带 request_id，靠有没有 live 区分。
  if ('live' in d || 'live_count' in d) {
    throw new Error('连接详情 拿到的是 list 的载荷形状')
  }
  requireSnapshotShape(d, '连接详情')
  return d as unknown as ConnectionSnapshot
}

/**
 * 恒在的 6 个键必须都在；4 个条件键**不在**必检里
 * —— 它们带 omitempty（`connection_registry.go:153-161`），
 * 「键缺失」就是「后端没这个值」，不是契约破坏。
 *
 * ★ 与第七十/七十一批那两条端点的处理**相反**：
 *   那两族的 `degraded` 恒发 ⇒ 缺键才异常；本族这 4 个条件键缺键才正常。
 */
function requireSnapshotShape(obj: object, where: string): void {
  const d = obj as Record<string, unknown>
  const missing = SNAPSHOT_ALWAYS_KEYS.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个恒在键（${missing.join(', ')}）`)
  }
  if (typeof d.closed !== 'boolean') throw new Error(`${where} closed 不是布尔值`)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

/** ★★ 注销历史是 `null`（从无注销记录）还是非空数组 —— 两者都不是「空列表」。 */
export function registryClosedIsAbsent(r: ConnectionRegistryListResponse): boolean {
  return r.closed === null
}

/** 兜住 `null`，让 UI 可以直接 `.map()`。★ 别忘了一句话：`[]` 意味着契约漂了。 */
export function registryClosedRows(r: ConnectionRegistryListResponse): ConnectionSnapshot[] {
  return r.closed ?? []
}

/** ★ 后端永不产生的形状：收到就说明契约漂了（解包器已抛错，这里供二次防御）。 */
export function registryClosedIsImpossibleEmpty(r: ConnectionRegistryListResponse): boolean {
  return Array.isArray(r.closed) && r.closed.length === 0
}

/** `live_count` 与 `live.length` 是否自洽（解包器已把关，这里供 UI 复核）。 */
export function registryCountAgrees(r: ConnectionRegistryListResponse): boolean {
  return Number(r.live_count) === r.live.length
}

/**
 * ★ 注册台水位：`live_count / capacity`。
 *
 * ★★ 2026-10-08（第一百零七批）：下面的 `cap <= 0` 分支**后端当前产生不出来** ——
 *   `NewConnectionRegistry` 把 ≤0 兜底成 `DefaultConnectionRegistryCapacity = 4096`，
 *   `r.capacity` 又是构造期常量 ⇒ `capacity` 恒 ≥ 4096。
 *   ⇒ 留它是**防御形态**（解包器只查键存在、不查 `capacity` 的类型），
 *     **不为此写判据** —— 能钉住它的那个夹具必须是后端发不出的值（同批 selfCheck 的
 *     `upstream_latency_ms === 0` 同理，见「可证等价 ⇒ 删判据」）。
 */
export function registryUtilization(r: ConnectionRegistryListResponse): number | null {
  const cap = Number(r.capacity)
  if (!Number.isFinite(cap) || cap <= 0) return null
  return Number(r.live_count) / cap
}

/** ★★ 从未写过帧 ⇒ `last_frame_at` 是 Go 零值时间。 */
export function snapshotNeverWroteFrame(s: ConnectionSnapshot): boolean {
  return s.last_frame_at === GO_ZERO_TIME
}

/** ★ 有帧 ⇒ `frames_written > 0`；两者应当自洽。 */
export function snapshotHasFrames(s: ConnectionSnapshot): boolean {
  return Number(s.frames_written) > 0
}

/** ★ 自查：`frames_written === 0` 时 `last_frame_at` **应当**是零值时间。 */
export function snapshotFrameFieldsAgree(s: ConnectionSnapshot): boolean {
  const zeroFrames = Number(s.frames_written) === 0
  return zeroFrames === snapshotNeverWroteFrame(s)
}

/** ★★ `closed: true` **不保证**有 `close_reason`（`snapshot(false, "")` 对 live 也走这条路）。 */
export function snapshotCloseReason(s: ConnectionSnapshot): string | null {
  return s.close_reason !== undefined && s.close_reason !== '' ? s.close_reason : null
}

/** ★ `closed` 恒发布尔 ⇒ 这一条判据**不是恒真**（真能区分 live 与 closed）。 */
export function snapshotIsClosed(s: ConnectionSnapshot): boolean {
  return s.closed === true
}

/**
 * ★ `tenant_id` 是条件键 ⇒ **缺失不代表没租户**。
 *   老连接（注册时未带租户）就是键缺失。
 */
export function snapshotTenantId(s: ConnectionSnapshot): string | null {
  return s.tenant_id !== undefined && s.tenant_id !== '' ? s.tenant_id : null
}

/** ★ 同上：`protocol` / `client_type` 缺失只是「注册时没带」。 */
export function snapshotClientType(s: ConnectionSnapshot): string | null {
  return s.client_type !== undefined && s.client_type !== '' ? s.client_type : null
}

export function snapshotProtocol(s: ConnectionSnapshot): string | null {
  return s.protocol !== undefined && s.protocol !== '' ? s.protocol : null
}

/**
 * ★★ `live` 的顺序**没有保证**（`List()` 的注释自陈 map walk is unordered）
 *   ⇒ 想稳定渲染必须客户端自己按 `registered_at` 排。
 */
export function registryLiveSortedByRegisteredAt(
  r: ConnectionRegistryListResponse,
): ConnectionSnapshot[] {
  return [...r.live].sort((a, b) => Date.parse(a.registered_at) - Date.parse(b.registered_at))
}

/**
 * ★★★★ 「已暴露满 50 行」⇒ **可能还有更早的没被端点暴露**，但**不是**「更早的已经丢了」。
 *
 * ★★★ 2026-10-08（第一百零七批）修正措辞：原注释写「可能被**静默截断**」，
 *   那个说法会让人以为「50 是历史上限，超出的被丢掉了」—— **不成立**。
 *   环形缓冲 `closedHistory = DefaultClosedHistoryDepth = 256`（`:65`/`:254`，
 *   `:355-356` 裁剪）⇒ **256 > 50，端点永远取不满缓冲区**
 *   ⇒ 返回 50 行时缓冲区里**至少还躺着另外 206 条**没暴露。
 *   ⇒ 行为不变（`rows.length >= 50` 确实成立），但**理由与 UI 文案都必须按上面写**。
 *   ⇒ 真正的丢弃点在环形缓冲的 256 条 —— 那是另一个阈值，本响应里**量不出来**。
 */
export function registryClosedPossiblyTruncated(
  r: ConnectionRegistryListResponse,
): boolean {
  const rows = registryClosedRows(r)
  return rows.length >= CLOSED_HISTORY_LIMIT
}

/**
 * ★★★ **这个函数有盲区，调用方不能单靠它决定要不要发详情请求。**
 *
 * 它只看 `closed` 布尔，因此分不出两格：
 * | 行的来源 | `closed` | 在 `r.entries` 里？ | 详情端点 |
 * |---|---|---|---|
 * | `closed` 段（已进环形缓冲） | `true` | **否** | **必然 404** |
 * | `live` 段（`WriteFrame` 超时竞态窗口，见文件头 (8)） | `true` | **是** | **查得到** |
 *
 * ⇒ ★★ 正确口径是「**行来自哪一段**」—— 调用方在 UI 里天然知道
 *   （live 段的行发详情请求，closed 段的行直接用列表里那份快照）。
 * ⇒ 本函数只回答「这行的 `closed` 字段是不是 true」，**不回答**「能不能按 id 查到」。
 */
export function registryRowIsNotFetchableById(s: ConnectionSnapshot): boolean {
  return s.closed === true
}

/**
 * ★★ 503 与「没数据」是两种不同的失败：
 *   未装配时后端返 **503 `connection registry not wired`**，不是空列表。
 *   ⇒ 「空列表」永远只表示「装配了但当前没有连接」。
 */
export function registryNotWired(status: number): boolean {
  return status === 503
}

/* ── 内部工具 ────────────────────────────────────────────────────────────── */

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v)
}

function requireObject(resp: unknown, where: string): Record<string, unknown> {
  if (!isPlainObject(resp)) {
    const actual = resp === null ? 'null' : Array.isArray(resp) ? 'array' : typeof resp
    throw new Error(`${where} 响应形状不符：期望裸对象，实得 ${actual}`)
  }
  if ('success' in resp && 'timestamp' in resp) {
    throw new Error(`${where} 拿到的是 dashboardapi 信封形状，本族应为裸 JSON`)
  }
  return resp
}

function requireKeys(obj: object, keys: readonly string[], where: string): void {
  const d = obj as Record<string, unknown>
  const missing = keys.filter((k) => !(k in d))
  if (missing.length > 0) {
    throw new Error(`${where} 缺 ${missing.length} 个键（${missing.join(', ')}）`)
  }
}