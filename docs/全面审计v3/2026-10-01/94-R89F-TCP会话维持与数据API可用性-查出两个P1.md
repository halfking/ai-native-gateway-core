# 94-R89-f：TCP 会话维持可靠性 + 数据 API 可用性 —— 查出 **2 个 P1**

- 轮次：R89-f
- HEAD 基线：`106e38fd1`
- 触发：`00-审计覆盖台账.md` 待跟进项「【下轮 C/E】TCP 会话维持可靠性 + 数据 API 可用性（状态未定）」
- 结论：**两条 objective 项从「状态未定」变为「已审」**，各查出 1 个 P1
- 改动：**零生产代码、零配置、零门**

---

## 0. 一句话

「TCP 会话维持」这条 objective 项，**入站侧的帧分类、帧间超时、防重复输出、连接清理、请求体可重放全部核实健康**；
真正的缺口是**写端**——数据面**没有任何写超时**，而**同一套代码在 admin 侧有**。
「数据 API 可用性」这条，**分页/超时/资源释放健康**，真正的缺口是**鉴权**——
三个端点（含两个写）在 Go 代码层**零防护**，只靠 nginx 挡着。

---

# 第一部分：TCP 会话维持 / 网络请求处理可靠性

## 1. 【P1】数据面写端无任何写超时 ⇒ 慢客户端可**无限期**占用凭据并发槽位

### 1.1 证据链（四环，每环独立核实）

**环 1 —— 数据面没有任何写超时。** 三种检索方式，全部零命中于数据面：

```
grep -rn "SetWriteDeadline|SetReadDeadline|http.TimeoutHandler" --include=*.go . | grep -v vendor | grep -v _test.go
```

| 命中 | 归属 |
|---|---|
| `admin/live_stream_sse.go:2017` `rc.SetWriteDeadline(now+5s)` | **admin 侧**，5s 写超时 |
| `connection_registry.go:393` | **只是注释**，不是调用 |
| （无 `http.TimeoutHandler` 任何命中） | — |

