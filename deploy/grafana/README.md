# LLM Gateway Grafana Dashboards

这个目录包含 LLM Gateway Proxy 功能的 Grafana 监控面板配置文件。

## 面板列表

### 1. 概览面板 (Overview)
**文件:** `proxy-overview-dashboard.json`

提供 Proxy 系统的整体健康状况视图。

**主要可视化内容:**
- 订阅总数（活跃/非活跃）统计卡片
- 节点总数、可拨号节点数、不健康节点数
- 节点健康率趋势图（时间序列）
- 当前健康率仪表盘
- 订阅刷新成功率趋势
- 刷新状态分布（饼图）
- 平均刷新耗时仪表盘

**使用场景:** 快速了解系统整体状态，发现异常趋势

---

### 2. 订阅详情面板 (Subscription Details)
**文件:** `proxy-subscription-dashboard.json`

展示订阅相关操作的全局聚合性能和状态。

**主要可视化内容:**
- 全部订阅节点总数时间序列图
- 当前节点总数横向条形图
- 全部订阅的刷新成功率（5分钟窗口）
- 刷新耗时分位数（P50/P95/P99）
- 平均刷新耗时条形图
- 刷新失败原因分布（饼图）
- 刷新操作次数（按状态聚合）

**使用场景:** 观察全局订阅刷新状态与趋势

---

### 3. 节点详情面板 (Node Details)
**文件:** `proxy-node-dashboard.json`

监控代理节点的健康状态和连接质量。

**主要可视化内容:**
- 节点健康状态分布（饼图：健康/不健康/未探测）
- 节点数量按状态分类（条形图）
- 节点响应时间分布（直方图）
- 响应时间分位数（P50/P95/P99）
- 健康检查成功率时间序列
- 当前健康检查成功率仪表盘
- 健康检查速率（成功/失败）
- 健康检查总数（饼图）

**使用场景:** 诊断节点故障，识别响应慢或频繁失败的节点

---

### 4. 性能面板 (Performance)
**文件:** `proxy-performance-dashboard.json`

关注系统性能指标和资源使用。

**主要可视化内容:**
- 节点选择耗时分位数（P50/P95/P99）
- 平均节点选择耗时仪表盘
- 节点选择速率（操作/秒）
- 健康检查耗时分位数（P50/P95/P99）
- 平均健康检查耗时仪表盘
- 健康检查速率（操作/秒）
- Transport 缓存大小时间序列
- 当前缓存大小统计卡片
- Transport 失效总次数
- Transport 失效速率时间序列
- 当前失效速率仪表盘
- 性能指标对比图（选择/健康检查/刷新耗时）
- 性能摘要表格（平均值和 P95）

**使用场景:** 性能优化，识别瓶颈，监控缓存效率

---

## 导入步骤

### 方法 1: 通过 Grafana UI 导入

1. 登录 Grafana（默认地址：http://localhost:3000）
2. 点击左侧菜单 "+" → "Import"
3. 选择 "Upload JSON file" 或直接粘贴 JSON 内容
4. 选择文件：
   - `proxy-overview-dashboard.json`
   - `proxy-subscription-dashboard.json`
   - `proxy-node-dashboard.json`
   - `proxy-performance-dashboard.json`
5. 选择数据源：Prometheus
6. 点击 "Import"

### 方法 2: 通过 Provisioning 自动加载

1. 将 JSON 文件复制到 Grafana provisioning 目录：
   ```bash
   cp deploy/grafana/*.json /etc/grafana/provisioning/dashboards/
   ```

2. 创建 provisioning 配置文件 `/etc/grafana/provisioning/dashboards/llm-gateway.yaml`：
   ```yaml
   apiVersion: 1
   
   providers:
     - name: 'LLM Gateway'
       orgId: 1
       folder: 'LLM Gateway'
       type: file
       disableDeletion: false
       updateIntervalSeconds: 10
       allowUiUpdates: true
       options:
         path: /etc/grafana/provisioning/dashboards
         foldersFromFilesStructure: false
   ```

3. 重启 Grafana：
   ```bash
   systemctl restart grafana-server
   ```

### 方法 3: 使用 Docker Compose (推荐)

如果使用 Docker Compose 部署，已经配置了卷映射：

```yaml
grafana:
  image: grafana/grafana:latest
  volumes:
    - ./deploy/grafana:/etc/grafana/provisioning/dashboards:ro
```

只需重启 Grafana 容器：
```bash
docker-compose restart grafana
```

---

## 数据源配置

