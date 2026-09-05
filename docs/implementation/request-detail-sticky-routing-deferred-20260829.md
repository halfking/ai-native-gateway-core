# Change Proposal: 跨副本可见性方案 B (Sticky 路由) — 不实施决策记录

**日期**: 2026-08-29  
**作者**: Request-Detail 审计闭环第五轮（方案 B 调研）  
**状态**: ⚠️ **不实施** — 记录决策依据，避免未来重复调研  
**关联文档**:
- `request-detail-cross-replica-visibility-20260828.md`（方案 B 推荐理由）
- `request-detail-performance-audit-20260828.md`
- `KNOWN_LIMITATIONS_MULTI_INSTANCE.md`（多实例架构限制）
- `deploy/nginx/active-20260821/README.md`（252 备份上游配置）

## 1. 摘要（TL;DR）

方案 B (k8s Ingress `sessionAffinity` / envoy `use_hash_policy` by `X-Request-ID`) 在当前生产与预生产架构下 **不可实施**，原因是 **所有环境都是单实例部署**。sticky 路由的前提是"多副本"，单副本下没有"跨节点查询"问题。

| 决策项 | 结论 |
|---|---|
| 实施 sticky 路由 | ❌ **不实施** |
| 跨副本可见性短期补救 | ✅ **方案 D 已上线**（commit `2a29eebf`，2026-08-29 245 verified） |
| 跨副本可见性长期方案 | ⏸️ **暂不推进方案 C（Redis Store）** — 没有真实的多副本业务需求 |
| 文档同步 | ✅ 把本决策写入跨副本 recommendation §3 "中期"，标记为"已关闭（不适用）" |

## 2. 当前架构事实（2026-08-29 调研）

### 2.1 llmgo.kxpms.cn 链路

```
客户端
    ↓ HTTPS
DNS → 8.136.114.245 (245 公网 IP)
    ↓ 443
245 nginx (server_name llmgo.kxpms.cn)
    upstream llmgo_local_245 { server 127.0.0.1:8781; }
    ↓
245 本地 :8781 gateway (systemd llmgo-245.service, 单实例)
```

来源：`deploy/llmgo-245.nginx.conf:12-15`、245 上的 `/etc/nginx/conf.d/llmgo.kxpms.cn.conf`

### 2.2 llm.kxpms.cn 链路（生产）

```
客户端
    ↓ HTTPS
DNS → 115.29.212.252 (252 公网 IP)
    ↓ 443
252 nginx (server_name ~^(?<kxpms_host>(?:[^.]+\.)?kxpms\.cn)$ 通配)
    upstream kxpms_llm_backend {
        server <env:HOST_154_INTERNAL>:8781 max_fails=2 fail_timeout=30s; # primary
        server <env:HOST_245_INTERNAL>:8781 max_fails=2 fail_timeout=30s; # backup (failover)
    }
    ↓ proxy_next_upstream (默认: error/timeout/502/503/504)
154 gateway (生产，systemd llm-gateway-go.service)
245 gateway (备，仅在 154 连续失败 2 次后激活；active-passive 模式)
```

来源：`deploy/nginx/active-20260821/README.md:8-15`、252 上的 `kxpms-on-252.conf`

**关键事实**：
- 252 的 upstream 是 **active-passive**（不是 active-active）；同一时间只有一个 backend 接收流量
- 154 是 single systemd service（不部署多副本）
- 245 是 single systemd service（预生产 + 154 备份）
- 没有 envoy / k8s Ingress / 阿里云 SLB 多副本负载均衡器

### 2.3 k3s 集群（pms-test）

```yaml
# deploy/k8s/llm-gateway-go-deployment.yaml
spec:
  replicas: 1   # 单 pod
```

**关键事实**：`replicas: 1` 显式单实例，即便后续提到 3+，该集群只有内网测试流量，没有跨副本可见性的真实业务需求。

### 2.4 业务层 sticky（**与本方案无关**）

仓库内存在 `domains/routing/sticky.go`、`bg/sticky_cleaner.go`、`settings/sticky_ttl.go`，但这些是 **凭据级 sticky 路由**（同一凭据的连续请求打到同一实例），**不是 LB 层的 sticky 路由**（同一 request_id 的所有读写打到同一实例）。两者目的完全不同，不能误用本方案治理跨副本可见性。

## 3. 方案 B 适用性评估

### 3.1 方案 B 要求的前置条件

| 前置条件 | 当前架构是否满足 |
|---|---|
| LB 至少有 2 个 backend | ❌ 所有链路 single backend |
| LB 支持 consistent hashing by header | ⚠️ nginx 商业版 (`nginx-plus`) 支持 `hash $request_id consistent;`，开源版需要 `ngx_http_upstream_hash_module` 第三方模块 — 但**没有多 backend 时无需此能力** |
| 同一 request_id 的所有读写流量**都会**经过 LB | ✅（已确认 nginx 透传 `X-Request-ID` header） |
| 客户端能稳定产生同一 request_id | ✅（业务已使用 `X-Request-ID`） |
| 多副本 LB 才有意义 | ❌ **当前不满足** |

