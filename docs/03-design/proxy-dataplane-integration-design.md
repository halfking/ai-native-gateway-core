# 代理订阅子系统数据面接入设计（R35-gap §三#1）

- 状态：**设计稿（待产品决策，未实施）** —— R35-gap #1 / R36 遗留#1 的架构级闭环件
- 关联：`docs/audit/2026-09-17-r35-gap-audit-round.md` §三#1/#7、`docs/03-design/proxy-management-design.md`（子系统本体设计，本文不重复）
- 日期：2026-09-17（R36 审计轮撰写）

## 一、问题：三断裂

代理订阅子系统（proxy/）目前是一套**只有管理面与探测面的完整系统**，真实转发流量与它零交集：

| 断裂 | 事实（2026-09-17 复核） |
|---|---|
| 配置无读者 | `RequiresProxy`（proxy/types.go:76）、`egress_profile` / `default_egress_profile`（providers/providers 表，默认 'direct'）、免费池 `proxy_subscription_id` 全部只有写端与 admin 展示端，**零数据面读取方** |
| Transport 无数据面消费者 | `Manager.GetProxyTransportForNode` 唯一生产调用方是免费池探测（admin/free_pool_extra.go:938）；`InvalidateTransport` 只被 admin/proxy.go（ForceSwap/订阅编辑）调用 |
| 真实出口由 env 决定 | 真实转发出站走 upstream.Client 单例 Transport，其 `Proxy: proxy.ProxyFunc()` 只看 HTTP(S)_PROXY env + 国内直连白名单（upstream/proxy_resolver.go）；"自动节点切换"（ForceSwap）对真实流量无效 |

附带断裂（R35-gap #7，本设计一并闭环）：ProxyResolver 代理拨号失败（`proxyconnect tcp`）静默直连且错误归类 KindNetwork → 计入凭据健康——代理故障惩罚凭据。

## 二、目标 / 非目标

**目标**
1. `egress_profile='proxy'` 的供应商：其凭据的真实出站经订阅选定节点转发；
2. ForceSwap 后新连接走新节点，旧节点连接有序退役；
3. 代理故障与凭据故障归因分离：proxyconnect 失败不扣凭据健康，转为节点健康信号并驱动自动 ForceSwap；
4. 无代理订阅/未启用时零行为变化（默认 'direct' 不变）。

**非目标**
- 不改 env 代理（HTTP(S)_PROXY）语义——两者叠加时订阅代理优先于 env 代理；
- 不做代理协议认证之外的订阅账号体系；不承诺带宽/延迟 SLA。

## 三、接线设计

### 3.1 决策链（谁需要代理）

```
凭据 → provider.egress_profile（'direct'(默认) | 'proxy'）
     → 若 proxy：选定订阅（凭据级 proxy_subscription_id 缺省时取
       default_egress_profile='proxy' 的供应商唯一活跃订阅；多订阅冲突=配置错误，启动告警+回落 direct）
     → Manager.SelectNodeWithStrategy(订阅) → Node
     → GetProxyTransportForNode(subscriptionID, node) → *http.Transport
```

数据面读取方即 R35 登记的三个零读者：`egress_profile`（决策）、`proxy_subscription_id`（选订阅）、`RequiresProxy(baseURL)`（保底守卫，见 3.4）。

### 3.2 Transport 接入点：分流 RoundTripper（而非每凭据一个 Client）

upstream.Client 是全局单例（http.Client + 单 Transport）。逐凭据克隆 http.Client 会复制连接池（MaxIdleConns 128×N 倍内存与 FD）并侵入所有 executor 调用点。因此在**现有 Transport 之前加一层分流 RoundTripper**，单点改动：

```
http.Client.Transport = proxyDispatchRoundTripper{
    base:    现有 http.Transport（env 代理语义不变）,
    mgr:     *proxy.Manager（nil 时直通 base，零开销路径）,
    resolve: func(req) (transport, ok)  // 凭据→订阅→节点→缓存 Transport
}
```

