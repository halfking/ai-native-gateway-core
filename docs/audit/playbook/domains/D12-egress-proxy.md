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
