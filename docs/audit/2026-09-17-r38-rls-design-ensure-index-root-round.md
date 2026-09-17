# R38 — RLS 架构设计 + ensure 冗余索引根治轮（2026-09-17）

- 性质：用户指定专项（R37 §四遗留为起点，非 48h 滚动窗口轮）。首位 **RLS owner 绕过架构设计**（GUC 设置面盘点先行）；其次 **ensure 冗余索引根治**（parent_ts↔parent_request_id 先统一归属）与 **252 部署前置**（只读）
- 起点（重算）：合并 origin/main 快进 5 提交（b874368ae→058c2d20e，凭据误判不可用事故批 + 154 金丝雀模板修复），工作树起点 ff9e6f27d；收尾时再 fetch 无新提交
- 方法：2 路只读子代理并行（A: GUC 设置面全仓盘点 / B: db.go ensure 索引所有权底账），载荷断言逐条亲读复核 + 本机/252 双真库只读普查；719 迁移按新纪律存量真库实跑后定稿
- 纪律执行：R37 三教训已入 playbook conventions.md §5（迁移三纪律）；本轮全程按此执行（719 实跑 + 复活扫描 + 每行创建者核查 + origin/main 为准）

## 一、对 R37 记载的修正（真库为准，承教训#3）

| R37 记载 | R38 实测 | 修正 |
|---|---|---|
| "仅 9 表 FORCE ROW LEVEL SECURITY" | 本机 **73 表 FORCE** / 76 ENABLE 未 FORCE / 2 policy 空挂（attachments、candidate_failure_logs_columnar_old）/ 360 无 policy；252 同构 | 计数口径失实；FORCE 面远大于记载 |
| "RLS 对应用自身空操作（owner 绕过）" | 应用角色 `llm_gateway` 在**本机与 252 均为 superuser + BYPASSRLS**——superuser/BYPASSRLS 无条件绕过 RLS，FORCE 也拦不住；根因比 owner 绕过更深 | RLS 全库对应用连接零约束的真正根因是**角色特权**，不是（仅仅是）owner 身份；据此设计 §三 |

## 二、发现与处置

### P1

| # | 发现 | 证据 | 处置 |
|---|------|------|------|
| P1-1 | **管理员"重置凭据成功率"静默 no-op**：DELETE 谓词读 `app.current_role`/`app.current_tenant`，但跑在裸池连接（无事务无 GUC），current_setting 恒 NULL → 恒零行删除；注释声称 GUC 由 apihub.withTenantTx 设置，该 handler 根本不经过它。管理员点重置 → 返回成功且展示"新成功率"，实际失败样本未清 | admin/credential_success_rate.go:135-145（子代理 A 线索，主代理亲读全链 + 路由鉴权面 wrapAdmin 核实） | 删除逻辑抽 `resetCredentialSuccessRateRows(ctx, txBeginner, ...)`：withTx + setAllTenantGUC（super_admin/bypass GUC 事务内成对设置）+ 保留原谓词作二道防线。钉桩 ×2（pgxmock 严格期望：Begin→set_config×2→DELETE→Commit 顺序契约 + 谓词 GUC 分支形状） |

### P2

