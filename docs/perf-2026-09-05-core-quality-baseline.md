# 2026-09-05 核心质量与性能基线报告

## 结论

本轮完成了第一阶段“真实基线 + 最小质量门禁 + IR测试补强”。核心 IR、dispatch 和 streaming 包的普通测试、vet、修复后的 race 检测、benchmark smoke 和 IR fuzz smoke 均已验证。首轮 race 检测发现并修复了 `QueuedRequest` 路由状态的真实数据竞态；修复后核心 race 全集通过。

本轮没有提交、推送或修改生产配置，也没有覆盖工作区开始时已有的业务代码和审计文档改动。

## 环境

- 日期：2026-09-05
- 平台：darwin/arm64
- CPU：Apple M4 Max
- Go：本地 Go 1.26.4；CI workflow 使用 Go 1.25
- 仓库：`github.com/kaixuan/llm-gateway-go`

## 基线命令与结果

### 普通测试

```text
go test ./internal/ir -count=1 -timeout=300s
ok github.com/kaixuan/llm-gateway-go/internal/ir 0.911s

go test ./domains/dispatch ./domains/streaming/... -count=1 -timeout=300s
ok domains/dispatch 25.691s
ok domains/streaming 68.714s
ok domains/streaming/executors 22.981s
ok domains/streaming/executors/webcookie 1.272s
ok domains/streaming/integrity 2.882s
ok domains/streaming/state 1.728s
```

### Vet

```text
go vet ./internal/ir ./domains/dispatch ./domains/streaming/...
exit code 0
```

已固化为 `make vet-core`。

### Benchmark基线

```text
go test ./internal/ir ./domains/dispatch ./domains/streaming/... -run '^$' -bench . -benchmem -count=1
exit code 0
```

代表性结果：

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| ParseOpenAI | 22,559 | 15,814 | 234 |
| ParseAnthropic | 24,493 | 15,662 | 261 |
| SerializeOpenAI | 6,138 | 7,399 | 125 |
| SerializeAnthropic | 10,096 | 10,549 | 178 |
| DetectProtocol | 7,752 | 5,752 | 120 |
| ValidateSSEDataFrame | 931.6 | 1,487 | 25 |
| ValidateSSEDataFrame_Invalid | 88.37 | 200 | 5 |
| CalculatePressurePenalty | 1.557 | 0 | 0 |

结果受本地机器和测试样本影响，当前用于回归比较，不作为跨机器SLO。

已固化为 `make bench-core`。

## 新增测试

### IR fuzz smoke

新增：

- `internal/ir/fuzz_parsers_test.go`
  - `FuzzIRParsersNeverPanic`
  - `FuzzIRStreamParsersNeverPanic`

覆盖真实导出入口：

- `ParseOpenAI`
- `ParseAnthropic`
- `ParseGemini`
- `ParseResponses`
- `DecodeRequestDocument`
- `ParseOpenAIStreamChunk`
- `ParseAnthropicStreamEvent`
- `ParseGeminiStreamChunk`

断言只检查安全不变量：任意输入不得 panic；成功解析后可以编码为合法 request document；流式解析入口不得 panic。不要求任意畸形输入解析成功，也不强行断言跨协议语义等价。

验证：

```text
go test ./internal/ir -run '^$' -fuzz '^FuzzIRParsersNeverPanic$' -fuzztime=5s -count=1
PASS
约 1,188,469 executions，5.86s

go test ./internal/ir -run '^$' -fuzz '^FuzzIRStreamParsersNeverPanic$' -fuzztime=5s -count=1
PASS
```

### Gemini配置回归

新增：

- `internal/ir/gemini_safety_roundtrip_test.go`

验证 `safetySettings` 和 `cachedContent` 经 `ParseGemini` → `SerializeGemini` 后不静默丢失，包括 `SafetySetting.Method`。

## Race检测与修复

### 首轮失败

命令：

```text
make test-race-core
```

