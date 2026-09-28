# D12 — 审计代理（海外模型出口代理）

> 领域编号: D12 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：直连海外的模型必须经代理完成；代理订阅管理、节点探测、自动节点切换；**海外模型屏蔽香港 → 代理自动避开香港节点**；节点地域优先级可配置。
**不管**：凭据健康与错误账（D08）；节点状态统一模块（D09，代理节点状态汇入其中）；通用网络可靠性（D14）。

## 2. 参考基线

设计文档：
- `docs/03-design/proxy-management-design.md` — 代理管理系统设计（海外供应商走代理、节点探活、HK 节点示例、Free Pool 集成）——本域唯一专属设计文档
- `docs/03-design/01-architecture/architecture/node-probe-mechanism.md` — 经代理探测被墙供应商
- `docs/proxy-optimization-summary.md`

代码入口：
- `proxy/`（manager.go、health_checker.go、load_balancer.go、manager_region_test.go 等）
- 供应商侧经代理的出站路径：`internal/safehttpclient/`、provider 出站配置

## 3. 检查清单

1. **强制走代理**：标记为"需代理"的海外供应商，其所有出站请求（含窗口内新增供应商/新端点）都经代理管理器构造的 client；存在直连旁路 = P1。
2. **避开香港**：节点选择器对"海外模型供应商"默认排除 HK 地域节点（它们屏蔽 HK）；排除规则可配置（某些供应商不屏蔽时可放开）；有测试钉扎（manager_region_test.go 基准）。
3. **节点探测**：健康检查周期、失败阈值、恢复探测与自动切换衔接；切换时进行中的请求有明确语义（重试到新节点 vs 报错），不悬挂。
4. **订阅管理**：订阅源刷新、节点列表更新不中断服务；订阅失效有告警而非静默空节点。
5. **地域优先级配置**：优先级配置真实参与选择（不只是展示）；同优先级内的负载均衡与 D04 权重语义一致。
6. **降级**：全部代理节点不可用时的兜底（明确失败并计入凭据错误账，而非无响应挂死）。

## 4. 历史回归点（轮末回注区）

- [初版] manager_region_test.go / manager_health_race_test.go / manager_shutdown_test.go 为健康面基准；本域历史无登记缺陷，基线待首个 playbook 轮（R34）建立

## 5. 子代理派发提示词

```text
你是 D12（海外模型出口代理）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D12-egress-proxy.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的供应商出站路径是否遗漏代理强制；HK 排除与地域优先级配置是否仍生效。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R43 回注（2026-09-18，销账注记两条，免下轮重疑）
- **订阅 Priority 字段当前由调用方承担**：manager 节点选择只按 ConsecutiveFailures→SuccessRate→ResponseTimeMs 排序，不读 Priority；订阅粒度的"优先级"由调用方显式传 subscriptionID 承担（admin/free_pool_extra.go）。若要"priority 参与节点排序"须产品决策，不是缺口修复。
- **余额面走 env 代理非订阅节点**：⟳/bg probe/floor guard 的 FetchBalanceUSD 走 DefaultTransport（HTTP_PROXY/HTTPS_PROXY env）+ EgressBlocked 预检，不经订阅节点代理管理器——仅靠订阅出海的部署上，海外厂商余额探测会失败并显式落 balance_error（符合降级语义，不悬挂）。
- TargetProvider 接线（c6de4699d/7bb1708d5）只改 IR 序列化方言，出站 transport 不变（pool p.Client() → proxy_resolver 链）。

### R78 回注 · 出口代理是管理面 + 持久化子系统，14 个 proxy provider 实际直连

**B-01 复核结论：设计契约未接线**（不是缺测试）。

| 环节 | 取证 |
|---|---|
| 输入 | `providers.egress_profile='proxy'`（真库现值 direct 46 / proxy 14） |
| 设计期望 | `docs/03-design/proxy-management-design.md:153-157`：`proxy` → **使用代理** |
| 应有输出 | 派发该 provider 时使用 `proxy.TransportFactory.Get(subID, proxyURL)` 的带代理 `http.Transport` |
| 实际 | **无此代码路径** |

证据链（逐条 grep 取证，非推断）：

- `proxy` 包全仓仅被 `admin/handler.go`、`admin/proxy.go`、`admin/proxy_test.go` import——
  **请求路径（executor / dispatch / provider client）无人引用**。
- `NewTransportFactory` 的调用点只有 `proxy/manager.go:102`（包内）与 `proxy/proxy_test.go:903`。
- `TransportFactory.Get()`（唯一能产出带代理 transport 的方法）**无任何外部调用者**。
- `providers.egress_profile` 的非 admin 引用仅三处：catalog 种子列清单
  （`provider/catalog/seed.go:29`）、catalog schema 结构体字段（`schema.go:21`）、
  DDL（`db/db.go:7539-7552`）。**没有任何派发代码读它。**
- 请求路径的健康探测显式注释 "Health probes must be direct"
  （`domains/health/http_checker.go:38`）。

**附带差异**：设计要求 `VARCHAR(20) DEFAULT 'auto'`，实现为 `TEXT DEFAULT 'direct'`
（`db/db.go:7539-7543`），真库无任何 `'auto'` 取值——设计里的第三种「智能判断」模式整体
未实现。

**SF-01 复核：成立**。保留头 `X-LLM-Gateway-RateLimit-Scope`（`ratelimit/scope.go:11`）
在三条上游路径均被过滤（`protocol_handler.go:33` / `executor_ollama.go:1198` /
`executor_anthropic.go:1285`），API key 仅经 Authorization 发出。

**未修**：接线属路由/网络架构变更，需 owner 先定两件事——① 代理不可用时是回退直连还是
直接报错（前者会静默失去代理保护，后者会中断海外流量）；② 14 个存量 provider 是否按
实际网络可达性重新分类。审计轮不替 owner 做这两个决定。

**教训 I**：**「功能有管理界面 + 有数据库列 + 有完整实现包」三者同时存在，仍不等于功能
生效。** 本域的 proxy 包有 1900+ 行实现、8 个测试文件、admin 端点、DB 索引——只有
「请求路径接线」这一环缺失，而那正是唯一决定它是否工作的一环。**验收要追到「输入 →
消费方 → 输出」的完整链路，中间任何一环缺失都不能算通过**；`grep 包名` 只证明存在，
`grep 调用方` 才证明被使用。
