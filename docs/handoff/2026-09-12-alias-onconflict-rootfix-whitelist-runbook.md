# Handoff: 别名 ON CONFLICT (raw_name) 缺陷根修 + 白名单运营化 + bare 解析跨周期复核 + 审计轮 —— 本机 2089

**日期**: 2026-09-12（含同日审计轮）
**状态**: ✅ 三项闭环 + 审计轮 1 项同族缺陷修正；本机 kx-llm-gateway-local:**2.5.4.2089**（17de96f1 合并态，含 dcd4ea46f 审计修正）@8782 已验证
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
4. **审计轮（同日，dcd4ea46f）—— 自审揪出同族第 3 处缺陷 + 两个 operator-intent 缺口，全部修正**。
   - **admin 建别名/批量导入 42P10 实发（同族第 3 处）**：`admin/models.go` 两条语句仲裁器是**表达式** `(raw_name, COALESCE(quantization,''), COALESCE(surface,''))`，库上无对应表达式唯一索引（2cef36255 引入起每次必 42P10）——管理端"创建别名/批量导入"自上线即全坏，之前抽屉复验只测了编辑链路（patchAlias 按 id UPDATE，不受影响）故未暴露。修正：语句提取为常量 `aliasUpsertSQL`/`aliasDemoteCompetitorsSQL`，改用真实 pair 仲裁；原 DO UPDATE 携带 `canonical_id = EXCLUDED`（把 raw_name 抢给当前编辑的模型），pair 约束无法对他人行表达 repoint，改为**降级竞争行**（'active'→'disabled'，与 taxonomy 语义一致）。admin upsert 对 disabled 目标**无** WHERE 守卫——API 调用本身就是 operator 在动作。
   - **operator-intent 守卫**：taxonomy upsertAlias 与 rebuildAliasIndex 的 DO UPDATE 加 `WHERE model_aliases.status <> 'disabled'`——operator 显式 kill 的别名不再被 6h 自动同步复活；taxonomy 降级只碰 `status='active'` 的竞争行（deprecated/hidden 本就不可见，翻标是无效 churn）。
   - **③ 核验结论**：`-json` 走默认 Encoder，`WhitelistReason` **已在输出中**（主程序 SetIndent 形态）；新增 `TestJSONOutputCarriesWhitelistReason` 钉住契约（reason 逐字出现、whitelisted 行永不携带 remediation Target）。教训：写形状断言要用与主程序相同的编码器设置（json.Marshal 紧凑无空格 vs SetIndent 带空格，首版测试因此误红）。
5. **两项决策定案**。
   - **AliasSyncService（discovery/alias_sync.go）：保留不接线**。依据：本地库悬空别名行 = 0（cleanOrphanedAliases 的孤儿清理无事可做）；752 行 active 自别名即使被它禁用，resolver 直查阶段也不需要它们（收益为零）；rebuildAliasIndex 与 discovery 活路径逐行 upsert 重复（discovery.go:794 注释自认）；syncCanonicalNames 的"canonical 非活性→批量禁用"与 discovery 无条件复活语义会形成对抗（别名卫生轮已证明数据层对抗 discovery 无意义）。删除则丢弃已修复+有测试+注释完整的实现且零功能收益。接线前置条件（若未来要做）：先定夺与 discovery 复活语义的冲突面、以及 752 自别名的处置是否是产品意图。
   - **生产 245/154 发布：本轮不推**。c6676f304/dcd4ea46f 缺陷在生产同样潜伏（无 YAML、admin 建别名若真有人用早就有报错信号）；且并行 necessity-gate 会话的 round-23 观测基线显式声明 "bg/deploy zero change"——突然的 245/154 cutover 会作废其基线并触发他们的回滚排查。随下次计划发布窗口自然带上。
6. **部署链事件（已自愈）**：2089 首次 deploy 在 db-revision-sequence 步骤失败——并行会话 8af09c799（win11 侧）把 `scripts/apply-db-revision-sequence.sh` 及 2 个文件提交成 **CRLF 行尾**，`set -euo pipefail\r` 报"无效的选项名"。LF 归一化后（并行会话随后续提交已把归一化内容带走入库）重跑 VERIFY_PASS=1；顺带首次把并行会话的 696/697 迁移应用到本地库。**win11 侧 CRLF 是复发类**（同日 handoff 文档也中过招），建议并行会话侧配 .gitattributes。

