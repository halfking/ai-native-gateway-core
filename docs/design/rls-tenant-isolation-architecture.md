# RLS 租户隔离根治架构设计（owner/superuser 绕过消除）

- 状态：设计定稿（R38，2026-09-17）；实施分四阶段，见 §五
- 起点登记：R37 §四遗留 #1（"RLS owner 绕过是架构级缺口，不可盲修，需产品级设计"）
- 证据基线：本机活库（llm_gateway@127.0.0.1:5432）与 252 真库只读普查（2026-09-17），GUC 设置面全仓扫描（R38 子代理报告 + 主代理亲读复核）
- 关联：[[docs/audit/2026-09-17-r37-sql-focused-audit-round.md]] §四 #1

## 一、问题与根因（对 R37 记载的两处修正）

R37 记载："全库 510 表 owner=llm_gateway（应用连接角色）而仅 9 表 FORCE ROW LEVEL SECURITY——~40 张租户隔离表的 RLS 对应用自身空操作"。

R38 真库普查修正两点：

1. **FORCE 面远大于记载**：本机 511 张 public 表中 73 张已 FORCE RLS、150 张 ENABLE（含 73 FORCE）、76 张 ENABLE 未 FORCE、2 张 policy 空挂（attachments / candidate_failure_logs_columnar_old，policy 存在但 RLS 未 ENABLE）、360 张无 policy。"仅 9 表"为误记。
2. **根因比 owner 绕过更深**：应用连接角色 `llm_gateway` 在**本机与 252 均为 superuser + BYPASSRLS**（pg_roles.rolsuper=t, rolbypassrls=t）。superuser/BYPASSRLS 角色**无条件绕过 RLS**，连 FORCE 也拦不住。因此现状不是"73 张 FORCE 表已受保护"，而是**全部 149 张带 policy 的表对应用连接零约束**——FORCE 与否在 superuser 面前没有区别。

推论：**角色降权（去 superuser + 去 BYPASSRLS）是唯一的总闸**。降权后：
- 73 张 FORCE 表：RLS 立即对应用生效（FORCE 的语义 = 约束 owner）
- 76 张 ENABLE 未 FORCE 表：owner 旁路仍然生效（owner 仅被 FORCE 约束），行为不变
- 即降权这一步**精确激活且仅激活** FORCE 面——FORCE 天然就是分批开关

## 二、现状盘点（实测事实）

### 2.1 角色面

| 角色 | 属性 | 说明 |
|---|---|---|
| llm_gateway | superuser + BYPASSRLS + 表 owner（510/511） | 应用连接角色（本机与 252 同） |
| postgres | superuser | 容器内置 |
| memora_runtime / memora_admin / platform_migrator | BYPASSRLS | 同库其他产品 |
| gateway_rls_test | 普通登录角色 | 历史测试残留 |

无任何专用的"应用非特权角色"或"维护 bypassrls 角色"。

### 2.2 Policy 表达式的 GUC 词汇碎片化

199 条 live policy（151 张表）按谓词形态去重后的 GUC 依赖：

| GUC | policy 数 | 设置面（Go 侧） | 风险 |
|---|---|---|---|
| `app.current_tenant`（直读或经 `get_current_tenant()`，后者回退 `'default'`） | ~90 | **覆盖良好**：admin/tenant_ctx.go（约 45 调用点）、apihub/pg_store.go、domains/hostedtask/store.go、freeresource 系、OmniFree 计量、审批队列等，全部 `set_config($1,$2,true)` 或事务内 `SET LOCAL` | `get_current_tenant()` 的 `'default'` 回退 = 忘设 GUC 时静默看到 default 租户行（fail-open）；裸 `current_setting` 无回退 = 零行（fail-closed）。两种形态并存 |
| `app.current_role = 'super_admin'` | ~57 | admin withAllTenantTx + 后台 worker（reaper/aggregator/scan 等），set_config is_local=true | 与下项语义重复 |
| `app.bypass_rls = 'true'` | ~57 | 全部后台跨租户 worker（observation_outbox、anomaly_harvester、supplier_error_logger、session v2 reaper、partition_manager 等） | 与上项语义重复 |
| `app.tenant_id` | ~160（V371 supplier_errors 系 + 早期 policy） | **全仓零设置点**（V371:88 注释自认，靠 bypass_rls 分支兜底） | 词汇弃用候选：policy 应统一读 `get_current_tenant()` |
| `app.current_user` | sessions/session_turns/session_dim/session_turn_snapshots 的 owner_filter | **全仓零生产设置点**（仅测试） | 见 §四 D5 决策点 |
| `app.is_super_admin` | baseline 遗留 2 条 | 零设置点 | 已被 ensure 重建版覆盖，清残留即可 |
| `app.actor` / `app.current_admin` | 审计触发器（非 RLS） | 各有唯一设置点（admin/routing.go:1436、setPolicyTxGUCs） | 健康，不在本设计范围 |
| `llmgw.admin_override` | trg_cmb_protect_manual_disable 触发器 | 零设置点（override 分支不可达） | P3 顺带清账 |

