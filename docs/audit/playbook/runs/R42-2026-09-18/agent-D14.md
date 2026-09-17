# D14 安全(并发锁/资源竞争/泄漏/溢出/网络可靠性/密钥) 子代理报告(窗口:0a015af51^..b75c91900)

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P0**(全新安装阻断) | **723 对 candidate_failure_logs_columnar_old 的裸 ALTER 无 to_regclass 守卫,该表不在迁移链创建面上**。仅由旧 deploy 链 V359 在"原表是 columnar AM"条件下 rename 产生(252 血统库),normalize-columnar-historical.sql 瞬态产生且必 DROP;startup 迁移与 01-schema 均不创建。全新安装走 StartupFiles 到 723 报 42P01 → InitSchema 整体失败。与 720 修掉的同一类 42P01 部署阻断完全同型;252 存量库已实跑通过,故漏了 fresh-install 路径 | sql/migrations/startup/723_...sql:17(down:5 同);deploy/sql/migrations/V359:56;sql/fixes/normalize-columnar-historical.sql:53-61;installer/internal/dbinit/runner.go:171,207-212;对照 720 头注 :29-34 | 照抄 720 的 per-table to_regclass 守卫(up+down);补 fresh-installer 断言钉桩 |
| 2 | P2(守卫盲区) | **census 三驾守卫的静态两驾不覆盖 baseline/objects 面**:01-schema 与 sql/objects/policies/ 仍持有弃用词汇,静态扫只扫 sql/migrations/startup/*.sql 与 db.go/db_omnifree.go 两文件。pg_dump 段序回灌 policies 段会静默回归,唯一净网是 opt-in 活库 census;ensure 扫描按文件名钉死;policyWindows 大小写敏感 | db/rls_policy_census_test.go:85-115、:28;sql/schema/01-schema.sql:29872-29889;sql/objects/policies/*.sql:5 | 把 sql/schema、sql/objects/policies 纳入静态扫(或生成物豁免);ensure 扫描改目录遍历 |
| 3 | P3(文档漂移) | 720 头注释"57+2=59"总数正确,但 knowledge 分组注释写 "8 tables" 实际 9 块 | 720_...sql:258 vs :259-356 | 顺手改注释 |
| 4 | P3(头注释事实性) | 723 头注释称两表均 "relrowsecurity=false";252 血统库的 _columnar_old 在 V359 rename 前已由 052 ENABLE(rename 保留 relrowsecurity),该半句对目标库可能不成立(ENABLE 幂等无害) | 723_...sql:10-13;对照 052:22-24、V359:56 | 复核活库后修订注释 |
| 5 | P3(接口口径) | refresh-balance 把 QueryRow 的任何错误(含连接池耗尽/超时)统一回 404 "credential not found",基建故障被误报为资源不存在 | admin/provider_credential_balance.go:62-66 | 区分 pgx.ErrNoRows 与其他错误 |

## 二、核实为健康的面

- **720 的 59 条 policy 重写:DROP/CREATE 严格成对,无删后漏建**;OR/PERMISSIVE 语义与旁路分支无权限放大(供应商旁路两 GUC 同为无特权 custom GUC,同信任域;superuser 时代 dormant);省略 WITH CHECK 语义等价。
- **723 的 policy 存在性前提成立**(在表存在的库上):attachments policy 在 baseline;_columnar_old 经 rename 继承 052 canonical policy。#1 的问题是"表本身缺席"而非 policy 缺席。
- **durable/rls.go 旁路 GUC 不可能泄漏到事务外**:三参 is_local + Begin→defer Rollback(WithoutCancel)→Commit 三段式;17 个调用点均处显式事务内;包内无裸 SET app.*;空租户拒绝在位。
- **refresh-balance 出网探测面**:路由挂 providerConsole,tenant_admin 被 POST 白名单拒 → 实际仅 super_admin 可触发,SSRF 的 URL 决策者与触发者同信任级;EgressBlocked 拒非 http(s)/缺 host/link-local/CGNAT;**响应体不落 balance_error**(GET-only、10s+12s 双超时、LimitReader 4096、失败仅返回 bool,写入的是固定格式串)→ 无日志注入/敏感泄漏面;truncateBalanceError rune 边界护栏在。
- **mDNS TXT 无凭据**:仅 version/api/proto 三项;默认关闭;绑定失败降级;Stop 幂等/double-close 安全;局部 stopCh 拷贝;QueryContext+TRUST BOUNDARY+SPN 后缀剥离;实例名端口后缀。
- **SQL 注入面**:窗口内新增 SQL 全参数化;R41 Sprintf 修复把含裸 % 的片段改走参数位,值仍走 $n,带回归测试。
- **窗口内新增并发原语无死锁对/无泄漏点**:diff 增点仅 atomic.Int64/atomic.Bool/mutex/WaitGroup;新增 goroutine 均有界且退出闭合;无双 shutdown 上下文。
- **721 五点同步齐**(embeddata/runner/migrations/截断约定/CHECK 幂等)。

## 三、未覆盖项与原因

活库 census/_columnar_old 实际 relrowsecurity 验证(无凭据);fresh-install 42P01 实复现(建议一次性 docker PG);-race 三门留给修复轮;balance_error 文本写入未穷尽追踪;installer 字节等价守卫未实跑(静态确认存在)。
