# Handoff: junk canonical 治理后的独立收尾轮 —— review free 白名单 / 全局别名卫生 / 694 升级通道 + 本机部署 2085

**日期**: 2026-09-12
**状态**: ✅ 三项全部闭环（free 定夺 + 别名卫生清零 + 694 通道修复与 2085 部署）
**运行环境**: 本机容器 kx-llm-gateway-local:2.5.4.2085（46e3bc7e→bump 5a877aa9d），@8782 健康检查 ok
**前置**: 20260912-junk-canonical-remediation（14 行弃用、37/37 可路由）遗留三项

---

## 任务①：review `free`（models_canonical 2664333）定夺 —— 保留 + 白名单

**语义取证（决定性）**：
- openrouter（provider 21，raw `openrouter/free`，pm 2661145）**全历史零流量**：`request_logs` 中 `canonical_id=2664333` 0 行、`provider_id=21` 0 行（该 provider 从未承载过请求）。
- provider 21 的 2026-08-25 discovery 里，所有 per-model `:free` 变体（`z-ai/glm-5.2:free`、`minimax/minimax-m3:free`、`nvidia/nemotron-3-ultra-550b-a55b:free`…）各自有专属 canonical 行；唯独裸 `openrouter/free` 被剥前缀种成 junk `free`。
- `openrouter/free` 无单一模型身份（OpenRouter 免费池伪形态）：govern 工具 pair-test 证据 0.99×5 同分（glm-5.2:free / inkling:free / minimax-m3:free / laguna-s-2.1:free / lfm-2.5-2.6b:free），重指向任一都是错的。
- 引用面：仅 1 条 pm + 1 条自名别名，无 work_type_model_route 引用。

**定夺：免费池伪模型 → 保留 + 白名单**。落地为工具机制（非文档一笔）：
- `scripts/govern-junk-canonical/plan.go` 新增 `VerdictWhitelisted` + `OperatorWhitelist`（name→rationale，`free` 首条带全部证据）。白名单覆盖一切 verdict（即使将来算出 fixable 也不动）；诊断仍以 whitelisted 桶 + 理由透明呈现；BuildApplyPlan 天然不产 plan，且 whitelisted 行留在 suspectIDs → 永不作为重定向落点。
- main.go 报告：suspects 计数行、逐行理由、尾部 note 均含 whitelisted。
- 测试：`TestDiagnoseAmbiguousTargetsRelegatedToReview` 临时清空白名单继续覆盖 tie→review 路径；新增 `TestDiagnoseWhitelistOverridesReview` 断言 verdict/reason/无 plan/无重定向落白名单行。
- README：verdicts 增 whitelisted；新增 Operator whitelist 章节（free 的完整证据与 2026-09-12 决策）。
- 复诊：`go run ./scripts/govern-junk-canonical` → suspects: 1 (0 fixable, 0 review, 0 withheld, 1 whitelisted)。
- 提交：`e3ff2e45f`。若 OpenRouter 流量将来变得相关，应作为整体重访该行，而非重定向。

## 任务②：全局别名卫生轮 —— 371 多目的地 = 369 惰性 + 2 真歧义（已逐条定夺修复）

**方法（关键：按 resolver 代码语义分类，不按直觉）**。`resolve/resolve.go` 查找顺序 = ① variant 矩阵逐个 canonical 精确匹配（`NormalizeRouteKeyAliases`：点位笛卡尔积 + wrapper 剥离，首中即胜，确定性）→ ② 同序别名 `LIMIT 1` **无 ORDER BY**。故"canonical 同名遮蔽"只是惰性的一种——**跨形 variant 的 canonical 拦截同样使别名不可达**。
- 一次性分析器（/tmp/aliasaudit，replace 引 modelname）精确复刻该顺序：371 个多目的地活跃拼写中 **369 个 canonical-intercept（惰性，含 241 个同名遮蔽 + 128 个跨形拦截）**，真歧义仅 2 个。上轮担心的 `claude-haiku-4-5` 标点双胞胎属前者（`claude-haiku-4-5` 本身是活跃 canonical 352，canonical 路径确定性命中）。
- 两拼写全历史零流量（歧义从未实际触发）。

