# R91 域 A 报告：SSOT 08:00 分区边界遗产专项（真库验证）

HEAD=ad34b2bc5，全部结论基于本次实际读取与一次性临时库（PG 17.10，`llm_audit_r31_904423` / `llm_audit_r31b_597076`，已 DROP）实测。审计日 2026-10-02。

## 一、发现（候选，待主代理复核）

**A1【高｜功能性】fresh install 通道对 08:00+08 遗产的自愈（687）依赖会话时区，UTC 会话下 14/14 全部静默漏检 —— 遗产在纯 installer 新装库上存活**
- SSOT 三镜像 md5 一致（`904f4b53…`）：`sql/schema/01-schema.sql`、`deploy/sql/schemas/baseline/01-schema.sql`、`installer/cmd/llm-gw-installer/embeddata/01-schema.sql`。
- 14 处 08:00+08 ATTACH 边界（sql/schema/01-schema.sql）：:19477/:19484（credential_model_index 07/08）、:19491/:19498（credit_ledger 07/08）、:19533（request_logs 2026_08）、:19561/:19568（request_wal 07/08）、:19575/:19582（routing_decision_log 07/08）、:19589（rdl_archive 2026_08）、:19659/:19666（tool_usage_stats 07/08）、:19673/:19680（usage_ledger 07/08）。另 :19519 model_probe_runs_2026_07 是 00:00+08 正典边界 + 列存（:9970 SET columnar / :9976 CREATE）。
- 列存遗产实测共 **8 个分区**（比轮 30 口径多 7 个）：cmi_2026_07/08、request_wal_2026_08、rdl_2026_07/08、rdl_archive_2026_08、usage_ledger_2026_08（均 columnar），加 model_probe_runs_2026_07。
- 687（`sql/migrations/startup/687_fix_473_partition_0800_bounds.sql:55`）指纹 `position('08:00:00' in v_bounds)` **无时区钉扎**。临时库实测（UTC 会话）：

```
credential_model_index_2026_08 | FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00') | fingerprint_687_hits = f
（14/14 全部 f）
```

  而 `docker-compose.yml` 与 installer PG 容器 env（installer/main.go:1749 envEntries）均无 TZ/PGTZ 设置，citusdata/citus 镜像默认 UTC → 纯 installer 新装库 687 是 no-op。811 头注（:45-47）自证「新 PG17 Docker 默认 UTC」。对照：811（811 文件 ：48）有 `SET LOCAL TIME ZONE 'Asia/Shanghai'` 钉扎，实测在 UTC 外层会话下仍正确重建 3 个目标分区。
- 后果链（临时库 B/A 实测）：遗产 2026_08 上界 08:00+08 存活 → ensure(2026_10/11) 成功（9 月被 PartitionManager 跳过，bg/partition_manager.go:341 只 ensure offset 0/1）→ `ensure_request_logs_partition('2026-09-15')` 报 `ERROR: partition "request_logs_2026_09" would overlap partition "request_logs_2026_08"`（42P17，credit_ledger 同）→ 9 月时间戳 INSERT 23514（无 default 表永久失败；request_logs 行落 default 后 promote 因同一 42P17 永久卡死）。

**A2【高｜功能性】SSOT fresh schema 在 HEAD 上无法独立应用——199c65747 引入的前向引用回归（阻断 A1 的修复路径）**
- 实测：空库 `psql -v ON_ERROR_STOP=1 --single-transaction < sql/schema/01-schema.sql` → `ERROR: relation "public.candidate_failure_logs_hot" does not exist`（:18499 `CREATE VIEW v_adaptive_probe_targets` 引用 hot 表，视图建时校验；hot 表由 392 建且 392 又反向依赖 schema 的父表，双向死锁）。6692b99f6（9-30）声称 fresh install exit=0，其后 **199c65747（2026-10-02 11:45，R21/814）** 重写该视图引入新前向引用。审计用 stub hot 表才完成分区验证。6692b99f6 修复类（视图/SQL 函数体前向引用）的工具链未防回归。
- 【主代理处置 2026-10-02】已修：SSOT 内联 candidate_failure_logs_hot 终态 DDL（显式 SET heap，防上游 columnar 作用域泄漏致 622 UPDATE 撞 ColumnarScan 的二次坑），三镜像 cp 同步，installer 注册守卫对 813/814 具名豁免入册；fresh-install e2e 红→绿（16.3s，一次性库已清理）。见轮文档 §二#2。

**A3【中｜功能×cosmetic 边界】687 自愈时丢弃列存 AM（811 保留）**
- 实测（临时库 B，+08 会话跑 687）：14/14 重建为 00:00+08 正典边界，但 8 个列存分区（含 rdl 正典单族列存 :14581 块）全部变 heap。811 有 `v_using`（:86）保留 AM，实测 rdl_2026_07/08 自愈后仍 columnar。687 路径触发的库上 `columnar_healthcheck/drift_report`（schema :967/:1015）将报 rdl 族 noncompliant（仅 past 空分区，影响低）。

