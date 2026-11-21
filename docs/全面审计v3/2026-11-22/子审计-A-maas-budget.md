# 子审计 A — MaaS 租户月度预算硬闸 meta-audit + 计帐面（第 62 增量审计轮）

- **仓库**：`/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`，HEAD = `c446de9a4`（干净，工作区仅本报告未跟踪文件）
- **审计窗口**：`c6bf29aea..c446de9a4`（236 文件，+17445/−522）
- **被审对象**：并行车 `a2cb60f5b`（迁移 996 + 门主体 + chat 面接入）与并行车审计轮 `f712ea685`（F3 修复：四面接入）
- **纪律**：只读审计（go build / go test / grep / git；未连接任何生产/远程服务）；真库相关判定以代码级证据 + 仓内登记互证，未做活体复跑
- **方法**：不采信自述——`git show` 逐字读 diff → 当前文件全文关键段复读 → 全仓 grep 反证 → 独立复跑定向门禁

---

## 一、逐问判定表（总览）

| # | 问题 | 判定 |
|---|------|------|
| 1 | F3 修复 meta-audit（6 项子断言） | **全部成立**（子项④有一个 P3 级措辞瑕疵：warn「零变化」在日志面上不严格） |
| 2 | 穷尽性（有无第 5 个扣积分面） | **无第 5 面**；四面全覆盖。附带发现：messages/responses 面无 PreCheckCredits/CheckBudget（基线存量，非本闸范围） |
| 3 | 门执行次数 / 流式行为 / ctx / R-MAAS3 | **每请求一次**；ctx 取消安全；R-MAAS3 登记属实、未被改坏 |
| 4 | R-MAAS4/5/6/7 现状 | **四项登记全部属实且未被改坏**；R-MAAS6 未收口且暴露面随 F3 扩大（无新钉测落库） |
| 5 | M-26 installer 全绿核验 / api-maas 死面 / gin+echo 依赖图 | **M-26 两门所在包实测全绿**（`cmd/llm-gw-installer` 5.6s + `internal/dbinit` ok，两红确已收）；`go test ./...` 全量存在 **1 个环境性红**：`launcher/backend` 因本机 docker 守护进程挂死把 `skipIfNoDocker` 的 `docker info` 探测拖到 600s 包超时（**基线存量**测试健壮性缺陷 + 本机环境态，非窗口回归）；api/maas 十端点**仍是死面**；gin+echo **仍在** gateway 依赖图（sonic 不在） |
| 6 | 定向门 go build/test | **全绿**（streaming 150.4s / maas 0.013s；零失败集合） |
| 7 | audio_billing/audio_service 窗口改动 / 凭据链 / 价格面 | audio 两文件改动 = F3 接入本身；凭据链只被 `diagnostics_credential.go` 重构触碰（行为等价）；**MaaS 价格面窗口内零变化** |

---

## 二、Q1 meta-audit F3 修复（`f712ea685`）逐项判定

### ① 四面真的都调用了门 —— **成立**

共用判定体 `applyTenantMonthlyBudgetGate`（`domains/streaming/handler.go:2704-2754`），四个接入点全部命中：

| 面 | 接入点 | 证据 |
|----|--------|------|
| chat（/v1/chat/completions、/v1/completions） | `serveWithExecutor` 鉴权+预算检查之后 | `domains/streaming/handler.go:2994-2998`（经 `enforceTenantMonthlyBudget`，handler.go:2774-2779） |
| messages（/v1/messages） | 鉴权成功分支内、`state.EventAuthed` 之后 | `domains/streaming/messages.go:251-256` |
| responses（/v1/responses） | 鉴权成功分支内、`state.EventAuthed` 之后 | `domains/streaming/responses.go:269-274` |
| audio（/v1/audio/transcriptions、/v1/audio/speech、/v1/mcp） | `authenticate()` 内 | `domains/streaming/audio_service.go:1342-1344`；三面共用 authenticate（audio_transcriptions.go:52 / audio_speech.go:46 / audio_mcp.go:125） |

### ② 调用位置正确 —— **成立**

