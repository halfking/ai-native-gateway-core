# LLM Gateway 运维仪表盘配置

## Grafana Dashboard JSON

完整的 Grafana Dashboard 配置，包含所有关键指标。

### 仪表盘概述

**名称：** LLM Gateway 运维监控  
**刷新间隔：** 30 秒  
**时间范围：** 最近 1 小时（可调整）

---

## Dashboard 配置（JSON）

```json
{
  "dashboard": {
    "id": null,
    "uid": "llm-gateway-ops",
    "title": "LLM Gateway 运维监控",
    "tags": ["llm-gateway", "operations"],
    "timezone": "browser",
    "schemaVersion": 30,
    "version": 1,
    "refresh": "30s",
    
    "panels": [
      {
        "id": 1,
        "title": "🎯 请求成功率（5分钟）",
        "type": "stat",
        "gridPos": {"h": 4, "w": 6, "x": 0, "y": 0},
        "targets": [{
          "expr": "rate(llm_gateway_requests_total{status=\"200\"}[5m]) / rate(llm_gateway_requests_total[5m]) * 100",
          "legendFormat": "成功率"
        }],
        "options": {
          "graphMode": "area",
          "colorMode": "value",
          "unit": "percent",
          "decimals": 2
        },
        "thresholds": {
          "mode": "absolute",
          "steps": [
            {"value": 0, "color": "red"},
            {"value": 95, "color": "yellow"},
            {"value": 99, "color": "green"}
          ]
        }
      },
      
      {
        "id": 2,
        "title": "📊 总请求数（5分钟）",
        "type": "stat",
        "gridPos": {"h": 4, "w": 6, "x": 6, "y": 0},
        "targets": [{
          "expr": "sum(rate(llm_gateway_requests_total[5m])) * 300",
          "legendFormat": "请求数"
        }],
        "options": {
          "graphMode": "area",
          "colorMode": "value",
          "unit": "short"
        }
      },
      
      {
        "id": 3,
        "title": "⚡ P95 响应延迟",
        "type": "stat",
        "gridPos": {"h": 4, "w": 6, "x": 12, "y": 0},
        "targets": [{
          "expr": "histogram_quantile(0.95, rate(llm_gateway_request_duration_seconds_bucket[5m]))",
          "legendFormat": "P95"
        }],
        "options": {
          "graphMode": "area",
          "colorMode": "value",
          "unit": "s",
          "decimals": 2
        },
        "thresholds": {
          "mode": "absolute",
          "steps": [
            {"value": 0, "color": "green"},
            {"value": 2, "color": "yellow"},
            {"value": 5, "color": "red"}
          ]
        }
      },
      
      {
        "id": 4,
        "title": "🔧 可用模型数",
        "type": "stat",
        "gridPos": {"h": 4, "w": 6, "x": 18, "y": 0},
        "targets": [{
          "expr": "llm_gateway_available_model_bindings",
          "legendFormat": "可用模型"
        }],
        "options": {
          "graphMode": "none",
          "colorMode": "value",
          "unit": "short"
        },
        "thresholds": {
          "mode": "absolute",
          "steps": [
            {"value": 0, "color": "red"},
            {"value": 100, "color": "yellow"},
            {"value": 500, "color": "green"}
          ]
        }
      },
      
      {
        "id": 5,
        "title": "📈 请求趋势（QPS）",
        "type": "graph",
        "gridPos": {"h": 8, "w": 12, "x": 0, "y": 4},
        "targets": [
          {
            "expr": "rate(llm_gateway_requests_total{status=\"200\"}[1m])",
            "legendFormat": "成功 - {{method}}"
          },
          {
            "expr": "rate(llm_gateway_requests_total{status!=\"200\"}[1m])",
            "legendFormat": "失败 - {{status}}"
          }
        ],
        "yaxes": [
          {"format": "reqps", "label": "Requests/sec"},
          {"format": "short"}
        ],
        "legend": {
          "show": true,
          "alignAsTable": true,
          "values": true,
          "current": true,
          "avg": true
        }
      },
      
      {
        "id": 6,
        "title": "⏱️ 响应时间分布",
        "type": "graph",
        "gridPos": {"h": 8, "w": 12, "x": 12, "y": 4},
        "targets": [
          {
            "expr": "histogram_quantile(0.50, rate(llm_gateway_request_duration_seconds_bucket[5m]))",
            "legendFormat": "P50"
          },
          {
            "expr": "histogram_quantile(0.95, rate(llm_gateway_request_duration_seconds_bucket[5m]))",
            "legendFormat": "P95"
          },
          {
            "expr": "histogram_quantile(0.99, rate(llm_gateway_request_duration_seconds_bucket[5m]))",
            "legendFormat": "P99"
          }
        ],
        "yaxes": [
          {"format": "s", "label": "Latency"},
          {"format": "short"}
        ]
      },
      
      {
        "id": 7,
        "title": "❌ 错误类型分布",
        "type": "piechart",
        "gridPos": {"h": 8, "w": 8, "x": 0, "y": 12},
        "targets": [{
          "expr": "sum by (error_code) (rate(llm_gateway_requests_total{status!=\"200\"}[5m]))",
          "legendFormat": "{{error_code}}"
        }],
        "options": {
          "pieType": "pie",
          "legend": {
            "displayMode": "list",
            "placement": "right"
          }
        }
      },
      
      {
        "id": 8,
        "title": "🔥 热门模型（请求数）",
        "type": "table",
        "gridPos": {"h": 8, "w": 8, "x": 8, "y": 12},
        "targets": [{
          "expr": "topk(10, sum by (model) (rate(llm_gateway_requests_total[5m])))",
          "format": "table",
          "instant": true
        }],
        "transformations": [
          {
            "id": "organize",
            "options": {
              "renameByName": {
                "model": "模型",
                "Value": "QPS"
              }
            }
          }
        ]
      },
      
      {
        "id": 9,
        "title": "🏥 凭据健康状态",
        "type": "bargauge",
        "gridPos": {"h": 8, "w": 8, "x": 16, "y": 12},
        "targets": [
          {
            "expr": "llm_gateway_credentials_total{status=\"active\"}",
            "legendFormat": "活跃"
          },
          {
            "expr": "llm_gateway_credentials_total{circuit_state=\"open\"}",
            "legendFormat": "熔断"
          },
          {
            "expr": "llm_gateway_credentials_total{quota_state=\"exhausted\"}",
            "legendFormat": "配额耗尽"
          }
        ],
        "options": {
          "orientation": "horizontal",
          "displayMode": "gradient"
        }
      },
      
      {
        "id": 10,
        "title": "📋 模型发现状态",
        "type": "timeseries",
        "gridPos": {"h": 6, "w": 12, "x": 0, "y": 20},
        "targets": [{
          "expr": "llm_gateway_model_discovery_models_count",
          "legendFormat": "发现的模型数"
        }],
        "options": {
          "tooltip": {"mode": "multi"},
          "legend": {"displayMode": "list"}
        },
        "fieldConfig": {
          "defaults": {
            "color": {"mode": "palette-classic"},
            "custom": {
              "lineWidth": 2,
              "fillOpacity": 10
            },
            "thresholds": {
              "mode": "absolute",
              "steps": [
                {"value": 0, "color": "red"},
                {"value": 100, "color": "yellow"},
                {"value": 500, "color": "green"}
              ]
            }
          }
        }
      },
      
      {
        "id": 11,
        "title": "💾 数据库性能",
        "type": "timeseries",
        "gridPos": {"h": 6, "w": 12, "x": 12, "y": 20},
        "targets": [
          {
            "expr": "pg_stat_database_tup_fetched{datname=\"llm_gateway\"}",
            "legendFormat": "行读取"
          },
          {
            "expr": "pg_stat_database_tup_inserted{datname=\"llm_gateway\"}",
            "legendFormat": "行插入"
          }
        ]
      },
      
      {
        "id": 12,
        "title": "🚨 告警历史",
        "type": "table",
        "gridPos": {"h": 6, "w": 24, "x": 0, "y": 26},
        "targets": [{
          "expr": "ALERTS{job=\"llm-gateway\"}",
          "format": "table",
          "instant": true
        }],
        "transformations": [
          {
            "id": "organize",
            "options": {
              "renameByName": {
                "alertname": "告警名称",
                "severity": "严重程度",
                "alertstate": "状态"
              }
            }
          }
        ]
      }
    ],
    
    "templating": {
      "list": [
        {
          "name": "datasource",
          "type": "datasource",
          "query": "prometheus"
        },
        {
          "name": "interval",
          "type": "interval",
          "query": "30s,1m,5m,10m,30m,1h",
          "current": {
            "value": "5m"
          }
        }
      ]
    },
    
    "annotations": {
      "list": [
        {
          "name": "部署事件",
          "datasource": "prometheus",
          "expr": "llm_gateway_deployment_timestamp",
          "tagKeys": "version",
          "textFormat": "部署 {{version}}"
        }
      ]
    }
  }
}
```

