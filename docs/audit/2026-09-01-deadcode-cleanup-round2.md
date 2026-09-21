# 死代码清理 Round 2（2026-09-01）

分支：`fix/24h-audit-round2-20260901`
原则：只删除"已验证零行为风险"的死代码，宁可少删不可误删。

## 一、删除清单与引用核查证据

### 1. UnifiedProbeScheduler 死分支（整体移除）

背景：2026-06-28 引入（commit 20ad9de0），cutover 未完成即中断，
`bg/unified_probe_scheduler.go` 文件头已自标 `DEPRECATED / DEAD BRANCH
as of 2026-06-29`。启动开关 `LLM_GATEWAY_ENABLE_UNIFIED_PROBE_SCHEDULER`
默认 false。

| 文件 | 改动 | 引用核查（grep 全库，排除自身/测试） |
|---|---|---|
| `bg/unified_probe_scheduler.go` | 整文件删除（601 行） | `NewUnifiedProbeScheduler(` 全库 0 处生产调用 |
| `cmd/gateway/main.go` | 删 :3120 `var unifiedProbe` 声明、:6524-6526 永假 `if unifiedProbe != nil { Stop() }`、:1736-1740 注释与 `routingExec.UnifiedProbeScheduler = nil` 赋值（删后实际行号略有偏移） | 生产引用共 3 处，全部在 main.go，全为死分支 |
| `domains/streaming/executors/executor.go` | 删 :831-838 `UnifiedProbeScheduler interface{ OnRealRequest(...) }` 字段 | 生产引用仅 main.go:1740（nil 赋值）与 nodehealth 2 处 nil-guard |
| `domains/streaming/executors/executor_nodehealth.go` | 删 :211-218 两个 case 内 `if e.UnifiedProbeScheduler != nil` 调用（字段恒为 nil，no-op）；case 分支保留并加注释 | 同上 |
| 根目录 `llm-gateway-go` | 删除 58MB 编译产物（git 未跟踪、被 .gitignore 忽略，`rm` 即可） | 非源码 |

**风险说明**：`admin/probe_dashboard.go` 的 `queryUnifiedProbeQueueStats` /
`queryUnifiedProbeSystemHealth` / `UnifiedProbeQueueStats` 等是**队列快照
合并的另一套活语义**（probe queue dashboard），与 scheduler 死分支仅名字
相近，本次**未做任何改动**；其测试 `admin/probe_dashboard_test.go` 全部保留。

### 2. v1 SessionTurnsHandler（未挂载死代码，精简保留共享符号）

原计划整体删除 `admin/session_turns.go`（231 行）+ 其测试。核查发现：
`NewSessionTurnsHandler` 生产调用 0 处（仅自身测试引用，v2 已由
`serveSessionTurnSubroute` 接管），但该文件还定义了被 3 个活文件使用的
共享符号，整删会破坏编译：

- `TurnListItem` ← `admin/turns_list.go:42`、`admin/session_turns_v2.go:110,113`
- `validateCursor` / `errCursorMismatch` / `encodeCursor` / `cursorPayload` ←
  `admin/turns_list.go:84-86,182`、`admin/session_turns_v2.go:74-76,155`、
  `admin/turns_sessions.go:347`

**实际处理**：`admin/session_turns.go` 精简为仅含上述共享 cursor 符号
（231 行 → 112 行，原样迁出、零行为变更），删除 SessionTurnsHandler、
Routes、listTurns、getTurn/snapshot stub、AdminClaims（后者全库无其它引用）；
`admin/session_turns_test.go` 整删（117 行，仅测试已删 handler）。

### 3. 二进制

根目录 `llm-gateway-go`（58MB，7月23日构建产物）：`git ls-files` 确认未跟踪、
被 .gitignore 覆盖，直接 `rm` 删除。

## 二、验证结果

在隔离环境（暂存其它代理的未提交改动后）验证：

- `go build ./...` 通过
- `go vet ./cmd/gateway/... ./bg/... ./domains/streaming/executors/... ./admin/...` 通过
- `go test ./bg/... ./admin/... ./domains/streaming/executors/... -count=1` 全部 ok

注：混合工作区状态下 `bg` / `admin` 各有 1 个失败（`TestMaterializedViewRefresher_ConcurrentRefreshAllSerialized`
nil-pool panic、`TestHandleTrigger_ProbeEnqueueError` 期望 503 实得 200），
两者经 stash 对照均与本次删除无关——前者源自工作区中未完成的
`materialized_view_refresher.go` 改动，后者在干净 HEAD 同样失败。

## 三、回滚项

- `admin/session_turns.go` 首次整删导致 `admin` 编译失败（共享 cursor 符号
  被 turns_list / session_turns_v2 / turns_sessions 引用）。已"回滚整删、
  改为精简"：仅删 handler 死代码，共享符号原样保留。无其它回滚。

## 四、下一批候选（仅记录，未执行）

1. `modelProbe` / `suspiciousProbe`（`cmd/gateway/main.go` 声明 + bg 实现）：
   注释标记 "TODO: remove after unifiedProbe validation"，因 unifiedProbe
   已删而失去迁移前提，待确认 ProbeQueue 路径完全覆盖后删除。
2. `internal/release` Gin 死包：确认无生产引用后整删。
3. `writeJSON` 收敛：多处 handler 手写 `w.Header().Set + json.NewEncoder`，
   统一到 admin 包公共 helper。
4. `admin/handler.go` 拆分：单文件过大，按资源域拆分。
5. 579 处中文 i18n：错误/日志文案国际化（量大，需产品确认方案）。
