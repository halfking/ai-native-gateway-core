# 衰减优化 — 总体说明

> 本文档系列分析第三方调度场景下的算力损耗与高延迟问题，基于 llm-gateway-go 代码库当前架构，给出分层优化方案。
>
> 最后更新: 2026-07-18 | **已完成二次审计 (AUDIT_V2.md)，发现10个关键问题并提供修正方案**

---

## 📋 文档导航

| 文档 | 说明 | 状态 |
|------|------|------|
| **INDEX.md** (本文档) | 总体说明与快速导航 | ✅ 完成 |
| **AUDIT.md** | 基于开源项目最佳实践的审计报告 | ✅ 完成 |
| **AUDIT_V2.md** | 二次审计报告（批判性复核，发现10个问题） | ✅ 完成 |
| **IMPLEMENTATION_PLAN.md** | 分4阶段、12周的执行计划 | ⚠️ 需根据AUDIT_V2更新 |
| **01-网络层优化.md** | 专线/HTTP3/连接池详细方案 | ✅ 完成 |
| **02-算力损耗补偿.md** | Streaming/边缘调度详细方案 | ✅ 完成 |
| **03-现有能力映射.md** | 代码现状与差距分析 | ✅ 完成 |

---

## 🎯 核心发现 (来自 AUDIT.md)

### 关键问题

| 问题 | 影响 | 优先级 | 参考 |
|------|------|--------|------|
| 🔴 **连接池参数过于保守** | MaxIdleConnsPerHost=16 vs 行业标准64-128 | **P0立即** | Kong/Envoy基准 |
| 🔴 **HTTP/2 Server Push未使用** | 错失40%重试延迟优化 | **P0新增** | Kong实践 |
| 🟡 **Stream缓冲8-chunk过时** | 增加150-300ms首Token延迟 | **P0修正** | APISIX 2-chunk |
| 🟡 **QUIC优先级被高估** | 稳定网络收益<15%，投入产出比低 | P2→P3降级 | Cloudflare数据 |

### 立即行动 (本周可完成)

```bash
# 零代码配置调优 — 预期收益: TTFB ↓ 20-30%
1. MaxIdleConnsPerHost: 16 → 64 (pool/pool.go:24)
2. MaxConnsPerHost: 64 → 256 (pool/pool.go:25)
3. IdleConnTimeout: 90s → 180s (pool/pool.go:26)
4. HTTP/2 MaxConcurrentStreams: 250 → 1000 (cmd/gateway/main.go)
5. emptyGateMaxChunks: 8 → 2 (domains/streaming/stream.go:32)

# 1周内部署 → 验证48小时 → 金丝雀发布
```

---

## 二次审计关键发现 (AUDIT_V2.md)

**审计日期**: 2026-07-18 | **发现**: 🔴 4个严重问题、🟡 5个中等问题、🟢 1个轻微问题

**核心结论**: 初版方案存在参数配置冲突、实施风险和遗漏优化点，需前置修正才能启动 Phase 0。

| 编号 | 问题 | 影响 | 修正优先级 |
|------|------|------|-----------|
| **A1** | 🔴 双层配置冲突 (pool 16 vs upstream 32) | Phase 0收益从30%降至15-20% | P0-PRE |
| **A2** | 🔴 HTTP/2 Server Push 在 h2c 下不可用 | 40%优化目标无法实现 | P1 (改用Link Preload) |
| **A3** | 🔴 回滚脚本不覆盖 DB/Nginx/Env | 回滚失败风险 | P0-PRE |
| **A4** | 🔴 金丝雀方案缺流量控制 | 无法灰度发布 | P0-PRE (Nginx weight配置) |
| **A6** | 🟡 DNS缓存缺失 | 每次解析+10-20ms | P1 (Phase 1新增) |
| **A7** | 🟡 TLS Session复用未启用 | 握手开销1-RTT | P1 (Phase 1新增) |
| **A9** | 🟡 Prometheus分桶不合理 | P99观测精度差 | P1 |

**修正后的收益预测**:
- Phase 0: ~~20-30%~~ → **15-20%** (因A1配置冲突打折)
- Phase 1: ~~15%~~ → **25-30%** (A6+A7 DNS/TLS优化被初版遗漏)
- **总计**: 40-50% TTFB优化 (目标不变，但执行路径调整)

