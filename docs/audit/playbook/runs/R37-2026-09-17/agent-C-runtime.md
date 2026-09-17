# Lane C — 运行时热路径 SQL 审计（R37，2026-09-17）

## 一、发现（候选）
| # | 级别 | 发现 | 证据 |
|---|---|---|---|
| 1 | P2 | session_aggregate_outbox done 行永不删除（实库 365,089 行≈5万/日）；claim 因 OR+ORDER BY 放弃 partial 索引 Seq Scan | session_aggregate_outbox_reaper.go:278-293,356 |
| 2 | P2 | systemmonitor fallback drain 事务内 FOR UPDATE 后循环持锁做 Redis LPUSH+逐行 DELETE，Redis 故障时锁拖到超时 | bg/systemmonitor/monitor.go:365-415 |
| 3 | P2 | request_envelope/sticky_sessions 零索引，expires_at DELETE 与 RestoreFromDB 全表扫 | bg/envelope_cleaner.go:54; bg/sticky_cleaner.go:54; sticky.go:637 |
| 4 | P2 | partition_manager/attachments/journey retention TTL DELETE 无界单语句+30s 超时 → 积压后 livelock（高危表 request_context_attrs 11万/7d） | bg/partition_manager.go:552-1432 多处; attachments/repository.go:286; requestjourney/retention.go:135,144 |
| 5-12 | P3 | dispatch 失败风暴逐条同步写 probe 状态；concurrency_peak_collector 逐 key 单条；TurnLogsWriter 逐 stage 串行；ursm 迁移 2RTT/entry；settings_kv 无 ON CONFLICT；hostedtask FOR UPDATE 缺 tenant；routing_health_checks fix_sql Sprintf；provider_error_aggregator 逐条+rollup 同窗 9 扫 | executor.go:2632-2790 / concurrency_peak_collector.go:211-247 / session_writer_v2.go:655 / pg_store.go:488 / store_db.go:64 / hostedtask/store.go:357 / routing_health_checks.go:61 / provider_error_aggregator.go:190 |

## 二、健康面
session v2 热写 advisory lock 64 位无桶撞+ON CONFLICT COALESCE 单调守卫；requestjourney outbox 幂等+SKIP LOCKED 命中索引；sessionv2mirror outbox claimed 守卫；trimmer 族 LIMIT 5000 批式；dbx QuoteIdentifier 白名单；stats_event_inbox(77万行) claim 命中 partial 索引；freeresource 租户白名单转义；dispatch 主链路零同步 SQL。

## 三、未覆盖
hooks 其余子包、internal/agent/durable/orchestration、model-quality 等仅 grep 级；EXPLAIN 基于本机行数（生产计划需复核 #1）。