**逐条定夺**（原则：bare 家族名 → undated 基准行；dated 快照经自身 canonical 名 opt-in）：
1. `deepseek-v4`：→ **deepseek-v4-flash (120)**（work_type_model_route 5 个 work_type 的 pinned primary，90 天 2252 次 vs pro 1571 次）；弃用 → deepseek-v4-flash-260425 (122350) 行。
2. `doubao-embedding`：→ **doubao-embedding-vision (47)**（5 目的地中唯一 undated 基准行；家族 90 天流量≈0）；弃用 4 条 dated 快照行（5724/122286/122304/122331）。

执行：先 `pg_dump --data-only --table=model_aliases` 备份（/tmp/backup_alias_hygiene_20260912_030557.sql），单事务 2 条 UPDATE + DO 块复核（各拼写活跃目的地数=1 且=定夺目标、无活跃别名指向死 canonical）。**禁批量盲改遵守**：仅 2 项、逐条裁量。
外部复核：重导数据重跑分类器 → **true-ambiguity 0**，369 全部 canonical-intercept。

## 任务③：migration 694 升级通道修复 + 本机部署 2082→2085

**部署前预检发现同款缺口（693-class）**：694 只存在于仓库文件（installer 全新安装路径），db.go 无 ensure 镜像、apply-db-revision-sequence.sh 清单止于 693——升级库无任何通道应用它。已在预检阶段修复（而非部署后补救）：
- `scripts/apply-db-revision-sequence.sh` 增补 694 条目（提交 `46e3bc7e2`）；运行脚本 → 仅 694 真正应用（`CREATE FUNCTION`），gateway_db_revision_sequences 登记_marker；补 `schema_migrations` '694' 行与 693 双账本对齐。
- 验证：`pg_get_functiondef(promote_request_logs_hot_to_partition)` 含 demote 自愈块。
- 注：687/688 亦未入账（早于本轮即如此、无任何错误信号）；694 体=688 全量+demote，应用后本库 promote 函数已是对齐形态。687 的分区上界守卫修复未见本库对应 42P17，未越界处理。

**部署**（local-deploy 配方照做）：docker stop → `bash scripts/deploy-local.sh deploy` → **2.5.4.2085**（8781/8782 双实例 verify ok，凭据解密冒烟 587 providers/7 creds/0 failed）；版本身份 chore(release) 提交 `5a877aa9d`（menu-config.json churn 还原未提交）。部署后：0 条 42703/42P17/23505，routing_health_checker 正常完成，data-lifecycle hot cron 正常。

## 改动与数据变更清单

| 对象 | 变更 | 提交 |
|---|---|---|
| scripts/govern-junk-canonical/{plan.go,main.go,plan_test.go,README.md} | whitelisted verdict + OperatorWhitelist(free) | e3ff2e45f |
| model_aliases | 5 行弃用（deepseek-v4→122350；doubao-embedding→4 快照行） | 数据态（备份见上） |
| scripts/apply-db-revision-sequence.sh | 增补 694 | 46e3bc7e2 |
| VERSION/version.json/web/public/version.json | 2085 bump | 5a877aa9d |
| DB | 694 入账（双账本）+ promote 函数自愈体 | 部署路径 |

## 遗留 / 备注

1. 备份均在 /tmp（junk 轮 `backup_junk_canonical_20260912_0147.sql`、本轮 `backup_alias_hygiene_20260912_030557.sql`）——重启即失，需长期留存请转移。
2. `openrouter/free` 重访条件：provider 21 出现真实流量时整体重审（届时先看 discovery 是否已修复伪模型种行）。
3. 687/688 未入账为既存状态，无错误信号；若未来出现 request_logs 分区上界 42P17 再按 687 处理。
4. 别名卫生的分析器在 /tmp/aliasaudit（一次性），方法已录本档：分类必须按 resolver variant 矩阵语义，同名遮蔽≠惰性全集。