**前置任务（Day 0.5，必须在 Phase 0 前完成）**:
```bash
# A1: 统一代码参数
vim pool/pool.go        # maxIdleConnsPerHost: 16→64
vim upstream/client.go  # MaxIdleConnsPerHost: 32→64

# A3: 增强回滚脚本
vim scripts/rollback-optimization.sh  # 增加DB/Nginx/Env回滚

# A4: 配置金丝雀
ssh root@192.168.1.252
vim /etc/nginx/conf.d/llm-gateway-upstream.conf
# 添加: upstream llm_gateway_canary { server 192.168.1.71:8781 weight=1; }
```

详见 **AUDIT_V2.md** 完整报告。

---

## 问题背景

第三方调度（跨云调度、通过第三方代理调用）通常会引入 **5%–20% 的算力损耗或高延迟**。主要来源：

| 衰减类型 | 来源 | 典型幅度 | 优化方向 |
|---------|------|---------|---------|
| 物理衰减 | 跨地域网络延迟、公网 QoS 不稳定 | 10–100ms RTT 增加 | 专线/同地域 |
| 技术衰减 | TCP 握手、TLS 协商、代理转发 | 每次请求 1–3 次 RTT | 连接池/HTTP2 |
| 架构衰减 | 集中式调度的回源延迟、鉴权串行化 | 50–200ms 调度延迟 | 边缘调度 |
| 协议衰减 | HTTP/1.1 队头阻塞、短连接重复建连 | 10–50ms 每请求 | HTTP/2多路复用 |

---

## 优化方向与文档结构

```
衰减优化/
├── INDEX.md (本文档)           ← 总览 + 快速开始
├── AUDIT.md                    ← 审计报告 (Kong/Envoy/APISIX最佳实践)
├── IMPLEMENTATION_PLAN.md      ← 执行计划 (Phase 0-3, 12周)
│
├── 01-网络层优化.md            ← 物理衰减 + 技术衰减
│   ├── 专线与同地域部署
│   ├── HTTP/3 (QUIC) 协议 [已降级到P3]
│   └── 长连接连接池优化 [P0立即执行]
│
├── 02-算力损耗补偿.md          ← 架构衰减 + 协议衰减
│   ├── 流式传输优化 (Streaming) [P0]
│   │   ├── Stream缓冲2-chunk策略
│   │   ├── HTTP/2 Server Push [新增P0]
│   │   └── Pre-Stream Keepalive自适应
│   └── 边缘调度 (Edge Scheduling) [P3]
│
└── 03-现有能力映射.md          ← 代码现状对照
    ├── 已具备能力 (已投产) - 18项
    ├── 部分具备 (需加固) - 7项
    └── 缺失能力 (需开发) - 9项
```

---

## 修正后的优先级 (基于审计)

| 优先级 | 项目 | 原优先级 | 预期收益 | 工程量 | 执行时间 |
|--------|------|---------|---------|--------|---------|
| **P0** | **连接池参数调优** | P1 | TTFB ↓ 15-25% | 极小 | Week 1 |
| **P0** | **HTTP/2 SETTINGS调优** | 无 | 并发 ↑ 4x | 极小 | Week 1 |
| **P0** | **Stream缓冲2-chunk** | P0 | TTFB ↓ 20-30% | 极小 | Week 1 |
| **P0** | **HTTP/2 Server Push** | 无 | 重试延迟 ↓ 40% | 小 | Week 2 |
| **P0** | **Keepalive自适应** | P0 | 超时率 ↓ 50% | 小 | Week 2 |
| P1 | TTFB分位统计 + Prometheus | P1 | 可观测性 | 中 | Week 3-4 |
| P1 | 连接预热 (Prewarm) | P1 | 冷启动 ↓ 60% | 中 | Week 5-6 |
| P2 | Zero-Copy流式直通 | P2 | CPU ↓ 10-15% | 中 | Week 7-8 |
| P2 | 专线与同地域部署 | P2 | RTT ↓ 80% | 大 | Week 9-10 |
| **P3** | HTTP/3 (QUIC) | P2 | 不稳定网络场景 | 中 | 降级 |
| P3 | 边缘调度 Phase 1-2 | P3 | 回源 ↓ 30-50% | 大 | Week 11-12 |

