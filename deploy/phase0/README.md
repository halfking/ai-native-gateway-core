# Phase 0: 零代码配置调优

> **目标**: TTFB ↓ 15-20%  
> **方式**: 纯配置变更，无代码风险  
> **基于**: AUDIT_V2 修正 + IMPLEMENTATION_PLAN Phase 0  
> **状态**: ✅ 前置任务已完成，可以部署

---

## 快速开始

### 本地测试（推荐先做）

```bash
# 1. 导出配置
export $(cat deploy/phase0/optimization.env | grep -v '^#' | xargs)

# 2. 运行服务
go run cmd/gateway/main.go

# 3. 验证（另一个终端）
bash deploy/phase0/verify.sh local
```

### 部署到 kaixuan-1

```bash
# 1. 部署
bash deploy/phase0/deploy.sh --target=kaixuan-1

# 2. 验证
bash deploy/phase0/verify.sh kaixuan-1

# 3. 监控 24 小时
watch -n 60 'ssh root@192.168.31.28 "curl -s http://localhost:8781/metrics | grep -E ttfb"'
```

---

## 配置参数说明

| 参数 | 原值 | 新值 | 影响 |
|------|------|------|------|
| `MAX_IDLE_CONNS_PER_HOST` | 16 | **64** | 连接复用率 ↑ 4x |
| `MAX_CONNS_PER_HOST` | 64 | **256** | 并发能力 ↑ 4x |
| `IDLE_CONN_TIMEOUT` | 90s | **180s** | 连接保持时间 ↑ 2x |
| `HTTP2_MAX_CONCURRENT_STREAMS` | 100 | **250** | HTTP/2 并发 ↑ 2.5x |
| `CONNECT_TIMEOUT` | 10s | **5s** | 连接超时 ↓ 50% |
| `TCP_KEEPALIVE` | 30s | **60s** | Keep-alive 间隔 ↑ 2x |

**关键修正（AUDIT_V2 A1）**:
- ✅ 代码已统一参数（pool.go + upstream/client.go 都改为 64）
- ✅ 配置与代码对齐，不会冲突

---

## 预期收益

**Phase 0（本次）**:
- TTFB P99: 800-1500ms → **650-1200ms** (↓ 15-20%)
- 连接复用率: 30% → **70%**
- 新建连接延迟: 100-300ms → **20-50ms**

**累计（Phase 0 + Phase 1）**:
- TTFB P99: 800-1500ms → **400-750ms** (↓ 40-50%)

---

## 回滚

如果出现问题，立即回滚：

```bash
# 方式 1: 使用回滚脚本
bash scripts/rollback-optimization.sh

# 方式 2: 手动回滚（< 1分钟）
ssh root@<target> "systemctl restart llm-gateway"
```

备份位置: `/opt/llm-gateway/backups/pre-phase0-*/`

---

## 监控指标

### 关键指标

```bash
# TTFB 分位数
curl -s http://localhost:8781/metrics | grep ttfb_seconds

# 连接池状态
curl -s http://localhost:8781/metrics | grep pool

# 错误率
curl -s http://localhost:8781/metrics | grep http_requests_total
```

### 预期变化

| 指标 | 优化前 | 优化后 | 说明 |
|------|--------|--------|------|
| `ttfb_seconds{quantile="0.99"}` | 1.2-1.5 | **0.95-1.2** | P99 延迟 |
| `pool_idle_connections` | 5-10 | **20-40** | 空闲连接增加 |
| `pool_active_connections` | 2-5 | **持平** | 活跃连接不变 |
| `http_requests_total{code="5xx"}` | <1% | **<0.5%** | 错误率下降 |

---

## 文件清单

```
deploy/phase0/
├── README.md (本文件)
├── optimization.env (配置文件)
├── deploy.sh (部署脚本)
└── verify.sh (验证脚本)
```

---

## 相关文档

- **审计报告**: `docs/衰减优化/AUDIT_V2.md`
- **执行计划**: `docs/衰减优化/IMPLEMENTATION_PLAN.md`
- **回滚脚本**: `scripts/rollback-optimization.sh`
- **金丝雀配置**: `deploy/nginx/llm-gateway-canary.conf`

---

**最后更新**: 2026-07-19  
**状态**: ✅ 就绪，可以部署
