# Lane D — DDL 对象审计（R37，2026-09-17）

## 一、发现（候选）
| # | 级别 | 发现 | 证据 |
|---|---|---|---|
| 1 | 高(安全) | RLS owner 绕过：510 表 owner=llm_gateway(应用角色) 仅 9 表 FORCE RLS，~40 张租户隔离表 RLS 对 owner 无效 | pg_tables 实测; objects/tables 仅 9 处 FORCE |
| 2 | 高(安全) | attachments 有 policy 但 RLS 未 ENABLE（策略 inert）；candidate_failure_logs/request_wal 声明式缺 ENABLE | objects/policies/attachments_*.sql; 01-schema.sql:29875 |
| 3 | 高(性能) | 129 对同表同列序同谓词重复索引（objects 声明 9 组），热表全命中：credential_model_index_hot 三重 UNIQUE、request_wal_hot pkey↔udx、routing_decision_log_hot 双 UNIQUE 等 | objects/indexes 对照 + pg_indexes 实测 |
| 4 | 中 | request_logs.client_model 5 重叠索引（btree/hash/lower/text_pattern/gin trgm） | idx_request_logs_client_model* 5 文件 |
| 5 | 中 | usage_ledger_hot/model_probe_runs_hot/candidate_failure_logs_hot 无 PK 零唯一索引；审计表无 PK | pg_index 对账 |
| 6 | 中 | api_key_model_cost 触发器链死亡：函数+transition table 声明在但全库零挂载 | objects/functions/update_api_key_model_cost*.sql |
| 7 | 中 | RLS policy 每行双 EXISTS 子查询（asset_relationships/agent_relationships） | objects/policies/asset_relationships_*.sql |
| 8-11 | 低 | 15 可空列 FK；v_model_health_dashboard 重聚合未物化（SSOT 在 ensure，文件为镜像）；approval_requests 双序列；objects 声明与活库漂移（383 policies vs 122 文件等） | objects/other/*_fkey.sql 等 |

## 二、健康面
113 函数 0 SECURITY DEFINER、EXECUTE 全 %I/%L；32 触发器全挂配置表；无>2 层视图链、无视图内裸 SELECT *；策略均无 FOR 子句（USING 复用作 WITH CHECK 不构成 UPDATE 绕过）；sequences CACHE 全 1 无跨表共享。

## 三、未覆盖
EXPLAIN 未逐条跑；migrations 侧仅对账；Go 消费方确认（#6）；columnar 膨胀统计；722 索引文件全人工审未做（方法学=文件解析+pg_indexes 对账）。