### 3.2 结论

方案 B 在当前架构下 **没有实施场景**：

1. **没有"跨副本"问题** — 所有 backend 都是单实例
2. **没有运维窗口** — 当前生产 154 + 245 备用，251 备份链路没有任何 sticky 配置需求
3. **改造成本 > 收益** — 假设为未来多副本改造，sticky 路由配置需要在 LB 上预留；但当前所有 LB 都用 round-robin（无意义）+ failover（active-passive），加 sticky 反而引入新风险（节点摘除时大量 request_id 重新打散）
4. **业务阻力** — sticky 路由依赖 `X-Request-ID` header 在所有客户端稳定出现；如果某些客户端用自定义 ID 格式（如 UUID v4 不带业务语义），hash 分布会不均匀

### 3.3 何时重新评估

以下条件**任一**满足时，重新开启方案 B 调研：

- 生产 154 部署从单实例改为多副本（**主动决策**，非被动扩缩容）
- 引入新的多实例业务环境（如 k3s `pms-test` 改 `replicas: 3+` 用于压测）
- 跨副本可见性问题在方案 D + 单副本架构下**仍然**频繁出现（说明有未知的多副本链路）

## 4. 跨副本可见性的当前有效路径

| 时间窗 | 方案 | 状态 | 备注 |
|---|---|---|---|
| 短期（≤ 1 周） | 方案 D：read-your-writes retry | ✅ **已上线**（`2a29eebf` @ 245） | 100ms × 1 次 retry；`requestdetail_locator_db_retry_total{outcome}` 计数器监控 |
| 中期（≤ 1 月） | 方案 B：sticky 路由 | ❌ **不实施**（本文档决策） | 当前架构无多副本 |
| 长期（≥ 1 季度） | 方案 C：Redis Store | ⏸️ **冻结**，直到方案 D 在 soak 1 周后效果不达标 | `hit/(hit+miss) < 0.2` 持续 1h 触发评估 |

## 5. 关闭方案 B 后，下一步是什么？

### 5.1 立即（24 小时内）

- ✅ 已完成：方案 D 部署 245、计数器注册、L1-L8 验证
- 待办：业务真实烟测（运维手动触发 clear / oversize body 验证计数器递增）

### 5.2 Soak 1 周（2026-09-05 截止）

按 `request-detail-cross-replica-visibility-20260828.md` §5 监控：

- `rate(requestdetail_locator_db_retry_total{outcome="hit"}[5m])` > 0（retry 生效）
- `rate(requestdetail_locator_db_retry_total{outcome="miss"}[5m])` < 1/min
- `hit / (hit + miss)` > 0.5
- `GET /api/admin/request-detail/{id}` 5xx = 0

**任一指标触发调查阈值** → 重新评估方案 B/C。

### 5.3 文档同步（本交付的一部分）

- ✅ `docs/implementation/request-detail-cross-replica-visibility-20260828.md` §3 "中期" 标记为"已关闭（不适用）"，指向本提案
- ✅ 本提案存放在 `docs/implementation/request-detail-sticky-routing-deferred-20260829.md`

## 6. 风险评估

### 6.1 不实施方案 B 的残留风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 单副本故障 → 整个服务不可用 | 高 | 当前 154 主 + 245 备 active-passive 链路；252 nginx 的 `proxy_next_upstream` 触发 failover（30s 内） |
| 单副本故障期间跨副本查询需求 | n/a | 不存在（单副本） |
| 未来如果扩到多副本，跨副本 404 复发 | 中 | 方案 D 的 100ms retry 缓解窗口期；正式扩到多副本前需重新评估方案 B/C |

### 6.2 不实施的可逆性

完全可逆 — 如果未来重新评估，本提案的决策可以撤回，所有调研记录（架构图、nginx 配置位置、header 名）都已固化在此文档中，可作为重新启动方案 B 实施的依据。

## 7. 引用

- 跨副本 recommendation：`docs/implementation/request-detail-cross-replica-visibility-20260828.md`
- 性能审计：`docs/implementation/request-detail-performance-audit-20260828.md`
- 多实例限制：`docs/archive/process/process/2026-07/KNOWN_LIMITATIONS_MULTI_INSTANCE.md`
- 252 nginx README：`deploy/nginx/active-20260821/README.md`
- 245 nginx 配置：`deploy/llmgo-245.nginx.conf`、`deploy/nginx/active-20260821/llmgo-245.kxpms.cn.conf.20260821-spa-fallback-only`
- k3s deployment：`deploy/k8s/llm-gateway-go-deployment.yaml`
- 方案 D commit：`2a29eebf` `feat(requestdetail): 方案 D read-your-writes retry`

## 8. 决策者签字栏（占位）

| 角色 | 决策 | 备注 |
|---|---|---|
| 架构 | （待评审） | |
| 运维 | （待评审） | |
| 业务 | （待评审） | |

---

**最终交付**：本文档是本轮方案 B 调研的**完整闭环**，包括「为何不做」的依据与「何时重新评估」的触发条件。下次有人提"我们是不是该加 sticky 路由"时，直接引用本文档即可，无需重新做架构调研。
