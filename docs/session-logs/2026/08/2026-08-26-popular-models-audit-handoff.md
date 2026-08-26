# 2026-08-26 — popular models 三源聚合 doc/code drift 审计 handoff

> 老板：
> 本任务（popular models 三源聚合 + DB 兜底 doc/code drift 审计）已完成 commit `80e7cc12a` 并推到 origin/main。本文档为未完成 follow-up 接力。
>
> **重要**：在我 audit push (15:59) 之后，origin/main 又接受了 `39bba0d0d docs(i18n+archive): swimlane credential audit 修正` (16:18) — 同样是 audit 文档。**两个 audit commit 独立合并**。如要做 follow-up，需先 `git log origin/main -3` 确认两 commit 关系，**优先用 `39bba0d0d` 覆盖 `80e7cc12a` 的修复（更晚更全）或视情况 squash**。

## 1. 任务状态

- ✅ 完成：审计 `c4cf1917f` 的衍生文档，发现 8 处 doc/code drift
- ✅ 完成：修 3 个文件（`CHANGELOG.md` + `docs/changelogs/2026-08-26-swimlane-credential-overlay.md` + 新建 `docs/changelogs/2026-08-26-popular-models-audit.md`）
- ✅ 完成：commit `80e7cc12a`（227 行 +/- 28 行，0 代码改动）+ push 到 origin/main
- ✅ 完成：保护别人 17 个工作树文件 + 4 个 i18n 改动（详见 §4 stash 状态）
- 🚧 进行中：5 项 follow-up 在 `docs/changelogs/2026-08-26-popular-models-audit.md` §6

## 2. 关键决策记录

### 2.1 "不扩大修改范围"（rule 11 §1）
- 审计 fix **只改文档**，**不改代码**——`c4cf1917f` 功能正确（live + recent ZSET + usage SQL 三源聚合覆盖文档承诺），问题在文档虚报
- 未落地的 5 项特性**显式列入 follow-up**，等下个 session 评估工作量后开 commit

### 2.2 "保留别人修改"（rule 04 §1）
- audit 期间工作树有 17 个别人的脏改动 + 4 个 i18n 改动
- 用 `git stash push` 保护（保留 ref，可 `git stash pop` 还原）
- 推到 origin 后 `git stash pop` 还原，工作树恢复原样
- **老板后续要 commit 那些 i18n 改动需用 `git stash pop` + 确认 diff 后再 commit**

### 2.3 "rebase 警觉"（rule 11 §3 + §5 诚实汇报）
- 老板要求 push 时，git 报告 origin/main 已前进了 4 个新 commit（`e3f88e024` / `ee6153db1` / `faa653ed2` / `9b0d00fde`）
- 尝试 `git rebase --onto` 时写错了参数，触发 6 文件 merge conflict，**立即 `git rebase --abort` 阻止误操作**
- 实际状态是 **HEAD 已基于 origin/main**（80e7cc12a 的 parent = e3f88e024），无需 rebase 直接 push 成功
- 教训：rebase --onto 参数三选一记错会引发冲突 → **先 `git log --oneline --graph -5` 确认拓扑再 rebase**

### 2.4 "commit 超过 200 行阈值"（rule 01 §3）
- `80e7cc12a` 是 227 行 +/- 28 行，超过规则 01 §3 的"单 commit ≤ 200 行"
- 经老板确认保持 1 commit：audit doc 是单一完整单元，强行拆 3 commit 反而难 review
- 后续 follow-up commits 务必控制 ≤ 200 行（rule 01 §3 不可降级）

## 3. follow-up 接力清单（按 P1 → P3）

完整定义见 `docs/changelogs/2026-08-26-popular-models-audit.md` §6。