- **chat 在鉴权后**：鉴权在 handler.go:2883-2943（`verifyRequestKey` → `keyInfo = ki` 于 2916），门在 2994 —— 且位于 key 级预算门（2955-2977 `CheckBudget`）与 MaaS credits pre-check（2979-2988 `PreCheckCredits`）**之后**，与 a2cb60f5b 交付时位置一致（diff 对拍：只是内联块抽成方法，相对顺序未动）。
- **audio 在 key 预算门之后**：`authenticate()` 内 `keyVerifier.CheckBudget`（含 fail-closed 503）在 audio_service.go:1325-1336，租户门在 1337-1344 —— 恰在其后。
- messages/responses：门在鉴权成功分支内（keyInfo 非 nil 才会进；`applyTenantMonthlyBudgetGate` 自身再对 `keyInfo == nil` 兜底，handler.go:2709）。
- **每请求至多一次**：chat 面 `serveHTTPInner` 只调用一次 `serveWithExecutor`（handler.go:2669-2671）；`serveFallback`（2687）只在 executor 不可用（无 provider、无扣费）时走到，不触发门。详见 Q3。

### ③ 各面拒绝路径符合宣称 —— **成立**

- **chat 写 _request_logs 失败行**：`onReject` 钩子注入 `captureAndEmitFailure`（handler.go:2994-2996），该闭包 `logCtx.SetError + SetClientStatus + EmitFailure + MarkLogged`（handler.go:2832-2851）⇒ 落 `_request_logs` 族失败行。
- **messages/responses 写 attempt 错误码**：钩子只赋 `attemptErrCode / attemptErrMsg`（messages.go:252-253 / responses.go:270-271）⇒ 各自的 deferred safety net 统一写库：messages.go:180-185、responses.go:198-203（`recordFailedRequestWithKey`，handler.go:8400）。503 fail-closed 分支同样经钩子落 `tenant_budget_unavailable`（handler.go:2723-2725）。
- **audio 纯 HTTP**：`onReject` 传 `nil`（audio_service.go:1342）⇒ 共用体内 `if onReject != nil` 跳过（handler.go:2723/2742），只有 `writeErrorJSON` HTTP 响应。

### ④ warn 模式行为与修复前逐字零变化 —— **成立（chat 面）；P3 级措辞瑕疵**

- chat 面：diff 逐字对拍（`git show f712ea685 -- domains/streaming/handler.go`）——三段语义（hard 查询失败 503 / 非 hard 查询失败放行 / warn 超限放行）的日志文案、键值、顺序、writeErrorJSON 参数**逐字一致**，只是从内联块抽进共用体，warn 分支仍为 `slog.Warn("tenant monthly budget exceeded (warn mode, not blocking)", tenant_id, spent_cents, budget_cents)` + 放行（handler.go:2748-2753）。
- 瑕疵（P3）：messages/responses/audio 三面修复前**根本没有门**，warn 超限现在**每请求新增一条 Warn 日志**（零拦截行为）。请求结果面上「零变化」成立，日志面上三面是新行为。hard 未启用的 warn 租户若长期超限，会以每请求一条的频率刷日志——无节流、无指标（与 R-MAAS3 登记里「无指标」同一欠账族）。

### ⑤ hard 429 响应体 error.code == tenant_budget_exceeded —— **成立**

`writeErrorJSONCtx(ctx, w, 429, requestID, "insufficient_quota", i18n.MsgTenantBudgetExceeded, nil)`（handler.go:2745）→ `writeErrorJSON`（handler.go:9849-9851）→ `writeErrorJSONWithDebug`（handler.go:9952-9971）构造 `error.code = code = messageKey = "tenant_budget_exceeded"`（i18n/messages.go:28 常量逐字为 `tenant_budget_exceeded`）。8 语种 locale 全部带键（`grep -l tenant_budget_exceeded i18n/locales/*.json` = 8 文件）。注意 `error.type = "insufficient_quota"`（与 credits/budget 族既有 402 同 type），code 才是租户预算专属判别键。

### ⑥ SetMaas 可选接口 nil-safe —— **成立（双向）**

