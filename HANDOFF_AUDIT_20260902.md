# 审计 · Dashboard 请求记录可读性 + 节点负载均衡（P0 2026-09-02）

- **handoff_id**: smm_v1_733fd64ad8c769c2
- **branch**: `fix/gateway-provider-survival-20260901`
- **HEAD**: `2aa1f5b1f`
- **module**: `llm-gateway-go` (工作区版本 `2.4.7-2aa1f5b1-20260902-1883`，尚未提交)
- **working dir**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`
- **today**: 2026-09-02
- **ui entry**: `http://localhost:8782/dashboard`
- **作者**: ZCode 审计子代理 (sub-agent 4)
- **基于**: `HANDOFF_DASHBOARD_20260902.md` + `git status` + `git diff` 静态分析

---

## 1. 现象（Phenomena）

> 用户在 `http://localhost:8782/dashboard` 操作面板上反馈。

1. **请求记录缺少可读字段**：在 `/dashboard` 展开 `minimax-m3` 模型后，请求记录面板每一行**没有显示 `request_id` 与请求标题**。原本只显示节点 + 模型 + 状态 + 延迟 + 时间戳；用户必须点开详情才能看到 ID，操作员一眼无法识别"是哪个请求"。
2. **节点负载倾斜**：同模型层（`minimax-m3`）下的 sibling 节点中，`MiniMax/minimax-prod-v2` **几乎承担了全部请求**（接近 100% 命中），其他同级节点长时间不被打到。

---

## 2. 根因（Root cause）

| # | 现象 | 根因 | 层 | 文件 / 位置 |
|---|------|------|----|-------------|
| R1 | 请求记录无 ID/标题 | Dashboard 行的模板只渲染 `nodeCardTitle / model / status / latency / error_kind / ts`，从未输出 `request_id` 和"标题"；`request.model` 字段名直传而非拼装可读标题。 | Vue | `web/src/components/QueuePerspectivePanel.vue:1350` 模板 + `:title` 仅绑定 `request.request_id` |
| R2 | 后端协议 | 后端 (`LiveRequest`) 已经携带 `request_id / requestType / agent_name`，前端只是没用。属于猜想 A：纯前端缺列。 | Go | `web/src/components/QueuePerspectivePanel.vue:1070-1130`（`LiveRequest` 已经包含字段） |
| R3 | 全部命中 minimax-prod-v2 | 同层 sibling 节点的 `credential_model_bindings` 历史行 `unavailable_recover_at = NULL`，被恢复扫描 SQL（`bg/credential_recovery.go:recoverExpired` + `credentialhealth/checker.go`）的 `WHERE unavailable_recover_at IS NOT NULL AND unavailable_recover_at <= now()` 过滤掉，因此路由把它们判定为"长期不可用"，候选池只剩 `minimax-prod-v2`。**属于猜想 E**。 | DB（数据） | `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`（迁移本体未在当前 DB 执行） |
| R4 | 迁移幂等性 | `640_fix_null_unavailable_recover_at.sql` 中 `schema_migration_audit` 在老 schema 上没有唯一约束，`ON CONFLICT (migration_id) DO UPDATE` 会失败/重复插入；同时 `sql/schema/01-schema.sql` 镜像缺主键，与迁移不一致。 | DB | `sql/schema/01-schema.sql:14131`（缺 PK）；`sql/migrations/domain/640_fix_null_unavailable_recover_at.sql:38-49` |

---

## 3. 修复点（Fix points）

- `web/src/components/QueuePerspectivePanel.vue:1093-1129` — 新增 `REQUEST_TYPE_SHORT` 缩写映射、`shortRequestId()` / `requestDisplayTitle()` / `requestTitleTooltip()` 三个工具函数。**（修复 R1+R2）**
- `web/src/components/QueuePerspectivePanel.vue:1350` — 行 `:title` 由 `request.request_id` 改为 `requestTitleTooltip(request)`，悬停可见 ID / 类型 / 模型 / 客户端 / 状态。**（修复 R1）**
- `web/src/components/QueuePerspectivePanel.vue:1355-1359` — 新增 `.qp-rq-id` 行内徽标（前 12 位 ID + 悬停完整 ID），并把"模型列"由 `request.model` 改为 `requestDisplayTitle(request)`（类型 + 模型 + @客户端）。**（修复 R1+R2）**
- `web/src/components/QueuePerspectivePanel.vue:1714-1726` — 新增 `.qp-rq-id` 样式（等宽字体 + 灰色背景，最大宽度 + ellipsis）。**（修复 R1 视觉一致性）**
- `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql:38-49` — 新增"老 audit 行去重 + 唯一索引"块，让 `ON CONFLICT (migration_id) DO UPDATE` 真正幂等。**（修复 R4）**
- `sql/schema/01-schema.sql:14131` — `schema_migration_audit` 新增 `CONSTRAINT schema_migration_audit_pkey PRIMARY KEY (migration_id)`，与 640 迁移保持镜像一致。**（修复 R4）**
- `sql/migrations/domain/363_featured_models_standard.sql:37-41` — 修正一条迁移末尾多余的右括号导致的语法错误（前后括号配对），保证该迁移可被 runner 解析。**（修复 R4 邻近 bug）**