首轮发现：

- `TestStopNoSendOnClosedRace` 报告 `QueuedRequest.ResolvedModel` 的并发读写。
- `QueuedRequest.vendor` 在 dispatcher 写入与 waterfall 读取之间存在并发读写。
- 测试输出同时出现 Redis 本地连接拒绝日志；该环境噪声不能掩盖 race 本身。

### 修复

修改：

- `domains/dispatch/queued_request.go`
  - 为 vendor snapshot 增加 `vendorMu`。
  - 增加 `selectedVendor()` 和 `setVendor()`。
- `domains/dispatch/dispatcher.go`
  - credential/vendor 通过 accessor 写入。
  - observation 使用 `resolvedModel()` 快照读取。
- `domains/dispatch/waterfall.go`
  - model、vendor 使用 detached accessor 读取。

### 修复后结果

```text
go test -race ./domains/dispatch -run '^TestStopNoSendOnClosedRace$' -count=1 -timeout=120s
PASS

make test-race-core
ok internal/ir 1.541s
ok domains/dispatch 27.303s
ok domains/streaming 69.213s
ok domains/streaming/executors 25.282s
ok domains/streaming/executors/webcookie 2.982s
ok domains/streaming/integrity 3.633s
ok domains/streaming/state 4.002s
exit code 0
```

这证明本轮没有通过排除失败测试来处理问题；修复后原范围 race 全集重新通过。

## CI变更

修改：

- `.github/workflows/sessionforensics-ci.yml`

新增 `core-quality` job：

1. `make vet-core`
2. `make test-race-core`
3. IR `FuzzIRParsersNeverPanic` 5秒 smoke

没有新增重复 workflow，也没有把全仓库 `go test ./... -race` 作为PR门禁。原因是仓库包含数据库和网络集成测试，现有 workflow 已明确要求对这些路径隔离。

## Makefile变更

新增变量和目标：

- `CORE_GO_PACKAGES := ./internal/ir ./domains/dispatch ./domains/streaming/...`
- `make test-race-core`
- `make vet-core`
- `make bench-core`

使用 `make -n vet-core test-race-core bench-core` 验证目标展开正确。

## 交付前审计修正（2026-09-05）

提交前对本轮全部改动做了复核，发现并修正三处问题：

1. **回滚了本轮曾对 `sql/migrations/startup/649_routing_analytics_probe_filter.sql` 做的 `CREATE OR REPLACE` 编辑。**
   复核发现该编辑基于一个错误前提：迁移文件只会被迁移执行器运行一次，且同事务中两个 MV 先于 `DROP VIEW` 被 `DROP ... CASCADE`，不存在审计报告所称的 SQLSTATE 2BP01 重跑风险（该风险仅存在于会重复执行的 `db/db.go` ensure 路径，其内联 SQL 已使用 `CREATE OR REPLACE`）。原文件是正确契约，`migration_649_test.go` 也断言 `DROP VIEW` 文本；保留编辑会破坏该测试。已 `git checkout` 还原，测试重新通过。

2. **同步了 installer embeddata 的 649 迁移副本。**
   `installer/cmd/llm-gw-installer/embeddata/startup/649_routing_analytics_probe_filter.sql` 在 HEAD 上即缺少 startup 版已有的 `SET LOCAL statement_timeout = '10min'` 块（既有漂移，非本轮引入）。按仓库惯例（654、614 等均为字节级同步）已复制 startup 版本对齐，installer 模块测试通过。

3. **统一了 `domains/dispatch/dispatcher.go:202` 的模型写入路径。**
   模型切换时的 `qr.ResolvedModel = chosen` 直写改为 `qr.setResolvedModel(chosen)`，与竞态修复引入的锁语义一致。

修正后复核结果：`go build ./...` 通过；`go test ./sql/migrations/startup -run TestMigration649` 通过；installer 模块 `go test ./cmd/llm-gw-installer -run TestStats` 通过；gofmt 全部干净。

