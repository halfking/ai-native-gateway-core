# Handoff: 别名 ON CONFLICT (raw_name) 缺陷根修 + 白名单运营化 + bare 解析跨周期复核 —— 本机 2088

**日期**: 2026-09-12
**状态**: ✅ 三项闭环；本机 kx-llm-gateway-local:**2.5.4.2088**（c6676f304，含 alias 根修）@8782 已验证
**前置**: 20260912-closeout-free-whitelist-alias-hygiene-694-deploy（resolver ORDER BY 根修 f494d0695，本机 2087）

---

## 结论 / 根因

1. **任务① `ON CONFLICT (raw_name)` 缺陷根修 —— SQL 改写，零 schema 变更（c6676f304）**。
   - **启用方核实**：`bg/taxonomy_sync.go` 已接线（main.go:4594，非 data-plane 即启用，启动 + 每 6h）；`discovery/alias_sync.go`（`AliasSyncService`）**全仓零启用方**，属死代码，缺陷潜伏。本机与部署链均无 `model_taxonomy.yaml`（deploy 脚本不携带），故缺陷全域潜伏——YAML 一旦落位即静默断掉别名同步。
   - **缺陷形态**：`model_aliases` 无 `(raw_name)` 唯一索引（migration 357 只建了 `uq_model_aliases_canonical_raw (canonical_id, raw_name)`），两处语句每次执行必报 42P10：taxonomy 的错误被调用方静默吞掉（有 YAML 也永远 "aliases 0"）；rebuildAliasIndex 每 6h warn 一次。`provider/client.go:1415` 注释早已记载此缺陷。
   - **决策：改写 SQL 而非建 partial unique index**。现有数据仍容忍同名多行（resolver ORDER BY 根修的前提），建 `(raw_name)` 唯一/partial 索引需先做数据手术，且会把 discovery.go 三处与 admin/models.go 两处按 pair 仲裁的合法 upsert 变成 unique violation——爆炸半径大；SQL 改写用既有 pair 约束即可达成原语义。
   - **taxonomy upsertAlias 新语义**（"taxonomy 权威"适配 pair 约束）：目标映射 `INSERT ... ON CONFLICT (canonical_id, raw_name) DO UPDATE SET status='active'`，随后把同名指向其它 canonical 的竞争行降级 `'disabled'`——**降级而非 repoint**，因为把 N 行重指向同一 canonical 在 pair 约束下必然 23505（真库实测）。`disabled` 在 check 约束词汇表内且对 resolver 的 `COALESCE(status,'active')='active'` 过滤不可见 → 消歧。降级用独立第二条语句：`WITH d AS (UPDATE ... RETURNING 1) INSERT ... WHERE NOT EXISTS(...SELECT FROM d)` 的 CTE guard 形状不可靠（data-modifying CTE 与主句同快照并发执行，guard 可能在 RETURNING 落地前求值 → 23505，真库实测），语句顺序保证中途崩溃只会留下"权威行 active、竞争行未降级"的安全态。
   - **rebuildAliasIndex**（INSERT IGNORE 语义）：`NOT EXISTS` 跳过已有 active 别名的 raw_name；`canonical_id IS NOT NULL` 过滤（model_offers 是视图，该列可为 NULL，23502 真库实测）；pair 冲突从 `DO NOTHING` 改 `DO UPDATE SET status='active'`——否则以 `'deprecated'` 状态存续的 pair 会让 available offer 永远没有 active 别名（真库实测）。
   - **附带修复（同文件同类静默失败）**：alias_sync.go 两处清理 UPDATE 写 `'inactive'`——**非法值**，check 约束词汇表是 active/disabled/deprecated/hidden，全部行每次都失败且被 nolint 吞掉。改 `'disabled'`。
2. **任务③ 白名单运营化 —— 评估结论：不加 `-whitelist` flag、不建 DB 表，`plan.go` 硬编码 map 即系统台账**。理由：白名单是**决策台账**而非运行时配置，每条需要证据/书面 rationale/可审 diff/测试；flag 会让条目绕过审查痕迹（随 shell 历史蒸发）；DB 表会把台账摊到 junk 形态各异的环境（本地 keep 可能掩盖生产行，反之亦然）并废掉编译期不变量测试；工具一次性、操作员手动跑、条目频率约每轮治理 1 条，重编译零成本；且工具自身"不得顺手改 schema"的规则会迫使每个 DSN 指向的环境做建表引导。落地：README 新增「Adding a whitelist entry」六步流程（诊断→判定→改 plan.go→镜像 README→钉测试→同笔提交+复跑诊断）与「Why no flag / no table」评估段落（scripts/govern-junk-canonical/README.md）。
3. **任务② bare 拼写解析跨周期复核 —— 本地与生产双侧均落 120/47，稳定**。
   - 本地（2088 部署后 discovery 首轮 21:00:03 UTC 跑完，54 credentials/679 models）：`deepseek-v4`→120 (deepseek-v4-flash)、`doubao-embedding`→47 (doubao-embedding-vision)；歧义集最短名唯一（17 vs 24；23 vs 30，四个 dated 平 tie 全输给 23），无平局翻转面。
   - 生产 252 库（245 预发与 154 生产共享同一 PG17；245 canary 8781 与 154 均 2086/5c58bf34 在跑）：同落 120/47，且两拼写**各仅 1 行 active 别名**（本地多出的 122350/122286 等 dated 变体行为本地 discovery 特有），解析天然确定。
   - `ORDER BY length, name` 是全序——数据不变则结果必不变；唯一不稳定源是数据本身（新行/改名），已由两侧歧义集核查排除。第二轮复测（21:46:01 UTC，事件触发早于 1h ticker；54→56 credentials、679→853 models 数据增长态）同落 120/47，歧义集稳定 2 行。

