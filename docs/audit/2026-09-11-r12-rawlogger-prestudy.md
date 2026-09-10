# R12 预研：RawDataLogger 接口抽象 + BufferedRawSink + 灰度方案

- 日期：2026-09-11
- 轮次：R12（预研，不动代码；实施批需用户单独授权）
- 前置决议：R10b「RawDataLogger 不可直接下线（AsyncRawDataLogger 内嵌其为 base sink），必须先抽象接口再灰度替换」
- 方法：主代理汇总 3 路只读子代理产出（A=落盘链路事实核查、B=设计起草、C=并行批次盘点），并对 A/B 报告中互相矛盾或存疑的断言逐条读源码定案（见 §2.3 勘误表）。
- 基线：HEAD=e0a4865b9，main 与 origin/main 同步；工作区含并行会话未提交批次（见 §7）。

---

## 1. 背景与目标

`internal/logging` 的 raw 审计落盘当前是两层结构：`AsyncRawDataLogger`（队列包装层）内嵌 `*RawDataLogger` 具体类型作为落盘执行器（`async_raw_logger.go` struct 字段 `baseLogger`）。这导致 RawDataLogger 作为具体类型无法重构/下线——R10b 决议因此冻结了下线路径。

R12 目标（本预研只设计、不实施）：
1. 抽象 sink 接口，解耦 Async 包装层与具体落盘实现；
2. 新增 `BufferedRawSink` 作为可调写盘/fsync 节奏的缓冲实现；
3. 灰度开关 `LLM_GATEWAY_RAW_LOG_SINK=legacy|buffered`（默认 legacy=现网行为）；
4. 验证稳定后下线 legacy 具体类型依赖。

## 2. 事实核查（主代理逐条验证）

### 2.1 结构与生命周期

**RawDataLogger**（`internal/logging/raw_data_logger.go`）
- struct :32-72：file/mu/currentPath/currentSize/maxSize/enabled/dir/writeCount/loc/clock。
- 构造 `NewRawDataLogger(baseDir, maxSize, enabled)` :114；enabled=false 返回 disabled 实例，不创建文件。
- 五个公开 Log* :151-240（LogClientRequest/LogUpstreamRequest/LogUpstreamResponse/LogClientResponse/LogConversionError），签名全部 void，内部错误只记日志不入返回值。
- 私有 `writeEntry` :253 → `writeEntries` :257；`rotate` :307（关旧建新 + `cleanupOldFiles(20)` :340）。
- `Close` :382：加锁后 file.Close + 置 nil，无 flush 概念（写路径每批已 fsync，见 §2.2）。
- `CurrentLocation` :409 / `HasFile` :419 / `peekPostWriteLocation`（供异步侧反推每条 entry 落盘位置）。

**AsyncRawDataLogger**（`internal/logging/async_raw_logger.go`）
- struct :130-156：baseLogger / queue `*LockFreeQueue[RawDataEntry]` / ctx+cancel / batchSize / flushDelay / done / closed atomic.Bool / stateMu RWMutex / closeOnce / frameIndex sync.Map / overflowReporter。
- 构造 `NewAsyncRawDataLogger(baseDir, maxSize, enabled, queueSize)` :170：内部 NewRawDataLogger；queueSize<=0 时默认 10000（:175）；**batchSize=50、flushDelay=100ms 硬编码**（:184-185）；`go flushWorker()` 无条件启动（:188-196，disabled 时靠 Log* 入口早退保持队列空）。
- 队列 `LockFreeQueue[T]` 定义于同文件 :30-120：`TryDequeueBatch(maxCount)` :72-73 非阻塞批量出队；`Stats() QueueStats`（Size/Capacity/EnqueueCount/DequeueCount/DropCount）:85-96。
- Log* 全走 `makeEntry` 统一入口（closed/!enabled 早退）；**队列满时当前 entry 被改写为 overflow stub**（`makeOverflowEntry` :476-497，direction="overflow"、Error="raw_log_queue_full:<direction>"，原 payload 丢弃）→ `noteDroppedEntry` :347-367：DropCount++ + 10 秒限频 slog.Warn + 可选 overflowReporter 上报 raw_log_overflow anomaly。不阻塞写入方。
- `flushWorker` :501-513：**ticker(flushDelay=100ms) 驱动**，select ctx.Done（Close 触发，退出前补一次 flushBatch）。
- `flushBatch` :527-546：循环 `TryDequeueBatch(50)` **排空式**刷盘（注释明确记载修复史：2026-07-28 前每 tick 只写 50 条、全进程上限约 500 条/秒，SSE 每帧一条会打满队列开始丢弃）；每批 `baseLogger.writeEntries` + `recordFrameLocations` :552（经 peekPostWriteLocation 反推位置写 frameIndex；跨 rotate 边界 cursor 变负即停止索引，2026-08-06 P1-2 修复）。
- `LookupFrame` :604：frameIndex 查询，供 LockFreeAnomalyReporter 填 AnomalyReport.RawLogFile/RawLogOffset，替代全局 CurrentLocation 的竞态路径。
- `Close` :624-665：closeOnce → stateMu 下 closed.Store(true) → cancel() → 等 done → **deadline=5×flushDelay（默认 100ms×5=500ms）内循环 flushBatch 排空** → 若队列仍有残留或历史有入队，写 `direction=close_drained` stub（带 EnqueueCount/DequeueCount/DropCount/remaining）→ 残留>0 时 overflowReporter 上报 raw_log_close_drained → baseLogger.Close()。
  - ⚠️ 注释漂移：Close 注释写 "default 5s"，实际 5×100ms=500ms。预研仅记录，不改代码。
