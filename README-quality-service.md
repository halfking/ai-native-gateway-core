# 质量评分服务 (Quality Service)

## 概述

质量评分服务是一个独立的微服务，负责计算和提供 LLM 供应商的质量画像。

## 功能

### 1. 定时调度器
- 定期计算所有活跃供应商的质量评分
- 默认每 5 分钟执行一次
- 自动保存到数据库

### 2. HTTP API
- `GET /api/quality/profile` - 查询质量画像
- `POST /api/quality/calculate` - 立即计算
- `GET /api/quality/list` - 列出所有画像
- `GET /health` - 健康检查

### 3. 质量评分体系
- L1 可用性 (35%权重) - 成功率、错误率
- L2 性能 (25%权重) - 延迟、TTFT
- L3 可信度 (20%权重) - 稳定性、一致性
- L4 稳定性 (15%权重) - 抖动、波动
- L5 成本效益 (5%权重) - 性价比

## 编译

```bash
go build -o bin/quality-service ./cmd/quality-service
```

## 运行

### 启动服务 (含调度器)
```bash
export DATABASE_URL="postgres://user:pass@host:port/dbname"
./bin/quality-service \
  -http=:8081 \
  -scheduler=true \
  -interval=5m
```

### 仅启动 API (不含调度器)
```bash
./bin/quality-service \
  -http=:8081 \
  -scheduler=false
```

## API 使用示例

### 查询质量画像
```bash
curl "http://localhost:8081/api/quality/profile?provider_id=587&model=claude-3-5-sonnet-20241022"
```

响应:
```json
{
  "provider_id": 587,
  "model_name": "claude-3-5-sonnet-20241022",
  "availability_score": 98.5,
  "performance_score": 92.3,
  "reliability_score": 95.8,
  "stability_score": 91.2,
  "cost_efficiency_score": 88.5,
  "quality_score": 94.6,
  "calculated_at": "2026-07-19T03:00:00Z",
  "updated_at": "2026-07-19T03:05:00Z"
}
```

### 立即计算质量评分
```bash
curl -X POST "http://localhost:8081/api/quality/calculate?provider_id=587&model=gpt-4"
```

### 列出所有质量画像
```bash
curl "http://localhost:8081/api/quality/list"
```

响应:
```json
{
  "profiles": [
    {
      "provider_id": 587,
      "model_name": "claude-3-5-sonnet-20241022",
      "quality_score": 94.6,
      ...
    },
    ...
  ],
  "count": 10
}
```

### 健康检查
```bash
curl "http://localhost:8081/health"
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `DATABASE_URL` | PostgreSQL 连接串 | 必填 |

## 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-db` | 数据库 URL | `$DATABASE_URL` |
| `-http` | HTTP 监听地址 | `:8081` |
| `-scheduler` | 启用调度器 | `true` |
| `-interval` | 调度间隔 | `5m` |

## 日志

服务使用结构化日志 (JSON 格式):

```json
{"level":"INFO","msg":"quality scheduler started","module":"quality-scheduler","interval":"5m"}
{"level":"INFO","msg":"found active providers","module":"quality-scheduler","count":10}
{"level":"INFO","msg":"availability score calculated","module":"quality","component":"availability","provider_id":587,"model":"claude-3-5-sonnet-20241022","score":98.5}
{"level":"INFO","msg":"quality calculation run completed","module":"quality-scheduler","duration":"2.5s","success":10,"failed":0,"total":10}
```

## 数据库表

服务依赖以下数据库表:

- `provider_metrics_minute` - 分钟级指标 (数据源)
- `provider_metrics_hour` - 小时级指标 (数据源)
- `provider_quality_profiles` - 质量画像 (输出)

## 部署

### Docker
```bash
docker build -t quality-service .
docker run -d \
  -p 8081:8081 \
  -e DATABASE_URL="postgres://..." \
  quality-service
```

### Systemd
```ini
[Unit]
Description=Quality Service
After=network.target

[Service]
Type=simple
User=app
Environment="DATABASE_URL=postgres://..."
ExecStart=/opt/quality-service/bin/quality-service
Restart=always

[Install]
WantedBy=multi-user.target
```

## 监控

### Prometheus Metrics
TODO: 实现 `/metrics` 端点

### 健康检查
```bash
# 周期性健康检查
watch -n 10 'curl -s http://localhost:8081/health'
```

## 故障排查

### 调度器未运行
检查日志是否有 "quality scheduler started"

### API 返回 404
确认数据库中有对应的质量画像数据

### 计算失败
检查数据库中是否有最近 24 小时的 metrics 数据

## 开发

### 添加新的评分维度
1. 在 `internal/quality/` 创建 `scorer_<name>.go`
2. 实现 `Scorer` 接口
3. 在 `ProfileCalculator` 中注册
4. 更新数据库 schema

### 运行测试
```bash
go test ./internal/quality/...
```

## License

Internal Use Only