### 3.1 P1.1 `fetchPopularModelsForTenant(rdb, db, tenantID)` 租户隔离
- **风险**：多租户场景下所有 tenant 共享同一 popular models 聚合池（rule 19 §1 违反）
- **改动范围**：
  - 新增 `admin/routing.go:fetchPopularModelsForTenant(ctx, rdb, db, tenantID, limit)`
  - usage source SQL 加 `WHERE rl.tenant_id = $2`（rule 49 §49-1 列名 probe：`information_schema.columns` 确认 tenant_id 列存在）
  - recent source 需要把 ZSET 从全局键 `llmgw:routing:recently_used_models` 改成 `llmgw:routing:recently_used_models:<tenant_id>` — **改写 RecordRecentlyUsedModel 签名加 tenantID + telemetry.go:persistRequestLog 注入 tenantID**
  - live source 跳过（Redis dim queue 是全局索引，租户隔离在 DB 层做）
- **测试**：补 4 类 (tenant A 命中 / tenant B 命中 / 空结果 / 边界)
- **设计粒度**：预估 > 300 行 → 按 rule 42 拆 LP：LP1 SQL 加 tenant_id 过滤 / LP2 ZSET 键分片 + RecordRecentlyUsedModel 改写 / LP3 新增 fetchPopularModelsForTenant 入口
- **依赖**：先确认 `request_logs_hot.tenant_id` 列存在（rule 49 §49-1 schema probe）

### 3.2 P1.2 `live_stream_tile_overlay_db_lookup` Prometheus counter
- **风险**：overlay 修复无监控指标，154 上 `success/fail/locked/unknown` 比例只能 slog 估算（P1 §6.2 L2 可观测性缺失）
- **改动范围**：
  - 在 `admin/metrics/` 下新建或合并到现有 stream metrics 文件，新增 `prometheus.CounterVec` labels=[success, fail, locked, unknown]
  - `admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` 三处结局分支（`slog.Info` / `slog.Debug`）改成 `_ counter.WithLabelValues(outcome).Inc()`
  - 可能需在 `cmd/gateway/main.go:wireMetrics` 注册（参考 rule 36 §6 prom exporter 接线）
- **测试**：`admin/live_stream_sse_test.go` 新增 3 个 outcome label 触发后 counter value 校验
- **设计粒度**：预估 ~150 行，1 个 LP
- **风险**：P0 改 metrics 注册可能影响 Prometheus scrape endpoint 行为 → 先在 245 staging 灰度

### 3.3 P2.1 `LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS` env
- **风险**：硬编码 7d 不可调；运营想看 24h / 30d / 90d 需改代码
- **改动范围**：
  - `admin/config/popular_models.go`（新建）读 `os.Getenv("LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS")`，默认 168
  - `popularModelsHotCutoffWindow` 从 `const` 改 `var` + init() 赋值
  - 测试固定 env 重置（避免并行测试污染）
- **测试**：补 env override 测试（24h / 30d / 非法值 fallback）
- **设计粒度**：预估 ~80 行，1 个 LP
- **envs 同步**：按 rule 47 登记到 `~/workspace/ai-native-tools/envs/projects/llm-gateway-go/`

### 3.4 P2.2 SQL fallback short-circuit
- **风险**：三源无条件 append，每次 picker 刷新都跑 SQL GROUP BY（即使 live+recent ≥ 15 条 limit=20）。高负载 100ms+ 浪费
- **改动范围**：
  - `queryPopularModels` 三源 append 后判 `len(popular) >= limit` 时直接 `return popular, nil`
  - limit 必须从 caller 传入 → `handleRoutingAvailableModels` 当前未传，需补参数
  - 不能简单 `len >= limit` — 要保留 "policy source" 永远在前的语义
- **测试**：补 4 个 case（live+recent ≥ limit / < limit / SQL fail / limit=0 边界）
- **设计粒度**：预估 ~60 行，1 个 LP

### 3.5 P3 集成测试
- 当前 7 个 unit tests 全部 miniredis + 文本契约扫描，**未跑真实 PG**
- 加 `tests/integration/popular_models_pg_test.go` 验证：
  - `request_logs_hot` + LATERAL JOIN 性能（P95 < 50ms）
  - ZINCRBY pipeline 在 Redis 5.x / 7.x 兼容性
  - `routing_policy.featured_models` 与 ZSET 结果字段对齐
