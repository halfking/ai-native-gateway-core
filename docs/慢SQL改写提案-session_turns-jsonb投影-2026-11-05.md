# 慢 SQL 改写提案：session_turns jsonb 投影（来自编排台审计，待发布流采纳）

> 转交来源：wm-deploy 编排台 48h 审计轮（docs/15-慢SQL与系统日志审计-2026-11-04.md §3，
> 2026-11-04 EXPLAIN 实测）。本文为**提案副本**（未推送，未改动本仓任何代码）；
> 采纳与否由本仓发布轮决定。编排台按 docs/10 安全模型不代改产品代码。

## 实测结论（acc-pg17，2026-11-04）

- 24h 慢查询 746 条（>500ms）全归 llm_gateway 库；主模式＝`session_turns`/`bodies`
  逐行 `to_jsonb(r)` **全行展开**再由调用侧过滤，单会话可达 905 行。
- 索引不缺（tenant+session+turn 都在）；瓶颈在行宽度与整行 jsonb 序列化。
- **等价白名单投影改写 EXPLAIN 实测 -44%（553ms → 308ms）**。

## 改写 SQL（键名/值与原版一致，输出顺序无语义差异）

```sql
SELECT to_jsonb(s) FROM (
  SELECT tenant_id, session_id, request_id, partition_date, turn_no, ts, project_id,
         namespace, parent_request_id, task_type, submit_mode, compression_applied,
         compression_strategy, compression_meta, model, provider, status_code, success,
         error_kind, source_kind, quality, attachment_count, attachment_total_bytes,
         multimodal_types, tools, title, summary, digest, agent_name, agent_type, tags,
         request_status, is_abandoned, request_delta, response_delta
  FROM public.session_turns r WHERE tenant_id=$1 AND session_id=$2
) s;
```

注意：投影列清单需与本仓 `session_turns` 表结构对齐后再采纳（上表列名取自
2026-11-04 的实测版本；若表已加列，投影补齐即可，勿再回到整行展开）。

## 配套建议（按收益排序，均来自 wm-deploy docs/15 §3）

1. 应用层按 `turn_no` 分页/懒加载——905 行单次全量是主成本；
2. `REFRESH MV` 每小时 1–2.5s 属正常分析负载，无需处理；
3. `COPY` 批写慢条目为整点分析任务叠加期，无需处理。

## 相关背景（供发布轮参考）

- llm_gateway_pre_cutover_20261014 旧库（1169MB）与本题无关，另行处置
  （wm-deploy docs/17 B 项，待授权）。
- credentials 99007/99009/99012 的 fernet decrypt WARN 刷屏与本题无关
  （wm-deploy docs/17 C 项，判据已实测：均为 local-no-key 占位符、从未使用）。
