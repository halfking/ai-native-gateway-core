# Phase 0 基础设施部署 - 实施计划

> **任务**: LLM Gateway 优化 Phase 0 - 监控与告警底座
> **周期**: Week 1 (2026-07-18 ~ 2026-07-25)
> **状态**: 🚧 进行中
> **负责人**: Infrastructure Team

---

## 1. 目标

搭建四个优化方向（价格/基础/号池/衰减）共同依赖的监控与告警底座。

### 1.1 关键产出

| 产出 | 验收标准 | 负责方向 |
|------|---------|---------|
| Prometheus Server | 能采集 DCGM GPU 指标 | 价格优化 |
| Grafana 看板 | 展示 GPU 利用率/内存/功耗 | 全方向 |
| 飞书告警集成 | 能收到测试告警 | 全方向 |
| `request_logs` schema v2 | 设计文档完成 | 横向协调 |
| Prometheus metrics 命名规范 | 统一前缀文档 | 横向协调 |

---

## 2. 实施任务拆解

### 2.1 Prometheus 部署（P0）

#### 任务 1.1: Prometheus Server 容器化部署
- **产出**: `deploy/prometheus/docker-compose.yml`
- **配置文件**: `deploy/prometheus/prometheus.yml`
- **存储**: 本地持久化 volume (后续迁移到 VictoriaMetrics)
- **端口**: 9090 (内网访问)
- **部署目标**: kaixuan-1 (192.168.31.28) 或 184 生产服务器

```yaml
# deploy/prometheus/docker-compose.yml
version: '3.8'
services:
  prometheus:
    image: prom/prometheus:v2.47.0
    container_name: llm-gateway-prometheus
    restart: unless-stopped
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus-data:/prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--storage.tsdb.retention.time=30d'
      - '--web.enable-lifecycle'

volumes:
  prometheus-data:
```

#### 任务 1.2: DCGM Exporter 集成
- **目标**: 采集 GPU 指标 (利用率/内存/功耗/温度)
- **前置条件**: GPU 节点已安装 NVIDIA DCGM
- **Exporter**: `nvidia/dcgm-exporter:3.1.8-3.1.5-ubuntu22.04`
- **端口**: 9400
- **采集间隔**: 15s

```yaml
# prometheus.yml scrape_configs
- job_name: 'dcgm-gpu'
  scrape_interval: 15s
  static_configs:
    - targets: ['192.168.31.28:9400']  # kaixuan-1 GPU node
      labels:
        cluster: 'kaixuan-1'
        gpu_type: 'rtx4090'
```

#### 任务 1.3: Node Exporter 集成
- **目标**: 采集系统指标 (CPU/内存/磁盘/网络)
- **端口**: 9100

---

### 2.2 Grafana 部署与看板（P0）

#### 任务 2.1: Grafana 容器化部署
```yaml
# deploy/prometheus/docker-compose.yml (追加)
  grafana:
    image: grafana/grafana:10.1.5
    container_name: llm-gateway-grafana
    restart: unless-stopped
    ports:
      - "3000:3000"
    volumes:
      - grafana-data:/var/lib/grafana
      - ./grafana/provisioning:/etc/grafana/provisioning
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=${GRAFANA_ADMIN_PASSWORD}
      - GF_INSTALL_PLUGINS=grafana-piechart-panel
```

#### 任务 2.2: 通用监控看板
创建 `deploy/prometheus/grafana/dashboards/llm-gateway-overview.json`，包含：

**Panel 1: GPU 利用率**
- 指标: `DCGM_FI_DEV_GPU_UTIL`
- 可视化: Time Series (折线图)
- 按 GPU index 分组

**Panel 2: GPU 内存使用率**
- 指标: `DCGM_FI_DEV_FB_USED / DCGM_FI_DEV_FB_FREE * 100`

**Panel 3: GPU 功耗**
- 指标: `DCGM_FI_DEV_POWER_USAGE`
- 单位: Watts

**Panel 4: GPU 温度**
- 指标: `DCGM_FI_DEV_GPU_TEMP`
- 单位: °C
- 告警阈值: >85°C

**Panel 5: 系统资源**
- CPU: `node_cpu_seconds_total`
- 内存: `node_memory_MemAvailable_bytes`
- 磁盘: `node_filesystem_avail_bytes`