已知遗留（超出本轮范围）：`domains/dispatch` 中仍有约 25 处对 `ResolvedModel` / `SelectedCred` 的直读，依赖单一所有者约定；与非所有者 goroutine 的带锁写入在内存模型意义上仍可能构成竞态，race 检测在现有测试路径下未命中。建议后续工作包将这些读点统一迁移到 accessor。

> **2026-09-05 后续工作包已清除该遗留**：dispatch 域内 8 文件约 32 处直读已全部迁移为
> `resolvedModel()` / `selectedCredential()` accessor（commit `1eb00d411`）；审计另发现
> executor 适配层（`domains/streaming/executors/executor_dispatch.go`）3 处跨域直读，
> 通过新增导出读路径 `QueuedRequest.ResolvedModelSnapshot()` 迁移。dispatch 测试文件中
> 构造期（交付给 pipeline 前、单 goroutine）的直写保持不变，属测试 fixture 语义，
> 与锁保护无并发交集。

## 工作区说明

以下改动在本轮开始前已经存在，本轮未覆盖、回滚或重写：

- `cmd/gateway/main.go`（本轮追加了 unified_probe_scheduler 死分支清理，见下）
- `.audit-workspace/`
- `docs/audit-2026-09-04-comprehensive.md`
- `docs/audit/2026-09-04-24h-parallel-audit-plan.md`
- `docs/improvement-roadmap-2026-09.md`
- `docs/prompts/`

本轮新增或修改的相关文件：

- `.github/workflows/sessionforensics-ci.yml`
- `Makefile`
- `cmd/gateway/main.go`（删除 unified_probe_scheduler 残留分支：已确认 `bg/unified_probe_scheduler.go` 不存在，代码中无其他 `LLM_GATEWAY_ENABLE_UNIFIED_PROBE_SCHEDULER` 引用，仅历史审计文档提及）
- `installer/cmd/llm-gw-installer/embeddata/startup/649_routing_analytics_probe_filter.sql`（statement_timeout 同步）
- `domains/dispatch/dispatcher.go`
- `domains/dispatch/queued_request.go`
- `domains/dispatch/waterfall.go`
- `internal/ir/fuzz_parsers_test.go`
- `internal/ir/gemini_safety_roundtrip_test.go`
- 本报告

`git diff --check` 已通过。

## 风险与未完成项

1. 本轮没有运行全仓库 `make test`、根 `verify.sh` 或生产数据库集成测试；这些入口包含外部依赖，超出本轮核心包门禁范围。
2. `make test-race-core` 虽然修复后通过，但 dispatch 测试仍可能启动 Redis 相关 mock/失败路径；CI环境应继续保留其现有依赖隔离配置。
3. fuzz smoke 当前只在PR中运行5秒，nightly长时 fuzz、crash artifact脱敏和语料库治理尚未实现。
4. benchmark 是可重复入口，但尚未建立历史基准存储或自动回归阈值。
5. 未引入属性测试库、goleak或新的JSON实现，避免在没有基线收益前扩大依赖面。

## 建议下一步

1. 在独立工作包中补充 fuzz 失败样本自动最小化和脱敏回归流程。
2. 为 benchmark 建立固定样本、历史结果和允许回归阈值。
3. 在可观测性变更前先确认 core-quality CI 稳定运行若干轮。
4. 完成容量/保留策略基线后，再开始 URSM v2 shadow/canary 迁移；本轮不改变路由权威模式。

## 后续工作包交付（2026-09-05 第二批）

上述第 1、2、4 项已在本批落地，第 3 项为纯观察项（见下）。

### 1. fuzz 失败样本回归流程（已交付）

组成：

- `scripts/fuzz/fuzz-regress.sh` —— 驱动脚本。`run [fuzztime]` 对全部已登记 fuzz 目标限时运行；
  失败时定位 Go fuzz 引擎自动最小化后的崩溃样本并做脱敏检查，打印入库命令。
  `install <样本> <目标>` 经脱敏检查后把样本固化为 `internal/ir/testdata/fuzz/<目标>/` 回归语料，
  并当场执行 `go test` 验证回归生效。