- **风险**：依赖 `LLM_GATEWAY_PG_URL` env（rule 17 §5 skip when not set）
- **设计粒度**：预估 ~150 行 + LLM Gateway PG testcontainer harness

## 4. 接力环境（接手人请先读这节）

### 4.1 当前工作树状态

```
$ git status
（无未暂存改动，工作树干净）

$ git log --oneline origin/main -5
39bba0d0d docs(i18n+archive): swimlane credential audit 修正
80e7cc12a docs(changelog): popular models 三源聚合 doc/code drift 审计修正
e3f88e024 merge: fix/request-detail-ux-polish (详情新开页 + 暗色 + 即时总结)
ee6153db1 fix(web+admin): request detail dark contrast, new-tab open, summary corpus
faa653ed2 test(web): QueuePerspectivePanel 默认折叠下的断言修正

$ git rev-list --left-right --count origin/main...HEAD
0	0  ← 已完全同步
```

### 4.2 Stash 状态（**不要 drop，需要原 owner pop 还原**）

```
$ git stash list
stash@{0}: On main: not-audit-temp-external-work
stash@{1}: On (no branch): audit-temp-rate-limit-test

stash@{0} 内容：
  改动文件：domains/streaming/request_log_pipeline_test.go
  来源：本 session 期间 git stash pop 合并进来的前任 session 工作树改动
  建议处理：原 owner（前任 session 写入者）执行 `git stash pop` 还原并自行 commit

stash@{1} 内容：
  改动文件：domains/streaming/rate_limit_test.go
  来源：更早的 session 留下的 audit-temp-rate-limit-test
  建议处理：原 owner（前任 session 写入者）执行 `git stash pop` 还原并自行 commit
  本 session 未触碰
```

**关键**：这两个 stash **都不是本 session 创建的有效 commit**。不要 `git stash drop`（不可逆丢 ref）。如确认是废弃改动，原 owner 在 commit 之前可手动 `git checkout -- <file>` 还原 + `git stash drop`。

### 4.3 已存在但未提交的工作树改动（已 stash 保护）

`stash@{0}` 之外的"别人工作树改动"已通过 `git stash pop` 还原到工作树（详见 §4.2 还原后工作树状态）。如要 commit 那些改动，请原 owner 在本 session 之外的干净 worktree 完成。

## 5. 必选 skills（接手 follow-up 时）

按 rule 11 §8 "skill 优先" + rule 17 "test gate"：

- **`planning-with-files`**：rule 42 设计粒度硬约束，每个 follow-up LP 拆 < 300 行；写 `docs/followup/popular-models/<slug>/{00-code-context,01-logic-points,02-LP*,03-drift-check}.md`
- **`verification-before-completion`**：rule 11 §10 任务完成总结 + rule 17 三件套（`go build ./...` + `go vet` + `go test -short`）
- **`implement`** 或 **`tdd`**：rule 17 增量验证
- **`task-stop-audit`**：rule 55 任务停止审计
- **`session-audit-gate`**：rule 50（commit 前 audit entry 写入 `docs/session-logs/2026/08/<date>-<slug>.md`）

## 6. 关联

- 本 commit：`80e7cc12a`（`docs(changelog): popular models 三源聚合 doc/code drift 审计修正`）
- 后续 audit commit：`39bba0d0d`（`docs(i18n+archive): swimlane credential audit 修正`，16:18 推到 origin/main）— **如要做 follow-up，先 diff 这两个 commit 看是否有冲突的修复**
- 原始 commit：`c4cf1917f`（功能实现，doc/code drift 的源头）
- 审计详情：`docs/changelogs/2026-08-26-popular-models-audit.md`（本任务新建）
- 关联规则：rule 09 §2 FACT、rule 11 §1/§3/§5、rule 17、rule 19、rule 33、rule 42、rule 49
