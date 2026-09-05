# 2026-08-18 — URSM k2 迁移：审计、合并、handoff

## 审计

并行启动两轴审查 + 全量门禁重跑。

- **Standards 轴**：无硬性违规；1 个判断题 — dual.lua 缺失 ARGV/health-bridge 注释块（`record_request.go` 已对接注释，不阻断但单独看 Lua 文件偏薄）。
- **Spec 轴**：10/10 全通过（dual read precedence、key set 原子、PTTL 扣减、cleanup exact-key、四分类 + NO-GO、metadata 不可复用与 stale epoch、L1 legacy bytes 不变、scope creep、T0 完成式表述 0、CLI dry-run 默认 + --apply NO-GO 拒绝、PG 双轨同源）。
- **判断题修复**：恢复 `record_request_dual.lua` 的 ARGV[1..15] 注释与 health bridge 边界段（commit `243613173`）。
- **全量门禁**：URSM + migration + db + CLI 全绿，0 race failure；`go vet ./...` ✓、`go build /` ✓、`scan-secrets.sh --mode=strict` 0 findings、`pre-commit-check.sh` PASS(4) SKIP(2)。
- **范围**：miniredis/sqlmock/SKIP 一律标注替身；真实 Redis restart/NOSCRIPT/PubSub 与真实 PG lease/fencing 仍属 G4。

## 合并（feat/ursm-k2-migration ⇐ origin/main）

`git merge origin/main --no-ff` 产生 4 个冲突（doc 14 + keys_k2.go + keys_k2_test.go + pipeline.go）。

冲突解决策略（**不丢弃任何一方的代码**）：

1. **keys_k2.go**：保留本分支的命名（`KeySchema int`、`K2NodeKeyForTenant`、`ParseNodeKeyAny`），同时合入 origin/main 的 `ParseWindowKeyAny`/`ParseCandidateIndexKeyAny` 严格 parser 集合作为同义 API，并增加 `ParseKeySchema` 反向 wire → enum 助手（供持久化层使用）。
2. **keys_k2_test.go**：以本分支为核心测试 + 追加 origin/main 的 strict window/index 测试 + 生成 corpus 性质测试（收敛到 200 次以加速）。
3. **pipeline.go**：保留本分支 dual/canonical 分支逻辑 + 合入 origin/main 的"过期 cool 半开"修复（154 incident：minimax-m3 / cred 36）。
4. **doc 14 §9/§10**：保留本分支 §10 owner 表 + 合入 origin/main 的 §0 前置事实记录 + 在变更记录追加"合并"条目 + 用加注的旁注说明 ledger_id 并存状态（`ursm-v2-k2-20260818-001` vs `ursm-k2-mig-134e6d21-...`）。

提交链（feat/ursm-k2-migration）：

- `07ee022d9` docs: freeze migration owner / ledger_id / topology（Slice 0）
- `834cf5d0f` refactor: 字节等价预重构（Slice 0b）
- `88c830f2f` feat: k2 canonical key primitives（L1）
- `7224f1e13` fix: 应用 L1 双轴审查判断题
- `474846860` docs: L1 完成审计与并行冲突记录
- `28f6afb65` feat: dual-schema record + canonical-first reads（L2 core）
- `69a526a76` feat: schema-aware persist/recovery/bootstrap/config（L2 接线）
- `d9dc9b203` feat: preflight/copy/cleanup/rollback + PG ledger（L3）
- `dd105b09c` fix: `KeySchema.String` for the durable ledger
- `243613173` docs: restore the ARGV/health-bridge comments in record_request_dual.lua
- `f39b4446e` merge: integrate origin/main into feat/ursm-k2-migration

主工作树分支 `feat/v5-ui01-menu-sync` 在本次合并中**未被修改**（不同 worktree，互不影响）。origin/main 的所有提交（v1602/v1603/credentials17、154 incident fix、routing status matrix、release bumps）已通过合并被纳入本分支。

## 已确认门禁

- `go build ./...` ✓
- `go vet ./...` ✓
- `go test ./domains/ursm/... ./db/... ./cmd/ursm-k2-preflight/... -count=1` ✓
- `go test -race ./domains/ursm/... -count=1` ✓（0 DATA RACE）
- `scripts/scan-secrets.sh --mode=strict` 0 findings
- `scripts/pre-commit-check.sh` PASS(4) SKIP(2)