- 接口断言不通过 ⇒ 门字段保持 nil ⇒ `applyTenantMonthlyBudgetGate` 首行 `checker == nil` 放行（handler.go:2709；audio_billing.go:44-48）。
- typed-nil 边界也安全：`*maas.Service` 为 nil 时 `CheckMonthlyBudget` 首行 `s.Enabled()` 为 nil 接收者安全的 `s != nil && s.pool != nil`（maas/service.go:61-63）⇒ 返回 `(nil, nil)` 放行，不 panic。
- 生产接线：main.go:3330-3336（`maasSvc` 仅在 `dbConn.Enabled()` 时创建，`audioService.SetMaas(maasSvc)` 经 `audioService != nil` 守卫）。

---

## 三、Q2 穷尽性：扣积分写方全量清单 —— **无第 5 个未过门的面**

`maas_credit_consumption_buckets` 的写方只有两处：

| 写方 | 性质 | 是否请求面 |
|------|------|-----------|
| `maas/service.go:262-271`（`chargeTokensWithMultiplier` 尾部原子 upsert） | 实时扣费写 | 是（唯一实时写方） |
| `maas/credit_buckets.go:61-83`（`BackfillCreditConsumptionBuckets`） | 启动回填（从 `_request_logs.credits_charged` 重导出，`main.go:3345-3350`） | 否（运维收敛，无绕过语义） |

实时写方的全部上游调用点（全仓 grep `ChargeRequest*`，非测试、非定义）：

| # | 调用点 | 所在面 | 是否被门覆盖 |
|---|--------|--------|-------------|
| 1 | `domains/streaming/handler.go:7775`（emitTelemetry 内） | chat —— 调用点 handler.go:6875 | ✅ handler.go:2994 |
| 2 | 同上 | messages —— 调用点 messages.go:1240 | ✅ messages.go:251 |
| 3 | 同上 | responses —— 调用点 responses.go:1209 | ✅ responses.go:269 |
| 4 | `domains/streaming/audio_billing.go:79`（`chargeTranscription`） | audio transcriptions —— 调用点 audio_transcriptions.go:89、139 | ✅ audio_service.go:1342 |

反证 grep 结论：

- `ChargeRequest`（service.go:112）与 `ChargeRequestMultimodal`（service.go:125）两个非 multiplier 变体：**零生产调用方**（仅定义与注释引用）。
- **embeddings / speech(TTS) / audio-mcp 三面不产生 MaaS 扣费**：`embeddings.go`、`audio_speech.go`、`audio_mcp.go` 零 `maasSvc/SetMaas/PreCheckCredits/chargeTranscription` 命中 ⇒ 无「扣费却不过门」的绕过（它们本就不扣）。
- `domains/stats/minute_entry.go:251-252` 的 `CalcCreditsMultimodal` 是纯算术展示（stats 分钟条目估算），不写任何账表。
- `internal/bucket/credit/provider.go:97-108`：注释提及 ChargeRequest，实现只读不改桶。

**附带发现（基线存量，非本闸缺陷）**：`/v1/messages` 与 `/v1/responses` 两面**没有** key 级 `CheckBudget` 与 `PreCheckCredits`（两文件 grep 零命中；`PreCheckCredits` 系窗口前旧提交引入且只落 chat 面）⇒ 钱包耗尽（credits 余额不足）的租户在这两面请求不会被前置拦截，只在请求末尾 `chargeTokensWithMultiplier` 抛 `InsufficientCreditsError`（charge 失败不失败响应 ⇒ 该笔实际免费/欠账）。月预算闸现已补齐这三面，但 credits pre-check 的面覆盖仍不对称——建议登记（见 §八 P3-4）。

---

## 四、Q3 门执行次数、流式行为、ctx、R-MAAS3 现状