⇒ `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 的响应写路径
**没有 `SetWriteDeadline`、没有 `TimeoutHandler`**。

**环 2 —— 服务器层不兜底。** `cmd/gateway/main.go:7258` `WriteTimeout: 0`
（Go 语义：写响应无上限）。同文件 `:7257` `ReadTimeout: 300s`、`:7261` `IdleTimeout: 300s`
——**读有上限、写没有**。

**环 3 —— flush 层不兜底。** `domains/streaming/stream.go:1889-1908` `safeFlush`
只做 `recover` 兜底，**无超时、无 deadline**。

**环 4 —— 槽位会被长期占用。** `executor_dispatch.go:770`：
```go
lease, ok := e.FpSlots.Acquire(params.R.Context(), cand.CredentialID, cand.FpSlotLimit, ...)
```
请求全程**持有该凭据的并发槽位**，直到 attempt 结束。上限来自
`config.DefaultCredentialConcurrency`（`config/config.go:213`）。

### 1.2 为什么是「无限期」而不是「有界」

关键在于**帧间超时管不到写侧**：

- `StreamChunkTimeout`（默认 600s）包的是**从上游 body 读一行**：
  `internal/sse/line_reader.go:95-97` 每次 `ReadLineWithContext` **新建**一个
  `context.WithTimeout(ctx, timeout)`。
- 客户端读得慢 ⇒ socket 缓冲写满 ⇒ bridge 协程**卡在 `s.w.Write(p)`**
  （`serialized_stream_writer.go:90`，持锁直接写，**无缓冲、无队列**）
  ⇒ **不再回到读侧** ⇒ 帧间计时器根本没被创建。
- 而**整流总时长兜底不存在**：`StreamTimeout`（默认 900s，`config.go:594`）虽被
  `selectUpstreamTimeout` 算出（`executor_chat.go:289-295`），
  但 `upstreamContext`（`executor_chat.go:2536-2544`）在 `IsStream` 分支**丢弃该参数**：
  `IsStream && survive` → 2h；其余 `IsStream` → `context.WithCancel(r.Context())`，
  **无任何 wall-clock 上限**。`streaming.StreamTimeout()` 访问器**零调用方**。

⇒ **链闭合**：慢客户端 → bridge 卡在写 → 帧间计时器不启动 → 无总时长上限
→ **凭据槽位可被无限期占用**。该凭据的其余并发槽位耗尽后，**其他租户/用户被饿死**。

### 1.3 决定性对照：同一套代码在 admin 侧**有**这套防护

| 能力 | admin 侧（`connection_registry.go`） | 数据面（`domains/streaming`） |
|---|---|---|
| 每帧写看门狗 | ✅ `DefaultClientWriteTimeout = 30s`（`:61-62`、`:375-445`） | ❌ |
| 写 goroutine 配额 | ✅ `DefaultMaxConcurrentWriteGoroutines = 8192`（`:77`） | ❌ |
| 写 deadline | ✅ `admin/live_stream_sse.go:2017` 5s | ❌ |

⇒ **这不是「团队不知道」，是「知道但没应用到数据面」**。
与 R89-e 的 `ForceAttemptHTTP2` 遗漏属同一种形态：能力在仓内存在，只是没接到这条路上。

### 1.4 定级理由与限制

- **P1**：可被客户端稳定触发，影响面是**跨租户的资源饥饿**（不是单请求失败）。
- **如实登记未实测**：本条是**代码层证明**，**未做线上压测复现**，也**无法排除**反代层
  （nginx `proxy_read_timeout 1200s`）先行断开。`proxy_read_timeout` 能兜住「客户端不读」
  这类场景，但**兜不住**「客户端以极慢速度持续读」——那种情况 nginx 不会超时。
- 「卡在 `w.Write` 时帧间计时器不生效」这一步是**推理**，已在上面标出推理链。

## 2. 【P2】detached 之后写返回 `(len(p), nil)` —— **静默假成功**

`domains/streaming/serialized_stream_writer.go:84-115`（已读全文）：

```go
if s.detached {
    return len(p), nil          // ← 声称写了 len(p) 字节，err 为 nil
}
n, err := s.w.Write(p)
if err != nil { s.detached = true; s.detachErr = err; return n, err }
if n < len(p)  { s.detached = true; s.detachErr = io.ErrShortWrite; return n, io.ErrShortWrite }
```

后果链：`StreamSurvivesClientCancel=true` 时上游 ctx 已 detach
（`executor_chat.go:2537-2539`），bridge **看不到客户端取消**，
只在「上游 EOF / 帧间超时 / 2h 上限」处退出（`stream.go:263-291`）。
期间**持续读上游、持续走写路径**，而返回值 `(len, nil)` 让 commit gate
（`attempt_commit_gate.go:558-563`）认为一切正常。

⇒ **客户端已死这件事，在返回值上完全不可见**。
且 `grep -rn "\.Detach()" --include=*.go`（非测试）**零命中**——detach 只由写错误/flush panic 触发，
没有任何代码显式表达「这个客户端已经没了」。

**好的一面（如实记录）**：短写被视为错误是 2026-08-28 的正确修复（`:96-104` 注释自陈），
避免了「gate 以为字节已送出、实际丢了半帧」。

## 3. 【P3】主上游 client **HTTP/1.1-only**（实证 A/B）

`upstream/client.go:138-150` 配了自定义 `DialContext` 却**没有** `ForceAttemptHTTP2`。
Go 语义：一旦提供 `DialContext`，HTTP/2 **默认被保守关闭**。

**实证**（临时测试调**生产配置本体**打真实 HTTPS 端点，跑完即删）：

```
A-生产配置   proto="HTTP/1.1" major=1 minor=1 status=401
B-反向对照   proto="HTTP/2.0"  major=2 minor=0 status=401
```

⇒ 生产配置协商到 **HTTP/1.1**；加上 `ForceAttemptHTTP2: true` 后为 **HTTP/2.0**。

**这是遗漏而非决策**——同一仓库三个对照点：

| 文件 | `DialContext` | `ForceAttemptHTTP2` | 语义 |
|---|---|---|---|
| `proxy/transport.go:109` | 无 | **`= false`（显式）** | 代理路径**故意**禁 h2（注释：便于对 http/https 代理更可控） |
| `internal/safehttpclient/safe_http_client.go:89` | 有 | **`= true`（显式）** | 知道这条规则并正确使用 |
| `upstream/client.go:138` | 有 | **无** | ⇒ **h2 被静默关闭** |
| `domains/health/http_checker.go:43` | 有 | **无** | 同上 |

**定级 P3 而非 P2**（不夸大）：实测峰值仅 **18.25 QPS**（1095/分钟，93 号 §7），
`MaxConnsPerHost` 未设（0 = 不限）⇒ **无队头阻塞、无连接数瓶颈**。
真实代价是**突发时的 TCP+TLS 握手次数**与丧失多路复用，属**伸缩性特征**而非当前故障。

## 4. 【P3】第四例「注释承诺的接线从未落地」：探测的 egress 对等修复

`bg/active_probe_executor.go:166-172` 注释：

> Use this to inject a proxy-respecting transport so direct probes leave through
> the same egress as real request traffic (**2026-07-16 node-probe fix parity**).

**两种检索方式复核**：

| 方式 | 结果 |
|---|---|
| `grep -rn "SetHTTPClient" --include=*.go .` | **全仓 2 命中**：注释本身 + 函数定义本身。**无调用点，连测试都没有** |
| 查其它注入通道 | `NewActiveProbeExecutor(db, keyring, encKey, timeoutMs)`（`:152`）**不收 client**；无 option 模式；`ProbeEventSink` 接口（`active_probe_emitter.go:36`）与 client 无关 |

⇒ 探测 executor 用的是构造时的默认 `&http.Client{Timeout: 30s}`，
**不带自定义 Transport** ⇒ 走 `http.DefaultTransport` ⇒ h2 正常。
但它**绕过 egress 代理**，与真实请求流量**不走同一条网络路径**。

**可达性**：真库 `SELECT egress_profile, count(*) FROM providers GROUP BY 1`
⇒ **60 个 provider 全部 `direct`，`proxy` 为 0** ⇒ **当前不可达**（不可达的缺陷不是缺陷）。

⇒ 定级 **P3（潜伏）**：一旦有人把某个 provider 设为 `egress_profile='proxy'`，
该 provider 的探测结果就会**静默失真**（探测直连、真实流量走代理），
**无门会红**。这是**第四例**，见 §6。

## 5. 已核实为**不是**问题的项（避免下轮重复）

| 项 | 结论 | 证据 |
|---|---|---|
| 请求体可重放 | ✅ 全部 `bytes.NewReader` ⇒ Go 自动设 `GetBody` ⇒ stale 连接**可重试**（否则 POST 会静默失败） | `executor_chat.go:128`、`executor_anthropic.go:168`、`executor_ollama.go:169`、`context_summarize.go:491`、`upstream/client.go:518` |
| 帧间超时不因网关保活重置 | ✅ 计时器包的是**上游 body 的读**，保活是另一个 goroutine 往**客户端**写 | `line_reader.go:95-97` vs `stream_session.go:69-85` |
| 帧分类覆盖 | ✅ `response.created`/`completed`/`incomplete`/`failed` 全覆盖 | `stream_frame_classifier.go:233-252` |
| 防重复输出 | ✅ commit gate：`Resumable = !attemptHasClientSemanticOutput(...)`，已出内容即禁止透明重试 | `attempt_commit_gate.go:470-477` |
| 连接清理 | ✅ `Client.Stop()` → `CloseIdleConnections`；proxy 侧重建时关旧 | `upstream/client.go:217-222`、`proxy/transport.go:129` |
| `ResponseHeaderTimeout` | ✅ 可配置，且注释记录了 2026-09-30 真实事故（代理路径 30s 硬编码导致「只有池路由凭据看起来坏了」） | `upstream/client.go:178-199` |
| 保活帧可被客户端误感知 | ✅ `: keep-alive` / `: thinking:` 是 **SSE 注释帧**，按规范被忽略；注释记录了为何不用 `data:`（opencode 的 Zod union 会 `invalid_union`） | `stream.go:29`、`handler.go:402-414` |

---

# 第二部分：数据 API 可用性

## 6. 【P1】`/admin/api/v1/health-checks` 三个端点**零鉴权**（含两个写）

### 6.1 三环证据（我逐环回原代码复核，非采信子代理）

**环 1 —— 注册处不带鉴权 wrapper。**
`admin/health_check_handlers.go:205-208`：
```go
func (h *HealthCheckHandler) RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("GET  /admin/api/v1/health-checks",              h.List)
    mux.HandleFunc("POST /admin/api/v1/health-checks/dismiss",     h.Dismiss)
    mux.HandleFunc("POST /admin/api/v1/health-checks/fix",         h.ExecuteFix)
}
```
签名**只收 `mux`**，没有 `adminWrap`/`superAdminWrap` 形参。
**对照** `admin/systemmonitor_handlers.go:629` 的同类注册函数**签名接受**两个 wrapper 并逐条使用。

**环 2 —— 调用处裸传。**
`cmd/gateway/main.go:6497` `healthCheckHandler.RegisterRoutes(mux)`——紧跟构造、无任何中间件。
**对照**同文件 `:6577` `selfCheckHandler.RegisterRoutes(mux, adminMw, superAdminMw)`
——同一文件、同一批 wiring，**这里传了、那里没传** ⇒ 是遗漏。

**环 3 —— 全局鉴权不兜底，且 health-checks 恰好落在一条不变量与 bypass 之间的缝隙。**
`middleware/auth_mw.go:55` 的 bypass 前缀含 `/api/` **与** `/admin/`；
`Wrap`（`:64-70`）对命中前缀的路径直接 `next.ServeHTTP`。`/admin/api/...` 以 `/admin/` 开头 ⇒ 命中 bypass。

同文件 `:37-40` 的 SAFETY 不变量自陈：「**every registered `/api/*` endpoint is wrapped by
wrapAdmin/superAdmin**… Verified 2026-06-30 via grep」。**但该不变量只声明覆盖 `/api/*` 前缀**——
而 health-checks 注册在 `/admin/api/...`（`admin/health_check_handlers.go:206-208`，我已确认它
**只**注册在这一处路径下），既不在不变量声明的 `/api/*` 范围内，又落在 `/admin/` bypass 之下。
⇒ **既没有任何 wrapper，又被明确排除在全局鉴权之外**。这是本条最核心的一环。

### 6.2 匿名可做什么

| 端点 | 匿名能力 | 证据 |
|---|---|---|
| `GET .../health-checks` | 读 `routing_health_checks` 全表：`entity_name`、**`detail`**、**`fix_sql`** | `:22-107`、字段见 `:30-33` |
| `POST .../dismiss` | **任意 id 置为 dismissed**，且 `dismissed_by` 由**请求体任意指定** | `:110-137`、`:112-115` |
| `POST .../fix` | **触发写库**：`credential_model_bindings.billing_mode` / `provider_models.canonical_id` | `:169-203`、`:143-165` |

### 6.3 但**不是**任意 SQL 执行（这里的设计是对的，必须如实记录）

`cannedFix`（`:143-165`）是**白名单**，只有两条 canned 语句，
且 `entity_id` **从 DB 行里取**（`runHealthCheckFix`，`:184-190` 的
`SELECT entity_type, entity_id ... WHERE id = $1 AND status = 'open'`），**不是攻击者直传**。
注释自陈动机：存储的 `fix_sql` 是展示文本，直接执行会构成**存储型 SQL 注入**，
所以只允许白名单参数化语句。**这个防护是对的。**

⇒ 定级 **P1 而非 P0**：没有任意 SQL、没有凭据泄露、没有任意表写。
真实影响是 ① **基础设施健康态的未授权信息泄露**（侦察价值）、
② **监控表完整性的未授权破坏**（可批量 dismiss、可伪造 `dismissed_by` 污染审计痕迹）、
③ `fix` 可批量把 `billing_mode` 同步为凭据的 `plan_type`——**这是一个计费字段**。

### 6.4 部署侧当前挡着（这是本条的准确定级依据）

| 检查 | 结果 |
|---|---|
| `deploy/llmgo-245.nginx.conf` | 有 `location ^~ /api/`（`:167`）但**无** `location ^~ /admin/` ⇒ `/admin/api/...` 落到 `location /`（`:29`，301 跳转）**不被反代** |
| 同文件 `:241-254` | 有一条**显式注释**说明「Vue SPA routes live under /admin/*…Do not proxy the entire prefix: only this operational endpoint belongs to the gateway」，并只 carve out `/admin/config/reload` 一个 |
| `deploy/download.internal.example.com.nginx.conf` | 同样**无** `/admin/` 反代 |
| `docker-compose.yml:5` | `127.0.0.1:8781:8781`——**仅回环** |

⇒ **两套 nginx 部署都挡住该路径，且服务只绑回环。**

**但必须说清的三点**：

1. **Go 代码层零防护**——安全性完全来自部署配置。
2. **`/admin/config/reload` 是被 carve out 的、且由 `AdminTokenMiddleware` 单独保护**
   （`middleware/admin_token_mw.go:12`、`auth_mw.go:33-34` 明确点名它）
   ⇒ **团队知道 `/admin/` 这个前缀危险、并为唯一该暴露的端点做了正确处理**，
   只是**漏了 health-checks 这一处**。这与 §1.3 是同一种形态。
3. **任何新增 `/admin/api/*` 端点都会被 nginx 静默吞掉**（反之，Go 侧却以为它可访问）——
   这个前缀分裂（SPA 与 API 共用 `/admin/`）本身是脆弱设计。

## 7. 【P2】`/api/admin/dashboard/swim-lane-init` 缺 tenant 过滤

- 注册：`admin/handler.go:1050` `mux.HandleFunc("/api/admin/dashboard/swim-lane-init", admin(h.HandleSwimLaneInit))`
  ⇒ **`tenant_admin` 可达**。
- 查询：`admin/swim_lane_init.go:90-111`，`FROM request_logs_hot rl WHERE rl.ts >= $1`，
  **无 `tenant_id` 谓词**；全文件 `tenant_id`/`TenantID`/`GetTenantID`/`IsTenantAdmin` 出现 **0 次**。
- 返回 `request_id`/`model`/`provider`/`error_kind`/**`cost_usd`**（`:92-102`）⇒ **跨租户请求元数据与计费金额**。
- **对照同库正确写法** `admin/logs.go:503-505`
  `if IsTenantAdmin(r) { addFilter("rl.tenant_id = $%d", GetTenantID(r)) }` ⇒ 确认是遗漏。

## 8. 【P2】17 个 admin 文件的数据查询无 handler 级 deadline

`WriteTimeout: 0` 意味着**没有 handler 级超时的查询可以无限占用连接**。
逐文件扫描 `.Query*(r.Context()` 且文件内 `WithTimeout` 计数为 0：**17 个文件**。
已读包围函数确认的代表：`admin/stats.go:136,162,208,235,311`、
`admin/health_check_handlers.go:29,84,124,177,200`、`admin/format_anomalies.go:106,143,213,297`。

**对照做得好的一批**：`logs.go:470`(30s)、`logs.go:1096/1112`(hot 3s / cold 20s 分段)、
`body_resolver.go:156`(5s)、`session_summary_v2.go:108`(30s)、`analytics.go`(10-15s)。
⇒ **本仓已有正确范式，只是这 17 处没套用。**

## 9. 【P3】三处

1. **内部错误原文透给客户端**：`admin/session_audit.go:247` `fmt.Sprintf("query failed: %v", qerr)`（500，原始 pgx 错误）；
   另 `provider_credential.go:98`、`node_operations.go:74/86`、`dashboard_board.go:53/156`、
   `memora_handlers.go:162`、`proxy.go:880`。
   **对照正确范式** `admin/internal_error.go:53-64` `writeInternalTextErr`——
   记录 err 但只回固定 op 文本。
2. **错误信封三形态不一致**：标准 `{"error":{"detail":…}}`（`handler.go:1480-1484`）/
   扁平 `{"error":"…"}`（`health_check_handlers.go:117` 等）/ 裸 `http.Error`
   （`swim_lane_init.go:64`）⇒ 契约不稳定。
3. **前端调 3 个不存在的端点**（两种检索复核：`web/src/api/` 定位 + 全仓 `.go` 排除 vendor 为空）：
   `GET /api/admin/dashboard/session-recent`（`web/src/api/dashboard.ts:387`）、
   `GET .../session-anomalies`（`:557`）、`POST .../session-export`（`:583`）。

## 10. 已核实为**不是**问题的项

| 项 | 结论 |
|---|---|
| 无上限全表端点 | ✅ **未发现**。`logs.go:482-488` 上限 500、`:785-791` 两段式查询（子查询先 LIMIT 再 LATERAL JOIN，注释记录从 9.9s 优化下来）、`format_anomalies` 500、`model_integrity` 500、`stats` 200 |
| bodies 表雪崩 | ✅ 有刻意设计的闸 `bodyFetchGate`（`session_bodies_batch.go:108-130`，容量 4 < pool 16，非阻塞快速失败 503，注释记录单条 17-19s） |
| `rows.Close()` 泄漏 | ✅ 覆盖良好。子代理用「`.Query(` 计数 > `rows.Close()` 计数」筛出约 100 个文件，**逐个读包围函数后确认是假阳性**（`.Query(` 出现在 SQL 字符串与注释里） |
| panic 打挂实例 | ✅ `middleware/recovery_mw.go` 全局装于链首（`main.go:7188`），`:92` 对 `/admin/` 前缀有专门归类 |
| 请求体大小 | ✅ `readJSON` 统一 2 MiB 上限（`handler.go:1500-1506`），详情页超限 413 |
| 共享状态竞态 | ✅ `bg/system_health.go:69` 用 `atomic.Value`；`bodyFetchGate` 是 channel |
| pprof / metrics | ✅ 双重门：pprof 需环境变量 + 强制 loopback（`pprof_server.go:14-17,30-37`）；`/metrics` 挂 `AdminTokenMiddleware`（`main.go:6008`） |
| `/api/health/system` 匿名 | ✅ 可接受：只返回 `atomic` 缓存的全局聚合，无租户/凭据信息，且每请求不查库 |

---

# 第三部分：跨切面

## 11. 「注释承诺的接线从未落地」已累计 **4 例**

| # | 位置 | 注释承诺 | 实际 |
|---|---|---|---|
| 1 | `domains/credential/state_sync.go:25` | 消费面 `routing_health_checks.circuit_open` | 表里无该列 |
| 2 | `internal/paramledger/ledger.go:23-24` | Redis 镜像「供跨进程审计/观测」 | 三种检索复核：**零消费面**（93 号 §5.2） |
| 3 | `bg/active_probe_executor.go:166` | 「2026-07-16 node-probe fix parity」注入 egress 代理 | **零调用点**（本报告 §4） |
| 4 | `executor_chat.go:386-387` | mode-fallback 会用 chat 体填 `ResponsesBodyBytes` | 该函数从未写该字段（93 号 §6） |

**共同形态**：注释/文档里的**消费者或接线点**比代码跑得更快，
且**没有任何门会因为它不成立而报红**。

⇒ **已写入 `docs/audit/playbook/conventions.md` §13**：
「读到一条注释承诺了某个接线/消费面时，把它当成**待验证的断言**，
默认它没接；用**两种检索方式**复核后再决定要不要登记」。

## 12. 另一条同族形态：「能力在仓内存在，只是没接到这条路上」

| 能力 | 已有实现 | 未接的路径 |
|---|---|---|
| 每帧写看门狗 30s + goroutine 配额 | `connection_registry.go:61-62,77,375-445` | **数据面**（本报告 §1） |
| 写 deadline | `admin/live_stream_sse.go:2017` 5s | **数据面** |
| `ForceAttemptHTTP2` | `safehttpclient.go:89` =true / `proxy/transport.go:109` =false | **`upstream/client.go`、`health/http_checker.go`**（本报告 §3） |
| handler 级查询超时 | `logs.go:470`、`analytics.go` 等 | **17 个 admin 文件**（本报告 §8） |

⇒ **巡检建议（已记入台账）**：每轮审计遇到「这里缺 X」时，
**先问「仓里有没有已经实现好的 X」**——有就是接线遗漏（可低成本修、且说明设计已想清楚），
没有才是能力缺失（要设计、要排期）。这与 R88 的「写门前先查是否已有门」是同一条纪律。

---

## 13. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/94-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |
| `docs/全面审计v3/00-审计覆盖台账.md` | 待裁决第 39、40 条 + 关闭「C/E 状态未定」项 + 4 例接线缺口台账 |
| `docs/audit/playbook/conventions.md` | 新增 §13、§14 |

**零生产代码、零配置、零门、零 CI 行为变化。**
临时实证测试（`upstream/zz_r89f_tmp_test.go`）已 `mavis-trash` 移除。

## 14. 交叉引用

- conventions **§9.4**（变异/实证验证）、**§10.1**（量化自己数一遍）、**§12**（工具盲区）
- 93 号：同轮的 paramledger 审计；本轮两处「能力已在仓内、未接上」与之同族
- playbook **§13/§14**：本轮新增的两条纪律