| # | 发现 | 证据 | 处置 |
|---|------|------|------|
| P2-1 | **RLS 租户隔离架构级缺口（R37 遗留#1）**：全库 199 policy 对应用零约束（根因见 §一）；policy GUC 词汇碎片化——租户 GUC 两族（`app.tenant_id` ~160 条 policy **零设置点**、`app.current_tenant` 覆盖良好）、超管 GUC 三种（current_role/bypass_rls/is_super_admin，后者零设置点）；`app.current_user`（sessions owner_filter 系）零生产设置点；声明面漂移（objects/ 122 文件 vs live 199 policy/151 表） | 活库普查（pg_policy/pg_class/pg_roles）+ GUC 设置面全仓扫描报告（亲读复核载荷点：credential_success_rate/516 policy/V371 注释/apihub RESET/ScopeRunner 零生产调用） | **架构设计定稿** docs/design/rls-tenant-isolation-architecture.md：角色拆分（llm_gateway 降权 NOSUPERUSER/NOBYPASSRLS 保留 owner 供 ensure DDL + 新增 llm_gw_maint NOLOGIN BYPASSRLS 维护角色）+ GUC 唯一正典（app.current_tenant + get_current_tenant()，超管 OR 双分支标准形）+ 四阶段路线（Phase1 GUC 统一补齐→Phase2 降权总闸→Phase3 分批 FORCE→Phase4 声明对账）。关键设计事实：降权精确激活且仅激活 73 张 FORCE 表（ENABLE 未 FORCE 表 owner 旁路维持原行为），FORCE 即分批开关，回退 = 一条 ALTER ROLE。**本轮落地 Phase1 代码部分**（P1-1 修复 + rls_helper 死代码删除），policy 统一迁移与 durable GUC 补齐留后续轮 |
| P2-2 | **ensure 冗余索引根治（R37 遗留#2）**：db.go/db_omnifree.go ensure 创建的 9 个约束影蔽索引（applications/licenses/offline_activation_requests/releases/center_commands/instance_status_reports/keyless×2/free_resource_catalog）718 不敢 drop（每进程启动复活）；request_logs 父表 idx_request_logs_parent_ts ↔ idx_request_logs_parent_request_id 跨通道所有权冲突对（**两父索引各挂 5 attached 叶子，每张月度叶子同列组合双份索引存储**，亲证 pg_inherits）；tool_usage_stats_hot 三对 ASC/DESC 近重复 | 子代理 B 底账 + 主代理逐条亲读（9 约束影蔽判等复核 + 活表列集/查询面核实） | **迁移 719**（真库实跑后定稿）：A 段 drop 9 个影蔽索引（与 db.go/db_omnifree.go 同 commit 删除 ensure 创建者联动，根治复活循环）；B 段 drop idx_request_logs_parent_request_id 整棵分区索引树（**parent_ts 通道胜出**：ensure+013+三份 baseline 三点持有）；C 段 drop tool_usage_stats_hot LIKE 继承 ASC 三件（保留 348 显式 DESC 三件；查询面亲核=等值/范围+单列 ORDER BY，backward scan 双向可服务，无混合方向多列排序；toolexecution 的 tool_name/date 查询与活表列集不符且表空=死路径） |
| P2-3 | **252 迁移账本假登记**：schema_migrations 含 717@09:29 / 718@14:54（2026-09-17），但 **717 效果未落库**（agent_name/agent_type 仍 text，十列中 2 列漂移；时间早于本机加固版 09:54，疑似跑的是 R36 原版、42883 中止仍记账）；718 抽查全真实生效（3 补索引在 + 3 drop 索引不在）。**717 是唯一假登记**（修正过程：本轮先以错名误判 718 部分生效，正确名复检后更正——教训#3 现场再应用） | 252 只读：information_schema 十列复核 + 718 六名抽查 + 账本时间线 | 登记遗留 runbook（§五），不在本轮触生产 |

### P3

- freeresource/rls_helper.go 死代码删除（裸 SET 会话级 GUC + 无 RESET 配对的连接池污染面，全仓零非测试调用方亲证）：源文件删除，测试文件瘦身改名 quota_tracker_rls_test.go（保留 escapeTenant 用例与活集成测试）
- toolexecution/postgres_store.go 的 tool_usage_stats_hot 查询列（tool_name/date/total_calls）与活表列集（tool_id/usage_date/call_count）不符且表空——死路径，登记遗留待产品定夺（删除或修列名）

## 三、核实为健康的关键面

- **GUC 事务边界总体良好**：全部非测试 set_config 第三参 is_local=true；SET LOCAL 均在显式事务内；租户 ID 白名单转义（escapeTenant [A-Za-z0-9_-]{1,64}）；apihub 是唯一带 RESET 的实现（其注释记载 set_config 残留教训）
- **admin/worker GUC 设置面覆盖**：admin/tenant_ctx.go（withTenantTx/withAllTenantTx，~45 调用点）+ 后台 worker bypass_rls 成对设置（observation_outbox/anomaly_harvester/supplier_error_logger/session v2 reaper/partition_manager 等）——Phase2 降权的基本盘已在
- **719 在本机真库全绿**：13 drop 一次通过、复活扫描（新代码 ensure 链跑后 13 名零复活、parent_ts 5 叶完好、parent_request_id 叶族清零）、pg_is_in_recovery=false（无 R37 式 CONCURRENTLY 崩溃）
- **252 环境体量**：717 面仅 2 列同族漂移（text→varchar）+ hot 8373 行（ALTER 秒级）；713 面 session_turns 219,927 行/5 分区（ALTER TYPE 秒级~十秒级，ACCESS EXCLUSIVE 低峰可承受）；盘 78G 可用/内存 6G 可用