- **每请求一次，不是每 chunk 一次**。四处接入点全部位于「鉴权后、上游 dispatch 前」：chat 在 `serveWithExecutor` 前段（handler.go:2994，位于读 body / 选模型 / dispatch 之前）；messages/responses 在鉴权成功分支内、`readRequestBodyRestore` 之前（messages.go:251 / responses.go:269）；audio 在 `authenticate()` 内（audio_service.go:1342），三面共用且仅在 handler 入口调用一次。流式长连接的 chunk 循环在 dispatch 之后，不再进入门。
- **每请求至多一次的旁证**：chat 面 `serveHTTPInner`（handler.go:2669-2671）单次进入 `serveWithExecutor`；候选重试 / 换 provider 都发生在门之后的 dispatch 层，不回调门。
- **ctx 取消/超时**：门透传 `r.Context()`（调用方各点），`CheckMonthlyBudget` 内部 `context.WithTimeout(ctx, 5*time.Second)`（maas/tenant_budget_gate.go:96-97）。客户端断开 ⇒ ctx 取消 ⇒ 查询报错 ⇒ hard 模式走 fail-closed 503（对已断开连接写响应无副作用）、非 hard 走 fail-open + Warn。无 5 秒以上的悬挂风险。
- **R-MAAS3（fail-open）登记属实、未被改坏**：
  - 查询失败且预算行没读到（适用性未知）⇒ `TenantBudgetCheckError{Mode:""}`（gate.go:117）⇒ 共用体 `errors.As` 不命中 hard ⇒ `slog.Warn("tenant budget check failed; failing open…")` + 放行（handler.go:2730-2736）。
  - 迁移 996 未应用 ⇒ `enforcement_mode` 列不存在（42703）⇒ 同上 fail-open，仅 Warn 级日志、无指标 —— 与 12h 主档 F15 登记一致（docs/12小时内修订审计-20261122-0350.md:97、158）。
  - f712ea685 对该分支**零改动**（只是搬运进共用体，语义逐字保留）。
  - 补充观察（登记外）：租户**无预算行**时 `pgx.ErrNoRows` ⇒ `(nil, nil)` 静默放行、**连日志都没有**（gate.go:113-114，设计内「未配置预算」）。它与 R-MAAS7 组合后的失效形态是**零观测**的：非 owner 角色连库 ⇒ RLS 策略 `USING (tenant_id = get_current_tenant())` 把行过滤光 ⇒ 门读到 ErrNoRows ⇒ 静默放行。修 R-MAAS7 时应一并考虑「行被过滤」与「行不存在」的区分观测。

---

## 五、Q4 R-MAAS4/5/6/7 现状逐项确认

| 登记 | 现状判定 | 证据 |
|------|---------|------|
| **R-MAAS4** 预算检查与扣费间无锁 TOCTOU | **属实、未改坏**。门两次普通 SELECT（gate.go:105/127）与扣费事务（service.go:171-253，仅 wallet 行 `FOR UPDATE`）之间无锁无事务，N 并发可同读同一 spent 全部放行 | maas/tenant_budget_gate.go:92-132；maas/service.go:171-253；窗口 diff 无任何锁引入 |
| **R-MAAS5** 迁移未应用 ⇒ 管理面 GET/PUT 500 | **属实、未改坏**。GET 直接 `GetTenantBudget`（SELECT enforcement_mode 列，42703 ⇒ err ⇒ `writeInternalErr` 500）；PUT 先读后写同路径。无 42703 降级分支 | admin/maas_my_handlers.go:183-188、201-205、231-235；maas/my_usage.go:195-221 |
| **R-MAAS6** 门分支零自动化覆盖 | **属实、未收口、暴露面反而扩大**。全仓 grep `tenant_budget|TenantBudget|CheckMonthlyBudget` 的 `_test.go` 命中仅：maas/tenant_budget_gate_test.go（纯算术 7 用例 + 闭集常量）、maas/tenant_budget_gate_realdb_test.go（`TEST_MAAS_BUDGET_GATE_DATABASE_URL` 未配即 Skip）、internal/sqlguard/migration_996_delivery_test.go（交付链）、i18n/i18n_test.go（文案键）。**domains/streaming 全包零命中** —— 四面接入、hook 注入、SetMaas 断言接线、429/503 拒绝路径全部无钉测。F3 修复（f712ea685）给三面新增了门分支却未补 handler 级测试 | grep 全仓；`git diff c6bf29aea..c446de9a4 -- '*_test.go'` 中无 streaming 侧 tenant budget 测试 |
| **R-MAAS7** RLS 启而未 FORCE + 门从不设 app.current_tenant | **属实、未改坏**。`01-schema.sql:30933`（`ENABLE ROW LEVEL SECURITY`，无 FORCE；31048-31051 策略 `USING tenant_id = get_current_tenant()`）；门查询走共享池裸 SELECT，无 `set_config('app.current_tenant',…)`（tenant_budget_gate.go:105-112）。owner 连库（当前部署模板）下 RLS 对 owner 豁免不触发；非 owner 下门静默失活（见 §四补充观察：失活形态还是零日志的） | sql/schema/01-schema.sql:30930-30951；maas/tenant_budget_gate.go:92-132 |