`go test ./... -count=1` 与全仓 `-race` 在合并后执行中（背景命令 `exec_162c9632`）；如出现失败需复检外部 flaky（如 `plugin-runtime TestLifecycle_*`，已知与本分支零关联）。

## 推送与收口

合并 commit `f39b4446e` 已落盘于 `feat/ursm-k2-migration` 分支。推送 + 合并到 main 的具体执行留给下一会话（见"下一步"），需用户裁决 ledger_id 与切到 `main` 的 PR 策略。

## 下一步（请下一会话执行）

下列动作按顺序完成 G1/G4 证据采集并把分支合并到 `main` 推送远端。

### 1. 推送 worktree 分支

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/.worktrees/2026-08-18-ursm-k2-migration
git push -u origin feat/ursm-k2-migration
```

若 push 拒绝（main 已保护），先用 PR：到远端创建 `feat/ursm-k2-migration` → `main` 的 PR；本会话保留的合并 commit 即 PR 内容。

### 2. 裁决 ledger_id 双记录

doc 14 §10 现存两个 ledger_id：

- 本分支：`ursm-v2-k2-20260818-001`
- origin/main：`ursm-k2-mig-134e6d21-721b-41d4-af0b-adff143107e2`

doc 14 已在变更记录与加注中明示二者并存；下一会话需要在合并入 `main` 前二选一并在 doc 14 / 16 / CLI 默认 `--ledger-id` 中同步。**推荐保留 `ursm-v2-k2-20260818-001`**：

- CLI `cmd/ursm-k2-preflight/main.go` 默认值与本分支一致，迁移 owner 写 metadata 时无需指定 `--ledger-id`；
- 与 pr/feature 命名风格一致（日期前缀 + 短标识）；
- doc 14 §10 表与本分支提交链绑定的实现/移交清单已对齐此 id。

如保留 origin/main id，需同步修改 CLI 默认值并在本分支追加兼容说明。

### 3. 合并到 `main` 并推送

策略选择（用户决定）：

- **A. fast-forward merge**：在主工作树执行 `git merge --ff-only feat/ursm-k2-migration`，然后 `git push origin main`。前提：origin/main 在推送窗口内未推进。
- **B. merge commit**：`git merge --no-ff feat/ursm-k2-migration`，生成可追踪的合流 commit，再 `git push origin main`。推荐用于 PR 自动合流的语义保留。
- **C. rebase + fast-forward**：`git rebase origin/main feat/ursm-k2-migration`，再 fast-forward merge。本会话已含一次合并 commit，重 rebase 会重写 11 个本地 commit 的 SHA，不推荐。

推荐 B：保留本分支的合并 commit 作为可审计的合流点；冲突解决的取舍已经在 commit message 与 session log 里。

### 4. G1 本地门槛复跑

按 doc 14 §7 的"全本地门槛"在合并后主工作树复跑：

```bash
go test -count=1 ./...
go test -race ./...
go vet ./...
go build ./...
./scripts/pre-commit-check.sh
./scripts/scan-secrets.sh --mode=strict --paths docs/03-design/02-feature-design/会话优化v4/14-URSM* \
    domains/ursm/v2/store/domains/ursm/v2/migration/ cmd/ursm-k2-preflight/ \
    sql/migrations/080-* db/db.go
