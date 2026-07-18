# LLM Gateway - Prometheus 监控部署指南

> **Phase 0 基础设施**: Prometheus + Grafana + Alertmanager  
> **版本**: v1.0  
> **日期**: 2026-07-18

---

## 1. 架构概览

```
┌─────────────────────────────────────────────────┐
│              Grafana (3000)                     │
│          可视化看板 + 告警查看                   │
└──────────────┬──────────────────────────────────┘
               │ queries
               ▼
┌─────────────────────────────────────────────────┐
│           Prometheus (9090)                     │
│     指标采集 + 存储 + 告警规则评估               │
└──┬───────────┬───────────┬──────────────────────┘
   │           │           │
   │           │           └─→ Alertmanager (9093)
   │           │                 告警路由 + 飞书通知
   │           │
   ▼           ▼
Node-Exporter  DCGM-Exporter (待配置)
 (9100)         (9400)
系统指标        GPU 指标
```

---

## 2. 快速开始

### 2.1 前置条件

```bash
# 检查 Docker 和 Docker Compose
docker --version  # >= 20.10
docker-compose --version  # >= 1.29

# 检查端口可用性
netstat -tlnp | grep -E '9090|9093|3000|9100'
```

### 2.2 部署步骤

```bash
# 1. 进入部署目录
cd deploy/prometheus

# 2. 配置环境变量
export GRAFANA_ADMIN_PASSWORD="your-secure-password"
export LARK_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/YOUR-WEBHOOK-ID"

# 3. 启动服务
docker-compose up -d

# 4. 验证服务状态
docker-compose ps

# 5. 检查日志
docker-compose logs -f
```

### 2.3 访问 Web UI

| 服务 | URL | 默认凭据 |
|------|-----|---------|
| Prometheus | http://localhost:9090 | 无需认证 |
| Grafana | http://localhost:3000 | admin / $GRAFANA_ADMIN_PASSWORD |
| Alertmanager | http://localhost:9093 | 无需认证 |

---

## 3. 配置说明

### 3.1 Prometheus 配置

**文件**: `prometheus.yml`

**关键配置**:
- 采集间隔: 15s
- 数据保留: 30 天
- 告警评估间隔: 15s

**添加新的 scrape target**:
```yaml
scrape_configs:
  - job_name: 'your-service'
    static_configs:
      - targets: ['host:port']
        labels:
          environment: 'production'
```

### 3.2 告警规则

**文件**: `rules/alerts.yml`

**当前规则组**:
- `gpu_alerts`: GPU 利用率/温度/内存告警
- `system_alerts`: CPU/内存/磁盘告警
- `llm_gateway_alerts`: 应用服务告警

**添加新规则**:
```yaml
- alert: YourAlertName
  expr: your_metric > threshold
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "告警摘要"
    description: "详细描述"
```

### 3.3 Alertmanager 配置

**文件**: `alertmanager.yml`

**飞书 Webhook 配置**:
```yaml
receivers:
  - name: 'lark-webhook'
    webhook_configs:
      - url: '${LARK_WEBHOOK_URL}'
```

**获取飞书 Webhook URL**:
1. 在飞书群组中创建自定义机器人
2. 复制 Webhook URL
3. 设置到环境变量 `LARK_WEBHOOK_URL`

---

## 4. DCGM GPU Exporter 配置

### 4.1 GPU 节点部署 DCGM Exporter

```bash
# 在 GPU 节点上（如 kaixuan-1: 192.168.31.28）
docker run -d \
  --name dcgm-exporter \
  --restart unless-stopped \
  --gpus all \
  -p 9400:9400 \
  nvcr.io/nvidia/k8s/dcgm-exporter:3.1.8-3.1.5-ubuntu22.04
```

### 4.2 配置 Prometheus 采集

编辑 `prometheus.yml`，取消注释 DCGM 配置:

```yaml
scrape_configs:
  - job_name: 'dcgm-gpu'
    scrape_interval: 15s
    static_configs:
      - targets: ['192.168.31.28:9400']
        labels:
          cluster: 'kaixuan-1'
          gpu_type: 'rtx4090'
```

重载配置:
```bash
docker-compose exec prometheus kill -HUP 1
# 或
curl -X POST http://localhost:9090/-/reload
```

### 4.3 验证 GPU 指标

```bash
# 检查 DCGM Exporter 可用性
curl http://192.168.31.28:9400/metrics | grep DCGM_FI_DEV_GPU_UTIL

# 在 Prometheus 查询
curl -s 'http://localhost:9090/api/v1/query?query=DCGM_FI_DEV_GPU_UTIL' | jq
```

---

## 5. Grafana 看板配置

### 5.1 首次登录

1. 访问 http://localhost:3000
2. 使用 `admin` / `$GRAFANA_ADMIN_PASSWORD` 登录
3. (可选) 修改密码

### 5.2 数据源验证

导航到 **Configuration → Data Sources → Prometheus**，点击 "Test" 确认连接正常。

### 5.3 导入看板

**方式 1: 导入 JSON 文件**
1. 准备好看板 JSON 文件（如 `llm-gateway-overview.json`）
2. 导航到 **Dashboards → Import**
3. 上传 JSON 文件

**方式 2: 从 Grafana.com 导入**
- Node Exporter Full: ID `1860`
- DCGM Exporter Dashboard: ID `12239`

---

## 6. 常用运维命令

### 6.1 服务管理