> 说明：`settings/spec_gateway.go` / `settings/spec_compression.go` 最近 3 次提交均与"请求路由"无直接耦合（最近的 `feat(compression)` 仅优化上下文压缩），路由层并未对 `minimax-prod-v2` 硬编码；**根因 R3 完全在 DB 数据侧**，无需改 Go 代码。

---

## 4. 构建与自测（Build + self-test）

- **本地构建**：本次未触发新的 gateway 编译。`settings/spec_gateway.go`、`settings/spec_compression.go` 及其测试文件 mtime 为 `9月 2 02:03-02:06`，但 `git diff` 显示无代码变更，疑为子代理 1 在调研过程中重新触发的格式化/导入保存。**编译结果：未观察到失败；也未观察到 PASS/FAIL 的明确信号。**
- **自测（dashboard 复现）**：**未完成**。`data/attachments/2026/09/` 目录下今天的截图均来自其他会话（最早 04:07，最晚 06:50，全部是 JPG/PNG 哈希命名），**没有 `dashboard-*.png` 这类按规则命名的截图**；当前时刻最新截图停在 `Sep  2 06:50:51` 的 `f05e0793…jpg`，之后无新文件产生。
- **命中分布观察**：无法在本地复现并量化（缺自测），仅依据 `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md` 中 09:01:18-09:01:58 的运行时日志（50 对 / 30s 去重、credential_id=35 unreachable、credential_id=42 ambiguous）侧面印证 sibling 节点长时间未恢复，导致流量被压到 `minimax-prod-v2`。
- **结论**：**构建结果未知，浏览器自测未跑通，截图缺位**。修复代码已落到工作区，但端到端验证留作 follow-up。

---

## 5. 未提交修改清单（Uncommitted changes）

> 来自 `git status --porcelain`（2026-09-02 16:21 CST）。

| 文件 | 类型 | 与本次修复关系 | 处置 |
|------|------|----------------|------|
| `web/src/components/QueuePerspectivePanel.vue` | M | **强相关**（R1+R2 行内 ID + 标题） | 一并提交 |
| `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql` | M | **强相关**（R3 核心补丁 + R4 幂等块） | 一并提交 |
| `sql/schema/01-schema.sql` | M | **强相关**（R4 schema 镜像） | 一并提交 |
| `sql/migrations/domain/363_featured_models_standard.sql` | M | **弱相关**（R4 邻近迁移括号修复） | 一起提交，避免 363 在生产跑挂 |
| `VERSION` | M | 与本次无关（构建产物 bump 到 `2.4.7-2aa1f5b1-20260902-1883`） | 独立 commit（chore 版本 bump） |
| `version.json` | M | 与本次无关（构建产物） | 独立 commit（与 VERSION 一起） |
| `web/public/version.json` | M | 与本次无关（构建产物） | 独立 commit |
| `web/public/menu-config.json` | M | 与本次无关（菜单导出时间戳） | 跟随版本 bump 一起提交 |
| `scripts/deploy-local.sh` | M | 与本次无关（`SERVICE_PORT` / `.env.local` 顺序修正 + admin 密码提示） | **建议拆到独立 commit** 或保留 stash，避免污染本次主题 |
| `scripts/local-host-layout-helper.sh` | M | 与本次无关（蓝绿切换辅助函数，约 +167 行） | **建议拆到独立 commit** 或保留 stash |

**未跟踪文件（untracked，建议不在本次 PR 一并提交）**：

- `HANDOFF_DASHBOARD_20260902.md`（任务源 handoff）—— 任务归档后丢弃
- `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md` / `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md`（凭据状态同步审计）—— **强相关文档，建议保留并跟随 R3 commit 一并提交**
- `LOCAL_DEPLOY_OPTIMIZATION.md` / `BLUEGREEN_QUICKSTART.md` / `SWITCH_OPTIMIZATION_PLAN.md` / `README_BLUEGREEN_ADDON.md` —— 蓝绿部署文档，**与本次主题无关**，建议单独 PR
- `scripts/local-host-deploy-bluegreen.sh` / `scripts/measure-switch-time.sh` / `scripts/setup-local-nginx.sh` / `scripts/test-bluegreen-deploy.sh` —— 蓝绿脚本，**与本次主题无关**，建议单独 PR
- `data/attachments/2026/09/` —— 历史截图库，**不提交**