**R-MAAS6 新钉测落库情况**：窗口内（含 f712ea685）**没有**新增任何门分支钉测；新增测试只有 a2cb60f5b 交付时的 maas 包两份与 sqlguard 交付门。结论：登记维持，且建议升级紧迫度——F3 之后门的调用点从 1 处变 4 处、hook 从 0 变 3 种，回归面变大而覆盖仍为零。

---

## 六、Q5 M-26 收口核验 + 死面 + 依赖图

1. **installer 是独立 module，`cd installer && go test ./...`**：
   - **M-26 两门实测绿**：`go test ./cmd/llm-gw-installer/... ./internal/dbinit/...` ⇒ `ok … cmd/llm-gw-installer 5.587s` + `ok … internal/dbinit 0.097s`——两个历史红 `TestStartupFilesAreAllEmbedded` / `TestStartupMigrationsAboveCutoffHaveLockGuard`（所在包）确已收，根修在窗口内 `4b8658b74`（993 补 go:embed×2+var×2+map×2 与 up/down `SET lock_timeout='5s'`，四副本 shasum 一致）。M-26 移交宣称**与实测一致**。
   - **全量 `go test ./...` 有 1 个非 M-26 的红**：`FAIL … installer/internal/launcher/backend 600.027s`——`skipIfNoDocker`（compose_test.go:14-21）的 `exec.Command("docker","info").Run()` **无超时**，而本机 docker 守护进程当前挂死（`~/.local/bin/docker` 存在、`docker info` 独立探针同样无限悬挂，需手动 kill）⇒ 测试进程拖到 go test 默认 10 分钟包超时后 panic。归因 = **基线存量测试健壮性缺陷**（skip 守卫本身可悬挂）× **本机环境态**（守护进程无响应）；窗口 diff **未触碰** `installer/internal/launcher/backend/`（窗口 installer 改动仅 migrations/embeddata/main.go/dbinit）。其余 launcher 包（checker/e2e/orchestrator/proxy/store/upgrader）全 ok。
   - ★ 通用式：`cmd | tail` 会把管道退出码换成 tail 的 ⇒「exit 0」不等于被测命令绿；skip 守卫里的外部进程探测必须带超时，否则「跳过逻辑」自己变成悬挂点。
2. **api/maas v0.1 REST 死面未变**：十端点（`api/maas/server.go:45-60`：sellable:query / holds×4 / quotes:estimate / entitlements:activate / usage-holds×2 / ledger:correct）——全仓唯一 import `llm-gateway-go/api/maas` 的非自测文件为**零个**（仅其自身 server_test.go）；cmd/gateway/main.go 不 mount、admin mux 的 `/api/maas/*` 是另一族（admin/maas_handlers.go:51-60 + maas_my_handlers.go:294-299，活的）。死面登记（R-MAAS1 族）维持。
3. **gateway 依赖图**：`go list -deps ./cmd/gateway` 实测仍含 `github.com/gin-gonic/gin`（含 binding/render/bytesconv/codec/json）+ `github.com/gin-contrib/sse` + `github.com/labstack/echo/v5`；**sonic 不在依赖图**。双框架拖入现状未变（基线存量）。

---

## 七、Q6 定向门禁实测（HEAD=c446de9a4，本机 arm64，go sdk）

- `go build ./maas/... ./domains/streaming/` ⇒ **BUILD_OK**。
- `go test ./maas/... ./domains/streaming/...` ⇒ **全部 ok，零失败**（退出码 0）：
  - `domains/streaming` **150.358s**；`domains/streaming/executors` 24.173s；`integrity` 0.044s；`state` 0.061s；`webcookie` 0.030s；`maas` 0.018s（另以 `-count=1` 去缓存复跑 `./maas/` ⇒ ok 0.013s——maas 包常规测试是纯单测，realdb 用例无 DSN 即 Skip）。
  - 总时长 158.06s（real）。**失败集合为空**（无逐字可记的失败项）。

