# 2026-08-23 · credential priority flag 迁移 568 验证 + 路由凭据可观测落地

## 背景

hzx-2 round-3 引入 `credential_model_bindings.priority` 列（SQL 候选 ORDER BY 最高优先桶）后，迁移从 561 重编号为 568（566 被 `credentials_governor_revision`、567 被 `session_analysis_metadata` 占用）。本条目记录迁移的本地验证结论与配套可观测落地。

## 迁移 568 验证（本地 Docker pg561 / test561）

验证环境是上一会话为 561 搭建的最小 fixture（cmb + provider_models + scope_revision，已处于 561 迁移后状态）——等价于生产 154 的"schema_migrations 已记 561"状态。结论：**通过**。

1. **幂等重跑干净**：`psql -v ON_ERROR_STOP=1 -f 568_credential_priority_flag.sql` 全语句成功、COMMIT 退出码 0。生产可直接重跑。
2. **model_offers 视图暴露 priority 列**（up 迁移用 `CREATE OR REPLACE VIEW`，priority 追加为末列故合法；down 迁移用 `DROP VIEW ... CASCADE` + 重建）。
3. **候选排序 before/after**（复用 `provider/client.go` ORDER BY 逐字副本，4 凭据矩阵）：
   - priority=true + quota ok 的 per_token 凭据排第一，压过 free 计费凭据（priority 桶在计费轮次之前）✓
   - priority=true + periodic_exhausted 不进优先桶（quota 门禁生效）✓
   - 通过 `UPDATE model_offers SET priority=false` 翻转后 free 凭据回到第一（标志实际改变首选流量）✓
4. **路由缓存失效链路**：priority 翻转使 scope_hash 变化（59cbda90… → b20087b2…）、scope_version 3→4；INSTEAD OF UPDATE 触发器经视图写入成功 ✓

对照 handoff 预期："low-priority 凭证仍能拿到流量但 high-priority 凭证在 quota_state='ok' 时拿到更多"——符合：非 priority 凭据保留在有序列表承接 failover，priority+ok 凭据占据首选位。

完整步骤数据在本地 `docs/全方面测试/results/568_priority_routing_migration_report.md`（该目录按仓库约定不入仓）。

## 可观测落地

- **404/500 指标分野**：`applyForceEnable` 凭据查询失败时递增 `llmgw_routing_credential_reset_total{surface=lookup,result=not_found}`（404，操作员敲错 ID）或 `{lookup,error}`（500，链路故障），此前 404 路径完全不可见。
- **孤儿指标接线**：`llmgw_routing_priority_candidates_selected_total` 补上 Inc() 调用点——`planCandidates` 收尾处按首选候选三态分类（priority_only / spillover_to_non_priority / no_priority_candidates），受 `PriorityRoutingEnabled` 门禁。
- **Grafana**：新 dashboard `deploy/prometheus/grafana/provisioning/dashboards/routing-credentials.json`（8 面板覆盖全部 8 个 llmgw_routing 指标，含 404/500 对照 stat 与 priority 分布饼图；放在 provisioning 目录随 compose 自动加载）。
- **Alert 规则**：`deploy/prometheus/rules/routing-credential-state.yml` 7 条告警覆盖除 priority 分布（设计上仅面板）外的全部新指标，含 `RoutingRecoveryTickSlow`（tick >10s 预警连接池耗尽）与 `RoutingCoolingFallbackTriggered`（全员冷却降级即报）。配套 `routing_credential_state_test.go` 校验规则与低基数约束（无 model/tenant 选择器）。

## API 文档

- 新增 `docs/03-design/03-interface-design/api-yaml/llm-gateway-routing-admin.yaml`：gateway routing-admin OpenAPI 规范种子，首个条目 `POST /api/routing/credentials/{id}/reset-state`（请求/响应/404-500 语义与指标标签的对应关系）。
- `docs/03-design/01-architecture/architecture/optimization-roadmap.md` 头部加 v6 review 入口，指向 `docs/架构优化v6/` 的波次路线图。

## 遗留

- `LLM_GATEWAY_PRIORITY_ROUTING_ENABLED=false` 门禁的行为验证需要网关进程（本条目仅 SQL 层）；154 预发布部署后用 `llmgw_routing_priority_candidates_selected_total` 的 outcome 分布做线上确认。
- Grafana dashboard JSON 需在实际 Grafana 实例上导入验证渲染（本仓库只持有定义文件）。
