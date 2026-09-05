# 2026-08-25 — Expert Detection 审计发现修正

> **TL;DR**：审计 expert detection feature（commit `ae0ad0d4a` + `08566ce99`），发现 4 处问题：
> 1 处 Major（已修）+ 1 处设计文档内部矛盾 + 1 处冗余条件 + 1 处历史 commit doc drift。
> 本 commit 处理后 2 项；第 1 项由 `08566ce99` 处理；第 4 项为历史 commit 信息不修订。

## 1. 审计范围

- `ae0ad0d4a feat(sessionmeta): add Expert type detection from system prompt`
- `08566ce99 fix(sessionmeta): restore omitempty tags on Agent/Client/WorkTypes/Project`
- `08566ce99`（领先 `origin/main` 1 commit，待 push）
- 工作树脏 SQL 改动明确排除（老板确认"本次任务只关注 expert detection"）

## 2. 发现与处理

### F1 — Major — `ae0ad0d4a` 误删 `omitempty` tags ✅ 已修

`extractor.go` 的 `Result` struct 在 `ae0ad0d4a` 中把 `Agent / Client / WorkTypes / Project` 的 `omitempty`
JSON tag 全部去掉；这违反 rule 37 §3 精准修改（feature commit 不应触碰无关字段 tag）。
`08566ce99` 已恢复原 tag，本审计不重复修。

### F2 — Major — 设计文档 §6 与实现 / §3 不做清单三者矛盾 ✅ 已修

`docs/design/expert-detection/00-design.md` §6 曾描述"修改 `extractor_rules.go`：Expert 字段挂到 provisionalTitle 入参"，
但：
- §3 不做清单明确写"不改 `auto_title_generator.go`，保留向后兼容"
- §2.5 关系表写"`provisionalTitle()` 加 `[agent]` 前缀 — **不动**，避免标题过长"
- 实际 git diff 中 `extractor_rules.go` 未改动

三者矛盾。修正：从 §6 删除"修改 `extractor_rules.go`"那一行，新增 §7 说明此次审计补遗
（明确 §6 与 §3 / §2.5 / 实际代码三者一致）。

### F3 — Minor — `extractExpert` 冗余条件 ✅ 已修

`extractor_expert.go` 的 `extractExpert` 中：
```go
detected := DetectExpertFromSystemPrompt(system)
if detected != "" && detected != ExpertUnknown {  // detected != "" 是冗余
    return ExpertIdentity{Type: detected, Source: "system_prompt", Confidence: 0.85}
}
```

`DetectExpertFromSystemPrompt` 实现保证返回 `ExpertUnknown` 或非空 canonical 常量，**永不返回 `""`**，
所以 `detected != ""` 是 always-true 条件。简化为：
```go
if detected := DetectExpertFromSystemPrompt(system); detected != ExpertUnknown {
    return ExpertIdentity{Type: detected, Source: "system_prompt", Confidence: 0.85}
}
```

行为不变（既有 60+ 单测全 pass），消除认知负担。

### F4 — Info — 历史 commit message 与代码 drift ⚠️ 不动

`telemetry/request_metadata.go` 的 `ae0ad0d4a` commit message 提到 "extends telemetry cursor patterns
(powered by composer / operate in cursor)"，但实际加了 3 个 pattern：
- `powered by composer`（已提）
- `operate in cursor`（已提）
- `cursor's ai`（**未提**）

历史 commit 已 push，amend 会改 SHA，违反 rule 01 §5"不 force-push"。新加的 `cursor's ai` 模式合理
（兜底有人用 "cursor's ai" 描述 Cursor 的 AI 助手），保留不动。

## 3. 验证

| 验证项 | 命令 | 结果 |
|--------|------|------|
| 编译 | `go build ./...` | exit 0 |
| 单测（sessionmeta） | `go test -count=1 ./domains/analysis/sessionmeta/...` | ok（100% pass） |
| 单测（下游 4 包） | `go test -count=1 ./telemetry/... ./admin/... ./domains/streaming/...` | ok |
| 静态检查 | `go vet ./domains/analysis/sessionmeta/...` | exit 0 |
| Expert JSON 契约测试 | `TestExtract_ExpertJSONContract` / `TestExtract_ExpertAlwaysEmitted` | PASS |

## 4. 改动清单

| 文件 | 类型 | 改动 |
|------|------|------|
| `domains/analysis/sessionmeta/extractor_expert.go` | 修改 | 删除 `extractExpert` 冗余条件（-3 行 +2 行 +注释） |
| `docs/design/expert-detection/00-design.md` | 修改 | §6 移除"修改 `extractor_rules.go`"行；新增 §7 审计补遗说明 |

合计 2 文件改动（diff 共 13 行）；不触动无关代码（rule 37 §3）。

## 5. 范围声明

本次审计**不**触及工作树中的其他脏改动：
- `VERSION` / `version.json` / `web/public/version.json` — 版本号 bump
- `db/db.go` / `sql/objects/indexes/*` / `sql/schema/01-schema.sql` / `deploy/sql/*` / `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` — index predicate bug fix（migration 605）
- `docs/db-changelog.md` — DB 变更日志

这些属于独立任务，本 commit 不一起携带（rule 01 §3 "一 commit 一件事"）。