---

## 八、Q7 顺带触达

1. **audio_billing.go（+18）/ audio_service.go（+12）窗口改动 = F3 接入本身**：前者新增 `audioTenantBudgetGate` 接口 + `SetMaas` 内断言接线（audio_billing.go:25-48）；后者新增 `tenantBudgetGate` 字段 + `authenticate()` 门调用（audio_service.go:114-118、1337-1344）。无计费金额口径改动（`chargeTranscription` 的 multiplier 1.0 与 minimum-1s 语义未动）。
2. **凭据链（objective 13）被触碰处**：窗口内唯一凭据域 diff 是 `admin/diagnostics_credential.go`（`0c3474646` error-playbook 闭集扩容轮）——把 `handleForceRecoverSingle` 的恢复链抽成 `ForceRecoverCredential` 单一真源 + `steps` 留痕 + `ErrForceRecoverNotFound` 哨兵（admin/diagnostics_credential.go:244-266、312-372）。行为等价重构：同一 SQL、同一 lifecycle_status 守卫（263-265 RowsAffected==0 ⇒ NotFound）、同一缓存失效链；HTTP 响应仅**新增** `steps` 键。**凭据错误记录 / 凭据详情呈现链（objective 13 的对象）未被触碰**。
3. **MaaS 价格面（objective 20）窗口内零变化**：`git diff c6bf29aea..c446de9a4 --name-only` 中无 maas/model_rates*、pricing*、bills*；唯一带 "price/billing" 的 diff 是 audio_billing.go（门接入）与一份 maestro flow（price-compare-model-picker.yaml，量具资产）。`maas/my_usage.go` 的 diff 仅 enforcement_mode 贯通（TenantBudget 结构 +013 行），非价格面。

---

## 九、发现分级（P0~P3）与归因

> 本轮 **未发现 P0/P1**。F3 修复（f712ea685 的 P0-3）经六项子断言独立复核成立；登记项无一被改坏。

| 级别 | 发现 | file:line | 归因 |
|------|------|-----------|------|
| **P2-1** | **/v1/messages 硬闸拒绝信封协议形状错位**：租户门 429/503 经共用体写 OpenAI chat 形状（`{"error":{message,type,code,request_id}}`，writeErrorJSONWithDebug），而该面其余全部拒绝（missing_key/invalid_key/auth_unavailable/method_not_allowed 等 35 处）一律 `writeAnthropicError`（`{"type":"error","error":{type,message}}`，messages.go:1981-2000）。Anthropic SDK 按顶层 `type:"error"` 解析 ⇒ hard 预算拒绝可能被客户端渲染为不可识别错误（HTTP 429 状态码与 error.code 仍正确）。共用体把 `writeErrorJSON` 焊死，面专属形状无法注入 | domains/streaming/handler.go:2726-2727、2745；对照 messages.go:214/223/230/195 | **窗口内引入**（f712ea685 F3 新行为；a2cb60f5b 时该面无门） |
| **P2-2** | R-MAAS6 未收口且暴露面扩大：四面门分支（429 / 503 fail-closed / warn 放行 / default+IsInternal 豁免 / SetMaas 断言接线 / hook 注入）零自动化覆盖；F3 修复轮自己点名「门分支零自动化覆盖」却没有在修复时补钉测 | 全仓 grep（streaming 侧零命中）；接入点 handler.go:2994 / messages.go:251 / responses.go:269 / audio_service.go:1342 | **基线存量**（a2cb60f5b 起），F3 使其加重 |
| **P3-1** | 「warn 零变化」在日志面不严格：messages/responses/audio 三面 warn 超限新增每请求一条 Warn 日志（无节流无指标）；chat 面逐字零变化成立 | handler.go:2748-2753 | **窗口内引入**（新面上的新日志；拦截行为零变化） |
| **P3-2** | R-MAAS3 失效形态补充：无预算行的 `ErrNoRows ⇒ (nil,nil)` 完全静默（无日志无指标），与 R-MAAS7 的 RLS 过滤叠加后门失活零观测；登记未覆盖此组合形态 | maas/tenant_budget_gate.go:113-114 | **基线存量**（登记外补充观察） |
| **P3-3** | /v1/responses 面门拒绝形状与该面 `writeResponsesError` 差异较小但不同源（缺 `param:"model"` 键，其余同族） | handler.go:2745 vs responses.go:1861-1881 | **窗口内引入**（同 P2-1 根因） |
| **P3-4** | messages/responses 面无 `CheckBudget`/`PreCheckCredits` 前置闸（chat 有、audio 有 CheckBudget）⇒ 钱包耗尽租户在这两面不被前置拦截，charge 末尾失败即免费放行。非月预算闸范围，建议单独登记 | messages.go / responses.go 全文零命中；chat 侧 handler.go:2955-2988 | **基线存量**（PreCheckCredits 系窗口前提交，只落 chat 面） |
| **P3-5** | api/maas v0.1 十端点死面维持（唯一 import 方是其自身测试）；gin+echo 双框架仍在 gateway 依赖图（sonic 已不在） | api/maas/server.go:45-60；`go list -deps ./cmd/gateway` | **基线存量**（窗口零变化，R-MAAS1/死面登记维持） |
| **P3-6** | installer `skipIfNoDocker` 探测无超时：docker CLI 在 PATH 但守护进程无响应时，`docker info` 永久悬挂 ⇒ 整个 launcher/backend 包 600s 超时 panic（skip 逻辑自己变成悬挂点）。本机实测复现；窗口未触碰该目录 | installer/internal/launcher/backend/compose_test.go:14-21 | **基线存量** × 本机环境态（docker 守护进程挂死）；非窗口回归 |

