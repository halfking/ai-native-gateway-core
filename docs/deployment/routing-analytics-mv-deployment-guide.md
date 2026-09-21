# 路由分析物化视图 - 部署配置指南

> **版本**: 2026-09-01  
> **适用范围**: 154/245 生产环境  
> **前置条件**: P1-A, P1-B, P2-D 代码已部署

## 概述

本指南说明如何配置物化视图监控和告警功能的环境变量和定时任务。

### 相关功能

- **P1-B**: MV 刷新失败 LarkBot 告警
- **P2-D**: 数据一致性巡检脚本

---

## 一、环境变量配置

需要在 `/etc/llm-gateway-go/env` 文件中添加以下配置：

### 1. LARK_ALERT_RECIPIENT（P1-B 必需）

**用途**: MV 刷新失败时的 Lark 告警接收人

**格式**: Lark Open ID

**获取方法**:
1. 在飞书中打开"开发者后台"
2. 进入"通讯录" → "成员管理"
3. 查找目标用户，复制其 Open ID

**配置示例**:
```bash
LARK_ALERT_RECIPIENT=ou_xxxxxxxxxxxxxxxxxxxxx
```

**告警触发条件**:
- 物化视图刷新连续失败 2 次（约 20 分钟）

**告警消息示例**:
```
⚠️ 物化视图刷新失败

视图: routing_analytics_7d
连续失败次数: 2
错误: pq: connection refused
时间: 2026-09-01 10:30:00

请检查数据库连接状态。
```

---

### 2. LARK_WEBHOOK_URL（P2-D 可选）

**用途**: 数据一致性检查失败时的 Webhook 告警

**格式**: Lark 群机器人 Webhook URL

**获取方法**:
1. 在飞书群中添加"自定义机器人"
2. 配置机器人名称（如"LLM Gateway MV 监控"）
3. 复制生成的 Webhook URL

**配置示例**:
```bash
LARK_WEBHOOK_URL=https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

**告警触发条件**:
- MV 与基础视图数据差异 > 5%
- MV 刷新时间超过 15 分钟

**告警消息示例**:
```
❌ 物化视图数据一致性检查失败

服务器: 154 (<env:HOST_154_IP>)
检查时间: 2026-09-01 03:00:15

问题详情:
- MV 总数: 338931
- 基础视图总数: 356120
- 差异: 17189 行 (5.08%)
- 阈值: 5%

详细日志: /opt/llm-gateway-go/logs/mv-drift-check.log
```

---

### 配置文件完整示例

编辑 `/etc/llm-gateway-go/env`：

```bash
# ... 其他配置 ...

# P1-B: MV 刷新失败告警接收人
LARK_ALERT_RECIPIENT=ou_xxxxxxxxxxxxxxxxxxxxx

# P2-D: 数据一致性告警 Webhook（可选）
LARK_WEBHOOK_URL=https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

---

## 二、Cron 定时任务配置

### 任务说明

**功能**: 每天凌晨 3 点执行数据一致性检查

**脚本路径**: `/opt/llm-gateway-go/scripts/check-routing-mv-drift.sh`

**日志路径**: `/opt/llm-gateway-go/logs/mv-drift-cron.log`

### 配置步骤

#### 1. 确认脚本可执行权限

```bash
# 在 154 服务器上
ssh root@<env:HOST_154_IP>

# 检查权限
ls -l /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh

# 如果没有执行权限，添加
chmod +x /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh
```

#### 2. 创建日志目录

```bash
mkdir -p /opt/llm-gateway-go/logs
chown llm-gateway:llm-gateway /opt/llm-gateway-go/logs
```

#### 3. 配置 crontab

```bash
# 编辑 root 用户的 crontab
crontab -e

# 添加以下行（单行）
0 3 * * * export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-) && /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh >> /opt/llm-gateway-go/logs/mv-drift-cron.log 2>&1
```

**说明**:
- `0 3 * * *`: 每天凌晨 3:00 执行
- 从 `/etc/llm-gateway-go/env` 读取数据库连接配置
- 标准输出和错误都追加到日志文件

#### 4. 验证 cron 配置

```bash
# 查看已配置的 cron 任务
crontab -l | grep mv-drift

# 手动测试执行
export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-)
/opt/llm-gateway-go/scripts/check-routing-mv-drift.sh

# 检查输出
tail -50 /opt/llm-gateway-go/logs/mv-drift-check.log
```

#### 5. 在 245 服务器重复配置

```bash
# 在 245 服务器上执行相同的配置步骤
ssh root@<env:HOST_245_IP>

# 重复步骤 1-4
```

---

## 三、配置验证清单

部署完成后，请逐项验证：

### 环境变量验证

- [ ] 154 服务器 `/etc/llm-gateway-go/env` 包含 `LARK_ALERT_RECIPIENT`
- [ ] 245 服务器 `/etc/llm-gateway-go/env` 包含 `LARK_ALERT_RECIPIENT`
- [ ] (可选) 两台服务器都配置了 `LARK_WEBHOOK_URL`
- [ ] 重启服务后环境变量生效: `systemctl restart llm-gateway-go`

### Cron 任务验证

- [ ] 154 服务器 crontab 包含 `check-routing-mv-drift.sh`
- [ ] 245 服务器 crontab 包含 `check-routing-mv-drift.sh`
- [ ] 日志目录存在且有写权限
- [ ] 手动执行脚本成功，日志正常输出

### 功能验证