```bash
# 启动服务
docker-compose up -d

# 停止服务
docker-compose down

# 重启服务
docker-compose restart

# 查看日志
docker-compose logs -f [service-name]

# 查看资源使用
docker stats
```

### 6.2 配置重载

```bash
# Prometheus 热重载（不重启容器）
curl -X POST http://localhost:9090/-/reload

# Alertmanager 热重载
curl -X POST http://localhost:9093/-/reload
```

### 6.3 数据清理

```bash
# 清理 Prometheus 数据（慎用）
docker-compose down
docker volume rm prometheus_prometheus-data
docker-compose up -d
```

### 6.4 备份与恢复

```bash
# 备份 Prometheus 数据
docker run --rm -v prometheus_prometheus-data:/data -v $(pwd):/backup \
  alpine tar czf /backup/prometheus-backup-$(date +%Y%m%d).tar.gz -C /data .

# 恢复数据
docker run --rm -v prometheus_prometheus-data:/data -v $(pwd):/backup \
  alpine tar xzf /backup/prometheus-backup-YYYYMMDD.tar.gz -C /data
```

---

## 7. 告警测试

### 7.1 触发测试告警

**方式 1: 手动发送测试告警**
```bash
curl -X POST http://localhost:9093/api/v1/alerts \
  -H "Content-Type: application/json" \
  -d '[
    {
      "labels": {
        "alertname": "TestAlert",
        "severity": "warning"
      },
      "annotations": {
        "summary": "测试告警",
        "description": "这是一条测试告警消息"
      }
    }
  ]'
```

**方式 2: 修改告警阈值**
编辑 `rules/alerts.yml`，临时降低阈值触发告警:
```yaml
- alert: HighCPUUsage
  expr: 100 - (avg by(instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100) > 10  # 改为 10%
```

### 7.2 验证飞书通知

检查飞书群是否收到如下格式的消息:
```
[FIRING:1] TestAlert warning

告警详情:
- Alertname: TestAlert
- Severity: warning
- Summary: 测试告警
- Description: 这是一条测试告警消息
```

---

## 8. 故障排查

### 8.1 Prometheus 无法启动

**症状**: 容器不断重启

**排查步骤**:
```bash
# 查看日志
docker-compose logs prometheus

# 常见原因:
# 1. 配置文件语法错误
promtool check config prometheus.yml

# 2. 规则文件语法错误
promtool check rules rules/*.yml

# 3. 端口冲突
netstat -tlnp | grep 9090
```

### 8.2 指标采集失败

**症状**: Targets 显示 DOWN 状态

**排查步骤**:
```bash
# 检查 target 可达性
curl http://target-host:port/metrics

# 检查 Prometheus 日志
docker-compose logs prometheus | grep -i error

# 检查防火墙规则
iptables -L -n | grep <port>
```

### 8.3 告警未发送到飞书

**排查步骤**:
```bash
# 1. 检查 Alertmanager 是否接收到告警
curl http://localhost:9093/api/v2/alerts | jq

# 2. 检查 Alertmanager 日志
docker-compose logs alertmanager | grep -i webhook

# 3. 验证 Webhook URL
curl -X POST "${LARK_WEBHOOK_URL}" \
  -H "Content-Type: application/json" \
  -d '{"msg_type":"text","content":{"text":"测试消息"}}'
```

### 8.4 Grafana 无法连接 Prometheus

**排查步骤**:
1. 检查数据源配置中的 URL 是否正确 (`http://prometheus:9090`)
2. 确认 Prometheus 容器名为 `llm-gateway-prometheus`
3. 检查 Docker 网络: `docker network inspect prometheus_monitoring`

---

## 9. 性能优化

### 9.1 Prometheus 存储优化

**降低采集频率**（如果数据量过大）:
```yaml
global:
  scrape_interval: 30s  # 改为 30s
```

**减少数据保留时间**:
```yaml
command:
  - '--storage.tsdb.retention.time=15d'  # 改为 15 天
```

### 9.2 迁移到 VictoriaMetrics

当数据量增长到 Prometheus 无法高效处理时，迁移到 VictoriaMetrics:

```yaml
# docker-compose.yml 添加
  victoria-metrics:
    image: victoriametrics/victoria-metrics:v1.93.0
    ports:
      - "8428:8428"
    volumes:
      - victoria-data:/victoria-metrics-data
    command:
      - '--storageDataPath=/victoria-metrics-data'
      - '--retentionPeriod=12'  # 12 个月
```

配置 Prometheus remote write:
```yaml
remote_write:
  - url: http://victoria-metrics:8428/api/v1/write
```

---

## 10. 安全加固

### 10.1 启用 HTTPS

使用 Nginx 反向代理添加 SSL:
```nginx
server {
    listen 443 ssl;
    server_name monitoring.example.com;
    
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location / {
        proxy_pass http://localhost:3000;  # Grafana
    }
}
```

### 10.2 限制外网访问

修改 `docker-compose.yml`，将端口绑定到 `127.0.0.1`:
```yaml
ports:
  - "127.0.0.1:9090:9090"  # 仅本地访问
```

---

## 11. 相关文档

- [Prometheus 官方文档](https://prometheus.io/docs/)
- [Grafana 官方文档](https://grafana.com/docs/)
- [DCGM Exporter GitHub](https://github.com/NVIDIA/dcgm-exporter)
- [Phase 0 实施计划](../Phase0-实施计划.md)
- [Prometheus Metrics 命名规范](../prometheus_metrics_naming.md)

---

**维护者**: Infrastructure Team  
**最后更新**: 2026-07-18