---

## 十、修复建议

1. **P2-1（建议随 maas 列车收口）**：给 `tenantBudgetRejectHook` 增加面专属信封能力——钩子已携带 errCode/errMsg/clientStatus，最小改法是把「写 HTTP 响应」也交给钩子（chat 传 writeErrorJSON 形状、messages 传 writeAnthropicError 形状、responses 传 writeResponsesError 形状、audio 维持现状），共用体只保留判定与放行语义；或次优：messages/responses 在钩子里覆写响应。修复时按 R-MAAS6 处方补 handler 级钉测（四象限 + 形状断言一次到位）。
2. **P2-2**：按 12h 主档 R-MAAS6 修法方向落 handler 级钉测（假 checker 即可，不需真库）：hard 达限 429 + error.code、hard 查询失败 503 + attempt/失败行、warn 超限放行、default/IsInternal/nil checker 豁免、四面接线存在性（防再漂移）。
3. **P3-1**：warn 超限日志加租户级节流或仅首超限告警（与 R-MAAS3 的「至少打点」同一张账）。
4. **P3-2 / R-MAAS7**：修 R-MAAS7 时区分「无预算行」与「行被 RLS 过滤」（后者应报错而非静默放行），并补启动期 owner/角色断言。
5. **P3-4**：登记「messages/responses 面缺 PreCheckCredits/CheckBudget」为独立项（归 chat/三协议对齐列车），与月预算闸解耦。
6. **P3-6**：`skipIfNoDocker` 的 `docker info` 探测加 `exec.CommandContext`（2~5s）超时，守护进程挂死时跳过而不是拖死整包；属 installer 列车一次性小修。

---

## 十一、实测记录附注

- 门禁实测环境：本机 darwin/arm64，go via `$HOME/sdk/go/bin`；窗口 HEAD 工作区干净（仅本报告未跟踪）。
- installer 全量 `go test ./...` 实测 601s（其中 launcher/backend 独占 600s 超时，其余包合计 <7s）；M-26 两门包独立复跑 5.6s/0.1s 全绿。`4b8658b74` 自述 6.0s 为热缓存口径，与实测一致。
- 真库相关（R-MAAS4/7 的活体行为、RLS 角色）未做活体复跑（纪律禁止连生产），以 schema/代码级证据 + 仓内登记互证定级。
