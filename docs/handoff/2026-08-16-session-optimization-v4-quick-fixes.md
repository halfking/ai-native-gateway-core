# 2026-08-16 - 会话优化 v4 P1-F / P0-E 快速修复交接

> 状态：两项裁决和文档统一已完成；无运行行为变更。
> 项目：`llm-gateway-go-4`

## 1. 最终裁决

### P1-F Balanced.UseAudit

最终取值：`false`。

依据：

- `docs/会话优化v4/05-会话分析与模型选择设计.md` §2.1 的勘误语义明确为 balanced 不启用自动审计、aggressive 专属；同一句中的“需改为 true”与该语义直接冲突，属于残留错误行动项。
- `docs/会话优化v2/18-Goal模式成本控制与分级方案.md` 的原始三档定义为 minimal=false、balanced=false、aggressive=true。
- `docs/Goal模式用户指南.md` 与 `domains/hooks/goal/cost_presets_test.go` 同样锁定 balanced=false、aggressive=true。
- 因此未把 `ModePresets[CostModeBalanced].UseAudit` 改为 true；仅把代码注释改为“preset 不驱动审计/修正，自动审计为 aggressive 专属”，并统一冲突文档。

### P0-E HTTP 202 异步重试 header

最终取值：保留现有 `202 Accepted` + `X-Gw-Pending` + `X-Gw-Pending-Request` + `Retry-After`；不增加 `X-LLM-Gateway-Retry-Scheduled`。

依据：

- `docs/会话优化v2/17-Goal模式方案审计报告.md` §2.3 将 `X-LLM-Gateway-Retry-Scheduled` 放在“建议修正”示例中；顶部勘误及 `31-当前实现基线与修正决策.md` ADR-GOAL-001 均明确它是未实施提案，不是冻结契约。
- 当前 `domains/streaming/handler.go` 的 202 路径表达 pending/幂等 replay；durable 设计也冻结为 `X-Gw-Pending*` 契约。pending 不等于当前请求新调度了一次 retry。
- 在幂等 replay 路径增加 `X-LLM-Gateway-Retry-Scheduled` 会制造错误语义，且仓库内无代码、测试或客户端依赖该旧候选字面量。
- 因此修正文档，把 Goal retry 的历史提案与现有 pending/durable 202 契约明确分开；`handler.go` 不改。

## 2. 改动清单

- `domains/hooks/goal/cost_presets.go`：保持 balanced `UseAudit=false`，修正注释。
- `docs/会话优化v4/01-现状基线与版本裁决.md`：修正 balanced 审计冲突裁决和遗留项。
- `docs/会话优化v4/05-会话分析与模型选择设计.md`：消除 §2.1 内部矛盾；区分历史 Goal retry 提案与当前 pending 契约。
- `docs/会话优化v4/10-实施计划.md`：P1-F 改为已裁决的文档勘误；P0-E 标为 DEFER，不再要求 header 别名。
- `docs/会话优化v4/11-完成情况核实与并发执行方案.md`：修正 P1-F/P0-E 核实结论、T6 范围和触碰文件。
- `docs/会话优化v2/17-Goal模式方案审计报告.md`：明确旧 header 是非规范性候选命名。
- `docs/会话优化v2/59-会话完整业务流程与数据结构基准.md`：修正 balanced 历史口径并补 balanced 无自动审计核实项。

## 3. 验证

P1-F 完成后，在目标仓库执行：

- `go build ./...`：通过。
- `go test ./domains/hooks/goal/... ./domains/streaming/...`：通过。

P0-E 完成后，再次在目标仓库执行：

- `go build ./...`：通过。
- `go test ./domains/hooks/goal/... ./domains/streaming/...`：通过（缓存命中）。

提交前在基于最新 `origin/main` 的隔离 worktree 补跑：

- `go test ./...`：通过（仅 vendor clang warning，无测试失败）。
- `go vet ./...`：通过。
- `go build ./...`：通过。
- `go test ./domains/hooks/goal/... ./domains/streaming/...`：通过。
- `./scripts/scan-secrets.sh --mode=strict --paths=.`：被仓库既有基线阻断（57 BLOCK / 1052 WARN，包含已跟踪 `.env.example`、`deploy/phase0/optimization.env` 和历史内部域名文档），不是本次新增。
- 对本任务 8 个文件按脚本真实多路径语法执行 strict 扫描：8 files、0 findings，`CLEAN`。
- `git diff --check`：通过。

## 4. 审计收口

- Standards 审计发现验证记录缺少仓库级 `go test ./...`、`go vet ./...` 和严格密钥扫描；提交前已安排在隔离 worktree 补跑。
- Standards 审计发现 P0-E 对 pending 202 的描述范围过宽；已区分初始 accepted 响应与后续 `in_progress` 轮询响应。
- Spec 审计发现 T6 触碰面漏列 v2 文档与 handoff；已补齐。
- 修正后未发现剩余的规范或需求符合性问题。

## 5. 边界与后续

- 本次没有修改 `domains/streaming/handler.go`，也没有新增兼容 header。
- 若未来决定实现 Goal retry 的异步协议，应在完成前端/OpenAI SDK 兼容性验证后重新冻结状态、body、header 和轮询语义，不应默认复用 17 号示例。
- 共享工作树中的 `cmd/gateway/capabilities.go`、URSM v2、Goal↔Handoff 等并发改动均非本任务；本提交在独立 worktree 完成，未修改、未回退或混入这些变更。