- [ ] 检查最近一次 MV 刷新日志: `journalctl -u llm-gateway-go -n 100 | grep "refreshed routing_analytics"`
- [ ] 模拟刷新失败（可选）: 临时关闭数据库连接，观察是否收到 Lark 告警
- [ ] 等待凌晨 3 点后检查 cron 执行日志: `tail -100 /opt/llm-gateway-go/logs/mv-drift-cron.log`

---

## 四、故障排查

### 问题 1: 未收到 Lark 告警

**可能原因**:
1. `LARK_ALERT_RECIPIENT` 配置错误或未生效
2. 服务未重启，环境变量未加载
3. 连续失败次数不足 2 次

**排查步骤**:
```bash
# 1. 检查环境变量
grep LARK_ALERT_RECIPIENT /etc/llm-gateway-go/env

# 2. 检查服务是否重启
systemctl status llm-gateway-go | grep "Active:"

# 3. 检查刷新失败日志
journalctl -u llm-gateway-go -n 500 | grep -E "(refresh.*failed|failureCount)"

# 4. 检查 Lark 通道初始化
journalctl -u llm-gateway-go -n 1000 | grep -i lark
```

### 问题 2: Cron 任务未执行

**可能原因**:
1. cron 服务未运行
2. 环境变量未正确传递
3. 脚本权限问题

**排查步骤**:
```bash
# 1. 检查 cron 服务
systemctl status cron  # Debian/Ubuntu
systemctl status crond # CentOS/RHEL

# 2. 检查 cron 日志
grep CRON /var/log/syslog | grep mv-drift  # Debian/Ubuntu
grep CRON /var/log/cron | grep mv-drift    # CentOS/RHEL

# 3. 手动模拟 cron 环境执行
sudo -u root bash -c 'export LLM_GATEWAY_DATABASE_URL=$(grep "^LLM_GATEWAY_DATABASE_URL=" /etc/llm-gateway-go/env | cut -d= -f2-) && /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh'

# 4. 检查脚本输出
tail -100 /opt/llm-gateway-go/logs/mv-drift-cron.log
```

### 问题 3: 数据一致性检查误报

**现象**: 报告差异超过 5%，但实际数据正常

**可能原因**:
1. MV 正在刷新中（数据暂时不一致）
2. 时间窗口边界效应（新数据刚写入）
3. 阈值设置过严格

**解决方案**:
```bash
# 1. 等待 10 分钟后重新执行
sleep 600
/opt/llm-gateway-go/scripts/check-routing-mv-drift.sh

# 2. 手动检查 MV 新鲜度
psql $LLM_GATEWAY_DATABASE_URL -c "
SELECT 
    matviewname,
    NOW() - last_refresh AS staleness
FROM pg_matviews 
WHERE schemaname='public' 
  AND matviewname LIKE 'routing_%';"

# 3. 调整阈值（如果持续误报）
# 编辑脚本，修改 THRESHOLD 变量
vi /opt/llm-gateway-go/scripts/check-routing-mv-drift.sh
# 将 THRESHOLD=5 改为 THRESHOLD=10
```

---

## 五、监控指标

### 关键指标

1. **MV 刷新成功率**: 应保持 > 99%
2. **MV 刷新耗时**: 应保持 < 30 秒
3. **数据一致性差异**: 应保持 < 5%
4. **MV 新鲜度**: 应保持 < 15 分钟

### 日志查询命令

```bash
# 最近 24 小时的刷新统计
journalctl -u llm-gateway-go --since "24 hours ago" | grep -E "refreshed routing_analytics|refresh.*failed" | wc -l

# 刷新耗时分布
journalctl -u llm-gateway-go --since "24 hours ago" | grep "refreshed routing_analytics" | grep -oP "took \K[0-9.]+" | sort -n

# 最近一次数据一致性检查结果
tail -20 /opt/llm-gateway-go/logs/mv-drift-check.log | grep -E "差异|一致性"
```

---

## 六、回滚方案

如果 MV 功能出现问题，可以安全回退到基础查询：

### 方案 A: 临时禁用 MV（无需重启）

```sql
-- 在数据库中执行
DROP MATERIALIZED VIEW IF EXISTS routing_analytics_7d;
DROP MATERIALIZED VIEW IF EXISTS routing_audit_summary_7d;
```

**效果**: 
- 代码自动检测 MV 不存在，回退到基础查询
- 性能下降（1s → 3s），但功能正常

### 方案 B: 停止 MV 刷新器（需重启）

修改 `cmd/gateway/main.go`:

```go
// 注释掉 MV 刷新器初始化
// if err := bg.StartMaterializedViewRefresher(ctx, db, gLarkCh); err != nil {
//     return fmt.Errorf("start MV refresher: %w", err)
// }
```

重新构建并部署。

---

## 附录

### A. 相关文档

- [TROUBLESHOOTING-routing-analytics.md](../troubleshooting/routing-analytics.md) - 问题排查指南
- [scripts/README-check-mv-drift.md](../../scripts/README-check-mv-drift.md) - 巡检脚本使用文档
- handoff_20260901_021000.md（.handoff/ 已清理，见 git 历史）

### B. 联系方式

- **技术支持**: 查看 handoff 文档中的开发者信息
- **告警接收**: 配置 `LARK_ALERT_RECIPIENT` 的飞书联系人

---

**文档版本**: 1.0  
**最后更新**: 2026-09-01  
**维护者**: LLM Gateway Team
