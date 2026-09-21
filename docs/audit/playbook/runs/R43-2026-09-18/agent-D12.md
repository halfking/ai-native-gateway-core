# D12 海外模型出口代理 子代理报告（窗口：643735a28^..HEAD，48 commits；重点 b75c91900..HEAD，44 commits）

## 一、发现（候选，待主代理复核）
| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3（窗口外存量） | 订阅 `Priority` 字段只落库与展示，节点选择完全不读它：manager 排序仅用 ConsecutiveFailures→SuccessRate→ResponseTimeMs，无 ORDER BY priority，load_balancer 亦无。D12 §3.5 要求"优先级真实参与选择（不只是展示）"。非窗口内改动（R35 45054170c 之后 proxy/ 零改动），疑似基线即如此，需主代理判定是缺口还是"优先级由调用方选 subscriptionID 承担"（admin/free_pool_extra.go:934 由调用方传 subscriptionID） | proxy/manager.go:402-410；proxy/types.go:34；proxy/store_pg.go:113,843 | 亲读确认语义后：要么登记遗留，要么在域文档 §4 记"priority 由调用方承担"销账 |
| 2 | P3（记录性） | 余额刷新平面（⟳/bg probe/floor guard）不经订阅节点代理管理器，仅走 env HTTP_PROXY（DefaultTransport）+ EgressBlocked 预检；若部署仅靠订阅节点（非 env 代理）出海，海外厂商余额探测会直连失败——但失败显式落 `balance_error` 不悬挂，符合 §6 降级语义。非回归，属数据面/余额面分工现状 | internal/providercap/capability.go:182-184（窗口外，09-13 最后改动）；admin/provider_credential_balance.go:86,100；bg/credential_probe_v2.go:1883-1887 | 无需修复；建议 D12 域文档补一句"余额面走 env 代理非订阅节点"免下轮重疑 |

## 二、核实为健康的面
- **重点窗口 b75c91900..HEAD 代理主链路零改动**：`git log b75c91900..HEAD -- proxy/ internal/safehttpclient/ admin/proxy.go` 为空；健康面基准测试 manager_region_test.go / manager_health_race_test.go / manager_shutdown_test.go / transport.go / load_balancer.go / health_checker.go 窗口内零 commit。
- 48h 全窗内 proxy/ 改动（parser.go+9、store_pg.go+42、types.go+3 注释、admin/proxy.go+8）全部来自 45054170c（R35，2026-09-17），经 `git merge-base --is-ancestor` 确认在 b75c91900 **之前**；三项修复（新建订阅默认禁 HK、订阅拉取 client 补 ProxyFromEnvironment、刷新保留节点级 banned_regions）内容正确且已有轮次审计。
- **TargetProvider 接线未触出站 transport**：c6de4699d/7bb1708d5 仅改 IR 序列化方言（executor_chat.go:1744-1871）；出站 client 仍是 executor_chat.go:637-650 的 pool `p.Client()` → pool/pool.go:104-132 transport（proxyFunc 来自 cmd/gateway/main.go:763 `pool.NewPoolManager(upClient.Proxy().ProxyFunc())` → upstream/proxy_resolver.go:245），与既有路径共用同一 transport，国内域名直连白名单不变。
- **FetchBalanceUSD/⟳ 横向不变量**：capability.go 窗口外未动；⟳ 路径预检 EgressBlocked（元数据/CGNAT 拒绝）后才发请求，R42 已补失败 slog+balance_error 落账。
- **HK 排除 HEAD 仍生效**：选择时按"订阅 bans ∪ 节点 bans"过滤（manager.go:373），全被禁时显式 `region_banned` 失败并计数（382-386），不静默不悬挂；新建订阅默认 `["HK"]`（admin/proxy.go:379-385，R35）；节点级 ban 在订阅刷新 DELETE+re-INSERT 中按名称保留（store_pg.go:441-497,530-554）。

## 三、未覆盖项与原因
- 订阅源真实拉取、节点探活/自动切换的实网行为（§3.3/3.4 运行时语义）——需真机出网与真实机场订阅凭据，窗口内零改动故未起环境验证。
- 降级链末端（全部节点不可用→凭据错误账计账，§3.6）只核了显式失败返回与 metrics 计数路径，未实跑 D08 错误账写入联动——需跨域 D08 联核。
- upstream.ProxyResolver 国内直连白名单 env 覆盖项（upstream/proxy_resolver.go:20-39 硬编码默认）未逐条核对配置读取点——非窗口改动面，且属 D14/配置域边界。

**主代理复核结论（R43）**：#1/#2 采纳为域文档回注（§4 记录"priority 由调用方承担、余额面走 env 代理"两条销账注记，免下轮重疑）；健康面采信（代理主链路零改动 + TargetProvider 接线不触 transport + HK 排除在位）。