## 任务② bare 拼写解析跨周期复核（前置轮实测）—— 本地与生产双侧均落 120/47，稳定
   - 本地（2088 部署后 discovery 首轮 21:00:03 UTC 跑完，54 credentials/679 models）：`deepseek-v4`→120 (deepseek-v4-flash)、`doubao-embedding`→47 (doubao-embedding-vision)；歧义集最短名唯一（17 vs 24；23 vs 30，四个 dated 平 tie 全输给 23），无平局翻转面。
   - 生产 252 库（245 预发与 154 生产共享同一 PG17；245 canary 8781 与 154 均 2086/5c58bf34 在跑）：同落 120/47，且两拼写**各仅 1 行 active 别名**（本地多出的 122350/122286 等 dated 变体行为本地 discovery 特有），解析天然确定。
   - `ORDER BY length, name` 是全序——数据不变则结果必不变；唯一不稳定源是数据本身（新行/改名），已由两侧歧义集核查排除。第二轮复测（21:46:01 UTC，事件触发早于 1h ticker；54→56 credentials、679→853 models 数据增长态）同落 120/47，歧义集稳定 2 行。

## 改动文件与关键行为

| 对象 | 变更 | 提交 |
|---|---|---|
| bg/taxonomy_sync.go | upsertAlias：pair 仲裁 upsert + 竞争行降级 'disabled'（含 CTE/23505/inactive 注释）；审计轮加 operator-disabled 守卫 + 只降级 active 竞争行 | c6676f304 → dcd4ea46f |
| discovery/alias_sync.go | rebuildAliasIndex：NOT EXISTS + canonical_id NOT NULL + DO UPDATE 复活；2 处 'inactive'→'disabled'；审计轮加 disabled 守卫 | c6676f304 → dcd4ea46f |
| admin/models.go | 审计轮：建别名/批量导入 42P10 根修——语句提取为 `aliasUpsertSQL`（pair 仲裁）+ `aliasDemoteCompetitorsSQL`（跨 canonical 竞争行降级） | dcd4ea46f |
| bg/taxonomy_sync_alias_upsert_live_test.go | 新增：真库走真实 RunOnce 路径（TEST_DATABASE_URL 门控），zz-* 行自清理；审计轮增 operator-disabled 获胜 / deprecated 竞争行保标两场景 | c6676f304 → dcd4ea46f |
| admin/models_alias_sql_live_test.go | 新增：真库 RR 单事务直测提取出的两条 SQL（新建/同 pair 冲突/竞争行降级），回滚零足迹 | dcd4ea46f |
| discovery/alias_sync_live_test.go | 新增：REPEATABLE READ 单事务跑 rebuildAliasIndex + 回滚零足迹 | c6676f304 |
| scripts/govern-junk-canonical/README.md | 白名单增补六步流程 + 无 flag/无表评估 | 17ecc8ada |
| scripts/govern-junk-canonical/plan_test.go | -json 契约钉测试（WhitelistReason 序列化 + whitelisted 无 Target） | dcd4ea46f |
| VERSION/version.json/web/public/version.json | 2088/2089 bump | d60511146, 54f312d68 |

## 测试命令与结果

```
go build ./... ; go vet ./admin/ ./bg/ ./discovery/        # ok
# 备份先行：pg_dump -t model_aliases -t models_canonical → /tmp/llmgw-backup-20260912/（4018 行）
TEST_DATABASE_URL=$(grep ^DATABASE_URL ~/kaixuan/llm-gateway-go/bin/current/env | cut -d= -f2-) \
  go test -count=1 -run TestTaxonomyUpsertAlias_Live ./bg/  # PASS（aliases=6；dup 收敛 active@a + disabled@b；oped 保持 disabled；depr 竞争行保持 deprecated）
TEST_DATABASE_URL=… go test -count=1 -run TestRebuildAliasIndex_LiveArbiter ./discovery/
                                                           # PASS ×3（单事务 RR 隔离，回滚零足迹）
TEST_DATABASE_URL=… go test -count=1 -run TestAdminAliasSQL_Live ./admin/
                                                           # PASS（新建/同 pair 冲突/竞争行降级；42P10 无法被 sqlmock 抓住，只有真库能）
go test ./bg/ ./discovery/ ./admin/ ./resolve/ ./provider/ ./modelname/ ./modelcatalog/ ./scripts/govern-junk-canonical/
                                                           # 全 ok
docker stop llm-gateway-local-8782 && bash scripts/deploy-local.sh deploy
                                                           # 2088: VERIFY_PASS=1；2089 首跑败于 CRLF（见结论6），归一化后 VERIFY_PASS=1
curl /healthz                                              # 2.5.4-17de96f1-20260911-2089, ready；解密冒烟 providers=587 creds=7 failed=0
# bare 解析：2088 两轮 120/47；生产 252 库 120/47；2089 部署后 120/47
# 测试 zz-* 行清理后复核：model_aliases=0、models_canonical=0 残留
```

## 过程教训（写测试时抓到的三类真实问题）