---

### 2.3 飞书告警集成（P0）

#### 任务 3.1: Alertmanager 部署
```yaml
# deploy/prometheus/docker-compose.yml (追加)
  alertmanager:
    image: prom/alertmanager:v0.26.0
    container_name: llm-gateway-alertmanager
    restart: unless-stopped
    ports:
      - "9093:9093"
    volumes:
      - ./alertmanager.yml:/etc/alertmanager/alertmanager.yml
```

#### 任务 3.2: 飞书 Webhook 配置
```yaml
# deploy/prometheus/alertmanager.yml
global:
  resolve_timeout: 5m

route:
  receiver: 'lark-webhook'
  group_by: ['alertname', 'cluster']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 1h

receivers:
  - name: 'lark-webhook'
    webhook_configs:
      - url: '${LARK_WEBHOOK_URL}'
        send_resolved: true
```

#### 任务 3.3: 告警规则
创建 `deploy/prometheus/rules/gpu-alerts.yml`:

```yaml
groups:
  - name: gpu_alerts
    interval: 30s
    rules:
      - alert: GPUHighUtilization
        expr: DCGM_FI_DEV_GPU_UTIL > 90
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "GPU {{ $labels.gpu }} 利用率过高"
          description: "GPU {{ $labels.gpu }} 利用率持续 5 分钟 > 90% (当前: {{ $value }}%)"

      - alert: GPUHighTemperature
        expr: DCGM_FI_DEV_GPU_TEMP > 85
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "GPU {{ $labels.gpu }} 温度过高"
          description: "GPU {{ $labels.gpu }} 温度 > 85°C (当前: {{ $value }}°C)"

      - alert: GPUMemoryPressure
        expr: (DCGM_FI_DEV_FB_USED / (DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE)) > 0.95
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "GPU {{ $labels.gpu }} 内存不足"
          description: "GPU {{ $labels.gpu }} 内存使用率 > 95%"
```

---

### 2.4 `request_logs` Schema v2 设计（P0）

#### 任务 4.1: 统一字段扩展设计
创建 `docs/request_logs_schema_v2.md`，整合四个方向的字段需求：

**价格优化新增字段**:
```sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS compute_pool_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS pricing_tier TEXT; -- reserved | on_demand | spot
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cost_breakdown JSONB; -- {gpu, memory, network, shared}
```

**基础优化新增字段**:
```sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS content_safety_score NUMERIC(3,2);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS sensitive_words_matched TEXT[];
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS circuit_breaker_triggered BOOLEAN DEFAULT FALSE;
```

**衰减优化新增字段**:
```sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS network_latency_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS streaming_first_token_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS edge_node_id TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS connection_reused BOOLEAN DEFAULT FALSE;
```

**号池优化新增字段**:
```sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_pool_strategy TEXT; -- round_robin | wrr | least_loaded
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_wait_time_ms INT;
```

#### 任务 4.2: 分区表优化（可选）
评估 `request_logs` 是否需要转换为按月分区表（TimescaleDB / 原生 PG 分区）。

---

### 2.5 Prometheus Metrics 命名规范（P1）

#### 任务 5.1: 统一命名规范文档
创建 `docs/prometheus_metrics_naming.md`:

**命名规则**:
- 前缀: `llm_gateway_`
- 类型后缀: `_total` (counter), `_seconds` (histogram), `_bytes` (gauge)
- 标签命名: snake_case

**价格优化 metrics**:
```
llm_gateway_compute_pool_utilization{pool_id, gpu_type}
llm_gateway_spot_price_usd{pool_id, tier}
llm_gateway_cost_breakdown_usd{tenant_id, model, resource_type}
```

**号池优化 metrics**:
```
llm_gateway_credential_pool_utilization{pool_id, strategy}
llm_gateway_credential_wait_time_seconds{pool_id}
llm_gateway_credential_allocation_total{pool_id, status}
```

**衰减优化 metrics**:
```
llm_gateway_network_latency_seconds{edge_node, protocol}
llm_gateway_streaming_first_token_seconds{model, provider}
llm_gateway_connection_reuse_total{provider}
```