- `scripts/fuzz/sanitize_corpus.sh` —— 脱敏门禁。扫描语料中的密钥/令牌/邮箱/内网 IP 等
  结构特征（只输出截断片段，避免日志二次泄漏）；命中退出 1。误报走
  `scripts/fuzz/allowlist.txt` 文件级放行（默认为空）。
- `internal/ir/testdata/fuzz/` —— 14 个脱敏边界种子语料（深嵌套、数值溢出、类型混淆、
  非法 UTF-8、重复键、CRLF/裸 DONE、未知事件类型等，全部合成数据）。
  普通 `go test ./internal/ir` 即会执行，崩溃样本入库后自动成为永久回归用例。
- Makefile：`make fuzz-smoke`（FUZZTIME 可调）、`make fuzz-corpus-lint`。
- CI：core-quality job 在 fuzz smoke 前新增 `fuzz-corpus-lint` 步骤。

约定：样本最小化由 `go test -fuzz` 内置完成，本流程不重复实现；`SKIP_SANITIZE=1`
仅限本地调试。详见 `scripts/fuzz/README.md`。

### 2. benchmark 基线与回归阈值（已交付）

组成：

- `docs/perf/bench-baseline.txt` —— 基线文件（含生成环境与 git 提交头），由
  `make bench-baseline` 生成并人工提交。
- `scripts/perf/bench_compare.sh` + `bench_compare.py` —— 运行核心包 benchmark 并解析；
  `--check` 对照基线逐项比较，原始输出归档 `docs/perf/bench-history/`（已 gitignore，不入仓）。
- 阈值：allocs/op 超过 +2% 且绝对差 ≥1 判回归；B/op +10%；ns/op +15%（默认），
  可用 `scripts/perf/bench_thresholds.txt` 按基准名覆盖。ns/op 与 allocs/B 同超时按
  allocs 优先报告。环境（go 版本/os/arch）与基线不一致时 allocs/B 回归仍拦截，
  ns/op 回归降级 WARN——ns/op 跨机器不可比。
- Makefile：`make bench-baseline`、`make bench-check`（BENCH_COUNT 可调，多次运行取 ns/op 最小值）。

本轮基线以 12 项核心 benchmark 建档（较首轮报告新增 QueueProjectionSnapshot 等 4 项，
因 bench-core 的包范围覆盖 `domains/dispatch` 与 `domains/streaming/...` 全部 benchmark）。
首轮 8 项代表性数值与基线文件一致量级。详见 `scripts/perf/README.md`。

### 3. core-quality CI 稳定性确认（观察项，未关闭）

本仓库托管在私有 git 主机，本机 `gh` 不可用，无法代查运行历史。判定标准：
core-quality job 连续若干轮 PR 通过（含本批新增的 corpus-lint 步骤）后，方可启动
可观测性变更。本批未改动既有 CI 步骤语义。

### 4. 容量/保留策略基线采集工具包（已交付，待实采）

- `scripts/capacity-baseline/` —— 01 表与分区尺寸全景 / 02 分区家族保留覆盖 /
  03 增长速率估计（archive 月度分区尺寸序列），`run.sh` 汇总输出带时间戳报告
  （宿主机无 psql 时自动回退本地容器 `llm-gateway-pg`，或 `FORCE_PG_CONTAINER=1` 强制）。
- 三个查询已在本地开发库实测通过；只读目录查询（pg_class/pg_inherits/pg_stat），无锁风险。
- 本地实采首个信号：`ursm_node_snapshot_min` 已 6.6 GB（13.5M 行），是 URSM v2
  容量门禁的首要观察对象。