### 2.3 GUC 无覆盖面（降权前必须补齐，否则降权即断流）

1. **supplier_errors_hot / supplier_errors / supplier_error_stats**（V371，已 FORCE）：policy 主分支读 `app.tenant_id`（零设置点），全链路依赖 `app.bypass_rls` 旁路——writer（executors/supplier_error_logger.go:50）、聚合（bg/supplier_error_stats_aggregator.go:384）、分区轮转（bg/partition_manager.go:1089）都设了 bypass，**当前安全**；但 admin 读端若走非 bypass 路径会零行。
2. **durable_llm_tasks / durable_llm_task_events / durable_pending_outbox**（516，ENABLE 未 FORCE）：policy `tenant_id = current_setting('app.current_tenant', true)` **无回退**；durable store（store.go / store_claim.go / store_terminal.go / settlement_outbox.go）**全部不设 GUC**。该族当前靠 owner 旁路活着；**若在补 GUC 前 FORCE，写入自身 INSERT 即被 WITH CHECK 拒绝**。
3. **sessions 族 owner_filter**（app.current_user 零设置点）：见 §四 D5。
4. **admin/credential_success_rate.go:135** 的 DELETE 谓词读 `app.current_role`/`app.current_tenant`，但跑在裸池连接（无事务无 GUC）→ **恒 no-op**（R38 已确认的管理员"重置成功率"静默失效缺陷，本轮修复）。
5. **freeresource/rls_helper.go**：裸 `SET`（会话级、无 RESET）污染连接池——经查为**死代码**（仅测试引用），应删除而非修复。

### 2.4 声明面漂移

live 199 policy / 151 表 vs `sql/objects/policies/` 122 文件。约 2/3 的 live policy 不在声明式管理内（多为 ensure 链与各迁移通道内联创建）。attachments 与 candidate_failure_logs_columnar_old 的 policy 空挂（RLS 未 ENABLE，policy inert）。

### 2.5 既有可复用机制

- **admin/tenant_ctx.go**：withTenantTx / withAllTenantTx（current_tenant / current_role+bypass_rls 成对设置）——admin 面事实标准
- **apihub/pg_store.go**：唯一带 `RESET app.current_tenant` 的实现（commit 后显式 RESET，注释记载 set_config 残留教训）
- **internal/dbx ScopeRunner**（WithTenantTx/WithSuperAdminTx，GUC 约定注入 manifests）：通用框架**生产零调用**（Phase 3 pilot shadow-read 阶段）——角色降权后的统一收口候选
- **internal/modelpolicy/checker.go:265**：`SET LOCAL row_security=off` 已存在（modelpolicy 专用路径）

## 三、目标架构

### 3.1 角色拓扑（目标态）

| 角色 | 属性 | 用途 |
|---|---|---|
| `llm_gateway`（保留名） | **NOSUPERUSER NOBYPASSRLS**，继续 own 全部表 | 应用连接 + ensure 链 DDL（DDL 不受 RLS 约束，owner 身份保留以确保 `CREATE INDEX IF NOT EXISTS` 等幂等 DDL 可跑）+ 受 FORCE RLS 约束的 DML |
| `llm_gw_maint`（新增） | NOLOGIN + BYPASSRLS + 对全 schema 的必要 GRANT | 运维/紧急修数：`SET ROLE llm_gw_maint`（psql / 维护作业）。日常不可登录，杜绝长期旁路会话 |
| `postgres` | 不变（容器底座，部署面管理） | 仅容器管理 |

关键取舍：**不把表 owner 转给独立角色**。理由：db.go ensure 链（约 72 个 ensure，每进程启动执行）以应用连接跑 DDL，owner 身份是幂等 DDL 的前提；FORCE 已提供对 owner 的 RLS 约束，分批 FORCE 即分批获得保护，无需 owner 迁移这步高风险操作。

### 3.2 GUC 词汇规范（唯一正典）

