# 请求面（Request Surface）—— 本仓的子系统设计页

> 适用：任何要改「请求从进来到出去」的代码的人。
> 体例照 magpie 的 `docs/subsystems/*`：**路径 / 阶段 / 状态 / 契约 / 源码链接**。
> 判据纪律：**本页的每条断言都有 file:line，且有门禁对拍现实**
> （★ `scripts/ursmcheck/request_surface_doc_currency_test.go` ——
> ★ **专为本页而写**：逐条核对本页引用的 `file:line` 仍指向它声称的那段代码）。
> 最后更新：2026-10-24（magpie v0.1.1367 对标轮；★ 同日补上本页的漂移门）

---

## 一、一句话

**本仓的请求面只有一条链**（HTTP 中间件链），其余扩展机制要么是它的**骨架**
（阶段词表）、要么挂在它的某个 **phase** 上、要么**判死**。

---

## 二、请求路径

### 2.1 HTTP 中间件链（外向内）

`cmd/gateway/main.go:7758-7776`：

```
Recovery → RequestID → Logging → Locale(i18n) → CORS → Prometheus → Tracing
        → Auth → Origin(trusted proxies) → SecurityHeaders → publicSurface → mux
```

两处顺序是踩坑后改的，有注释自陈：

- **Logging 必须在 Auth 之外** —— 否则 401 不落日志，客户端拿 `X-Request-Id` 去查命中 0 次
  （`cmd/gateway/main.go:7760-7767`）。
- **maintain/mobile 必须组合在链内**（`publicSurface`）—— 否则运维面不带安全响应头
  （`cmd/gateway/main.go:7749-7754`）。

### 2.2 数据面：v1（生产默认）与 v2（默认关）

| 路径 | 开关 | 说明 |
|---|---|---|
| **v1**（生产默认） | 无 | 4 个入口 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 直派 `relay.{ChatHandler,MessagesHandler,ResponsesHandler}`；前飞（auth/key/model policy）与后飞（telemetry/WAL/audit）**全部 inline**（`cmd/gateway/main_pipeline.go:1-80`） |
| **v2 Hook Pipeline** | `LLM_GATEWAY_USE_V2_PIPELINE`（或 legacy `LLM_GATEWAY_V2_ENABLED`），**默认关** | 同样 4 个入口被 `v2DispatchHandler` 包一层：preflight Pipeline → v1 handler → postflight。**开关启动时读一次，改要重启**（`cmd/gateway/main_pipeline.go:160-169`、`:1112`） |

★ **P2-1 已落地**（计划书 §6-P2-1）：v2 的切换判据（8 条）与「两套路径并存」期间的
行为差异清单（17 条）都在 `internal/pipelinev2`，由 `scripts/ursmcheck/magpie_p2_v2_criteria_test.go` 守护。
⇒ 当前**两套路径并存**仍是**未完成**状态，不是"已决定关掉 v2"。
⇒ ★ 差异 D09（v2 的 request-id 合成/写回）曾被登记为「无测试覆盖」——
★ **2026-10-24 订正**：该说明**失实**（称"`v2pipe` 字面量零命中"，实则
`cmd/gateway/main_pipeline.go:749-758` **有**该合成逻辑）⇒ 现已**补上对拍测试**，
见 `cmd/gateway/main_pipeline_requestid_test.go`。

---

## 三、扩展点：裁决表（单一真值源 = `internal/extpoints`）

| 机制 | 位置 | 裁决 | 生产 | 阶段 |
|---|---|---|---|---|
| HTTP 中间件链 | `middleware/` + `cmd/gateway/main.go:7758-7776` | **主链** | ✅ | — |
| Request Pipeline | `domains/pipeline/` | **骨架**（阶段词表的唯一事实源，16 个 phase） | ❌（v2 默认关） | — |
| `HookRegistry` | `domains/hooks/registry.go:36` | **判死** | ❌（生产零调用方） | — |
| Hook 阶段词表（5 个旧名） | `domains/hooks/types.go:13-23` | **别名视图** | ❌ | 映射到骨架的同名 phase |
| ★ 执行器生命周期钩子 | `domains/streaming/executors/executor_chat.go:60-67` | ★ **判死**（2026-10-24 订正：类型在、读取方在，但**零生产安装方** ⇒ 钩子从不触发） | ❌ | — |
| 响应拦截链 | `domains/hooks/response/types.go:179` | **挂在 phase** | ✅ | `response` |

> ★ **2026-10-24 订正**（审计档 §3.14）：本页原写「执行器生命周期钩子 = 挂在 phase（upstream）、✅ 在生产」。
> ⇒ **不成立**：`WithExecuteHooks(` 在**生产代码里零调用**（只有测试在调）
> ⇒ ★★ `git log -S` 追出：SP-03（`902e95237`）**只引入了定义**，
> 注释指向的 **SP-02 安装方从未存在** ⇒ **不是「被删了」，是「从未接线」**。
> ⇒ ⇒ 判据口径订正：**「类型在」≠「被安装」** ⇒ 凡判「在不在生产」，
> 判据必须是**调用点**。
>
> ★ 与 `HookRegistry` **同一种形态**（框架在、链路不通），但**更隐蔽**：
> 它有完整的 API，还有 on/off 对照用例（那两条只证明「API 装上能用」）。

### 3.1 骨架的 16 个 phase（`domains/pipeline/pipeline.go:25-40`）

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

**词表**：magpie 的 20 类 rest 词表（★ **magpie 侧**的文件：`internal/gateway/routing.go:270-325`；
★ **本仓没有这个路径**）
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

## 五之三、扩展点台账（2026-10-24 新增）