## 改动文件与关键行为

| 对象 | 变更 | 提交 |
|---|---|---|
| bg/taxonomy_sync.go | upsertAlias：pair 仲裁 upsert + 竞争行降级 'disabled'（含 CTE/23505/inactive 注释） | c6676f304 |
| discovery/alias_sync.go | rebuildAliasIndex：NOT EXISTS + canonical_id NOT NULL + DO UPDATE 复活；2 处 'inactive'→'disabled' | c6676f304 |
| bg/taxonomy_sync_alias_upsert_live_test.go | 新增：真库走真实 RunOnce 路径（TEST_DATABASE_URL 门控），zz-* 行自清理 | c6676f304 |
| discovery/alias_sync_live_test.go | 新增：REPEATABLE READ 单事务跑 rebuildAliasIndex + 回滚零足迹 | c6676f304 |
| scripts/govern-junk-canonical/README.md | 白名单增补六步流程 + 无 flag/无表评估 | 本轮 |
| VERSION/version.json/web/public/version.json | 2088 bump | d60511146 |

## 测试命令与结果

```
go build ./... ; go vet ./bg/ ./discovery/                  # ok
# 备份先行：pg_dump -t model_aliases -t models_canonical → /tmp/llmgw-backup-20260912/（4018 行）
TEST_DATABASE_URL=$(grep ^DATABASE_URL ~/kaixuan/llm-gateway-go/bin/current/env | cut -d= -f2-) \
  go test -count=1 -run TestTaxonomyUpsertAlias_Live ./bg/   # PASS（aliases=4；dup 收敛 active@a + disabled@b）
TEST_DATABASE_URL=… go test -count=1 -run TestRebuildAliasIndex_LiveArbiter ./discovery/
                                                            # PASS ×3（单事务 RR 隔离，回滚零足迹）
go test ./bg/ ./discovery/ ./resolve/                       # 全 ok
go test ./scripts/govern-junk-canonical/ ./modelname/       # ok
docker stop llm-gateway-local-8782 && bash scripts/deploy-local.sh deploy
                                                            # VERIFY_PASS=1，8781/8782 双端口 + 凭据解密冒烟过
curl /healthz                                                # 2.5.4-c6676f30-20260911-2088, ready
# taxonomy sync 日志：started/completed 正常（无 YAML → canonical_models=0 aliases=0 属预期空转）
# bare 解析：本地 120/47（首轮后）；生产 252 库 120/47；第二轮复测同落点
# 测试 zz-* 行清理后复核：model_aliases=0、models_canonical=0 残留
```

## 过程教训（写测试时抓到的三类真实问题）

1. **data-modifying CTE guard 不可靠**：`WITH d AS (UPDATE…RETURNING) INSERT…WHERE NOT EXISTS(SELECT FROM d)` 在 PG 里 guard 可能先于 UPDATE 的 RETURNING 求值（同快照并发语义）→ 23505。psql 单跑因 docker exec 缺 `-i`（stdin 没接上、静默零输出）曾误判"无错"，Go probe 才拿到真值——**容器内 heredoc 必须 `docker exec -i`**。
2. **活库上验证不变量要用 REPEATABLE READ 单事务**：READ COMMITTED 下 INSERT 与验证两条语句各取新快照，2088 容器 discovery/探活在两语句间持续变更数据，造成"4 行 absent"的假阴性。
3. **测试清理的 ctx/连接池生命周期**：`defer cancel()` 与 `defer pool.Close()` 都先于 `t.Cleanup` 执行，cleanup 里复用测试 ctx/池会静默失败——cleanup 每次自建 ctx、`t.Cleanup(pool.Close)` 先注册（LIFO 保证后关池）。

## 遗留风险

1. `AliasSyncService`（discovery/alias_sync.go）仍无启用方——SQL 已修成可正确执行的形态，但该服务是否接线/删除属产品决策，本轮未动。
2. taxonomy upsertAlias 的 UPDATE+INSERT 两语句非原子（微秒级竞态窗口）；6h 单控制面节点节奏下可忽略，注释已记载。
3. 测试备份在 /tmp（`/tmp/llmgw-backup-20260912/`），重启即失；本轮测试全部 zz-* 前缀自清理，未动真实数据行。
4. 245 上 llmgo-245-canary@8782 处于 failed 状态（8781 在服务）；属预发蓝绿旧槽位问题，与本轮无关，未处置。
5. 245/154 生产仍跑 2086/5c58bf34，本机修复（c6676f304）随下次生产发布窗口自然带过去；两处缺陷在生产形态下同样潜伏（无 YAML），无紧急性。

## 下一轮提示词

> 聚焦目标：本轮（2088/c6676f304）后的独立项。可选：① AliasSyncService 去留决策（死代码删除 vs 正式接线，接线前先定 cleanOrphanedAliases 的清理语义与 'disabled' 复活策略是否合理）；② 生产 245/154 发布窗口把 c6676f304 带上去（低优先：缺陷在生产同样潜伏，无错误信号）；③ governance 工具链顺手项：govern-junk-canonical 的 -json 输出加 whitelisted reason 字段核验。约束：不自动开新审计轮；数据操作先备份、单事务、事务内复核。

## 相关记忆

`provider-model-drawer-verification`（已更新：本轮根修 + 2088）、`local-deploy-gotchas`、`llm-gateway-local-db`、`routing-local-provider-gotchas`。
