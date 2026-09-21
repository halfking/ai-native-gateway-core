# Lane A — R36 遗留 SQL 项核验（R37，2026-09-17）

## 一、发现/现状核验
| # | 项 | 现状结论 | 证据 file:line |
|---|---|---|---|
| 1a | 三份 baseline | 00-prereqs/01-schema/02-seed 由 dump-schema.sh:12-15 经 db-init-lib.sh pg_dump 再生成；01 为幂等叠加式，无结构修正语句 | sql/scripts/dump-schema.sh:12-15; sql/schema/01-schema.sql:12-14 |
| 1b | 717 本机状态 | 未应用。schema_migrations 最高 715；本机母表↔hot 9 列型漂移（4 varchar↔text + 4 jsonb↔其它 + protocol_conversion bool↔text）+ 列集漂移（hot 独有 caller_id/session_correlation_id/status_code；母表独有 is_terminal/outbound_body/request_depth） | information_schema.columns 实测 |
| 1c | 本机重导 baseline 可行性 | 不可行作为对齐手段（IF NOT EXISTS 对已存在表零作用；baseline 源库≠本机）。正确前置 = 直接执行 716+717 | 01-schema.sql:13570-13705,18785 |
| 2a | credential_recovery 守卫读冻结旧表 | 仍直读 model_probe_state 无 useNewProbeMode 门控；credRecovery 在 main.go:3718 无门控启动 | bg/credential_recovery.go:594-612; cmd/gateway/main.go:3718 |
| 2b | BrokenProbeReviver 无门控 | 无条件 UPDATE model_probe_state broken_confirmed→recovering；(a)击穿 2a 守卫 (b)清 provider/client.go:1586 路由排除 | bg/broken_probe_reviver.go:67-72; main.go:3731-3733 |
| 3a | 716 全文等价守卫 | 只有片段守卫无全文等价 | db/probe_views_unified_test.go:44-58 |
| 3b | 716 down 半回滚 | drop 6 视图+1 函数仅重建 3；v_model_priority_details/v_model_availability_timeline/get_model_state_summary 42P01 | 716_*.down.sql:1-157 |
| 4 | A-3 余面 | ①cache.go:165 仍直读但门控安全；②checker.go:642 无门控（随 2a 修）；③client.go:1586 直读无门控；④client.go:1657 死代码无害（AND FALSE 内）；⑤client.go:2368 直写无门控；⑥routing.go:5542/5557 直写为 admin 人工动作低危 | 逐点 |

## 二、本机库关键实测
- 索引总数 2276；request_logs_hot 31,608 行；model_probe_state broken_confirmed 仅 1 行；node_probe_state 459 行（新系统在用）
- 部署前置路径建议：对本机直接执行 716→717（716 已在 sequence 账本）