| 用途 | 唯一 GUC / 形态 | 说明 |
|---|---|---|
| 租户 | `app.current_tenant`，policy 侧统一经 **`get_current_tenant()`**（`'default'` 回退） | 保留 default 回退的理由：现网 request_logs 等表的存量单租户数据全部 tenant_id='default'（本机 375087 行实证），回退保证降权后忘设 GUC 的路径仍能看到 default 租户（连续性优先）；fail-closed 硬化（去回退）列为 Phase 4 可选项，须先给全部路径补齐显式 GUC |
| 超管/跨租户 | `app.current_role = 'super_admin'` **或** `app.bypass_rls = 'true'` | 双形并存（57 条 policy 既成事实）；新 policy 一律写全 `OR` 两分支的标准形 |
| 审计 | `app.actor` / `app.current_admin`（触发器用，非 RLS） | 不变 |

**policy 标准形模板**：

```sql
CREATE POLICY <name>_tenant_isolation ON <tbl> USING (
    tenant_id = get_current_tenant()
);
CREATE POLICY <name>_super_admin_bypass ON <tbl> USING (
    current_setting('app.current_role', true) = 'super_admin'
    OR current_setting('app.bypass_rls', true) = 'true'
);
```

需重写的词汇：`app.tenant_id` → `get_current_tenant()`（supplier_errors 系及早期 policy）；`app.is_super_admin` 残留清理；`llmgw.admin_override` 触发器分支定夺。

### 3.3 分批 FORCE 的开关语义

- **降权 = 一次性激活现有 73 张 FORCE 表**（且仅激活它们；76 张 ENABLE 未 FORCE 表靠 owner 旁路维持原行为）
- **此后每张表的保护进度 = 该表何时 FORCE**。逐表 FORCE 前置条件 = 该表全部查询路径（admin 读、网关写、后台 worker、迁移回填）的 GUC 覆盖审计通过
- 回退：`ALTER ROLE llm_gateway SUPERUSER BYPASSRLS` 一条 SQL 即刻回到现状（总闸带回退闸）

## 四、决策点登记

- **D1 降权范围**：`llm_gateway` 全局降权（无按表粒度）。降权门槛 = §2.3 清单全部补齐 + 73 张 FORCE 表逐表路径审计。若个别表审计不过，临时手段是 `ALTER TABLE ... NO FORCE`（回到 owner 旁路）而非推迟全局降权。
- **D2 ensure 链与降权的关系**：DDL（CREATE/ALTER）不受 RLS 约束，owner 身份 + ensure 幂等 DDL 在降权后不变。已验证 db.go ensure 链仅做 DDL + 少量目录查询（SELECT 不触 RLS 表的业务行）。**降权后必须回归验证 ensure 链全量跑通**（新装 + 存量两路径）。
- **D3 迁移执行角色**：startup SQL 通道（apply-db-revision-sequence.sh）与 gateway migrate 继续用 `llm_gateway`（owner）执行；其中含 DML 的迁移（backfill 类）在降权后将受目标表 RLS 约束——**含 backfill DML 的新迁移必须在事务首句设 `SET LOCAL app.bypass_rls = 'true'`**（R37 outbox reaper 已是此模式，定为规范）。
- **D4 apihub RESET 先例推广**：所有经池连接执行、且在事务外设置过 GUC 的路径，必须配对 RESET（现仅 apihub 这么做）。is_local=true 的 set_config 事务结束自动回收，无需 RESET——审计确认全部生产 set_config 第三参为 true（仅 freeresource 死代码为 false 裸 SET，删除即可）。
- **D5 sessions 族 owner_filter（app.current_user）**：产品语义未定（"用户只能看自己会话"还是"租户内全可见"）。选项 a) 会话读写路径设置 app.current_user（对齐 owner_user 列）；b) 删除 owner_filter policy（承认租户内共享）。**Phase 3 sessions 批次 FORCE 前必须先决**，本轮不定。
- **D6 空 schema 声明漂移**：以活库为准出 census 报告（本设计 §二数据即出该报告），Phase 4 决定"补声明文件"还是"豁免清单"。

## 五、实施路线（四阶段）

### Phase 1 — GUC 统一与覆盖补齐（零行为变化，superuser 时期全部 dormant）