**A4【低｜cosmetic】纯 installer 新装库（无序列通道）上 810/811/812/813/814 forward 永不到达**
- installer StartupFiles（installer/internal/dbinit/runner.go:37-578 + main.go:700-903）止于 809；embeddata/startup 277 文件中 810/811/812 仅 down；813/814 forward 不在 embeddata；序列通道 `scripts/apply-db-revision-sequence.sh:751-772` 含 808/810/811/812 但**不含 687**。即：installer-only 新装 = A1 存活 + 列存遗产全留；序列通道补跑 = 811 修 rdl/request_logs 两表（自钉扎，可靠），其余 5 表（cmi/credit_ledger/request_wal/tool_usage_stats/usage_ledger）在任何 UTC 会话通道下都无人修（687 是唯一覆盖者且漏检）。
- 【主代理处置 2026-10-02】813/814 已按「终态已烤 SSOT、一发修复不重放」具名豁免入册（注册守卫从红转绿）；810-812 原有同族豁免不变；687 缺席序列通道并入 A1 Owner 材料。

## 二、核实为健康的面

1. **正典 ensure 链时区安全**：schema 内 10 处 ensure 函数（:1508-:2159）体首 `SET LOCAL TIME ZONE 'Asia/Shanghai'` + 裸 date 字面量；实测 UTC 会话下 ensure(2026_10) 产出 `'2026-09-30 16:00:00+00'`（=10-01 00:00+08）正典网格。714/694 函数级钉扎补齐其余。
2. **Go ensure 触发面**：bg/partition_manager.go:239-242 启动即跑 + 24h tick；ensureSpecs（:1236-1281）覆盖全部 8 个受影响父表；:33-36 partitionTZ 钉扎派生 current/next 月。
3. **接缝闭合后的行为（临时库 B 实测）**：687（+08 会话）自愈后，`ensure(2026_09/10)` 全 7 表成功，21 分区边界无缝无叠（00:00+08 连续网格）；request_wal 缝窗插入 `2026-08-31 23:59:59+08`→2026_08、`2026-09-01 00:00:01+08`→2026_09 路由正确。
4. **「表示不一致 vs 时刻不一致」判定**：SSOT 遗产 `'2026-08-01 08:00:00+08'` 与 UTC 形式 `'2026-08-01 00:00:00+00'` 实测同一时刻（t）；与正典 `'2026-08-01 00:00:00+08'` 是**不同时刻**（f，差 8h）→ 遗产是**时刻级**错位（UTC 零点网格），非 cosmetic；一旦 ensure(+08 零点) 与之相邻即 42P17 实弹。
5. **808/812 行为**：808 建 default 成功且幂等；812 将 model_probe_runs_2026_07 转 heap 且边界时刻保留（实测量时刻不变）。
6. **缓解已生效面**：252 生产与本机 llm_gateway 库分别被 811（10-02）/687（9 月）治愈（迁移头注+台账）；当前/未来月写入在两场景下均正常。

## 三、未覆盖项与原因

1. 生产 252/本机 llm_gateway 真库直接查询未做（纪律禁止触碰既有库）；其现状采信迁移台账与 811/687 头注自述。
2. 705 repair 通道在 A1 场景下的完整 DETACH/ATTACH 演练未跑（推断受同一 42P17 影响，未实测）。
3. citusdata/citus:11.3.0 镜像内实际默认时区未启动该镜像验证（本地无该镜像运行实例）；「UTC 默认」采信 811 头注 + compose 无 TZ 配置的静态证据。
4. 811 第二遍跨月回灌（bak 含次月 8h 行）未构造数据实测（临时库分区全空，走 0 行通道）。

## 四、Owner 拍板材料（候选处置）

- **选项 1｜SSOT 重烤去遗产 + 修 A2 回归**：从已治愈库重 dump 三镜像（边界 00:00+08、model_probe_runs heap、补 :19533 request_logs_2026_08 与 :19519 的 8h 缝）。代价：重烤+三镜像回归（6692b99f6 的 fresh-install 测试可复用，A2 已由本轮先行修复）。收益：installer-only 新装零遗产，687/811 变纯幂等空跑。风险：dump 源库选择不当会烤入本机特有对象。
- **选项 2｜ensure/启动侧防御**：把 687 的指纹改为时区无关判定（按时刻比较或双形态指纹），作为 815 类迁移进 installer+序列双通道；同时把 810-814 forward 补进 installer（或维持本轮「终态已烤 SSOT」豁免口径并注册进序列通道兜底）。代价：小迁移一枚。收益：不动 SSOT。风险：与未来重烤产生双保险冗余（无害）。
- **选项 3｜保持现状+文档登记**：依赖「新装库 7 月/8 月是过去窗 + 808 default 兜底 + 升级时序列通道 811 修两表」。残余：其余 5 表的 08:00 边界在 installer-only 库永久存活，任何 9 月时间戳回放/对账写入将 23514，credit_ledger 等无 default 表永久不可回填。
- 域倾向：**选项 2（或 1+2 组合）**；A2 已由第三十一轮先行修复（是无论选项均需的前置）。