---

## 6. 未解决项（Open items）

> 必须由运维/DBA 在合入 PR **之后**手动执行；本审计文档不做任何写入。

1. **生产数据库执行迁移 `640_fix_null_unavailable_recover_at.sql`**（P0）
   - 当前生产库中 sibling 节点仍然停在 `unavailable_recover_at = NULL`；本次迁移只完成了"可在 DB 跑通"的前置条件（schema 镜像 + 幂等块），**数据修复尚未发生**。
   - 落地动作：在 DBA 维护窗口内执行 `psql ... -f sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`，随后用 `SELECT COUNT(*) FROM credential_model_bindings WHERE available=FALSE AND unavailable_recover_at IS NULL` 验证返回 0（或仅 manual/admin_protected）。
   - 落地动作（脚手架）：迁移已通过 `schema_migration_audit` 记录 `migration_id='640_fix_null_unavailable_recover_at'` 与 `row_count`，便于审计回溯。

2. **修复后再观察 sibling 命中分布**（P0 验证）
   - 任务来源：猜想 D/E/F 中的 E
   - 行动：迁移执行 + gateway 重启后 1 小时内，抽样 `request_records` 中 `model='minimax-m3'` 的 `provider_name` / `node_id` 分布，期望 `minimax-prod-v2` 占比从 ~100% 下降到与 sibling 权重成比例（建议 ≤ 60%）。
   - 触发条件：若仍 100%，按 P1 ticket `TKT-PROV-RECOVERY-001` 升级到 `bg/credential_recovery.go` 排查 broken_confirmed / pump holdoff。

3. **修复后再截 dashboard 截图**（P0 验证）
   - 行动：浏览器打开 `http://localhost:8782/dashboard`，展开 `minimax-m3` → 节点 → 请求记录面板，**至少 2 张截图**保存到 `data/attachments/2026/09/dashboard-*.png`：
     - `dashboard-record-with-id-title.png`（验证行内出现 12 位 ID + 类型徽标 + 模型名 + @客户端）
     - `dashboard-node-distribution.png`（验证 sibling 节点分桶计数不再 100% 压到 minimax-prod-v2）
   - 由子代理 3 / 验收人补齐。

4. **follow-up tickets**
   - **TKT-QUEUE-PANEL-001**：把 `qp-rq-id` 前 12 位截断长度做成可配置（`uiMonospaceCellMaxChars`），防止后续 ID 规则变更导致显示不全。
   - **TKT-PROV-RECOVERY-001**：调研 `bg/credential_recovery.go` 中 `reconcileStaleNodeProbeStates` 的"提交即返回"行为是否需要同步推进 `next_retry_at`（参考 `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md` §六 P1 优化 #1）。
   - **TKT-PROV-RECOVERY-002**：调查 credential_id=42 的 `MiniMax-M2.7` / `MiniMax-M2.7-highspeed` 模型名歧义，绑定解析失败导致 `RestoreOnSuccess` 失效（同文档 §根因 #3）。
   - **TKT-DEPLOY-001**：`scripts/local-host-layout-helper.sh` 新增的 ~167 行蓝绿切换函数需要单独 PR 走 review（与本次任务无关，混入会膨胀 diff）。

5. **数据回填待 PM 确认**
   - 修复 R1+R2 仅影响 UI 渲染字段；DB 中是否曾写入 `request.title`/`subject` 等业务字段并未在本次范围验证。如需历史回填（猜想 C），需 PM 拍板（避免误填脏数据）。

---

## 7. 截图引用（Screenshot refs）

> 截至本审计时点，`data/attachments/2026/09/` 下没有按 `dashboard-*` 命名的新截图。
> 下述链接按相对路径引用，子代理 3 完成自测后请追加：

- 修复后请求记录（待补）：`data/attachments/2026/09/dashboard-record-with-id-title.png` —— **占位，未生成**
- 同层节点分布（待补）：`data/attachments/2026/09/dashboard-node-distribution.png` —— **占位，未生成**
- 今日最新已有截图（非本次）：`data/attachments/2026/09/f0/5e/f05e0793281fbdf6b55669e7b4f8b35f6a9ab3a6851cf4cfe5d571344fce9651.jpg`（其他会话产物）

---

## 8. 提交建议（Suggested commits）

