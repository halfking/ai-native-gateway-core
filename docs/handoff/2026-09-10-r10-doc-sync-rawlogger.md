# Handoff — 2026-09-10 — R10 closure (lite-mode doc-sync + RawDataLogger scope verdict)

## Origin

This session closes the two deferred candidates from R8 (see `audit-2026-09-10-r8-round.md`):

1. `docs/design/lite-mode-*.md` 中 `storage/memory` 引用的最小同步方案
2. `internal/logging` 中 `RawDataLogger` 的替代方案评估

Working tree at start: clean on `main` @ `c1fc3edf5` (R7 closure pushed earlier today).

## What I did this session

### 1. Lite-mode 文档同步核查 (R10)

**起点 (R8 记忆)**: 活文档 6 处 `storage/memory` 失真需要 `doc-sync-fix`。

**核查手段**:

- 对 `docs/design/lite-mode-*.md` 与 `docs/storage/README.md` 做行号级 `grep -n`。
- `git log --all -- '*memory*'` + `git ls-files | grep memory` 确认历史命名空间实际范围。
- 对照 `internal/storage/lite/*.go` 当前真实包结构。

**实际命中 (行号级)**:

- `docs/design/lite-mode-overview.md:35`
- `docs/design/lite-mode-overview.md:64`
- `docs/design/lite-mode-overview.md:65`
- `docs/design/lite-mode-storage.md:1` (frontmatter 标题)
- `docs/storage/README.md:23`

合计 **5 处**,不是 R8 记忆的 6 处。R8 计数差异源于"路径 + 测试数量"合并口径；行号 grep 才是真值。

**是否为活文档**:

- `lite-mode-overview.md`:活文档,代码示例引用 `memory.KVState` / `memory.NewKVStateStore` 等失真 API。**待改**。
- `lite-mode-storage.md`:frontmatter 标题里的 `memory` 是历史命名快照,R6 已将其与 `Storage` 子章节切分。frontmatter 改名属于"改写历史",**保留**(已在文档末段说明命名已演化)。
- `docs/storage/README.md`:活文档,引用 `MemoryStateStore`(`storage/lite` 内仍存在的 facade 兼容符号)。**待改**措辞,但**不需要删符号**。

**最终建议 (已在 R10 备忘录落档)**:

- 改 `lite-mode-overview.md` 3 处 → 引用真实包 `internal/storage/lite` + 真实类型 `KVState`/`KVStateStore` 等。
- 改 `docs/storage/README.md` 第 23 行,把 `memory` 关键词替换成 `lite` 包定位语句,保留对 `MemoryStateStore` 的说明(它仍是 facade)。
- **不改** `lite-mode-storage.md` frontmatter 标题(历史快照)。
- **不改** `final-report.md` / `implementation-checklist.md` / `docs/design/archive/*`(冻结文档)。

### 2. RawDataLogger 评估 (R10)

**实际依赖图 (行号级 grep)**:

- `RawDataLogger` (`internal/logging/raw_data_logger.go`) 是底层 sync 写入器:Write→open file→append→sync→close。
- `AsyncRawDataLogger` (`internal/logging/async_raw_data_logger.go:28`) 持有一个 `*RawDataLogger` 实例做底层落盘。
- 调用方 6 处全部为 `AsyncRawDataLogger`,**没有直接调用** `RawDataLogger` 的业务点。
- 测试 1 处:`async_raw_data_logger_test.go` 直接构造 `RawDataLogger`(单元测试桩)。

**结论**:

- `RawDataLogger` 不可下线 —— `AsyncRawDataLogger` 强依赖。
- 安全替代方案的前置条件:
  1. 把 `AsyncRawDataLogger` 的文件落盘改为 channel + 后台 goroutine 写到 `*os.File` 或 `io.Writer`(标准库)。
  2. 维持 `LogRequest/LLogResponse` API 不变。
  3. 保持 `fallback` 行为(队列满时降级同步落盘)。
