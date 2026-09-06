# 物化视图数据一致性巡检脚本

## 用途

`check-routing-mv-drift.sh` 用于检测 `routing_analytics_7d` 物化视图与基础视图 `request_logs` 之间的数据漂移（drift），确保物化视图的数据准确性。

**P2-D from handoff 20260901_011500**

## 功能

1. ✅ 检查物化视图是否存在
2. ✅ 检查物化视图新鲜度（refreshed_at < 15分钟）
3. ✅ 对比 MV 与基础视图的总请求数
4. ✅ 计算差异百分比，超过阈值时告警
5. ✅ 详细对比各 task_type 的数据差异
6. ✅ 支持 Lark webhook 告警

## 使用方法

### 方式 1：使用环境变量

```bash
export LLM_GATEWAY_DATABASE_URL="postgresql://user:pass@host:5432/llm_gateway"
./scripts/check-routing-mv-drift.sh
```

### 方式 2：传递参数

```bash
./scripts/check-routing-mv-drift.sh "postgresql://user:pass@host:5432/llm_gateway"
```

### 在生产服务器上运行

```bash
# 154 服务器
ssh -p 25022 root@<env:HOST_154_IP>
export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-)
/opt/llm-gateway-go/scripts/check-routing-mv-drift.sh

# 245 服务器
ssh -p 25022 root@<env:HOST_245_IP>
export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-)
/opt/llm-gateway-go/scripts/check-routing-mv-drift.sh
```

## 配置 Cron 定时任务

建议每天运行一次，在低峰时段（如凌晨 3 点）：

```bash
# 编辑 crontab
crontab -e

# 添加以下行（每天凌晨 3 点运行）
0 3 * * * export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-) && /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh >> /opt/llm-gateway-go/logs/mv-drift-cron.log 2>&1
```

## 配置 Lark 告警

设置环境变量 `LARK_WEBHOOK_URL` 以启用飞书告警：

```bash
export LARK_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxxx"
./scripts/check-routing-mv-drift.sh
```

## 阈值配置

默认阈值为 **5%**。如需调整，编辑脚本中的 `DRIFT_THRESHOLD` 变量：

```bash
DRIFT_THRESHOLD=5  # 允许的最大差异百分比
```

## 输出示例

### 成功案例（差异在阈值内）

```
2026-09-01 02:05:10 [INFO] === 开始物化视图数据一致性检查 ===
2026-09-01 02:05:10 [INFO] 检查物化视图是否存在...
2026-09-01 02:05:10 [INFO] ✓ 物化视图存在
2026-09-01 02:05:10 [INFO] 检查物化视图新鲜度...
2026-09-01 02:05:10 [INFO] ✓ 物化视图新鲜 (214s 前刷新)
2026-09-01 02:05:10 [INFO] 对比总请求数...
2026-09-01 02:05:11 [INFO] 物化视图总数: 338931
2026-09-01 02:05:11 [INFO] 基础视图总数: 339120
2026-09-01 02:05:11 [INFO] 差异: 189 行 (.05%)
2026-09-01 02:05:11 [INFO] ✓ 数据一致性检查通过 (差异 .05% ≤ 5%)
```

### 告警案例（差异超过阈值）

```
2026-09-01 02:05:11 [ERROR] ⚠️  数据漂移超过阈值！
2026-09-01 02:05:11 [ERROR]    差异: 8.5% (阈值: 5%)
2026-09-01 02:05:11 [ERROR]    MV 总数: 320000
2026-09-01 02:05:11 [ERROR]    Base 总数: 350000
2026-09-01 02:05:11 [ERROR]    差值: 30000 行
```

## 日志位置

- 脚本日志：`logs/mv-drift-check.log`
- Cron 日志：`logs/mv-drift-cron.log`（如果配置了 cron）

## 故障排查

### 1. 物化视图不存在

**错误**：`物化视图 routing_analytics_7d 不存在`

**原因**：migration 632 未应用

**解决**：
1. 确认 `db/db.go` 中的 `ensureRoutingAnalyticsMaterializedViews` 已执行
2. 检查启动日志中是否有迁移错误
3. 手动运行迁移 SQL（见 `sql/migrations/startup/up/632_routing_analytics_materialized_view.sql`）

### 2. 物化视图过期

**警告**：`物化视图已过期 (1200s > 900s)，数据可能不一致`

**原因**：`bg.MaterializedViewRefresher` 未运行或刷新失败

**解决**：
1. 检查 refresher 是否启动：`journalctl -u llm-gateway-go-* | grep materialized_view_refresher`
2. 检查刷新日志：`journalctl -u llm-gateway-go-* | grep "refreshed routing"`
3. 检查告警（P1-B）：应该有 LarkBot 告警如果连续失败 2 次

### 3. 差异持续超过阈值

**可能原因**：
1. 刷新频率不足（当前 10 分钟一次）
2. 数据写入速度过快
3. 刷新过程中出现错误

**解决**：
1. 检查 `bg.MaterializedViewRefresher` 日志
2. 考虑缩短刷新间隔（需评估数据库负载）
3. 检查基础视图是否有异常数据

## 相关文档

- [TROUBLESHOOTING-routing-analytics.md](../docs/troubleshooting/routing-analytics.md) - 物化视图问题排查指南
- [bg/materialized_view_refresher.go](../bg/materialized_view_refresher.go) - 刷新器实现
- [admin/analytics_materialized.go](../admin/analytics_materialized.go) - 物化视图查询逻辑

## 监控建议

除了定时运行此脚本外，还建议：

1. **Prometheus 指标**：暴露 `routing_mv_refresh_success_timestamp` 指标
2. **告警规则**：`time() - routing_mv_refresh_success_timestamp > 900`（15 分钟未刷新）
3. **日志监控**：监控 `failed to refresh` ERROR 日志

## 更新日志

- 2026-09-01：初始版本（P2-D）
  - 基础数据对比功能
  - 差异百分比告警
  - 按 task_type 详细对比
  - Lark webhook 集成