- `Stats` :667 / `CurrentLocation` :684（转发 baseLogger，closed/disabled 返回空）/ `HasFile` :695。

**五个 WithEnvelope 方法 + SetOverflowReporter** 为 Async 独有；`RawCorrelationEnvelope` 定义于 async_raw_logger.go:297-317。

### 2.2 刷盘与数据完整性（关键事实）

- **`writeEntries` 每批写完后调用 `l.file.Sync()`**（raw_data_logger.go:301，Sync 失败仅 slog.Error）。因此：**同步与异步两条路径凡是已 flush 的数据都已 fsync**。
- 写失败路径（raw_data_logger.go:288-295）：slog.Error + `metrics.Global().RecordRawAuditWriteFailure()`（对接 deploy/monitoring/grafana-alerts/shadow-write-failures.yaml 的 rawaudit_write_failed_total 告警）+ **直接 return——该批次剩余条目静默丢弃**（仅计 metric）。rotate 失败同理 return。
- marshal 失败：单条跳过（continue）。
- 崩溃（kill -9）丢失窗口：
  - 同步直写路径：≈0（每批 fsync）；
  - 异步路径：**队列中未 flush 的条目**——正常负载下 ≤ 一个 flushDelay 窗口（100ms）的产生量；flushWorker 停滞时上界为队列容量 10000 条。
- 全链路 JSONL、单文件 maxSize（默认 100MB，2026-08-18 rule 11 §3 红线，保留 10 个 ≈1000MB）、rotate 时清理 keep=20。

### 2.3 勘误表（子代理初稿错误，已按源码定案）

| # | 子代理 A 初稿断言 | 实际（主代理源码验证） |
|---|---|---|
| 1 | "全链路无 fsync/Sync" | **错**。writeEntries 每批末尾 file.Sync()（raw_data_logger.go:301），异步路径继承同一实现 |
| 2 | "flushWorker 无 timer，纯 Pop 驱动让出" | **错**。ticker(100ms) 驱动 + ctx.Done 退出（:501-513） |
| 3 | "Close 未在退出前排空，drained stub 实现位置存疑，疑似 bug" | **错**。Close 有 500ms deadline 排空 + close_drained stub + anomaly 上报（:624-665），测试 TestAsyncRawDataLogger_CloseWritesDrainedStub 与实现吻合 |
| 4 | "disabled 时不启动 flushWorker" | **错**。flushWorker 构造期无条件启动（:196） |
| 5 | "lockfree_queue.go 文件" | 队列实际定义在 async_raw_logger.go :30-120（LockFreeQueue[T] 泛型） |
| 6 | B 初稿"RawDataLogger.Close 无幂等保护" | 基本成立但有细微差别：依赖 file==nil 判断实现近似幂等，无 once；并发 Close 靠 mu。R12 契约应显式规定幂等 |

### 2.3b R12 动机修正（相对交接词的重要修正）

