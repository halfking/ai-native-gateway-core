# Lane F — 动态 SQL/注入横扫（R37，2026-09-17）

## 一、发现（候选）
| # | 级别 | 发现 | 数据流 |
|---|---|---|---|
| F1 | 高(二阶) | 健康检查 fix_sql 以 '%s' 裸拼 DB 字符串（raw_model_name 可受上游 /models 影响），端点原样执行库中 SQL 文本 | routing_health_checks.go:74 → admin/health_check_handlers.go:130-140 → POST /admin/api/v1/health-checks/fix |
| F2 | 低 | pg_total_relation_size('%s') 单引号内插（表名过白名单但无字符集校验，extended protocol 单语句难成真注入） | admin/data_lifecycle_storage.go:632 |
| F3 | 信息 | LIKE/ILIKE 通配符直通（已参数化，仅过匹配面） | logs.go:531 等 7 处 |

## 二、健康面（横扫覆盖量）
Sweep1 fmt.Sprintf+SQL：非测试 77 命中逐条判定（~60 纯 $%d 占位、~8 int LIMIT/OFFSET、余内部常量）；Sweep2 拼接 90 命中全为硬编码列名子句；Sweep3 ORDER BY 用户输入 3 处全有白名单闸；Sweep4 CopyFrom 仅内部工具+pgx.Identifier；set_config 全常量 GUC 或 $1；Sweep5 plpgsql 113 文件 26 处 EXECUTE 全 %I/%L；Sweep6 值全参数化；Sweep7 search_path 0 命中（freeresource 租户经 isValidTenantID 白名单）。重点核销：dbx QuoteIdentifier、telemetry 分区名正则白名单、data_lifecycle partitionedTables 静态白名单、analytics_materialized 包内常量。

## 三、未覆盖
migrations DDL 非运行时动态 SQL；vendor/test 排除；库侧存量 fix_sql 数据核验建议主代理跑 SELECT ... WHERE fix_sql LIKE '%''%'。