---

## 导入步骤

### 1. 通过 UI 导入

1. 登录 Grafana：`http://grafana:3000`
2. 点击 **+** → **Import**
3. 粘贴上面的 JSON 配置
4. 选择 Prometheus 数据源
5. 点击 **Import**

### 2. 通过 API 导入

```bash
curl -X POST http://admin:admin@grafana:3000/api/dashboards/db \
  -H "Content-Type: application/json" \
  -d @dashboard.json
```

### 3. 通过 Terraform

```hcl
resource "grafana_dashboard" "llm_gateway" {
  config_json = file("${path.module}/dashboard.json")
  folder      = grafana_folder.operations.id
}
```

---

## 快速访问链接

创建书签：
- **主监控面板**：`http://grafana:3000/d/llm-gateway-ops`
- **告警列表**：`http://grafana:3000/alerting/list`
- **日志查询**：`http://grafana:3000/explore?datasource=Loki&query={job="llm-gateway"}`

---

## 自定义变量

Dashboard 支持以下变量：

| 变量 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `$datasource` | datasource | prometheus | Prometheus 数据源 |
| `$interval` | interval | 5m | 查询时间间隔 |
| `$credential_id` | query | All | 筛选特定凭据 |
| `$model` | query | All | 筛选特定模型 |

---

## 告警规则集成

Dashboard 会自动显示以下告警：

- 🔴 **Critical**: 模型发现失败、请求成功率<95%
- 🟡 **Warning**: P95延迟>5s、熔断器打开
- 🔵 **Info**: 配额即将耗尽

---

## 维护建议

### 每日检查
- 查看请求成功率趋势
- 检查错误类型分布
- 关注热门模型性能

### 每周检查
- 审查告警历史
- 分析性能趋势
- 优化慢查询

### 每月检查
- 更新仪表盘配置
- 添加新的业务指标
- 清理过期数据

---

**创建日期：** 2026-07-03  
**维护者：** AI 运维团队  
**Grafana 版本：** 9.x+