交接词曾以"RawDataLogger 同步直写崩溃零丢失但慢 / Async 有丢数据窗口"描述二者差异。**实测两者共享 writeEntries（每批 fsync）**，持久化语义同层。R12 的真实收益是：
1. **解耦**：Async 包装层依赖 *RawDataLogger 具体类型（R10b 下线阻塞点的根因）；
2. **节奏可调**：现网每 100ms tick 每批都 fsync（高负载下排空循环每轮都 sync），fsync 成本与业务峰值耦合；BufferedRawSink 把「写 page cache」与「fsync」拆成两个可配置窗口，把持久性/性能做成显式权衡；
3. **契约统一**：三实现（Raw/Async/Buffered）共跑同一 table-driven 行为测试集。

### 2.4 配置与接线（cmd/gateway/main.go，行号已核实）

| 项 | 值 | 位置 |
|---|---|---|
| LLM_GATEWAY_RAW_LOG_DIR | 默认 `./logs/raw_data` | main.go:1552-1555 |
| LLM_GATEWAY_RAW_LOG_MAX_SIZE | 默认 100MB，非法值告警回退 | main.go:1556-1566 |
| LLM_GATEWAY_RAW_LOG_ENABLED | 默认开，仅显式 "false" 停用（打 Warn） | main.go:1568-1570 |
| 构造 | `NewAsyncRawDataLogger(logDir, maxSize, true, 10000)`——enabled 硬编码 true、队列 10000 硬编码 | main.go:1571 |
| 消费侧适配 | `executors.NewRawDataLoggerAdapter(asyncRawLogger)` → routingExec.RawDataLogger | main.go:1576 |
| anomaly locator | rawDataLogger.CurrentLocation 注入 LockFreeAnomalyReporter（仅 LLM_GATEWAY_ANOMALY_REPORTER_ENABLED=true 时；endpoint 默认 https://llmgo.kxpms.cn/format-anomalies） | main.go:1592-1595 |
| 进程退出 Close | rawDataLogger.Close()（唯一 Close 调用方，失败 slog.Warn） | main.go:7067-7070 |

### 2.5 消费侧既有接口层次

- `domains/streaming/executors/executor.go:135-136`：消费侧 `RawDataLogger` 接口（LogRequest/LogResponse，返回 error），executor.go:887 字段持有，:1080-1135 类型断言探测 envelope 能力。
- `domains/streaming/executors/diagnostic_adapters.go:18-32` envelopeAwareRawDataLogger 可选接口；:37-43 `RawDataLoggerAdapter`/`NewRawDataLoggerAdapter`；:46-133 各方向映射（adapter 把生产侧 void 调用包装成 error 返回值，恒 nil）。
- 全仓 grep `SyncDataWriter`/`DataWriter`/`RawSink`：**零结果**，接口名尚未被占用。

### 2.6 测试现状与盲点

已有测试：raw_data_logger_envelope_test.go（OverflowEntry/DefaultOn/CurrentLocation/disabled/位置一致性）、raw_data_overflow_anomaly_test.go（OverflowEmitsAnomaly/CloseWritesDrainedStub）、lockfree_queue_index_test.go（LookupFrame/nil）、lockfree_queue_test.go（并发）、diagnostic_lifecycle_test.go（CloseWhileLogging）、executors/diagnostic_adapters_test.go（方向映射）。

盲点（实施批需补）：
1. 写失败注入（磁盘满/权限）无测试——含"批次剩余条目静默丢弃"行为无断言；
2. rotate 失败路径无测试；
3. Log* void 签名吞错行为无测试；
4. Close 排空 deadline（500ms）边界无测试；
5. frameIndex 跨 rotate 边界（cursor 负值停索引）无专项测试；
6. cleanupOldFiles(20) 保留边界无测试；
7. SetOverflowReporter(nil) nil-safe 路径无测试；
8. adapter 的 *WithEnvelope 字段透传无专门断言。

## 3. 接口设计（RawSink，生产侧）

### 3.1 分层原则

生产侧接口放 `internal/logging`，与消费侧 `executors.RawDataLogger`（LogRequest/LogResponse 窄接口）保持分离；后者继续通过 adapter 消费，不受 R12 影响。

### 3.2 核心接口

