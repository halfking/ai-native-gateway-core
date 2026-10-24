# 请求面（Request Surface）—— 本仓的子系统设计页

> 适用：任何要改「请求从进来到出去」的代码的人。
> 体例照 magpie 的 `docs/subsystems/*`：**路径 / 阶段 / 状态 / 契约 / 源码链接**。
> 判据纪律：**本页的每条断言都有 file:line，且有门禁对拍现实**
> （`scripts/ursmcheck/magpie_p0_extension_adjudication_test.go`）。
> 最后更新：2026-10-24（magpie v0.1.1367 对标轮）

---

## 一、一句话

**本仓的请求面只有一条链**（HTTP 中间件链），其余扩展机制要么是它的**骨架**
（阶段词表）、要么挂在它的某个 **phase** 上、要么**判死**。

---

## 二、请求路径

### 2.1 HTTP 中间件链（外向内）

`cmd/gateway/main.go:7717-7738`：

```
Recovery → RequestID → Logging → Locale(i18n) → CORS → Prometheus → Tracing
        → Auth → Origin(trusted proxies) → SecurityHeaders → publicSurface → mux
```

两处顺序是踩坑后改的，有注释自陈：

- **Logging 必须在 Auth 之外** —— 否则 401 不落日志，客户端拿 `X-Request-Id` 去查命中 0 次
  （`main.go:7723-7729`）。
- **maintain/mobile 必须组合在链内**（`publicSurface`）—— 否则运维面不带安全响应头
  （`main.go:7710-7716`）。

### 2.2 数据面：v1（生产默认）与 v2（默认关）

| 路径 | 开关 | 说明 |
|---|---|---|
| **v1**（生产默认） | 无 | 4 个入口 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 直派 `relay.{ChatHandler,MessagesHandler,ResponsesHandler}`；前飞（auth/key/model policy）与后飞（telemetry/WAL/audit）**全部 inline**（`cmd/gateway/main_pipeline.go:1-80`） |
| **v2 Hook Pipeline** | `LLM_GATEWAY_USE_V2_PIPELINE`（或 legacy `LLM_GATEWAY_V2_ENABLED`），**默认关** | 同样 4 个入口被 `v2DispatchHandler` 包一层：preflight Pipeline → v1 handler → postflight。**开关启动时读一次，改要重启**（`main_pipeline.go:160-168`、`:1112`） |

⚠ **本轮未做**（计划书 §6-P2-1）：v2 的切换判据与"两套路径并存"期间的行为差异清单。
⇒ 当前**两套路径并存**是**未完成**状态，不是"已决定关掉 v2"。

---

## 三、扩展点：裁决表（单一真值源 = `internal/extpoints`）

| 机制 | 位置 | 裁决 | 生产 | 阶段 |
|---|---|---|---|---|
| HTTP 中间件链 | `middleware/` + `cmd/gateway/main.go:7717-7738` | **主链** | ✅ | — |
| Request Pipeline | `domains/pipeline/` | **骨架**（阶段词表的唯一事实源，16 个 phase） | ❌（v2 默认关） | — |
| `HookRegistry` | `domains/hooks/registry.go:34` | **判死** | ❌（生产零调用方） | — |
| Hook 阶段词表（5 个旧名） | `domains/hooks/types.go:13-24` | **别名视图** | ❌ | 映射到骨架的同名 phase |
| 执行器生命周期钩子 | `domains/streaming/executors/executor_chat.go:60-67` | **挂在 phase** | ✅ | `upstream` |
| 响应拦截链 | `domains/hooks/response/types.go:179` | **挂在 phase** | ✅ | `response` |

### 3.1 骨架的 16 个 phase（`domains/pipeline/pipeline.go:25-42`）

```
pre_authentication · authentication · post_authentication
pre_routing · routing · post_routing · governance
pre_transform · transform · post_transform
pre_upstream · upstream · post_upstream
pre_response · response · post_response
```

★ **只有这一份阶段词表是真的**。`domains/hooks` 的 5 个旧阶段名
（`pre_routing`/`routing`/`pre_upstream`/`post_upstream`/`post_response`）
**逐字**是它的子集 ⇒ 旧词表降级为别名视图（`internal/extpoints.HookPhaseAliases`）。

### 3.2 "判死"的准确含义

`HookRegistry` 判死 **≠** 删代码：

- **保留** `domains/hooks/registry.go`（读历史的人还需要它）；
- **禁止新增生产调用方** —— 门禁会扫全仓，发现调用方就红；
- 若将来要复活它，**必须先改 `internal/extpoints` 的裁决** ⇒ 门会红给你看。

---

## 四、失败归责（本轮新增的契约）

**词表**：magpie 的 20 类 rest 词表（`internal/gateway/routing.go:270-325`）
→ 本仓 `ErrorKind` 的对照表，写在 `errorsx/restclasses.go`（**判定在代码里，不在注释里**）。

**判定的三个层次**：

