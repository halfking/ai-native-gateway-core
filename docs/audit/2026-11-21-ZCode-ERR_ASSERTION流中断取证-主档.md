# ZCode `ERR_ASSERTION: assert(statusCode >= 100)` 流中断取证主档

> **2026-11-21 goal 轮**。owner 报障：ZCode 访问网关报 `Cannot connect to API: The expression evaluated to a falsy value: assert(statusCode >= 100)`，要求「检查网关的安全处理模块是否过于敏感，根据 211 的 log 进行分析和修正」。
>
> **结论先行**：**安全处理模块没有参与，更谈不上过于敏感**（`security.enabled=false`，当日 2,350 个 turn 零 policy/security/governance/sanitize/block 事件，该会话 censor 记录 0 条）。网关侧交付了**完整且帧序合法**的响应。根因在**客户端运行时**（Node fetch / undici HTTP 解析器内部断言，一次性边缘事件，当日 1,193 笔 ZCode 请求中唯一一例）。**网关侧零产品缺陷 ⇒ 零产品代码改动**，本轮交付＝取证链 + 排除证明 + 登记与建议。

## 一、报障与取证起点

- ZCode 报错（session `sess_4bf43de3`，工作区 产品宣传册，2026-11-21 13:43:49 +08 / 05:43:49Z，traceId `9bd81ea6-40c9-49fa-9ebb-1c640dd921ad`）：
  - `AiSdkModelAdapterError`，`errorPhase="stream"`，`providerCode="ERR_ASSERTION"`，`providerRequestId="toolu_3ZyoXV6w9UYHbrJWvAqL3V"`（流中已出现 tool_use 块 ⇒ 客户端确实收到过流式增量）。
  - `cause = AI_APICallError`，`cause.cause.code = ERR_ASSERTION`（`generatedMessage: true, operator "==", actual false`），URL = `https://llm.itestu.cn/v1/chat/completions`，UA `ZCode/3.18.2 ai-sdk/provider-utils/4.0.39 runtime/node.js/24`。
- 断言源头＝**undici H1 解析器** `client-h1.js onMessageComplete()`：`assert(statusCode >= 100)`。app 捆绑 undici 6.28.0 与 Node 24 内置 undici 同型；provider-utils 的错误包装链（`$Xs=["ConnectionRefused","ConnectionClosed","FailedToOpenSocket","ECONNRESET","ECONNREFUSED","ETIMEDOUT","EPIPE"]` / TypeError `fetch failed` cause 透传）把 `TypeError: fetch failed`（cause=AssertionError）渲染成「Cannot connect to API: <断言消息>」。

## 二、211 侧全链证据（每一环都实测）

1. **网关身份**：现役 `2.6.1-23b9d32b-…`（build_seq 3190，容器 a）；失败时刻现役＝容器 b（`2.6.1-5b4d28ca-20261121-211`，13:47:25 被蓝绿切换杀掉，失败发生在切换**之前** 3.5 分钟 ⇒ 与部署无关）。
2. **锁定请求**：容器 b 日志中 `bd9adb63`（`client_request_id=9ca2dd7b-11b0-4403-b11d-e788270c0456`，与报错完全一致）：
   - 13:43:35.69 入站，`request_bytes=2,034,113`（2MB body）；
   - 13:43:40.15 v2 preflight `provider_discovery` WARN（no healthy provider，**已知过渡态**：v2 阶段挂 demo 凭据仓，对真实模型恒失败后 fall-through v1，`cmd/gateway/main_pipeline.go` 注释明示，非缺陷）→ v1 `candidates_resolved` 3 个 / `routing_resolve` provider 587 / credential 31；
   - 13:43:40.26 `pre_stream_keepalive_started_early`（200 + SSE 注释先行，body 134,073B）；
   - 13:43:40.36 upstream `https://apiclaude.cc/v1/messages`（131,098B，stream）；
   - 13:43:43.48 upstream 200（TTFB 3,246ms）+ `attempt_commit_gate` 首语义帧提交（buffer 214B）；
   - **13:43:49.47 `audit: request completed success=true stream_chunks=94 ttfb=3246ms`**；
   - 13:43:49.48 `http_request status=200 response_bytes=21,735 duration=13,789ms`。
3. **turn 落库**：`session_turns_hot` request `4d3f9ae8…`（session `gw_4bf43de3` turn 121）status=200 success=true completion=425/prompt=5222。
4. **nginx 侧**：`access.log` 唯一一条 ZCode 499 —— `13:43:49 POST /v1/chat/completions 499 16237`（客户端在响应完成前断开；nginx 已向客户端发 16,237 / 上游共 21,735 字节）。当日 ZCode 流量 1,192×200 / 9×503 / 3×502 / **1×499（本案）** ⇒ 一次性事件。
5. **客户端侧对照**：ZCode ttft 事件序列显示该请求 13.8s 内**无 first_content/first_text**，失败发生在网关写完最后一帧之后 12ms ⇒ 客户端在收到流尾帧时在自己的解析器里崩掉并 abort（nginx 499 的「客户端断开」由此而来）。