- 采集后按 `scripts/capacity-baseline/README.md` 模板整理为
  `docs/perf/capacity-retention-baseline-<日期>.md` 提交，其"风险判定"结论即
  URSM v2 shadow/canary 的前置门禁。生产/目标环境实采超出本批范围。

#### 2026-09-05 第三批：本地开发库实采完成，门禁结论已产出

- 完整实采报告：`docs/perf/capacity-retention-baseline-2026-09-05.md`
  （原始输出已 gitignore；补用了表内时间跨度 + 精确 COUNT 推算增长斜率——
  开发库无 archive 分区序列且 pg_stat 计数器近期被重置，03 号查询两个视角均不可用）。
- **门禁结论：有条件通过。** URSM v2 停留 shadow（本地实测 `URSM_V2_MODE=shadow`
  且 double-write 未开 → persist writer 禁用、不落盘）；进入 canary/authoritative 前
  必须先给 `ursm_node_snapshot_min`（13.72M 行 / 6.6 GB / 占库 36%，唯一入口
  `persist/writer.go` 仅 INSERT 无清理，148 MB/天）建立保留或分区机制，
  并同批处理第二个无界增长表 `request_stage_events`（+60 MB/天）。
- 顺带实测确认：`HOT_CRON_*` 默认值与库内热窗口精确吻合（request_logs_hot 恰 8 小时）；
  `request_logs` 主表 / `stats_event_inbox` / `usage_facts` 均只有 default 分区、无月度滚动。
- 生产/目标环境实采仍待具备凭据后执行，基线报告已附可复用查询。

### 2026-09-05 第三批续：容量门禁项 1/2 整改交付（保留 cron 方案）

按基线报告 §5 门禁结论，两张无界增长表的保留机制已代码交付（不改动 schema）：

- `domains/ursm/v2/persist/retention.go`（`SnapshotRetentionWorker`，
  `URSM_SNAPSHOT_RETENTION_DAYS` 默认 30）与
  `internal/trace/stage_events_retention.go`（`StageEventsRetentionWorker`，
  `STAGE_EVENTS_RETENTION_DAYS` 默认 7）：均为 1h tick、批 5000、每批独立事务、
  单轮墙钟上限 10 分钟，显式设 0/负禁用；模仿 `requestjourney.RetentionWorker`
  的接口解耦与幂等 Start/Stop 语义，两表均无 RLS 故无需 bypass_rls。
- `cmd/gateway/main.go` 接线：snapshot 保留独立于 URSM v2 mode / persist writer
  启用状态（shadow 未开 double-write 时的历史残留同样回收）；stage events 保留
  与 journeyRetentionWorker 同点接线；shutdown 区统一 Stop。
- 验证：`go build ./...` OK；两包单测与 `-race` 通过；`go vet` 三包干净；
  `go test ./cmd/gateway` 通过；批量 DELETE 在开发库回滚事务内实测走索引
  （ursm 5000 行 196 ms、stage events 5000 行 10 ms），13.7M 存量净删除约
  10 分钟、按窗口上限分摊到数个 tick。`.env.local.example` 已补参数说明。
- 门禁状态更新：第 1、2 项解除路径已落地，待部署重启后观察
  `ursm.v2: snapshot retention` 日志确认存量回收；第 3 项（default 分区滚动）
  仍开放。URSM v2 维持 shadow。

### 验证记录

```text
go build ./...                OK
make vet-core                 OK
go test ./internal/ir         OK（含 14 个新语料种子回归）
go test -race ./internal/ir   OK
make fuzz-smoke               OK（2 目标 × 5s）
make fuzz-corpus-lint         OK
make bench-check              OK（12 项全可比，PASS）
shellcheck 新增脚本           OK
cap-baseline run.sh           OK（容器分支实采 4 组结果集）
```

负向用例：伪造 allocs 234→300 的样本被 `bench-check` 判 REG 并退出 1；
含假密钥语料被 `sanitize_corpus.sh` 与 `fuzz-regress.sh install` 拒绝。