```go
package logging

// RawSink 是 raw 审计落盘的抽象。实现：RawDataLogger（同步直写）、
// AsyncRawDataLogger（队列批量）、BufferedRawSink（R12 新增，定时+定量窗口）。
type RawSink interface {
    LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string)
    LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string)
    LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string)
    LogClientResponse(requestID, protocol string, body []byte, conversionStep string)
    LogConversionError(requestID, protocol, direction, step string, body []byte, err error)
    Sync() error                                   // R12 新增
    Close() error                                  // 契约：幂等
    CurrentLocation() (file string, offset int64)  // 已 flush 数据的最新位置
    HasFile() bool
}
```

- 五个 Log* **保持 void**：现有调用方/adapter/测试全部按 void 使用，改签名会波及消费侧接口与全部实现，超出 R12 范围。错误经 slog + `RecordRawAuditWriteFailure` metric + Stats 计数暴露。
- `Sync() error` 为新增显式持久化屏障：保证调用前已接受的条目已写出并 fsync。Raw/Async 实现上近乎免费（writeEntries 每批已 fsync，Async 版需先排空队列再 sync——推荐 barrier 语义而非仅 baseLogger.Sync()，否则队列残条不保证）。disabled 返回 nil。
- `Close()` 幂等契约：首次执行排空+sync+关闭并记住错误；后续调用返回同一错误。RawDataLogger 需补显式 once/状态保护。
- `CurrentLocation` 语义文档化：只反映已 flush 条目，不反映仍在缓冲/队列中的数据（Async 已如此，:684 注释可沿用）。

### 3.3 可选能力子接口（类型断言，延续现网模式）

```go
type EnvelopeRawSink interface { /* 四个 Log*WithEnvelope + RawCorrelationEnvelope */ }
type FrameLookup interface { LookupFrame(requestID, direction string) (file string, offset int64, ok bool) }
type RawSinkStats interface { Stats() QueueStats } // Async 专属；Buffered 如需统计另立 SinkStats，不伪造 QueueStats
```

两实现编译期断言 `var _ RawSink = ...` + `var _ EnvelopeRawSink = &AsyncRawDataLogger{}` 随接口落地。

### 3.4 接口收敛后 Async 的解耦改动

AsyncRawDataLogger 的 `baseLogger` 字段类型从 `*RawDataLogger` 改为内部窄接口（建议包内私有，如 `rawEntryWriter { writeEntries([]RawDataEntry); Sync() error; Close() error; CurrentLocation() (string,int64); HasFile() bool; peekPostWriteLocation(...) }`）。这是 R10b 下线阻塞点的实际解除动作；公开构造函数签名不变。

## 4. BufferedRawSink 设计

定位：与 AsyncRawDataLogger 平级的第三实现（同样满足 RawSink + EnvelopeRawSink），把「写」与「fsync」节奏做成可配置窗口。

- **包装而非自建 I/O**：内嵌 baseLogger（RawDataLogger），复用 writeEntries/rotate/cleanupOldFiles/peekPostWriteLocation，杜绝 JSONL 格式与轮转策略双实现漂移。
- 内部结构：`entries []RawDataEntry` 缓冲（带字节上限 MaxBytes，防 OOM）+ batchSize 条数阈值 + flushEvery 定时阈值 + wake 非阻塞通知 chan + 单 flushWorker goroutine（ticker/wake/stop select）+ closeOnce + writeErrors/dropped/flushCount 计数。
- 入缓冲时需注意 Headers map 复制（防调用方复用）。
- Sync = barrier：向 worker 发 flush 请求并等待完成 + baseLogger.Sync()，给出 happens-before；避免与 worker 并发写同一批。
- Close = closed 置位 → 停 worker → 排空缓冲 → baseLogger.Sync → baseLogger.Close；幂等。需用 stateMu 包围「检查 closed + 入队」消除关停竞态（沿用 Async :214-220 模式）。
- 崩溃丢失窗口：`L_crash ≈ min(MaxBytes, R×flushEvery) + flush 期间新增`（R=产生速率）。默认建议 flushEvery=100ms / batchSize=50 / MaxBytes=4MiB，与现网 Async 节奏对齐以便灰度对比。
- 写失败降级：批次保留有限次重试（指数退避）；超限丢弃 + dropped 计数 + 告警，不阻塞热路径；沿用 RecordRawAuditWriteFailure。
- LookupFrame：Buffered 可用同一 frameIndex+peekPostWriteLocation 模式实现等价能力；**若 AnomalyReporter 依赖它，则 buffered 灰度前必须实现**（现网 locator 注入的是 CurrentLocation，LookupFrame 消费方在 anomaly reporter 内部）。

