# 审计报告 · 轴 G：新增后台任务 / 持久化日志 / 资源监控的可靠性与安全性 + 横切面安全

- 仓库：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`（只读审计，未修改任何源码）
- 日期：2026-09-05
- 范围：`pkg/logger/persistent.go`、`pkg/monitor/resource_monitor.go` + `rlimit_nofile_{linux,darwin,other}.go`、`bg/auto_route_settle_worker.go`、`bg/auto_route_affinity_worker.go`、`domains/hooks/handoff/trigger_hook.go`、`domains/hooks/observability/telemetry/selection_writer.go`、`domains/authentication/keystore_sync.go` + `verifier.go` 改动、`cmd/gateway/main_helpers.go`、`cmd/gateway/recovery_gate_adapter.go`、`cmd/gateway/main.go` 生命周期接线
- 基线：已读 round1（`.audit/logging-resource-monitor-2026-09-05.md`）、round2、round3 报告；已读同日 axis-A~F 报告避免重复
- 方法：逐文件人工审读 + main.go 生命周期接线核对 + 针对本地 PG17 实测 `$1::interval` 参数格式 + `go test -race` 五个新包 + 对照代码库 panic-recover 约定

## 一、已修复核确认清单（round1-3 修复项全部复核通过，无回归）

| # | 原 发现 | 复核证据 | 结论 |
|---|---------|----------|------|
| 1 | round2#1：Stop-before-Start 后 Start 导致采集循环泄漏 / double-close | `pkg/monitor/resource_monitor.go:235-247`（`started`/`stopped` 双标志，stopped 后 Start 为 no-op）；`stopDone` 仅由 `collectLoop` 关闭（:275） | 通过 |
| 2 | round2#2：三处 os.Exit 未停监控/未落盘 | `cmd/gateway/main.go:445-452`（启动 panic defer）、`:6497-6504`（pprof 配置错）、`:6560-6568`（listen 失败）均先 `resourceMonitor.Stop()` → 异常记录 → `persistentLogger.Close()`；双层 panic defer 的 LIFO 次序（:343-355 无条件安装 + :443-456 dbConn defer）经推演无双重处理、无遗漏 | 通过 |
| 3 | round2#3/4：GO_VERSION 恒空、`append` 改写调用方变参 | `persistent.go:213`（`runtime.Version()`）、`:17-21`（`withFields` 复制后再追加） | 通过 |
| 4 | round3#1：初始化失败后 `(nil, nil)` | 包级 `persistentInitErr`/`resourceMonitorInitErr`（`persistent.go:54-69`、`resource_monitor.go:136-151`），无局部变量遮蔽路径 | 通过 |
| 5 | round3#2：RLIMIT_NOFILE 硬编码 7 | `rlimit_nofile_linux.go`(7)/`_darwin.go`(8)/`_other.go`(0)，`resource_monitor.go:366` 以 `rlimitNOFile != 0` 跳过不匹配平台 | 通过 |
| 6 | round3#3：panic 兜底仅在初始化成功分支安装 | `main.go:343-355` defer 无条件安装，内部 `persistentLogger != nil` 守卫 | 通过 |
| 7 | round3#4：Encode 错误静默丢写 | `resource_monitor.go:394-398`、`:475-477` 记错误并 `slog.Warn`（stderr 路径，无递归）；monitor 侧未再发现其它静默写路径 | 通过 |

### 其他横切面复核通过项（本轴新增验证）

| # | 项目 | 证据 |
|---|------|------|
| 8 | ticker/timer Stop 全部配对 | `resource_monitor.go:274`、`auto_route_settle_worker.go:144`、`auto_route_affinity_worker.go:135`（均 defer）；`selection_writer.go:133` defer + 批满后 drain-then-Reset 模式正确（:158-164） |
| 9 | lumberjack Close 幂等、5 个 writer 全部关闭 | `persistent.go:242-278`；正常关闭（main.go:6921）与三条异常路径重复调用无副作用 |
| 10 | 除零/下溢/NaN | FD 比率 `limit.Soft > 0` 守卫（`resource_monitor.go:368-370`）；泄漏检测 `duration <= 0` 跳过（:418-420）+ 有符号内存差值（:424）；`autoroute/affinity.go:228-291`（ShrinkAffinity/UpdateEMA/DecayAffinity 对 n<=0、NaN/Inf、idle<=staleAfter 全守卫）；`routingOnlyHealth` 分母 >0 守卫（settle worker :443-463）；RetryRatio 分母 >0（:417-419） |
| 11 | `$1::interval` 传入 Go duration 字符串 | 本地 PG17 实测：`'2m0s'::interval = 00:02:00`、`'336h0m0s'::interval = 336:00:00`，无 m=月歧义（settleDelay/affinityWindow/baselineWindow 均正确）；仓库先例 `bg/opslog_trimmer.go:127` 同模式 |
| 12 | SQL 参数化 | 新 SQL 全部 `$n` 占位；`writeReward`/`abandon` 的表名拼接（`auto_route_settle_worker.go:475-507`）取自内部 `storageTier` 常量（'hot'/'parent'），非外部输入；`selection_writer.go:198-265` VALUES 占位符由索引生成 |
| 13 | 敏感信息不入日志/快照 | monitor/logger 仅主机名、PID、资源计数（`resource_monitor.go` 全文无凭据/正文字段）；selection 行仅 ID+数值（`selection_writer.go:1-15` 隐私注释属实）；keystore 快照只含 HMAC-SHA256 hash+元数据（`verifier.go:607-611`），文件 0600/目录 0700（`keystore_sync.go:416-424`）；handoff 摘要经 `redactResumeSensitive` 脱敏（`request_hook.go:334-339`，应用于 `trigger_hook.go:546`） |
| 14 | keystore 共享状态加锁 | store map 全部经 `storeMu`（lookup/upsert/remove/full-load 整体替换/InvalidateKeyID，`keystore_sync.go` + `verifier.go:705-723`）；快照 marshal 持 RLock（:409-412）；读取时重验 expires_at（`verifier.go:641-649`）；例外见发现 #2 |
| 15 | LLM/webhook caller 超时 | `goal/llm_caller_adapter.go:60-63` 兜底 15s → ChatLLMCaller 的 client 不可能出现 0 超时；webhook client 5s（`trigger_hook.go:233-235`）；`CallLLMWithAPIKey` 仅用于 upstream 供应商凭据（`request_hook.go:289-297`），符合"网关认证 key 不得经过该接口"的注释约束 |
| 16 | resource_monitor 无 HTTP 端点 | 文件无 net/http 依赖；`GetLastSnapshot`/`GetAlerts` 全仓无外部调用方，不存在未鉴权暴露面 |

### 其他轴已报、本轴不重复（交叉引用）

- axis-D P1：settle worker 直写分区父表 `auto_route_selections`（`auto_route_settle_worker.go:475-507`）；axis-D P2：outcome join 仅读 `request_logs_hot` 与 8h promote 窗口冲突、656 无网关侧 ensure。
- axis-C P2#3：dispatch worker 无 recover —— 本轴发现 #1 是 **bg/hooks/auth 包**的同族缺口，文件不相交，不构成重复。

## 二、新发现清单

| # | 级别 | 位置 | 问题 | 影响 |
|---|------|------|------|------|
| 1 | **P2** | `bg/auto_route_settle_worker.go:140-188`、`bg/auto_route_affinity_worker.go:131-185`、`domains/authentication/keystore_sync.go:78-136`、`domains/hooks/observability/telemetry/selection_writer.go:129-194` | **四个新增常驻 goroutine（settle/affinity sweep 循环、keystore 同步循环、selection writer）无 panic recover**。任一 sweep/flush/同步迭代内 panic 将击穿 goroutine 直接杀死整个网关进程；且 main.go 的两处 panic-recover defer 只覆盖 main goroutine，`persistentLogger.LogPanic` 捕获不到 —— 崩溃后无持久化 panic 记录，只剩 stderr/journal | 后台任务内任一意外 panic（如 pgx 驱动层、未来对 sweep 代码的回调扩展）= 全进程宕机 + 无持久化痕迹。代码库已有约定：`bg/model_probe.go:174-179`、同包 `telemetry/client.go:636,904` 均在循环入口 recover，新文件偏离约定 |
| 2 | **P2** | `domains/authentication/keystore_sync.go:204-227` | **delta 同步的 invalid-set 查询在持有 `storeMu` 写锁期间迭代 DB 行**：`:215` 先 `kv.storeMu.Lock()`，`:216` 起 `invRows.Next()` 才开始读查询响应（pgx 流式），锁内可阻塞至 `keyStoreQueryTimeout`（30s）。而每个请求的认证快路径 `lookupStore`（`verifier.go:313-315`）都要取 `storeMu.RLock()` | DB 变慢期间，一次 5 分钟一轮的 delta 同步可让**全进程认证请求串行卡顿最长 30s**。对照同文件 `keyStoreFullLoad`（:141-169）的正确模式：先收集到局部 map、最后锁内一次性换入 |
| 3 | **P3** | `bg/auto_route_settle_worker.go:116-138`、`bg/auto_route_affinity_worker.go:109-129` | **Stop/Start 生命周期残留缺口**：`w.cancel` 在 `Start` 内 CAS 之后才赋值、`Stop` 中无同步读取 —— (a) Stop 与 Start 并发时存在 `w.cancel` 数据竞争；(b) 若 Stop 先消费 `stopOnce` 时 `w.cancel` 仍为 nil（或 Stop 先于 Start 调用后再次 Start），context 永不取消，`<-w.done`（:137/:128）永久阻塞、goroutine 泄漏。与 ResourceMonitor round2 修复的 `stopped` 标志模式不对称；`bg/auto_route_workers_test.go` 只覆盖 Stop-without-Start、双 Start、双 Stop，未覆盖并发/逆序场景 | 当前 main.go 唯一调用点是顺序 Start + defer Stop，不可达；属回归地雷（新增调用方即触发） |
| 4 | **P3** | `cmd/gateway/main.go:4830-4838` + `selection_writer.go:96-104,136-152` | **新 worker 的 Stop 全部挂在 defer 上，跑在 5s stopCtx 看门狗（main.go:6643-6660）与日志关闭（:6915-6928）之后、无超时保护**。`StopSelectionWriter` 的 drain 循环要清空 4096 容量队列、每批 flush 独立 5s 超时，DB 挂死时最坏 ~82×5s≈7 分钟；settle/affinity `Stop` 最坏等一个 sweep。进程退出可越过 systemd `TimeoutStopSec=35s` 被 SIGKILL；此阶段的 slog.Warn 只剩 stderr。另外 Stop 之后再入队的 `WriteAutoSelection` 行滞留队列永不持久化（无告警计数） | 拖慢/失控关闭窗口；非数据丢失（持久化日志已先行关闭），但与"5s 快速失败、超时 SIGKILL"的关闭预算设计相悖 |
| 5 | **P3** | `domains/hooks/handoff/trigger_hook.go:599-619`（设置注册：`settings/handoff_specs.go:347`、`admin/modules.go:141`；写权限：`admin/settings.go:236`） | **`handoff.notify_webhook` 为租户级可写设置，网关服务端向其 POST 会话摘要+session/tenant ID，无 scheme/host 白名单**：模块 DangerLevel=Warning（非 Dangerous），普通 admin 即可设置 → 可指向内网地址（SSRF，含云元数据端点）或外部地址批量外带会话摘要（数据外泄通道）。另 `:619` 失败时把完整 webhook URL 写入日志 —— 该类 URL 通常内嵌 access_token | admin 权限域内的 SSRF/外泄 + webhook 凭据入日志；建议：URL 白名单/私网段拒绝、日志脱敏 URL |
| 6 | **P3** | `domains/hooks/handoff/trigger_hook.go:312-384,390-480` | **handoff 决策+落库+摘要 LLM 调用全部同步在响应路径执行**：`evaluate` 最多 3 次 DB 读（:325,:328,:371），`fire` 再 2 次 DB 读（:402,:417）+ `RecordHandoff` + `buildSummary`（LLM 调用，client 超时 15s）后才返回 InterceptResult —— 流式响应的 `InterceptStreamEnd` 同样如此（:267-300）。min_messages/cooldown/max_per_session 限制了频率，但每个冷却窗口后的首次触发仍给该请求尾部加最长 15s+ 延迟并消耗摘要 token。且 guard 是先读后写（GetHandoffCount → RecordHandoff），同会话并发请求可同时通过 max/cooldown 检查，超发次数=并发度 | 尾延迟与 token 成本可控但不为零；TOCTOU 超发有界。建议：fire 移出响应路径（post-response 异步）、guard 改原子占用 |
| 7 | **P3** | `bg/auto_route_affinity_worker.go:250-256,374` | **EMA 前值查询错误被 `_ =` 吞掉**：`QueryRow(...).Scan(&prevEMA, ...)` 失败时 prevEMA=0，`UpdateEMA` 按"无历史"重新播种（`autoroute/affinity.go:256-266`）—— 瞬时 DB 错误会静默丢弃该 (task,profile,canonical,tenant) 桶的累计 EMA 历史，affinity 短暂回摆中性（后续窗口逐步恢复）。同函数 `applyStalenessDecay` 的 `Exec` 错误也整体忽略（:374 `_, _ =`），decay 失败不留任何日志 | 低影响（EMA 自愈、decay 有读时兜底），但属静默失败类，至少应 slog.Debug/Warn 留痕 |

## 三、验证结果

- `go test -race -count=1 ./pkg/logger ./pkg/monitor ./domains/hooks/observability/telemetry ./domains/hooks/handoff ./domains/authentication`：全部 ok（本机 darwin/arm64 实跑）
- `-race` 覆盖缺口：`bg` 包的 settle/affinity 仅有 nil-db 生命周期测试（`bg/auto_route_workers_test.go`），无并发 Start/Stop 用例（对应发现 #3）；`pkg/logger`、`pkg/monitor` 有 -race 覆盖（round2/3 报告声称，本轮复跑通过）
- `$1::interval` 格式：对本地 PG17（docker llm-gateway-pg）实测通过（见复核表 #11）
- 只读审计：未修改任何仓库文件；本报告为唯一新增产物

## 四、总体结论

- round1-3 的全部修复（1 个 P1、4 个 P2、2 个 P3）复核**通过，无回归**；资源监控与持久化日志的生命周期、退出落盘、静默丢写三类问题已闭环。
- 本轴新发现 **0 个 P0/P1、2 个 P2、5 个 P3**。两个 P2 的共性是"新常驻 goroutine 偏离了代码库已建立的健壮性约定"：panic recover（model_probe/telemetry 已有）与"先收集后换锁"（keyStoreFullLoad 已示范）。修复成本低（各自 ≤20 行），建议在下一次后台任务相关改动中一并处理。
- 横切面安全面整体干净：新代码无凭据/请求正文入日志，SQL 全参数化，快照文件只含 HMAC hash 且权限收紧；唯一外部出口是 admin 可配的 handoff webhook（发现 #5），建议加 URL 治理。
- 数值安全全面复核通过（除零、NaN/Inf、负时长、interval 单位歧义均无）。settle/affinity worker 的存储层问题（父表 UPDATE、hot 窗口冲突）已在 axis-D 报告，本轴不重复。