**基础优化 metrics**:
```
llm_gateway_circuit_breaker_state{provider, state}
llm_gateway_content_safety_violations_total{category}
llm_gateway_adapter_requests_total{provider, status}
```

---

## 3. 部署流程

### 3.1 前置条件检查
```bash
# 检查目标服务器
ssh root@192.168.31.28 "docker --version && docker-compose --version"

# 检查 GPU 可用性
ssh root@192.168.31.28 "nvidia-smi"

# 检查端口占用
ssh root@192.168.31.28 "netstat -tlnp | grep -E '9090|9093|3000|9400'"
```

### 3.2 部署步骤
```bash
# 1. 创建部署目录
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
mkdir -p deploy/prometheus/{grafana/provisioning/dashboards,rules}

# 2. 生成配置文件
# (见上述任务中的 YAML 配置)

# 3. 部署到 kaixuan-1
scp -r deploy/prometheus root@192.168.31.28:/opt/llm-gateway/
ssh root@192.168.31.28 "cd /opt/llm-gateway/prometheus && docker-compose up -d"

# 4. 验证服务
curl http://192.168.31.28:9090/-/healthy  # Prometheus
curl http://192.168.31.28:3000/api/health # Grafana
curl http://192.168.31.28:9400/metrics    # DCGM Exporter

# 5. 验证 GPU 指标采集
curl -s http://192.168.31.28:9090/api/v1/query?query=DCGM_FI_DEV_GPU_UTIL | jq
```

### 3.3 测试告警
```bash
# 触发测试告警（手动设置低阈值）
curl -X POST http://192.168.31.28:9090/-/reload

# 检查 Alertmanager
curl http://192.168.31.28:9093/api/v2/alerts

# 确认飞书收到告警
```

---

## 4. 验收清单

### 4.1 功能验收
- [ ] Prometheus 容器运行正常，能访问 Web UI (9090)
- [ ] DCGM Exporter 采集到 GPU 指标 (`DCGM_FI_DEV_GPU_UTIL` 有数据)
- [ ] Node Exporter 采集到系统指标
- [ ] Grafana 看板展示正常，至少 5 个 Panel 有数据
- [ ] Alertmanager 能接收 Prometheus 告警
- [ ] 飞书 Webhook 收到测试告警消息
- [ ] `request_logs` schema v2 文档审阅通过
- [ ] Prometheus metrics 命名规范文档审阅通过

### 4.2 性能验收
- [ ] Prometheus 采集间隔 ≤15s
- [ ] Grafana 看板加载时间 <2s
- [ ] 告警延迟 <1min (从指标超阈值到飞书收到)

### 4.3 数据质量验收
- [ ] GPU 指标无缺失（15s 间隔连续采集）
- [ ] 告警规则无误报（测试 10 次，误报率 <5%）

---

## 5. 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| kaixuan-1 GPU 节点无 DCGM | 🟡 中 | 🔴 高 | 预先检查 `nvidia-smi` + DCGM 安装脚本 |
| Prometheus 存储空间不足 | 🟢 低 | 🟡 中 | 设置 retention 30 天 + 监控磁盘使用 |
| 飞书 Webhook 限流 | 🟢 低 | 🟡 中 | 设置 repeat_interval 1h |
| `request_logs` 表锁表时间长 | 🟡 中 | 🟡 中 | 使用 `ADD COLUMN IF NOT EXISTS` + 分批执行 |

---

## 6. 下一步计划

### Week 2-5: Phase 1 并行开发
- **基础优化**: Circuit Breaker + Unified Adapter
- **号池优化**: WRR 调度器 + 水位监控
- **衰减优化**: HTTP/3 兼容性测试

### Week 6: Phase 2.0a
- **价格优化**: 统一成本模型 (`unified_rates` 表 + `CostBreakdown` 算法)

---

## 7. 文档产出

完成后提交以下文档：
1. `deploy/prometheus/README.md` — 部署与运维指南
2. `docs/request_logs_schema_v2.md` — 统一 schema 设计
3. `docs/prometheus_metrics_naming.md` — Metrics 命名规范
4. `docs/Phase0-验收报告.md` — 验收结果与截图

---

**创建日期**: 2026-07-18
**预计完成**: 2026-07-25
**当前状态**: 📋 待开始