## 三、排除证明（安全模块与网关帧序）

1. **安全开关**：`settings_kv` `security.enabled=false`（11-18 根修后总闸关闭，`Execute` 早退跳过全部检测含 WARN）。
2. **安全面当日活动**：2,350 个 turn 中 `error_kind` 含 policy/security/governance/sanitize/block 的**全部为 0**；`session_censors` 当日 2,907 条（account/secret/email），**属于该会话的为 0**。
3. **armor observe-only**：请求路径上的 armor 评分不拦截；该请求无 armor 事件。
4. **帧序实测三连**（临时 key id=365，验后已吊销回读 404）：
   - 直连 8781：`HTTP/1.1 200 + Transfer-Encoding: chunked + [DONE] + 0\r\n\r\n + Connection: keep-alive`，干净；
   - 过 nginx 443（H1.1 强制）：chunked + `[DONE]` 收尾干净；
   - **真实 claude-sonnet-5-5 模型走公网全链**（TLS→nginx→网关→apiclaude.cc）：`finish_reason` 帧 + `[DONE]` + `0\r\n\r\n` 全部规范，连接 keep-alive 完好。
5. **llhttp 行为实测**（驱动 undici 捆绑 wasm）：严格 RESPONSE 模式下 MESSAGE_COMPLETE 恒在 HEADERS_COMPLETE 之后；undici 7 的三条 `parser.onMessageComplete()` 直调旁路（socket end/error/close）全部有 `statusCode` 真值守卫 ⇒ 断言触发需要客户端内部竞态（与 undici issue #5360 同类），网关侧无可复现的字节序列（6 种网关形态 + AI SDK cancel-复用流全部无法触发）。

## 四、根因判定

**客户端 Node/undici 流式解析内部断言（一次性竞态边缘）**。网关交付的流完整、帧序合法；安全模块全程未参与。ZCode 把 `TypeError(fetch failed)` 的 cause（断言消息）渲染为「Cannot connect to API」并把 `retryable=false` 归为 unknown —— 是客户端错误分类的表层误导，不是网关行为。

## 五、修正与登记

- **网关产品代码：零改动**（没有可修的产品缺陷；为「修」而改帧序只会破坏已验证正确的输出）。
- **G1（客户端，非本仓）**：建议 ZCode 侧升级 node24/undici 或捕获该断言族并归类为 transient stream error（可重试）。owner 决策。
- **G2（登记）**：nginx `access.log` 的 499 + 网关 `audit: request completed success=true` 的组合是「客户端流尾断开」的指纹；网关已在 turn 行如实落库（success=true / 425 tokens 实际已投递 16,237/21,735 字节），计费与审计口径无需调整。
- **G3（登记，运维卫生）**：`api_keys` id=24（alias 空、无过期、enabled，名 `e2e-sec-probe-20261121`，其它会话今日所建探针 key）与新 key 创建的**空 alias 默认值冲突**（`POST /api/keys` 不带 `key_alias` 会撞它，`admin/keys.go:453` `findActiveKeyConflict`）⇒ 建议探针 key 一律显式 `key_alias` + TTL；id=24 归属其它会话，本轮不代删。
- **G4（登记，量具坑）**：`session_turns`（父表）不含 `session_turns_hot` 未聚合行（max t0 停在热聚合水位）⇒ 查近期 turn 必须用 `session_turns_with_current_month` 视图或 `session_turns_hot`；本轮取证首查即踩（「写入面 05:47 断裂」是误读，实为分区/热表布局）。
- **本轮临时资产清理**：key id=365 已吊销（回读 404 双确认）；`/tmp/sse-*.trace`、`/tmp/claude-stream.*` 留存于 211 供复核；本地 `/tmp/undici-repro/`（复现矩阵脚本）留存。

## 六、复现/复核配方

```bash
# ① 网关直连帧序
ssh 211 'curl -s --trace-ascii - -X POST http://127.0.0.1:8781/v1/chat/completions -H "Authorization: Bearer <key>" -H "Content-Type: application/json" -d "{\"model\":\"auto\",\"stream\":true,\"max_tokens\":50,\"messages\":[{\"role\":\"user\",\"content\":\"say PONG\"}]}"'
# ② 公网全链 H1.1 帧序（真实 claude 路径）
curl -sk --http1.1 --resolve llm.itestu.cn:443:192.168.31.211 --trace-ascii - -X POST https://llm.itestu.cn/v1/chat/completions ...
# ③ 容器 b（退出容器）日志仍可查
ssh 211 'docker logs llm-gateway-211-b --since 2026-11-21T05:43:38Z --until 2026-11-21T05:44:30Z | grep bd9adb63'
```