- 当前 `RawDataLogger` 的 `Write()`→ `Sync()` 同步调用是 hot path 上的潜在 latency 来源,但**生产路径不直接走它**,只走 async 包装层。

**所以替代工作的真实价值是**: 在 async 层引入 batched / pooled writer,降低单条 raw data 的 syscall 次数,而不是下线 `RawDataLogger`。

### 3. R10 审计备忘录

落档位置:`docs/audit/2026-09-10-r10-doc-sync-rawlogger.md`(已写入)。

包含:

- doc-sync 清单(5 处活文档 + 1 处 frontmatter 历史快照标注)
- RawDataLogger 依赖图
- "不直接下线 RawDataLogger"的理由
- 替代 batched writer 的前置条件

## 本会话产物

- `docs/audit/2026-09-10-r10-doc-sync-rawlogger.md`(新)
- `MEMORY.md` 索引新增 R10 条目(计划)
- git 提交(计划):`docs(audit): R10 closure — lite-mode 活文档 5 处失真清单 + RawDataLogger 不可下线评估`

## 下一步行动 (下一个会话接手时)

### 批次 A — 同步执行 (低风险)

1. 按 R10 备忘录 §1 改 `docs/design/lite-mode-overview.md` 3 处代码引用(包路径 + 类型名)。
2. 改 `docs/storage/README.md:23` 措辞,把"memory 子包"替换成"lite 子包定位 + MemoryStateStore facade 说明"。
3. `grep -n 'storage/memory' docs/design/lite-mode-*.md docs/storage/README.md` 应返回 0 行(只允许 `lite-mode-storage.md` frontmatter 历史标题保留)。
4. 不动 `final-report.md` / `implementation-checklist.md` / `docs/design/archive/*` / `lite-mode-storage.md` frontmatter。

### 批次 B — RawDataLogger 替代设计 (中风险,设计阶段)

1. 评估 `AsyncRawDataLogger` 的 batched writer 设计(参考 `internal/logging/async_*` 其他模式的 ring buffer / flush interval)。
2. 写 design doc `docs/design/async-raw-logger-batched.md`,前置条件如 R10 备忘录 §2 所列。
3. 不直接动 `RawDataLogger` 代码;先 design review,再分阶段重构。

### 批次 C — 收尾

1. 提交后跑:
   - `go vet ./...`
   - `go test ./docs/...` (契约测试)
   - `go test ./internal/storage/lite/...`
   - `go test ./internal/logging/...` (保持现有覆盖)
2. 更新 R11 审计备忘录记录 doc-sync 完成 + RawDataLogger 设计阶段推进。

## 不要重复做的事

- **不要**改 `final-report.md` / `implementation-checklist.md`(冻结快照,见 `audit-2026-09-09-r5-round.md` 原则)。
- **不要**改 `lite-mode-storage.md` frontmatter 标题(历史命名)。
- **不要**改 `internal/storage/lite/state_store.go` 里 `MemoryStateStore` 类型名(facade 兼容符号,外部 11 个引用,改起来无收益)。
- **不要**直接把 `RawDataLogger` 删掉(async 层强依赖)。
- **不要**回头改 migration 692 / 691 的代码(R7 已经过全量测试,改 = 回退)。

## 关键文件位置

| 用途 | 路径 |
|---|---|
| R10 备忘录 | `docs/audit/2026-09-10-r10-doc-sync-rawlogger.md` |
| R9 备忘录 | `docs/audit/2026-09-10-r9-round.md` |
| R8 备忘录 | `docs/audit/2026-09-10-r8-round.md` |
| 历史文档冻结清单 | `docs/design/archive/`, `final-report.md`, `implementation-checklist.md` |
| 真实包 | `internal/storage/lite/*.go` |
| 同步 logger | `internal/logging/async_raw_data_logger.go` |
| 底层 logger | `internal/logging/raw_data_logger.go` |
| 命名约定记忆 | `MEMORY.md` → lite-mode-naming-convention |
| 启动 migration 5-point sync | `MEMORY.md` → wiring-gap-recurrence-5-point-sync |
