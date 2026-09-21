# Audit 2026-09-10 — Lite-mode 文档同步与 RawDataLogger 下线评估

## 1. 范围与方法

针对 R8 提出的两个低优先级候选：
- 候选 X：扫描 `docs/design/lite-mode-*.md` 中 `storage/memory` 旧路径，区分历史文档与活文档。
- 候选 Y：评估 `internal/logging` 中 `RawDataLogger`（同步落盘）与 `AsyncRawDataLogger`（异步封装）的调用关系，判定能否直接下线同步版。

方法：**只读审计 + grep + import 关系核对**，不修改任何生产代码，不修改历史文档。本轮目标是产出修复方案与可执行判定，而不是动手改。

## 2. 候选 X — `docs/design/lite-mode-*.md` 中 `storage/memory` 引用审计

### 2.1 全量检索

`grep -rln "storage/memory" docs/design/lite-mode-*.md` 命中：

| 文件 | 引用数 | 文档类型 | 是否需要同步 |
|---|---|---|---|
| `lite-mode-final-report.md` | 多处 | 历史（终态报告，已冻结） | **否** |
| `lite-mode-implementation-checklist.md` | 多处 | 历史（实施清单，已冻结） | **否** |
| `lite-mode-implementation-status.md` | 1 处（L159） | 活文档（当前状态追踪） | **是** |
| `lite-mode-index.md` | 2 处（L125、L268） | 活文档（索引/导航） | **是** |
| `lite-mode-quick-reference.md` | 2 处（L60、L363） | 活文档（速查表） | **是** |

合计活文档需同步 **3 文件 / 5 行引用**（注：R8 初估"6 处"、R10 先前按"4 处"分布记录均不准确；行号级 `grep` 复核后确认 5 行）。

### 2.2 历史文档判定依据

- `lite-mode-final-report.md` — 文件名带 `final-report`，记录项目最终态（lit 模式投产回看），`storage/memory` 是当时事实存在的实现路径的描述（即使后来重命名，历史事实不变）。按"历史文档不修改"原则冻结。
- `lite-mode-implementation-checklist.md` — 实施清单记录了哪些项已 done，其引用反映的是当时落地的代码状态。改写会扭曲历史事实。

### 2.3 活文档判定依据

- `lite-mode-implementation-status.md` — 标题"implementation-status"是持续维护的进度表，引用必须与代码现状一致（现状：`storage/lite`，package `lite`）。
- `lite-mode-index.md` — 索引文档，读者按它跳到当前代码；如果路径错会直接 404。
- `lite-mode-quick-reference.md` — 速查表，开发日常查阅，路径必须正确。

### 2.4 最小同步方案（待执行批次 A）

只改这 3 个文件的 5 处引用（行号已 `grep -n` 复核）：

- `docs/design/lite-mode-implementation-status.md` L159 — `storage/memory/state_store.go` → `storage/lite/state_store.go`，package 描述补 `package lite`。
- `docs/design/lite-mode-index.md` L125 — `storage/memory/` → `storage/lite/`，目录列表与命名约定对齐。
- `docs/design/lite-mode-index.md` L268 — `storage/memory/*_test.go` → `storage/lite/*_test.go`（若 LiteStateStore 测试集在 lite 包下；执行前先 `ls storage/lite` 复核当前文件命名）。
- `docs/design/lite-mode-quick-reference.md` L60 — `storage/memory/state_store.go` → `storage/lite/state_store.go`。
- `docs/design/lite-mode-quick-reference.md` L363 — 同上。

约束：替换前先 `cat` 上下文，确认每行确实是代码路径引用而非历史叙述；保留所有其他文本不动。提交建议：`docs(design): 同步 storage/memory → storage/lite（活文档 5 处）`。

不在本批次处理：`docs/storage/README.md` 在本轮 grep 中未命中 `storage/memory`（已与命名约定对齐），无需改动。

## 3. 候选 Y — `RawDataLogger` 下线可行性评估

### 3.1 类型关系

| 类型 | 文件 | 角色 |
|---|---|---|
| `streaming.RawDataLogger` | `domains/streaming/executors/executor.go`（推断，R8 已确认） | **接口**，定义 `LogRequest`/`LogResponse`（推断） |
| `logging.RawDataLogger` | `internal/logging/raw_data_logger.go` | 同步落盘引擎（struct），writeEntries / writeEntry / Close / CurrentLocation / HasFile / peekPostWriteLocation |
| `logging.AsyncRawDataLogger` | `internal/logging/async_raw_logger.go` | 异步队列 + 复用 `RawDataLogger` 做实际落盘的封装 |
| `RawDataLoggerAdapter` | `cmd/gateway/main.go:1576` | 把 `*logging.AsyncRawDataLogger` 适配成 `streaming.RawDataLogger` 接口 |

### 3.2 调用关系核查（grep 结果）