1. **静态**（`errorsx.PenaltyOf`）：按 kind ⇒ 罚谁（none / binding / credential / provider）。
2. **有状态**（`domains/credential/slowrest.go`）：首字节超时的「反复才罚、罚在模型粒度、退避递增」。
3. **写库落点**（`domains/credential/writer.go`）：罚**绑定**还是罚**凭据** ⇒
   决定同一把 key 上的**其它模型**是否受牵连。

★ **新增 kind 及其理由**（每个都有真实生产者）：

| kind | 为什么要有它 | 生产者 |
|---|---|---|
| `KindFirstByteTimeout` | 首字节超时与流中超时同形 ⇒「第一次放过」无法表达 | `domains/streaming/{responses_stream,anthropic_stream,stream}.go` |
| `KindReplyLoop` | 回复循环与空回复同形 ⇒「不罚账号」无法表达 | `domains/streaming/stream.go:integrityBreachOutcome` |
| `KindAccountRestricted` | 账号被限制与鉴权失败同形 ⇒ 会被**永久**摘除 | ⚠ **尚无分类器**（已知缺口，见审计档 §五） |

---

## 五、插件面（本轮新增的契约）

**能力表**（`plugin-runtime/capability.go`）= 插件侧 HTTP 路由 → 所需能力。

| 能力 | 路由 | 实现 |
|---|---|---|
| `session.read` | `GET /_gateway/plugin/v1/sessions` | ✅ |
| `analytics.read` | `GET /_gateway/plugin/v1/analytics` | ✅ |
| `body.fetch` | `GET /_gateway/plugin/v1/bodies` | ❌ **未实现** |
| `policy.serve` | `GET /_gateway/plugin/v1/interception-policies` | ❌ **未实现** |
| `events.ingest` | `POST /_gateway/plugin/v1/events` | ❌ **未实现** |

★ 未实现的 ⇒ **manifest 声明即拒**（该能力永远不被授予 ⇒ 403 `capability_denied`）。

**沙箱**：`DefaultSandboxPolicy().Available = false`，且 `NewSandboxEnforcer`
**在生产代码里零调用方** ⇒ 数据面写能力（`request.mutate` / `tool.execute` 等 7 个）
当前**没有任何路径**能让它通过。谁负责置 `true` ⇒ 见
`plugin-runtime/sandbox_policy.go` 的注释（三条纪律）。

---

## 五之二、请求级观察面（2026-10-24 新增）

**契约**：`internal/requestobserver`（stdlib-only）
—— `Begin` / `Watch` / `End` 三类事件，**有界队列**（1024）、
**满则丢弃并计数**、**发布永不阻塞**、**字段级 allow-list**（观察者拿不到请求体/凭据明文）。

**接线（v1 真实路径，不是 v2）**：

| 事件 | 接线点 | 覆盖的出口 |
|---|---|---|
| `Begin` | `domains/streaming/request_context.go:beginRequestJourney` | — |
| `End` | `domains/streaming/request_context.go:finishRequestJourney` | 成功 / 失败 / 客户端取消 / panic（**四个出口**） |
| `Watch` | `domains/dispatch/pipeline.go` 的 observation emitter | 每次上游尝试（成功/失败） |

★ 观察者**永不影响请求结果**：panic 被逐个兜住、发布不阻塞、关闭即零开销。

---

## 六、门禁（改本页之前先看它们）

| 门 | 守什么 |
|---|---|
| `scripts/ursmcheck/magpie_p0_extension_adjudication_test.go` | 裁决表 vs 现实（扫源码）；**请求面只有一条链** |
| `scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go` | 能力表 vs 真实路由注册；声明即拒；沙箱不可被业务代码置可用 |
| `errorsx/restclasses_test.go` | 20 类词表的判定 vs `PenaltyOf`；内容类不得罚账号 |
| `domains/credential/slowrest_test.go` | 首字节超时的四条契约（放过/反复/模型粒度/退避） |
| `internal/requestobserver/requestobserver_test.go` + `domains/streaming/request_observer_bridge_test.go` | 观察者契约（不阻塞/丢弃计数/panic 兜住/关闭零开销）+ **v1 接线真的通** |
| `internal/routeorder/routeorder_test.go` + `domains/streaming/executors/routeorder_adapter_test.go` | 策略只排序、失败开放、**迁移前后顺序一致** |

---

## 七、已知缺口（本页不粉饰）

1. **v2 pipeline 仍默认关**，且**没有切换判据**（§6-P2-1 未做）⇒ 两套路径并存是**未完成**状态。
2. **`HookRegistry` 判死，但 `domains/hooks` 下仍有实现在跑的 hook**（例如 `audit` hook 走别的路径）
   ⇒ 「判死」只针对**注册表**这一层，不针对 `domains/hooks` 下的具体 hook。
3. **`KindAccountRestricted` 没有分类器** ⇒ 该 kind 当前不会被产生（归责判定已就位，识别未就位）。
4. **magpie 的 `proxy`（代理没连上）语义无法表达**（本仓出站代理不产生可区分信号）⇒ 本仓的网络类失败罚绑定。
5. **扩展点台账（P2-2）未落地** ⇒ 目前只有 Prometheus 计数，没有按扩展点统一的
   「调用次数 / p99 / 失败数 / 超时数 / 最近错误」。
