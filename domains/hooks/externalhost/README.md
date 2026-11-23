# W3 外部钩子层（进程外 hook host）

> 方案来源：《方案-llm-gateway-go会话能力整合与插件化优化-2026-10-28》§5宿主选型裁决
> —— **合同先行 + Go RPC(B) 先行，A(Bun) 延后 P3**（第 89 轮 §四.2 正式拍板）。

## 目录结构

| 路径 | 角色 | 状态 |
|---|---|---|
| `contract/contract.go` | 宿主无关的钩子合同：钩子点、载荷、上下文、决策、受保护字段、限额 | ✅ 已落地 |
| `host.go` | Go RPC 宿主调用面：超时/崩溃直通、受保护字段回滚、remote 隐私 | ✅ 已落地 |
| `loader.go` | 进程装载面：复用 plugin-runtime supervisor + handshake + health | ✅ 已落地 |

## 合同要点（对应 W3.1-W3.4）

- **W3.1 钩子点**：`on_request`（协议翻译前）/ `on_response`（上游响应返回后）。
  上下文含 `protocol/model/agent/session/origin/abort/state`。
- **W3.2 回滚语义**：改写采用**补丁**语义（字段缺省=不改写，显式置零=改写）。
  触碰受保护字段 `model/stream/tools` → 整体回滚并记 `protected_field` 失败；
  超时（默认 1s，可配 100ms-10s）/崩溃 → **原样直通**。
- **W3.3 捕获合同**：正文有界副本上限 **8MiB**；`ctx.state` 串联请求-响应；
  onResponse 为 fire-and-forget 语义，不阻塞 EOF。
- **W3.4 origin 分类**：`local/gateway-key/remote` 三类，未知取值一律拒绝（fail-closed）；
  **remote 来源默认不捕获正文**（隐私原则），需显式 `AllowRemoteBodyCapture` 才放开。

## 失败归因（Outcome.Failure）

| 归因 | 触发条件 | 网关行为 |
|---|---|---|
| `timeout` | 调用超时 | 原样直通 |
| `crash` | 连接断开/进程崩溃 | 原样直通 |
| `protocol` | 版本不匹配/字段非法/状态码异常 | 原样直通 + 记失败 |
| `protected_field` | 改写 model/stream/tools | 整体回滚 + 记失败 |
| `hook` | 钩子实现方返回 error | 原样直通 + 记失败 |

## 复用面（Go RPC 宿主的成本压缩来源）

| plugin-runtime 既有件 | 复用点 |
|---|---|
| `supervisor.go` | 进程生命周期 Start/Stop/Restart + socket 分配 |
| `manifest.go` | `Runtime.Protocol=http-unix-socket` 校验、Entrypoint/HandshakePath/HealthPath 校验 |
| `handshake.go` | 装载前就绪握手（api_contract / plugin_id / plugin_version / capabilities 比对） |
| `health_check.go` | socket 存活探活 |
| `apiproxy.go` | HTTP over unix domain socket 传输范式 |

>关键收益：**外部钩子与普通插件共享同一套装载与生命周期语义**，W3 宿主不重复造进程管理。

## 已知边界（下一轮）

1. **未接入请求管线**：合同与宿主可用，但尚未挂到协议翻译前的调用点（W3.2 要求的"管线挂接点=协议翻译前"未落地）。
2. **onRequest 失败后不再收 onResponse**：语义已由 `Outcome.Failure` 表达，但"本请求不再触发 onResponse"的编排逻辑尚未在管线层实现。
3. **fire-and-forget 的 onResponse 调度**：当前为同步调用，调度侧的非阻塞编排未落地。
4. **W3.5 首个真实钩子**：`domains/hooks/memoraauto`（内置版）尚未外化为外部钩子形态。
5. **A(Bun) 宿主**：按裁决延后 P3；合同与宿主实现分离，届时只需补宿主适配层 + 双宿主合同测试。

## 钉测覆盖

```bash
go test -race -count=1 ./domains/hooks/externalhost/...
```

- 合同层：`contract/contract_test.go`（版本钉住/钩子点路径/origin 三态与隐私/超时收敛/8MiB 有界副本/
  调用信封载荷配对/回复校验/受保护字段判定/补丁 merge 语义）。
- 宿主层：`host_test.go`（continue/rewrite 生效/受保护字段回滚/超时直通/崩溃直通/remote 隐私与显式放开/
  block 携带原因/版本不匹配/钩子 error/HTTP 状态异常/空参拒绝）。
- 装载层：`loader_test.go`（协议校验/插件 ID 必填/握手→探活→调用全链/握手失败回滚/探活失败回滚/
  缺 socket 路径拒绝/**环境代理不得劫持 unix 传输** 回归钉测）。