1. 迁移：policy 词汇统一（`app.tenant_id` → `get_current_tenant()`；清 `app.is_super_admin` 残留；补 bypass 分支使全库 policy 收敛到 §3.2 标准形）
2. 代码：credential_success_rate DELETE 改走 withAllTenantTx 事务（R38 已修）；删除 freeresource/rls_helper.go 死代码；durable store 写入/claim/terminal/settlement 事务补 `SET LOCAL app.current_tenant`（worker 路径补 bypass）
3. 空 RLS 补 ENABLE：attachments、candidate_failure_logs_columnar_old（policy 已存在；ENABLE 后 superuser 时期无行为变化，为降权做准备）
4. 钉桩测试：policy 词汇 census 守卫（防新 policy 再用弃用 GUC）

### Phase 2 — 角色降权（总闸，一次性）

1. 前置：73 张 FORCE 表逐表路径审计清单全绿（输出 = 每表一行：admin 读 / 网关写 / worker / 回填 四路径 GUC 状态）
2. 执行（低峰窗口，两环境分别）：
   ```sql
   CREATE ROLE llm_gw_maint NOLOGIN BYPASSRLS;
   GRANT USAGE ON SCHEMA public TO llm_gw_maint;  -- + 逐表 GRANT SELECT/UPDATE 救援面
   ALTER ROLE llm_gateway NOSUPERUSER NOBYPASSRLS;
   ```
3. 回退预案：`ALTER ROLE llm_gateway SUPERUSER BYPASSRLS`（即刻生效，无重启）
4. 观察面：42501/空结果告警、admin 各列表页抽查、后台 worker lag
5. installer 对应：新装路径直接建降权角色 + maint 角色（embeddata 变更 + 全新安装回归）

### Phase 3 — 分批 FORCE（逐域推进）

批次建议（每批 = GUC 覆盖证明 + FORCE + 观察期 ≥ 1 天）：
1. durable 族（Phase 1 补完 GUC 后）
2. apihub 表族（RESET 先例最完备）
3. sessions 族（前置 = D5 决策）
4. 其余 ENABLE 未 FORCE 的 76 张按域分批
5. 每 FORCE 一批，census 报告更新（目标：全库租户表 100% FORCE）

### Phase 4 — 声明面对账与硬化

1. objects/ ↔ live policy census 对齐（补文件或豁免清单）
2. fail-closed 硬化评估（去 'default' 回退）——须全路径显式 GUC 后
3. ScopeRunner 收口评估（替代散落的内联 set_config）

## 六、验收标准

- Phase 1 完成即验收：活库 census 中弃用 GUC（app.tenant_id/app.is_super_admin）policy 数 = 0；credential_success_rate DELETE 实测可删（真库集成测试）
- Phase 2 完成即验收：`SELECT current_user, row_security_active` 场景下 admin 列表/网关请求/worker 全链路冒烟通过；ensure 链全量跑通
- 终态验收：以 `gateway_rls_test` 类普通角色 + 未设 GUC 连接查询任意租户表 → 零行；设 GUC 后仅见本租户行；bypass GUC 会话见全量

## 七、与 R38 实施的衔接

本轮（R38）落地：本设计文档 + Phase 1 第 2 项中的 credential_success_rate 修复与 rls_helper 死代码删除。Phase 1 其余项（policy 统一迁移、durable GUC、ENABLE 补齐）按上述方案在后续轮次逐项落地，每项走"迁移真库实跑后定稿"纪律。

**Phase 1 完成标注（R40，2026-09-18）**：四项全部落地——①迁移 720 policy 词汇统一（f5328e13c 并行会话落 embeddata 单点，R40 补齐五点同步 + 通道真库实跑 + marker 登记，public 弃用 GUC policy = 0）；②durable 族 store GUC（durable/rls.go：前台写 setLocalTenantGUC、worker 17 路径旁路双 GUC，autocommit 单语句读写包显式事务）；③迁移 723 ENABLE attachments/candidate_failure_logs_columnar_old（原编 721 撞号重编，五点同步 + 真库 rls=true）；④census 守卫（db/rls_policy_census_test.go：静态双源——startup 迁移 .up 文件 + Go ensure CREATE POLICY 窗口扫弃用 GUC，.down 豁免；活库 census 走 TEST_DB_URL opt-in，§六 验收口径）。附带发现并修复：516 中间形态漂移库的 durable 家族缺表缺列（722 收敛迁移，真库落地）。§六 验收：活库 census 弃用 GUC policy 数 = 0 ✅（本机 llm_gateway 实测）。Phase 2 前置中的"73 张 FORCE 表逐表路径审计"仍待专项轮；D5（sessions 族 owner_filter 产品语义）仍未决。
