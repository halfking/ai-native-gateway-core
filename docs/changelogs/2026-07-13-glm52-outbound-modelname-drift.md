# 2026-07-13 fix: glm-5.2 请求被替换为 glm-5.1 的真实根因修正

## 现象

`llm.kxpms.cn` 客户端请求 `model=glm-5.2`，但抓包/上游 access log
显示供应商实际收到 `model=glm-5.1`。多次重复稳定复现。

## 之前误判的根因（sql/fixes/fix-glm52-alias-drift.sql v1）

v1 脚本注释里写的是：

> `model_aliases` 把 `glm-5.2` 错误关联到 `glm-5.1` 的 canonical_id (=49)
> → 所有 canonical_id=49 的 offer 被当作 glm-5.2 的候选返回。

## 现场 audit 后的真实根因（2026-07-13，pg-252-pg17/llm_gateway）

- `model_aliases` 表里 **根本没有** `glm-5.2` 的 alias 行（6 行 alias
  只覆盖 glm-4 / glm-4.5 / glm-4.5-air / glm-4.5-flash / glm-5 /
  glm-5.1）。所以 v1 描述的"alias drift"并不存在。
- `models_canonical` 里 `glm-5.2` (id=173264) 存在且 provider_models 的
  canonical_id 关联是正确的。
- 真正错的是 `provider_models.outbound_model_name` 这一列。6 条
  `raw_model_name` 形如 `glm-5.2` 的 pm 行里，6/6 行 `outbound_model_name`
  都被填成了 `'glm-5.1'`：

| pm.id  | provider_id | provider        | raw_model_name | outbound_model_name | created_at           |
| ------ | ----------- | --------------- | -------------- | ------------------- | -------------------- |
| 209423 | 18          | nvidia          | z-ai/glm-5.2   | **glm-5.1** ⚠️      | 2026-07-03           |
| 182701 | 24          | sensenova       | glm-5.2        | **glm-5.1** ⚠️      | 2026-07-02           |
| 170383 | 32          | zhipu           | glm-5.2        | **glm-5.1** ⚠️      | 2026-06-15           |
| 828035 | 37          | scnet           | GLM-5.2        | **glm-5.1** ⚠️      | 2026-06-24           |
| 535060 | 581         | glm-xianyu      | glm-5.2        | **glm-5.1** ⚠️      | 2026-06-20           |
| 600856 | 847         | glm-5.2-oneday  | glm-5.2        | **glm-5.1** ⚠️      | 2026-06-21           |

## 触发的请求路径

1. 客户端 `model=glm-5.2` 进入 `provider/client.go:loadCandidatesByModalityDB`
   命中 `raw_model_name='glm-5.2'` 的 6 条 offer（match clause 1）。
2. `provider/client.go:749`
   `cand.RawModel = COALESCE(outbound_model_name, raw_model_name)`
   由于 `outbound_model_name='glm-5.1'` 非空，`cand.RawModel='glm-5.1'`。
3. `domains/streaming/executors/executor_chat.go:resolveOutboundModel` /
   `prepareRequestBody` 检测到 `outboundModel != clientModel`，调用
   `replaceModelInRequestBody` 把请求体 `"model":"glm-5.2"` 替换为
   `"model":"glm-5.1"`。
4. 上游供应商收到 `glm-5.1` 而非 `glm-5.2`。

## 为什么对照组（glm-5.1 / glm-5-2-260617）"幸运地"不出问题

- `glm-5.1` 6 条 pm 行里 4 条 `outbound_model_name` 为空，COALESCE
  兜底为 `raw_model_name='glm-5.1'`，行为正确；2 条显式 `glm-5.1`
  的（volcano 34/35）是因为供应商要求字面 `glm-5.1`。
- `glm-5-2-260617` 2 条 `outbound_model_name` 全为空，正常。

## 修复

执行 `sql/fixes/fix-glm52-alias-drift.sql` v2（v2 已重写为真实根因版本）。
第 3 部分把 glm-5.2 的 6 条 `outbound_model_name` 清空为 NULL，让
COALESCE 兜底走 `raw_model_name='glm-5.2'`，请求原样转发给供应商。

依据：`provider_catalog` seed (zhipu 行) 显示 zhipu 上游模型清单已包含
`glm-5.2`，直接字面调用是可接受的；glm-5.1 / glm-5-2-260617 的
多数 provider `outbound_model_name` 本来就是 NULL，是一致行为。

## 验证

- COMMIT 完成后等待 30-120s（`resolve/resolve.go:38-49` 候选缓存 TTL
  默认 120s），gateway 候选缓存自动失效。
- 用 `llm.kxpms.cn` 请求 `model=glm-5.2`，抓取 trace 应能看到上游
  收到 `glm-5.2` 而非 `glm-5.1`。
- 备份：修复前 `pg_dump -t provider_models -t credential_model_bindings`
  到 `/root/backup_pm_<时间戳>.sql`。
- 回滚：把 6 条 `outbound_model_name` 恢复为 `'glm-5.1'`。