---

## 关键指标 (KPI) - 修正版

| 指标 | 当前 | Week 1目标 | Week 12目标 | 衡量方式 |
|------|------|-----------|------------|---------|
| **P50 TTFB (同地域)** | ~200ms | **<150ms** | <100ms | TTFBTracker 分位统计 |
| **P50 TTFB (跨云)** | ~500ms | **<350ms** | <250ms | TTFBTracker 分位统计 |
| **连接复用率** | ~60% | **>85%** | >90% | Prometheus指标 |
| **P99 TTFB (跨云)** | ~2000ms | **<1500ms** | <1000ms | Histogram分位 |
| 每秒新建连接数 | ~150 | **<50** | <30 | pool_connections_created_total |
| Streaming 启用率 | ~70% | 70% | >95% | 请求日志统计 |
| 首个 Token 返回时间 | ~800ms | **<600ms** | <400ms | 端到端分位统计 |
| HTTP/2流复用率 | 未知 | **>60%** | >80% | h2_streams_total |

---

## 快速开始

### 第一步：阅读审计报告

```bash
# 了解具体优化参数和理由
cat docs/衰减优化/AUDIT.md
```

**重点关注**：
- 第1节：连接池参数具体值（对比Kong/Envoy/Nginx标准）
- 第2节：HTTP/2 Server Push实现（Kong代码参考）
- 第7节：修正后的优先级矩阵

### 第二步：执行 Phase 0 (本周)

```bash
# 阅读执行计划
cat docs/衰减优化/IMPLEMENTATION_PLAN.md

# Phase 0: 零代码配置调优 (Week 1)
# Day 1-3: 修改配置文件
# Day 4-5: 测试环境验证
# Day 6-7: 生产金丝雀发布
```

**可直接复制的代码片段**：见 `IMPLEMENTATION_PLAN.md` Phase 0部分。

### 第三步：监控与验证

```bash
# 导入Grafana Dashboard
curl -X POST http://grafana.internal/api/dashboards/db \
  -H "Content-Type: application/json" \
  -d @docs/衰减优化/grafana-dashboard.json

# 监控关键指标
# - 连接复用率 (目标>85%)
# - P50 TTFB (目标降低20%+)
# - 内存/CPU (确保不增长>10%)
```

---

## 投入产出比分析

| 阶段 | 时间投入 | 代码变更 | 预期收益 | ROI |
|------|---------|---------|---------|-----|
| **Phase 0** | **1周** | 配置调整 (5处) | **TTFB ↓ 20-30%** | **极高** ⭐⭐⭐⭐⭐ |
| Phase 1 | 2周 | 新增2个功能 | TTFB ↓ 额外15% | 高 ⭐⭐⭐⭐ |
| Phase 2 | 3周 | 监控 + Prewarm | 可观测性完善 | 中 ⭐⭐⭐ |
| Phase 3 | 6周 | 架构优化 | 长期收益 | 中低 ⭐⭐ |

**建议**：优先执行 Phase 0（1周内见效），然后根据实测效果决定是否继续 Phase 1-3。

---

## 风险控制

### 回滚机制

所有变更均支持**1分钟内配置回滚**：

```bash
# 立即回滚脚本
bash scripts/rollback-optimization.sh

# 自动触发条件:
# - 错误率 > 1% 持续5分钟
# - P99延迟增加 > 50%
# - 内存增长 > 30%
```

### 金丝雀发布

```
Week 1: 10% 流量 (kaixuan-1测试环境)
  ↓ 监控48小时，指标达标
Week 2: 50% 流量 (生产环境)
  ↓ 监控72小时，无异常
Week 3: 100% 流量切换
```

---

## 相关资源

- Kong Gateway Performance: https://docs.konghq.com/gateway/latest/production/performance/
- Envoy HTTP/2 Best Practices: https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_conn_man/http2
- APISIX Streaming Proxy: https://apisix.apache.org/docs/apisix/stream-proxy/
- Cloudflare QUIC Analysis: https://blog.cloudflare.com/http-3-vs-http-2/

---

## 联系方式

- 技术 Owner: Infrastructure Team
- 审计执行: 2026-07-18
- 下次复审: Phase 0 完成后 (预计 2026-07-25)
