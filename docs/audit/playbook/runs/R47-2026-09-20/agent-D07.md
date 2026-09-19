# D07 admin 读面与 SQL 正确性 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | F9b 注释前提与真库列型不符：真库 request_logs(_hot).request_id 均 text 且母表存原始 hex32；uuid 型的是 routing_decision_log.request_id。代码行为正确，注释漂移（照注释把 L1 ::uuid 化会让探测 id 22P02 回归） | admin/analytics.go:740-741、:1105-1107 | 注释改实测口径 |
| 2 | P3 | 决策回放 >30d 契约收窄（30 天前 id 由可回放变 404，UI 无提示） | admin/analytics.go:757 | 接受，登记已知行为变化 |
| 3 | P3 | F3 视图对 promote 残留双行无 NOT EXISTS 兜底（hot∩母表窗口内同一行两次 → 聚合双计；blessed surface 本身同构非本轮引入） | 610:103、710:326-339 | 登记视图链已知差距 |
| 4 | P2 候选 | admin 读面残留裸母表读（其中 assertTaskInTenant 有实际功能影响：8h 内新建 task 母表无行 → tenant_admin 404；request_logs_hot 真库已带 gw_task_id 列实存 3/3）。典型：session_list.go:136、usage.go:789/796、session_analytics_timeseries、session_extract、session_panorama_handler、session_analytics_handler、quality_correlations、provider_models、probe_history、memora_handlers、session_sanitize_matches、session_tenant | admin/session_tenant.go:52-56 等 | 逐条转修复或带因豁免 |
| 5 | P3 方案 | grep 守卫测试落点：internal/sqlreadguard/guard_test.go，正则 (?i)\b(FROM|JOIN)\s+(public\.)?request_logs\b（\b 天然排除 _hot/_with_/_without_ 视图），白名单双层（文件级 map+行内 sqlreadguard:allow），首轮白名单含双腿/DDL/校验器/SQLite 豁免，收尾门 go test ./internal/sqlreadguard/ | 详方案的 (a)-(e) | 按方案落地 |

补充：uuidVariants 测试分支齐（3 返回路径全覆盖）；缺口是未钉 SQL 形态（母表腿 ::text+30d 界、L2 $1::uuid+v[1]）。

## 二、核实为健康的面
1. F9a verdict 重写（GROUP BY 收敛/ROW_NUMBER 合法/rank 第 5 列/Scan 5 对位；真库 EXPLAIN 通过；tenant 注入位置与参数编号一致）。
2. F3 五处切视图语义正确（610 v1 体无 turns 段，model_chosen 等全实列——R46 §三教训不影响这组查询；pg_views 确认在场）。
3. F9b 决策回放（L1 11 列对位；NullString 四可空列真库实证；errors.Is(pgx.ErrNoRows) 正确；L2 参数解耦——真库 RDL.request_id udt_name=uuid 恰好对位；探测 id 走 ANY($1::text[]) 无 22P02，真库实跑通过）。
4. F4 双探测列型安全（selections(_hot/_all).request_id 均 TEXT）；promote 互斥使双探测无重复排除。
5. attempt_quality（4 列对位；success NOT NULL 保证 FILTER 口径；hours≤168 归一）。
6. serveSessionSnapshot last_provider JOIN（plp.id 主键无放大；非数值→NULL-join→COALESCE 回退；Scan 槽位与指针目标 NULL 安全）。
7. taskprofile_audit_sink 纯日志无 SQL 面。
8. 窗口新增母表引用全部正当（双腿之一/NOT EXISTS 腿/SQLite DELETE）。
9. §五#11 全仓裸读清点（admin 13 文件 34 处 + 非 admin ~15 处 + shell/SQL 模板面 + R44 重写版 scripts/analysis 零裸读）。

## 三、未覆盖项
>30d 回放 404 端到端实测；非 admin 裸读逐条语义深审；guard 实施（派发限定只出方案）；shell/DB 对象面真库实跑；promote 残留双行存量量化。