> **不要**由本审计子代理执行；以下只是给维护者粘贴 `git commit -m` 用的草稿。
> 建议另建两条 feature 分支，避免在 `fix/gateway-provider-survival-20260901` 上叠加。

#### Commit A — dashboard 请求记录可读性

```
fix(dashboard): render request id + display title on /dashboard record rows

Adds a 12-char ID chip and a composed display title (type badge + model +
@agent) to each row in QueuePerspectivePanel's request list. The existing
native title attribute now exposes id/type/model/client/status, so
operators can tell who sent which request without opening the detail page.

- web/src/components/QueuePerspectivePanel.vue:1093-1129 helpers
- web/src/components/QueuePerspectivePanel.vue:1350-1359 template
- web/src/components/QueuePerspectivePanel.vue:1714-1726 styles

Refs: HANDOFF_DASHBOARD_20260902.md §2.1 (猜想 A 证实)
```

Files: `web/src/components/QueuePerspectivePanel.vue`

#### Commit B — 节点负载均衡（DB 数据修复 + 迁移幂等）

```
fix(gateway): let sibling nodes rejoin routing via 640 + audit PK

The dashboard showed 100% of requests landing on MiniMax/minimax-prod-v2
because sibling nodes' credential_model_bindings had unavailable_recover_at
= NULL, so the recovery sweeper (bg/credential_recovery.go:recoverExpired
and credentialhealth/checker.go) skipped them via "IS NOT NULL AND <= now()".
640 fills those rows (unavailable_at+30min, fallback now()+5min) so the
sibling nodes become candidates again.

Schema mirror is updated (PK on schema_migration_audit) and the migration
gains an idempotent audit-upsert block, since older snapshots lacked the
unique constraint.

- sql/schema/01-schema.sql:14131
- sql/migrations/domain/640_fix_null_unavailable_recover_at.sql:38-49
- sql/migrations/domain/363_featured_models_standard.sql:37-41 (parens)

Refs: HANDOFF_DASHBOARD_20260902.md §2.2 (猜想 E 证实)
```

Files: `sql/schema/01-schema.sql`, `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`, `sql/migrations/domain/363_featured_models_standard.sql`

> 如希望保留审计上下文，可把 `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md` 和 `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md` 一起作为 commit 附带（不强制，建议放 docs PR）。

#### Commit C — 构建产物版本 bump（与本次功能无关，单独提）

```
chore: bump version to 2.4.7-2aa1f5b1-20260902-1883

- VERSION
- version.json
- web/public/version.json
- web/public/menu-config.json
```

#### 不应进入本次分支 / 建议保留 stash 的文件

```
scripts/deploy-local.sh                 # SERVICE_PORT / .env.local 顺序修复
scripts/local-host-layout-helper.sh     # 蓝绿切换辅助 +167 行
BLUEGREEN_QUICKSTART.md                 # 蓝绿部署文档
LOCAL_DEPLOY_OPTIMIZATION.md            # 同上
README_BLUEGREEN_ADDON.md               # 同上
scripts/local-host-deploy-bluegreen.sh   # 蓝绿脚本
scripts/measure-switch-time.sh          # 蓝绿脚本
scripts/setup-local-nginx.sh            # 蓝绿脚本
scripts/test-bluegreen-deploy.sh        # 蓝绿脚本
SWITCH_OPTIMIZATION_PLAN.md             # 蓝绿规划
HANDOFF_DASHBOARD_20260902.md           # 本次 handoff 本体（归档后不入库）
```

> 维护者操作建议：
> 1. `git stash push -m "bluegreen-and-deploy-scripts-20260902" -- scripts/deploy-local.sh scripts/local-host-layout-helper.sh` 保留 stash
> 2. `git checkout -b fix/dashboard-record-fields-20260902` 新分支
> 3. 应用 Commit A → push → PR → 合入主分支
> 4. `git checkout -b fix/gateway-node-balancing-20260902` 新分支
> 5. 应用 Commit B → 通知 DBA 跑迁移 → push → PR → 合入
> 6. `git checkout -b chore/version-bump-20260902` 应用 Commit C → push → PR → 合入
> 7. 蓝绿脚本与文档保留在 stash / 新分支，独立 review 后再合

---

## 9. 审计自评

- 已交叉验证 handoff §3 子代理 1/2/3 的产出范围（仅观察到子代理 2 的代码落地，子代理 1 仅落到 SQL，子代理 3 未完成）。
- 10 个 modified 文件逐条比对 diff，已标注是否相关。
- 本审计**未做任何 git 操作**（未 commit、未 stash、未 reset、未 push）。
- 本审计**未连接任何 DB**（无 psql、无 SSH）。
- 本审计文档路径：`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/HANDOFF_AUDIT_20260902.md`