**契约**：`internal/extpointledger`（stdlib-only）—— 每个扩展机制一行：
**调用次数 / 失败数 / 超时数 / 丢弃数 / 拒绝数 / 最近窗口 p99 / 最近错误**。
**进程内、内存有界**（每机制 256 个延迟样本，环形缓冲覆盖最旧）⇒
★ 口径：**观测面，不是账本**（进程重启即清零）。

**读端**：`GET /api/admin/extension-points`（`superAdmin`）⇒
一行一机制 = **静态裁决表** × **运行期计数**的合并视图，
★ 两半是**独立来源** ⇒ 可互相印证（裁决表说不在生产 ⇒ 计数必须为 0）。

**记账点**（在产的 2 个机制都接了）：

| 机制 | 记账点 | 粒度 | p99 量的是什么 |
|---|---|---|---|
| `http_middleware_chain` | `middleware/ledger_mw.go`（外层 + 内层两个探针） | 每次穿过主链的请求 | ★ **主链自身的开销**（总耗时 − 业务耗时） |
| `response_intercept` | `domains/hooks/response/chain.go` | **单个拦截器**的一次调用 | 该拦截器的耗时 |

★ 结局分**五种**：`ok` / `failed`（5xx 真出错）/ `rejected`（4xx 闸门按设计拒绝）/
`timeout` / `dropped` ⇒ ★ **拒绝与出错分开**，否则「失败数」会变成「拒绝率」。

★ `unwired` 标志（= `Production && !Recorded`）用来区分
「**这个扩展点还没接台账**」与「**它没有流量**」⇒ ★ 进程重启即清零，
所以它**不蕴含**「真的没流量」。

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
| `scripts/ursmcheck/magpie_p0_extension_onoff_test.go` | 每个扩展点的 on/off 对照（★ on = 「API 装上能用」，**不是**「生产已装」） |
| `internal/pipelinev2/pipelinev2.go` + `scripts/ursmcheck/magpie_p2_v2_criteria_test.go` | v2 切换判据（依据须逐字在生产文件里）+ 行为差异（测试须名实相符 + **代码里真的断言了**） |
| ★★ `scripts/ursmcheck/request_surface_doc_currency_test.go` | ★★ **本页自己的守护者**（★ 2026-10-24 §3.19.1 新增）：逐条核对本页引用的 `file:line` 仍指向它声称的那段代码；★ 并与审计档 §3.18.3 认证表**对账**（同一语义对象的区间必须**精确相等**） |
| `internal/extpointledger/*_test.go` + `middleware/ledger_mw_test.go` | 台账契约（计数/最近窗口 p99/拒绝与出错分开/内存有界）+ **主链口径**（p99 量的是链开销） |
| `scripts/ursmcheck/magpie_p2_extension_ledger_test.go` | 台账**两份来源**对拍 + **接线真的通** + ★ **在产机制必须接台账**（源码级） |
| `admin/extension_points_test.go` | P2-2 **薄壳**：路由真注册 + 被鉴权包着（无凭据 ⇒ 401）+ 响应是合法 JSON |
| ★ `scripts/ursmcheck/ci_carrier_truth_test.go` | ★★ **「门在 CI 上跑」这件事本身**：从远端宿主推导**在跑的载体**，要求它**真的调门禁**（★ 2026-10-24 §3.17 新增） |
| ★ `scripts/ursmcheck/shell_cjk_var_crosscheck_test.go` | ★ 两道「$变量紧跟非 ASCII」门（Go 门 + shell 门）必须**逐条同判**；shell 门另带**扫描器自检**（★ 2026-10-24 §3.17.8 新增） |
| ★ `scripts/ursmcheck/cjk_var_expansion_test.go` | ★ 「$变量紧跟非 ASCII」的**真值源门**（登记册 = 棘轮，★ 2026-10-22 起存量清零 ⇒ **零容忍**形态）；★ 2026-10-24 §3.17.9：**自检夹具改用单引号赋值** ⇒ 两门结论一致，★ **哨兵排除机制已删除** |

---

## 七、已知缺口（本页不粉饰）

1. **v2 pipeline 仍默认关** ⇒ 两套路径并存是**未完成**状态。
   ★ **切换判据与行为差异清单已做**（`internal/pipelinev2` + 门，2026-10-24）
   ⇒ 8 条切换判据 + 17 条行为差异（每条挂**真实存在且名实相符**的测试）
   ⇒ ★ 其中 D09（v2 的 request-id 合成/写回）曾被登记为「**全仓无测试**」——
   ⇒ ★ **2026-10-24 已补齐**（见 `cmd/gateway/main_pipeline_requestid_test.go`）；
   ⇒ ★★ 补的不只是「有测试」，还有「门核对的是**代码里真的断言了**，不只是『测试存在』」。
2. **`HookRegistry` 判死，但 `domains/hooks` 下仍有实现在跑的 hook**（例如 `audit` hook 走别的路径）
   ⇒ 「判死」只针对**注册表**这一层，不针对 `domains/hooks` 下的具体 hook。
3. **`KindAccountRestricted` 没有分类器** ⇒ 该 kind 当前不会被产生（归责判定已就位，识别未就位）。
4. **magpie 的 `proxy`（代理没连上）语义无法表达**（本仓出站代理不产生可区分信号）⇒ 本仓的网络类失败罚绑定。
5. ★ **扩展点台账（P2-2）已落地**（`internal/extpointledger` + admin 读端
   `/api/admin/extension-points`）⇒ 在产的 2 个机制**都接了记账**。
   ⇒ ★ 剩余边界：① **进程内、重启清零**（观测面不是账本）；
   ② `unwired` **不蕴含**「真的没流量」；③ 4xx 一律记 `rejected`，
   **不区分**「闸门拒绝」与「客户端自己错」。