- `resolve` 的凭据识别：出站请求构造处已有 credential 上下文（executor 层），通过 `req.WithContext` 塞入 `credentialID`（context key），RoundTripper 侧 O(1) 查表；查不到=直通 base。
- 命中 proxy 供应商 → 取该订阅缓存的 `*http.Transport`（Manager 已按订阅缓存复用，proxy/manager.go:927/937）→ `transport.RoundTrip(req)`。
- 每订阅一个 Transport = 连接池按订阅分片，池上限可控（订阅数是个位数~两位数）。

### 3.3 归因与熔断（闭环 R35-gap #7）

- errorsx 分类：`proxyconnect` 拨号错误（`strings.HasPrefix(err.Error(), "proxyconnect")` 或 errors.As 到 `net.OpError` 且地址为代理）归类 **KindProxy**（新 Kind）；
- KindProxy：**不调 RecordFailure**（凭据健康不受损），记 `proxy_node_failures_total{subscription,node}`；
- 同节点滑动窗口 N 次（建议 3 次/5min）→ 调 `Manager.ForceSwap`（已有的自动换节点路径），旧节点进 banned 冷却；
- ForceSwap 后 `InvalidateTransport(subscriptionID)`（已有），分流层下一请求自然选新节点。

### 3.4 ForceSwap 连接失效语义（keep-alive 尾巴）

Transport 换代只影响**新建**连接；已建立 keep-alive 连接继续服务在途/复用请求，最长受 `IdleConnTimeout`（upstream 90s）约束。语义取舍：
- **接受尾巴**（推荐，阶段 1）：90s 内新旧节点并存，幂等于"出口 IP 渐变"；
- 强断（`Transport.CloseIdleConnections` 只清空闲，活性连接需 TrackConn 或换 node dialer）——仅在风控场景证明 90s 窗口有害时再做。

### 3.5 保底守卫

- `RequiresProxy(baseURL)`（已存在零读者）在订阅节点探测失败熔断期间作为**回落判定**：熔断窗口内是否允许 env 代理/直连兜底，是产品决策点（见 §五）；
- 启动一致性检查：`egress_profile='proxy'` 但无活跃订阅 → 告警 + 该供应商按 direct 走（与现状一致，不放大故障）。

## 四、分期

| 阶段 | 内容 | 风险 |
|---|---|---|
| 0 观测 | 只做归因：KindProxy 分类 + `proxyconnect` 计数指标（不再计凭据健康）；无路由行为变化 | 极低 |
| 1 免费池 | egress_profile 读取方上线，仅免费池供应商（admin/free_pool_extra 已写 proxy_subscription_id 的那批）接分流层 | 中（免费池流量小，爆炸半径小） |
| 2 全量 | 全部 proxy 供应商接入 + 自动 ForceSwap 闭环 + Grafana 面板 | 需产品确认后实施 |

## 五、开放决策点（产品确认）

1. 代理节点熔断期间回落策略：直连兜底 / 拒绝（503）/ env 代理兜底？
2. 订阅代理与 env 代理叠加时的优先级（本设计取订阅优先）是否成立？
3. HK 等地域规避（订阅 banned_regions，R35 已建默认）是否要求"节点地域≠凭据供应商受限地域"校验前置到选节点？
4. 阶段 1 的免费池先行范围名单。

## 六、验证清单（实施时红绿基线）

- 分流层：直通路径零行为差（现有 upstream 测试全绿）；proxy 路径命中订阅 Transport（新增钉桩）。
- 归因：proxyconnect 失败后凭据健康不变（红：现计 RecordFailure；绿：KindProxy 直通）。
- ForceSwap：InvalidateTransport 后新请求 dial 到新节点（pgxmock/httpmock 或本地 httptest 双监听）。
- 空配置：无订阅/egress_profile='direct' 时 `mgr=nil` 直通，benchmark 确认零额外分配。