1. **data-modifying CTE guard 不可靠**：`WITH d AS (UPDATE…RETURNING) INSERT…WHERE NOT EXISTS(SELECT FROM d)` 在 PG 里 guard 可能先于 UPDATE 的 RETURNING 求值（同快照并发语义）→ 23505。psql 单跑因 docker exec 缺 `-i`（stdin 没接上、静默零输出）曾误判"无错"，Go probe 才拿到真值——**容器内 heredoc 必须 `docker exec -i`**。
2. **活库上验证不变量要用 REPEATABLE READ 单事务**：READ COMMITTED 下 INSERT 与验证两条语句各取新快照，2088 容器 discovery/探活在两语句间持续变更数据，造成"4 行 absent"的假阴性。
3. **测试清理的 ctx/连接池生命周期**：`defer cancel()` 与 `defer pool.Close()` 都先于 `t.Cleanup` 执行，cleanup 里复用测试 ctx/池会静默失败——cleanup 每次自建 ctx、`t.Cleanup(pool.Close)` 先注册（LIFO 保证后关池）。

## 遗留风险

1. `AliasSyncService`（discovery/alias_sync.go）保留不接线（决策依据见结论5）——SQL 已修成可正确执行的形态并有线程缝测试，接线属产品决策且需先解决与 discovery 复活语义的对抗。
2. taxonomy upsertAlias / admin 竞争行降级的 UPDATE+INSERT 两语句非原子（微秒级竞态窗口）；6h/低频操作节奏下可忽略，注释已记载。
3. 测试备份在 /tmp（`/tmp/llmgw-backup-20260912/`），重启即失；本轮测试全部 zz-* 前缀自清理，未动真实数据行。
4. 245 上 llmgo-245-canary@8782 处于 failed 状态（8781 在服务）；属预发蓝绿旧槽位问题，与本轮无关，未处置。
5. 245/154 生产仍跑 2086/5c58bf34，本机修复（c6676f304/dcd4ea46f）随下次生产发布窗口自然带过去；缺陷在生产同样潜伏（无 YAML），无紧急性——但 admin 建别名 42P10 在生产**同样全坏**，若运营侧报"创建别名失败"即为此缺陷，发布本修复即解。
6. **并行会话 696 drift scanner 在本机每周期报 42703 WARN**（`integrity_fingerprint_drift: scan failed — column "raw_model_name" does not exist`）：2089 部署首次把 83bf582dd 的 scanner 带到本地容器，而本地库视图尚未具备其期望列（696/697 已随部署 revision-sequence 入账，疑似 scanner 与视图定义的列名错位）——属 necessity-gate/system-fingerprint 工作流在途事项，非本轮引入，未处置。
7. 共享工作区双会话并行：本轮工作区混有 necessity-gate 会话的未提交改动（partition_manager、db.go、grafana/prometheus、logging 等），本轮提交严格限定在本轮文件；**后续会话提交前必须按文件逐一核对归属，严禁 git add -A**。
8. win11 侧 CRLF 复发类（8af09c799 曾把部署链脚本提交成 CRLF 阻断 deploy）——建议补 .gitattributes 强制 LF。
9. **推送挂起**：本轮 3 提交（dcd4ea46f 审计修正、54f312d68 2089 版本身份、5b01f2929 handoff）已在本地 main，但 `git merge origin/main` 被并行会话对 docs/audit、necessity-gate handoff、installer/{main,stats_migrations_test,runner} 的**未提交改动**挡住（合并会覆盖其工作区编辑）——不可代为提交/贮藏。待该会话完成其提交周期后 merge + push 即可（其 round-25 addendum fa106fef2 已在远端，合并时注意 handoff 文档双方同日追加）。

## 下一轮提示词

> 聚焦目标：本轮（2089/dcd4ea46f）后的独立项。可选（按优先级）：① 696 drift scanner 本机 42703 WARN 根修——scanner 期望的 raw_model_name 列与本地视图定义错位（integrity_fingerprint_drift 每周期 WARN），属并行 fingerprint 工作流在途项，接手前先与该会话 handoff 对齐；② 生产 245/154 发布窗口把 c6676f304+dcd4ea46f 带上去（admin 建别名 42P10 在生产同样全坏，若有运营报障优先级提升）；③ 共享工作区治理：补 .gitattributes 强制 LF（win11 CRLF 已两次阻断/污染：handoff 文档、deploy 脚本），并与并行会话约定提交归属纪律。约束：不自动开新审计轮；数据操作先备份、单事务、事务内复核；共享工作区严禁 git add -A，提交前按文件核对归属。

## 相关记忆

`provider-model-drawer-verification`（已更新：本轮根修 + 2088）、`local-deploy-gotchas`、`llm-gateway-local-db`、`routing-local-provider-gotchas`。