确保 Grafana 中已配置 Prometheus 数据源：

1. 在 Grafana UI 中，导航到 Configuration → Data Sources
2. 点击 "Add data source"
3. 选择 "Prometheus"
4. 配置 URL：`http://prometheus:9090`（如果使用 Docker Compose）
5. 点击 "Save & Test"

---

## 指标说明

所有面板使用以下 Prometheus 指标：

### 订阅指标
- `llm_gateway_proxy_subscriptions_total{state="active|inactive"}` - 订阅总数
- `llm_gateway_proxy_subscription_node_count` - 全部订阅中的节点总数
- `llm_gateway_proxy_subscription_refresh_total{status}` - 刷新操作计数（按状态聚合）
- `llm_gateway_proxy_subscription_refresh_duration_seconds` - 刷新耗时（直方图）

### 节点指标
- `llm_gateway_proxy_nodes_total` - 节点总数
- `llm_gateway_proxy_nodes_dialable` - 可拨号节点数
- `llm_gateway_proxy_nodes_unhealthy` - 不健康节点数
- `llm_gateway_proxy_node_response_time_ms` - 节点响应时间（直方图）
- `llm_gateway_proxy_node_health_check_total{status}` - 健康检查计数
- `llm_gateway_proxy_node_health_check_duration_seconds` - 健康检查耗时（直方图）

### 性能指标
- `llm_gateway_proxy_node_selection_duration_seconds` - 节点选择耗时（直方图）
- `llm_gateway_proxy_transport_cache_size` - Transport 缓存大小
- `llm_gateway_proxy_transport_invalidations_total` - Transport 失效次数

---

## 刷新间隔

所有面板默认配置：
- **自动刷新间隔:** 30 秒
- **默认时间范围:** 最近 1 小时
- **可选刷新间隔:** 10s, 30s, 1m, 5m, 15m, 30m, 1h

可以在 Grafana UI 右上角调整这些设置。

---

## 告警建议

建议为以下情况配置告警：

1. **节点健康率 < 80%**
   ```promql
   (llm_gateway_proxy_nodes_total - llm_gateway_proxy_nodes_unhealthy) / llm_gateway_proxy_nodes_total < 0.8
   ```

2. **订阅刷新成功率 < 90%（全局聚合）**
   ```promql
   rate(llm_gateway_proxy_subscription_refresh_total{status="success"}[5m]) / 
   (rate(llm_gateway_proxy_subscription_refresh_total{status="success"}[5m]) + 
    rate(llm_gateway_proxy_subscription_refresh_total{status="failure"}[5m])) < 0.9
   ```

3. **节点选择耗时 P95 > 1秒**
   ```promql
   histogram_quantile(0.95, rate(llm_gateway_proxy_node_selection_duration_seconds_bucket[5m])) > 1
   ```

4. **不健康节点数 > 5**
   ```promql
   llm_gateway_proxy_nodes_unhealthy > 5
   ```

---

## 故障排查

### 面板无数据显示

1. 检查 Prometheus 是否正在采集指标：
   ```bash
   curl http://localhost:9090/api/v1/query?query=llm_gateway_proxy_nodes_total
   ```

2. 检查 LLM Gateway 是否正在暴露 metrics：
   ```bash
   curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" http://localhost:8781/metrics
   ```

3. 检查时间范围是否正确（右上角时间选择器）

### 指标标签缺失

所有 Proxy 面板均使用低基数聚合指标；节点和订阅标识不作为 Prometheus 标签。若面板无数据，请检查 metrics 端点及 Prometheus 抓取状态。

### 刷新间隔太长

如果发现数据更新不及时，可以：
1. 减小面板刷新间隔（右上角刷新图标）
2. 减小 Prometheus scrape_interval（在 `prometheus.yml` 中配置）

---

## 自定义

所有面板配置都是标准的 Grafana JSON 格式，可以：
- 在 Grafana UI 中直接编辑
- 修改 JSON 文件后重新导入
- 添加新的面板或查询
- 调整颜色、阈值、单位等

修改后建议导出更新的 JSON 文件以便版本控制。

---

## 版本信息

- **Grafana 版本要求:** 8.0 或更高
- **数据源:** Prometheus
- **创建日期:** 2026-08-29
- **维护者:** LLM Gateway Team

---

## 相关文档

- [Prometheus Metrics 规范](../prometheus/README.md)
- [部署指南](../DEPLOYMENT_GUIDE.md)
- [LLM Gateway 文档](../../README.md)