- `NewRawDataLogger(...)` 生产调用：**仅 1 处**——`internal/logging/async_raw_logger.go:171` 在 `AsyncRawDataLogger.New` 中构造同步引擎。
- `logging.RawDataLogger` struct 直接消费者：**仅 1 个**——`internal/logging/async_raw_logger.go` 内的 `baseLogger` 字段（在 `writeEntries`/`writeEntry`/`Close`/`CurrentLocation`/`HasFile`/`peekPostWriteLocation` 处调用）。
- `streaming.RawDataLogger` 接口消费者：`cmd/gateway/main.go`（通过 `NewRawDataLoggerAdapter(asyncRawLogger)`）以及 `domains/streaming/executors/executor_dispatch_fpslot_test.go`（测试用 mock，不依赖真实落盘）。
- 测试侧：`raw_data_logger_envelope_test.go` 等直接使用 `NewRawDataLogger` 单测同步引擎自身行为（13 个 envelope 场景）。

### 3.3 判定：**不可直接下线 `logging.RawDataLogger`**

**关键约束**：业务调用方持有的是 `streaming.RawDataLogger` 接口，并不区分同步/异步。当前 `AsyncRawDataLogger` 内部**复用同步引擎做实际落盘**（`baseLogger.writeEntries(...)` / `baseLogger.writeEntry(...)`），如果直接删掉 `logging.RawDataLogger`，`AsyncRawDataLogger` 就没有落盘实现，要么：
1. 把同步落盘的 writeEntries / 文件滚动 / 关闭语义全部 inline 到 `AsyncRawDataLogger`（侵入式重构，引入回归风险）；
2. 引入一个全新的同步落盘接口让 `AsyncRawDataLogger` 依赖——本质上是把同步引擎换皮，并未真正"下线"。

两条路径都依赖**先有一个替代的同步落盘引擎**（batch A 之前的必做前置）。在替代实现未落地、未测试前，删 `logging.RawDataLogger` 会让 `AsyncRawDataLogger` 编译失败或运行时无法落盘——会破坏 `cmd/gateway` 的 raw 数据审计能力。

### 3.4 推荐路径

不下线，先记录为待办。**前置工作**（独立批次 B，需独立立项）：

1. 抽象 `internal/logging` 中的"同步落盘引擎"接口（暂名 `SyncSink`），最小方法集：`Write(entries []Entry) error` / `Close() error` / `CurrentLocation() (path string, offset int64)` / `HasFile() bool`。
2. 把现有 `logging.RawDataLogger` 改名为 `logging.fileSink`（或保留文件名作 internal 细节），实现 `SyncSink`。
3. 在 `AsyncRawDataLogger` 中把 `baseLogger` 字段类型从 `*RawDataLogger` 改为 `SyncSink`。
4. 增加 `SyncSink` 接口的契约测试（覆盖文件滚动、关闭幂等、并发安全）。
5. 灰度切换：保持 `RawDataLogger` 作为 `SyncSink` 的默认实现，新增 `SyncSink` 的内存/mmap 备选实现并跑 benchmark，确认无回归后再考虑下线文件版。

**本轮动作**：仅在 audit 报告中登记候选 B 的前置条件；不修改 `internal/logging` 任何文件，不动 `cmd/gateway/main.go` 的适配层。

## 4. 并发安全提示（旁注）

`internal/logging/async_raw_logger.go` 多处出现 `l.baseLogger.writeEntries(entries)`（第 533 行）以及 `l.baseLogger.writeEntry(closeEntry)`（第 651 行）。`RawDataLogger` 的 writeEntries / writeEntry 在并发场景下需要保持原有同步语义（参考 `raw_data_logger_envelope_test.go` 的并发用例）。任何替代 sink 必须**先继承这套并发保证再做替换**，否则会在高 QPS 下出现文件损坏。

## 5. 结论

| 候选 | 结论 | 下一步 |
|---|---|---|
| X 活文档同步 | 确认 3 文件 / 5 行失真（R8 初估 6、R10 行号级复核后为 5：status×1 + index×2 + quick-reference×2） | 批次 A 提交 `docs(design): 同步 storage/memory → storage/lite（活文档 5 处）` |
| Y RawDataLogger 下线 | **不可直接下线**；`AsyncRawDataLogger` 强依赖其作为落盘后端 | 批次 B 独立立项：抽象 `SyncSink` 接口 → 保留 `RawDataLogger` 作为默认实现 → 增加契约测试与备选 sink → 评估是否下线文件版 |

**本轮无代码改动**，仅完成审计结论与最小同步方案的提案，待批次 A/B 各自独立推进。

## 6. 候选清单收口

R8 候选 X、Y 已全部审计完毕：

- X：审计完成，修复方案已落（待批次 A 执行）。
- Y：审计完成，**判定为不可直接执行**（需前置抽象工作），已明确前置条件与分阶段方案。

本轮不提交任何 commit；下一轮按批次 A → 批次 B 顺序独立推进。