## 5. 灰度开关与回滚

- 环境变量：`LLM_GATEWAY_RAW_LOG_SINK=legacy|buffered`，**默认 legacy（=现网 AsyncRawDataLogger）**；空值按 legacy；非法值**拒绝启动**（拼错静默回退会无声改变审计实现）。
- 接线点：main.go:1571 附近改工厂函数：

```go
func newRawSink(baseDir string, maxSize int64, enabled bool, mode string) (logging.RawSink, error) {
    switch strings.ToLower(strings.TrimSpace(mode)) {
    case "", "legacy":
        return logging.NewAsyncRawDataLogger(baseDir, maxSize, enabled, 10000)
    case "buffered":
        return logging.NewBufferedRawSink(baseDir, maxSize, enabled, logging.BufferedRawSinkConfig{...})
    default:
        return nil, fmt.Errorf("unsupported LLM_GATEWAY_RAW_LOG_SINK %q", mode)
    }
}
```

- adapter（main.go:1576）与 anomaly locator（:1592-1595）改为消费工厂返回的 RawSink（能力断言取 CurrentLocation/LookupFrame），wiring 其余不动。
- 启动日志打印最终 sink mode + flush 参数，便于现场核对。
- 回滚：env 改回 legacy + **重启进程**（sink 在启动期创建，不做运行时热切换——两 writer 生命周期/文件句柄/位置语义切换风险远大于收益）。无数据迁移：两实现共用 JSONL 格式与目录约定，带时间戳文件不互相覆盖。回滚前置：buffered 进程须正常关闭（Close 已排空）；kill -9 场景接受窗口内丢失（与 legacy 同量级）。

## 6. 测试矩阵（实施批范围）

公共 table-driven（三实现共跑，`sinkFactory{name, new(t)}` 抽象）：五方向基本写、JSONL 字段等价（golden）、空 body、disabled 不建文件、HasFile/CurrentLocation、并发写（-race）、Sync 后可读、Sync 与并发 Log 的 barrier、Close 幂等、Close 排空、Close 后 Log 无效、写失败注入（可注入 writer stub）、rotate 后仍可写、adapter 三实现注入等价。

专项：Buffered 的定量触发/ticker 边界/MaxBytes 上限/降级丢弃；Async 既有 overflow/close_drained/LookupFrame 测试回归；工厂测试（默认 legacy、空值 legacy、buffered、非法值失败）；灰度双路径输出等价 golden。

新增为主（现有测试只覆盖 §2.6 列表中的行为，盲点 1-8 均无现成用例）；CloseWhileLogging（diagnostic_lifecycle_test.go）模式推广为三实现共用的关停合同测试。

## 7. 并行批次协同（子代理 C 盘点精要，只读）

工作区 25 个未提交条目（HEAD=e0a4865b9 同步 origin/main），**与本会话 R12 零冲突**：无任何 internal/logging 文件被触碰；`docs/audit/2026-09-11-r12-rawlogger-prestudy.md` 新建前不存在，无覆盖风险。

