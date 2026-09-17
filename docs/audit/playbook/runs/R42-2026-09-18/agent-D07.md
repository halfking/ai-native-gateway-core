# D07 hot+columnar 分区存储 子代理报告(窗口:0a015af51^..b75c91900)

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P0/P1**(定义触发路径上部署硬失败) | **723 对 candidate_failure_logs_columnar_old 的裸 ALTER 无 to_regclass 守卫 → canonical 链库 42P01**。该表唯一创建者是 deploy 链 V359 的条件 RENAME(且仅当原表 amname='columnar' 才 rename),canonical startup 链 / 两份 01-schema 均无创建者。触发路径 A:canonical 链存量库走 apply-db-revision-sequence.sh → 42P01 中止整个序列,后续 revision 全部堵死;触发路径 B:installer 全新安装 → InitSchema 失败 | sql/migrations/startup/723:17(裸 ALTER);创建者唯一性:deploy/sql/migrations/V359:47-64(rename 条件 :55-62);应用通道:scripts/apply-db-revision-sequence.sh:10,462 与 installer/internal/dbinit/runner.go:207-213,232 | 照抄 720 的 D+1 修复模式(to_regclass DO 块,down 同修),五点同步重刷 embeddata;守卫补上后在 canonical 形态真跑一次再定稿 |
| 2 | **P1** | **722 的"全新安装零变化"前提在 installer 通道不成立:durable_llm_tasks 在 installer 全链无创建者**。722:66 ALTER、:50 REFERENCES;唯一创建者 516 不在 StartupFiles(runner 只收 515 没收 516),embeddata/01-schema 零命中,Go ensure 链零命中。全新装到 657 即 42P01 中止(657 断点为窗口前遗留);即使跳过 657,722 同样炸 | sql/migrations/startup/722:50,66;516:22(唯一创建者);installer/internal/dbinit/runner.go:33,99,171(515 在、516 不在,657/722 在);657:20 | 516 补进 StartupFiles(或 01-schema 收编),722 对 durable_llm_tasks 的 ALTER 加守卫;一并在 canonical/installer 形态实跑定稿 |
| 3 | **P2**(Phase 2/3 前置风险,窗口内零行为变化) | **分区母表 policy × PG 版本拓扑风险**:720 重写的 tenant_isolation_supplier_errors 落在声明式分区父表上。语句本身安全(真库实跑+每段 to_regclass 守卫);但 installer 生产栈为 citus:11.3.0(可能 PG14)、quickstart 钉 postgres:14-alpine——**PG15 之前父表 RLS policy 不经父查询下推到分区**,Phase 2 降权 + Phase 3 FORCE 后,hot/columnar 各分区母表在 PG<15 上经父表访问将不生效隔离,除非逐分区 policy 或升 PG15+。分区轮转 worker 已设 bypass 故后台路径不受降权影响 | 720:618-642;699:38;installer/cmd/llm-gw-installer/main.go:858;docker-compose.quickstart.yml:9 | 记入 RLS 设计 §五 Phase 2/3 前置条件:对 kx-citus 真机 SELECT version() 钉桩;若 PG14,FORCE 前出逐分区 policy 方案或升镜像 |
| 4 | P3 | 723 down 同样无守卫,canonical 链库回滚场景同样 42P01;与 #1 同修 | 723.down:5 | 随 #1 加守卫 |

## 二、核实为健康的面

- **8h hot 保留/批量 promote/分区轮转 worker 窗口内零触碰**:storage_retention_worker/partition_manager/columnar_invariant_check/dbx 零 commit;DefaultRetentionWindow=8h 未动。
- **durable 722 与分区管理器零交集(确认)**:grep 零命中;durable 三表普通 BIGSERIAL 表无分区结构。
- **supplier_errors_hot × 720 交互安全**:720 有 to_regclass 守卫,policy 名与 deploy 链原名一致;supplier_errors_hot 唯一 canonical 创建者 703,父/热两表分写 policy 无跨层遗漏。
- **720 依赖与幂等**:59 段全有守卫;get_current_tenant() 在 001 定义;分区父表上的 DROP/CREATE 合法(V371 先例真库验证)。
- **723 attachments 半边安全**:表+policy 在 canonical 01-schema 均存在,普通表无分区拓扑问题;cfl_old 是单张 citus_columnar 表非分区父表,ENABLE RLS 无父/子继承问题。
- **窗口写路径不变量**:settle worker 仅给 request_logs_hot LEFT JOIN 增列,无新增直写分区母表;721 纯 ADD COLUMN 幂等可重放。
- **720-723 五点同步字节一致**;census 守卫只扫词汇不查表存在性,拦不住发现 #1/#2。

## 三、未覆盖项与原因

fleet 内是否真实存在"canonical 链、无 V359 血统"的存量库(需真库查血统,决定 #1 爆炸半径);kx-citus 真实 PG 小版本与 installer 全新装自 657 以来成功先例(需真机);quickstart schema 初始化走向未完整追;GLOBAL_G2 读取属主代理轮文档固定动作。