## 四、测试与验证（真实实测）

```
go build ./...                                                          # clean
go vet ./db/ ./admin/ ./domains/freeresource/ + installer 模块           # clean
go test ./db/ -count=1                                                  # ok
go test ./admin/ ./domains/freeresource/ -count=1                       # ok（66.7s / 0.2s）
installer 模块 go build + go test ./cmd/llm-gw-installer/ ./internal/dbinit/  # 全绿（含 719 五点同步双向对账）
bash scripts/apply-db-revision-sequence_test.sh                        # passed（719 登记）
gofmt -l 本轮 9 触碰文件                                                # 清零
具名钉桩：
  TestResetCredentialSuccessRateRowsRunsInsideSuperAdminGUCTx          PASS（顺序契约：DELETE 必在 GUC 事务内）
  TestResetCredentialSuccessRateRowsKeepsGUCGuardPredicate             PASS（谓词 GUC 分支形状）
  TestEscapeTenant_InvalidFallback                                     PASS（转义白名单）
真库实测（本机 llm_gateway@127.0.0.1:5432）：
  apply-db-revision-sequence.sh 全序列跑通（仅 719 新账）
  719：13 个 drop 全部落库；复活扫描 = 新代码 `gateway migrate` 跑完整 ensure 链后
       13 名零复活、idx_request_logs_parent_ts 5 attached 叶完好、
       parent_request_id 叶族（..._ts_idx1 ×5）清零
252 只读实测（pg-252-pg17 容器内 psql）：十列复核/718 六名抽查/账本时间线/角色属性/盘内存
```

## 五、遗留登记（252 部署前置 runbook + 后续轮）

1. **P2 | 252 717 假登记修复（运维窗口）**：252 schema_migrations 已有 717 行但效果未落（agent_name/agent_type 仍 text）。执行序：①低峰窗口经隧道/容器内 psql 直接执行**现行加固版** 717 文件体（逐列守卫幂等，已对齐列自动跳过，仅动 2 列 + 视图捕获重建）②information_schema 复核十列 0 漂移 ③无需账本手术（行已存在，效果补齐即可）。前置注意：252 docker 实为 podman、DDL 前 df 查盘（现 78G 可用）
2. **P2 | 252 719 执行**：随下一次部署走 revision-sequence 通道即可（非分区表 CONCURRENTLY 标准形态；252 无本机 CONCURRENTLY 崩溃史，安排可回退窗口）；713（session_turns 220K 行/5 分区）同窗口低峰评估
3. **P2 | RLS Phase 1 剩余项**（按设计 §五）：policy 词汇统一迁移（app.tenant_id→get_current_tenant() 等）、durable 族 store 补 GUC、attachments/cfl_columnar_old 补 ENABLE、census 守卫测试
4. **P3 批**：toolexecution 死路径定夺；llmgw.admin_override 触发器不可达分支；R37 §四 P3 批全部顺延（本轮未触碰）
5. **运维事实**：本机网关容器（8782）仍在跑旧二进制——其 ensure 含已删的 9 个 CREATE INDEX，**重启会复活 9 索引直到部署新二进制**；本机下次 deploy-local 时自然收敛（复活是暂态，719 账本已防重放，deploy 后再跑一次复活扫描确认）

## 六、下一轮提示词（建议）

> 以 docs/audit/2026-09-17-r38-rls-design-ensure-index-root-round.md §五 为起点：首位 **252 运维窗口执行**（遗留#1：加固版 717 文件体直跑 + 十列复核归零；#2：719/713 随部署低峰）；其次 **RLS Phase 1 剩余项**（设计文档 §五 Phase1：policy 词汇统一迁移 720 + durable 族 GUC 补齐 + 空 ENABLE 补齐 + census 守卫，全部走"真库实跑后定稿"纪律）。动迁移前 git fetch 核对远端编号（当前已至 719）；五点同步 + apply-db-revision-sequence_test.sh 双门禁必跑。审计入口：docs/audit/playbook/orchestrator-prompt.md（下一轮 R39）。