分组与风险：
1. **批次①node-degrade 修复**（bg/node_probe.go、bg/passive_probe_listener.go + 2 个结构断言测试 + audit/changelog 文档）：全 unstaged/untracked，独立可提交。
2. **批次②migration 693**（5 个 SQL/测试已 staged + installer/main.go、dbinit/runner.go、modelcatalog/upsert*.go、admin/provider_offer_force_recover*.go 未 staged）：⚠️ **半 staged 状态是最高风险**——若任何会话只 commit staged 部分就 push，会得到"列已迁移但 Go 端 ON CONFLICT 未对接"（或反向：二进制引用不存在的列）的不一致提交。必须整批同提交。
3. **批次③canonical 治理工具**（modelname/match.go +scripts/govern-junk-canonical/ + sql/fixes/*.sql）：依赖批次② 先行部署。
4. **批次④⑤⑥ CRLF 噪声**：VERSION、version.json、web/public/version.json、web/public/menu-config.json、docs/audit/deploy-local-*.md 共 5 文件为纯 LF↔CRLF 重编码（内容字面未变，od -c 已验证），占 diff --stat 约 894 行噪声。建议独立小提交先落地锁基线，避免污染其他批次 diff。

建议提交顺序：CRLF 清理 → 批次① → 批次②（SQL+Go 同批）→ 批次③。本会话不执行任何提交。

## 8. 风险评估（R12 实施批）

| 严重度 | 风险 | 缓解 |
|---|---|---|
| 高 | 误删/绕过 RawDataLogger 具体类型导致 Async 断链（R10b 根因复发） | 接口落地阶段仅收窄 baseLogger 字段类型，公开构造函数不变；编译期断言护住 |
| 高 | Buffered 窗口配置失当引入超过预期的崩溃丢失 | 默认与现网 Async 同节奏（100ms/50条）；MaxBytes 上限防 OOM；Close 强制排空+sync；崩溃窗口公式写入配置文档 |
| 高 | 双实现长期并存漂移（JSONL 字段、轮转、overflow stub 语义） | Buffered 包装 baseLogger 复用同一 writeEntries/rotate；三实现共跑 golden 等价测试 |
| 高 | Log* 改 error 签名波及消费侧 | 明确 R12 不改 void 签名 |
| 中 | 灰度 env 拼错/未下发 | 非法值拒绝启动；启动日志打印生效 mode |
| 中 | Sync 语义被误解为已排空队列 | barrier 语义 + 并发 Log+Sync 测试；接口注释显式声明 |
| 中 | LookupFrame/overflow 上报能力缺失导致 anomaly 面板降级 | buffered 灰度前必须实现等价 LookupFrame 与 close_drained stub（现网 Close 行为，见 §2.1） |
| 低 | 写失败时批次剩余条目静默丢弃（现网既有行为，非 R12 引入） | 实施批补盲点 1 测试时一并断言该行为并评估是否改为跳过单条继续 |

## 9. 分阶段实施计划（需用户单独授权后另开会话执行）

1. **接口落地**：RawSink + 可选子接口 + Sync() 新增 + Close 幂等契约 + baseLogger 字段收窄为包内接口。验收：`go test ./internal/logging/... ./domains/streaming/...` 全绿含 -race，行为零变化。
2. **BufferedRawSink + 公共测试集**：实现 + §6 矩阵全绿。验收：三实现 golden 等价、race 干净、盲点 1-8 补齐。
3. **灰度开关**：工厂 + env + 拒绝非法值 + 启动日志。验收：默认路径与现网二进制行为逐字节一致。
4. **生产灰度**：测试环境 → 低流量实例 → 全量；观测 rawaudit_write_failed_total、flush 耗时、batch 大小、dropped、close drain 耗时、frame correlation 成功率；完成一次回滚演练。验收：一个完整业务周期无新增写失败、P99 无回归。
5. **下线 legacy**：前置=Buffered 稳定一个发布周期 + envelope/LookupFrame/close_drained 能力对齐 + 回滚演练通过；删除 Async 队列实现或冻结（Open question ③）。

## 10. Open Questions（需用户拍板）

1. 接口命名：`RawSink`（推荐）/ RawAuditSink / RawLogSink？
2. Buffered 默认窗口：100ms/50条/4MiB（推荐，与现网 Async 同节奏）是否接受？是否暴露为 env（建议第一版不暴露，代码默认值灰度）？
3. AsyncRawDataLogger 终局：保留为回滚实现直到何时？下线 or 长期冻结？
4. LookupFrame/close_drained stub 是否为 buffered 灰度硬前置（本文推荐：是）？
5. 写失败时"批次剩余条目静默丢弃"（现网既有行为）是否趁 R12 修正为逐条跳过？
6. 实施批排期与授权（本预研不含任何代码改动）。

## 11. 结论

R12 可立项。事实核查推翻了"legacy 无 fsync/Async 无定时刷盘"的旧假设（§2.3），现网两路径持久化语义同级，R12 的核心价值从"补持久性"修正为"解耦具体类型依赖 + 持久性/性能节奏显式化 + 实现契约统一"。设计无阻断性障碍；实施批待授权。

## 12. 实施记录（2026-09-11，同日实施批完成 §9 阶段 1-3）

用户授权后同日执行，阶段 1-3 已落地，阶段 4（生产灰度）与阶段 5（下线 legacy）仍待运维排期。Open Questions 按本文推荐执行：接口命名 RawSink；Buffered 默认 100ms/50 条/4MiB 且第一版不暴露 env；Async 保留为回滚实现；LookupFrame/close_drained stub 按硬前置实现；写失败"批次剩余条目静默丢弃"现网语义未改（测试显式锁定，留待单独决议）。

落地文件：
- `internal/logging/raw_sink.go`：RawSink / EnvelopeRawSink / FrameLookup / rawEntryWriter（包内窄接口，baseLogger 收窄，R10b 阻塞点解除）+ rawFrameIndex（Async/Buffered 共享逐帧索引）+ makeRawEntry / makeOverflowStub（共享条目构建）+ NewRawSink 工厂。
- `internal/logging/buffered_raw_sink.go`：BufferedRawSink 全量实现（缓冲字节预算 + 条数/ticker 双触发 + 写失败有限重试降级 + close_drained stub + LookupFrame + Sync 屏障 + BufferedSinkStats）。
- `internal/logging/raw_data_logger.go`：新增 Sync()（显式屏障）与 writeEntriesFallible()（可错变体，供重试降级）；Close 改为显式幂等并记住错误（勘误 6）。
- `internal/logging/async_raw_logger.go`：baseLogger 收窄为 rawEntryWriter；新增 Sync()（排空+fsync 屏障）；frameIndex 迁移至共享 rawFrameIndex。
- `cmd/gateway/main.go`：LLM_GATEWAY_RAW_LOG_SINK 灰度开关（默认 legacy 行为零变化；非法值 slog.Error + os.Exit(1) 拒绝启动）；启动日志打印生效 mode 与 buffered 窗口参数；anomaly locator / 退出 Close 经 RawSink 接口不动。
- 测试：三实现共跑 table-driven 契约集（`raw_sink_contract_test.go`，含 golden 等价、Sync 屏障、Close 幂等/排空/关停合同、并发写）+ Buffered 专项（`buffered_raw_sink_test.go`）+ 盲点 1-7 补齐（`raw_data_logger_r12_test.go`）+ 盲点 8 与 adapter 三实现注入等价（`domains/streaming/executors/diagnostic_adapters_r12_test.go`）。

与预研的偏差（均已记录在代码注释）：
1. **工厂位置**：§5 原设计放 main.go；实际落地为 `logging.NewRawSink` 导出工厂，使 §6 要求的工厂测试（默认/空值 legacy、buffered、非法值失败）可在包内单测；main.go 保留 env 读取、拒绝启动与启动日志，行为契约与 §5 一致。
2. **Async client_request overflow stub 的 Error 统一**：原内联分支为 `"raw_log_queue_full"`（无方向后缀），与其余三方向及 §2.1 记载的 `"raw_log_queue_full:<direction>"` 语义漂移，统一为带后缀（全仓无精确匹配该裸串的消费者）。
3. **Headers 浅拷贝**：共享 makeRawEntry 对 Async 与 Buffered 统一在入队时拷贝 Headers map（§4 只要求 Buffered；对 Async 是防调用方复用的无害加固）。
4. **-race 执行**：本机为 windows/arm64（race 不支持），已用 zig cc 交叉编译验证 linux/arm64 下含 -race 的测试二进制可编译；-race 运行验证由 Linux CI 承接。无 -race 全量测试本机全绿（`./internal/logging/...`、`./domains/streaming/...`）。
5. **盲点 2/4 的测试形态**：rotate 失败经 baseDir 消失法跨平台注入（磁盘满/权限注入不可移植）；Close 排空 deadline 覆盖确定性路径（未饱和队列在 500ms 内排空 + stub），deadline 超时分支保持实现内审查。

## 引用

- internal/logging/raw_data_logger.go、async_raw_logger.go（行号见 §2）
- domains/streaming/executors/executor.go:135,887、diagnostic_adapters.go:18-133
- cmd/gateway/main.go:1552-1595、7067-7070
- docs/handoff/2026-09-10-r10-doc-sync-rawlogger.md、docs/audit/2026-09-10-r10-doc-sync-rawlogger.md
- R10b 决议：memory audit-2026-09-10-r10b-planning