```

预期结果：与本会话一致（URSM/migration/db/CLI 全绿；`plugin-runtime TestLifecycle_*` flaky 仍可能与本分支零关联地出现一次，不构成 release-ready 阻断）。

### 5. G1 独立审计

`M5-0/T0` 仍 `BLOCKED / NO-GO`。G1 的本质是"我可以宣布 L1/L2/L3 满足 doc 14 §7 的本地门槛"——它不能由本会话/本审计员自行宣告。下一会话或独立审计员需：

- 完整重跑本会话的 Standards + Spec 双轴（固定点 `fafdc60a5`），
- 阅读 `2026-08-18-ursm-k2-l1-canonical-primitives.md` 与 `2026-08-18-ursm-k2-l2-l3-implementation.md`，
- 在 doc 14 §9 变更记录追加"GA/M5-2 独立审计通过（无 owner 变更）"。

### 6. G4 真实依赖证据采集

G4 不能由 miniredis 替身证明。需要真实环境：

| 证据 | 方式 | 退出判据 |
|---|---|---|
| 真实 Redis restart / `NOSCRIPT` / PubSub / 连接重建 | `TEST_REDIS_URL` 指一次性实例；`scripts/run-migrations-strict.sh` 模式；`store/record_request_test.go:639` 的 `TestRecordRequestLuaSmokeOnRealRedis` 套件覆盖；加 `SCRIPT FLUSH` 后立即触发 RecordRequest/dual-write | 全场景零 SKIP |
| 真实 Redis PTTL 精度 | miniredis `FastForward` 已预演；真实时钟还需采集至少 3 个 1m/5m/30m 跨度的 key 的 `PTTL` 与 copy 后目标 `PTTL` 时序 | copy TTL ≤ 源剩余毫秒 |
| 真实 PG lease/fencing | `TEST_PG_URL` 或 testcontainers（migration 532 注释已修正 testcontainers 可用）；`durable/store_claim.go:79-165` 复用模型 | ledger copy 在 owner 被 fencing 时正确中止 |
| 真实 provider / slow-client / chaos | `tests/integration/request_flow_fault_injection_test.go` 模式 + llm-gateway-test skill 的网关级矩阵 | 报告为环境隔离、key 走环境变量、JSON/MD/HTML 工件齐全 |

`llm-gateway-test` skill 的"协议 × 模型 × 错误路径 × 性能"矩阵可在此处复用，但需切换到迁移 staging 环境而非现网。

### 7. runbook 增补（迁移 owner 决）

按 doc 15 §5 增补 `docs/06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md`：

- 模式表加 schema mode 维度（与 URSM_V2_MODE 的两层关系）；
- preflight 操作步骤 + NO-GO 判据；
- copy 限速/暂停/重跑命令与验收；
- dual 观察窗判据（fallback=0、错误率/无候选率不超 24h 基线、P99 增幅 <5%）；
- exact-key cleanup 前置 + 严禁 SCAN…DEL；
- key-schema rollback 步骤（区别于 authoritative→off）；
- 仿照 §7 evidence 记录模板（preflight_checksum、ledger_id、操作者、指标链接）。

本会话未触碰 `scripts/rollback/ursm_v2_to_legacy.sh`（origin/main 的 merge 已带回该脚本的修正），但 runbook 文档本身需新增 schema mode 章节。

### 8. 工件目录

本会话的 session log 全部在 `docs/session-logs/2026/08/`：

- `2026-08-18-ursm-k2-migration-implementation-start.md`
- `2026-08-18-ursm-k2-l1-canonical-primitives.md`
- `2026-08-18-ursm-k2-l2-l3-implementation.md`
- `2026-08-18-ursm-k2-audit-merge-handoff.md`（本文件）

`docs/03-design/02-feature-design/会话优化v4/` 中的 13/14 号为规格源；doc 14 §10 已记录 owner/ledger/拓扑与显式移交清单。

## 阻塞与风险

- **T0 仍 BLOCKED**：G1 未独立审计；G4 真实依赖证据未采集；
- **T8/245** 仅 G5 GO 后执行；
- **次轮会话注意**：`go test ./...` 全仓背景运行在 `exec_162c9632`，请在合并后复跑；如遇 `plugin-runtime TestLifecycle_*` 失败，与本分支零关联，可忽略并记录入 session log；
- **ledger_id 决策**：合并前由用户裁决。

## 备注

- 全部分支隔离在 worktree `.worktrees/2026-08-18-ursm-k2-migration`（`feat/ursm-k2-migration`），主工作树 `main` / `feat/v5-ui01-menu-sync` / `feat/m3-sr-w3c` 未被本会话修改；
- 主工作树 11 个未归属 Web 导航文件改动原样保留，未被本会话触碰；
- origin/main 与本分支的差异仅在命名/API surface，行为契约完全等价；
- 本会话所有提交均符合 Conventional Commits 与 CONTRIBUTING.md。