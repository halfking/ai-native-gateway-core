# 245 prompt_too_large 审计报告（2026-09-17）

- 触发：用户报错 `prompt exceeds gateway budget: estimated 2099505 tokens > 2097152 limit (LLM_GATEWAY_MAX_PROMPT_TOKENS)`，TraceID `bb0483f0-4f86-4b5e-baf8-ad20d6d3c0bd`，request `5f0f3d75-8bc9-4205-b669-20e19390c6c0`，provider `ef7bed64-de6f-42d8-86f2-eab4b62d9812/minimax-m3`，status=413 retryable=false
- 用户关注点：「我们有上下文压缩，按道理不应该有这个错误，除非这个错误是我们发出的」
- 窗口：2026-09-16 全天 + 客户端日志回溯至 2026-09-15

## 一、结论（修正前一轮的误判）

1. **413 由 245 网关的 prompt budget guard 发出，请求确实到达了网关。**
   前一轮（2026-09-17 凌晨会话，sess_57bf74de）「ZCode CLI 客户端侧预检拦截、请求未达网关」的结论**错误**，当时以客户端 requestId/错误文案 grep 网关日志零命中就下了结论。两处方法论错误：
   - 网关在 RequestIDMiddleware 为每个请求**重新生成服务端 request_id**，客户端 `5f0f3d75-…` 永远不会出现在网关日志（对应服务端 id 为 `1ca795598fd144d3d2c51e56643aa954`）；
   - 网关 413 响应体（含完整错误文案与 2099505 估值）只进 PG request_logs，jsonl 侧仅有 `safety_net_defer_fired` 的 `attempt_err_code` 短标记。
2. **网关行为符合设计，非 bug**：prompt budget guard（2026-08-24 为 245 memcg OOM 事故引入）对 ≥2,097,152 估算 tokens 的入口请求在 JSON 解析前返回 413，`retryable=false` 正确（重发超大 prompt 无意义）。本次修正属于**同簇顺带修复的真实缺陷**，不是为 413 行为本身翻案。
3. **根因在客户端**：涉事 ZCode 会话（workspace `~/workspace/smm`，sess_9edef815 及 09-15 的 sess_af936fa6 等）在 88–109 轮后 prompt 体积达 8–8.4 MB（估值 209–210 万 tokens），上下文压缩未在到达网关预算前生效。`provider_code=prompt_too_large` 是客户端对网关响应体 `error.code` 的转述，非上游供应商错误。

## 二、证据链

### 客户端侧（本机 ~/.zcode/cli/log/zcode-2026-09-16.jsonl）

- `2026-09-16T13:13:33.383Z` `model.request.failed`：`baseURL=https://llmgo.kxpms.cn/v1`、`statusCode=413`、`statusMessage=prompt exceeds gateway budget: estimated 2099505 tokens > 2097152 limit`、`requestId=5f0f3d75-…`、`sessionId=sess_9edef815-…`、`turnNumber=88`
- 同秒 `turn.failed`：`responseBodySummary.error.code=prompt_too_large`（网关标准 413 信封形状，与 handler.go writeJSON 输出一致）
- 同型事件 09-15 起 multiple（2097301 / 2100339 等估值，modelId 有 minimax-m3 与 claude-opus-5）——**周期性复现，非孤例**
- TraceID `bb0483f0-…` 实为该 ZCode 会话的 `rootTraceId`（`2026-09-15T18:15:14Z` bootstrap.app.startup，workspace /Users/xutaohuang/workspace/smm），是客户端自产标识，与网关 trace 无关

### 网关侧（245 canary-8781，build 2125）

- `/var/log/llm-gateway-go/canary-8781-2026-09-16T21-24-27.181.jsonl.gz:858`：`21:13:33.425 CST safety_net_defer_fired request_id=1ca795598fd144d3d2c51e56643aa954 attempt_err_code=prompt_too_large` —— 与客户端 413 时刻 21:13:33.383 CST 相差 42ms（NTP 时钟偏差量级），同秒同错误码，配对成立
- 08-31 存档 gateway-*.log.gz 有完整同型链：UA `ZCode/3.10.1 ai-sdk/…` POST /v1/chat/completions，request_bytes≈4.25MB → 413；一秒前 4.20MB 同型请求 200 通过 —— 请求卡在预算边界的行为特征吻合
- 该 21 点窗口的 jsonl 轮转文件已被 logrotate 清理（本轮复核时仅存 23 点后段），证据以本报告与本会话早前抓取为准

### 代码侧（main @ 2f3151a27）

- 入口 guard×3：domains/streaming/handler.go:2245-2254（chat，唯一带 `(LLM_GATEWAY_MAX_PROMPT_TOKENS)` 后缀文案——用户报错文案与之一致，证实走 chat 入口）、messages.go:277-283、responses.go:271-277
- 限额：request_meta.go:37-38 `2*1048576`；env 回退 `LLM_GATEWAY_MAX_PROMPT_TOKENS`（:59）；热更 key `gateway.max_prompt_tokens`
- 估算：auto_route.go:234 `estimateTokens`（ASCII 4B/token、CJK 2 tokens/rune）——对纯中文 body 高估约 2 倍（真实 tokenizer ≈1 token/字），是「有压缩仍报超限」的次要放大因子，本轮未改（改口径属行为变更，需独立评审）
- `Turn execution failed / provider=…` 文案在 anthropic_bridge.go:238/1508 仅为注释（描述上游 relay 诊断块的拦截逻辑），非日志调用

## 三、本轮修正（三处真实缺陷，F2/F3 为 252 部署实抓）

### F1 handler.go:2234 客户端 wire 错误码 typo

32MiB body-cap 拒绝的 JSON `code` 写成了 `body_too-large`（连字符），与同函数 `SetError/EmitFailure`（`body_too_large`）、失败分类器（handler.go:6786/6846）、messages/responses 两入口全部下划线写法不一致。客户端按 `error.code` 归类时会拿到非法码值。

- 修复：`body_too-large` → `body_too_large`（1 行）
- 回归：prompt_budget_test.go 新增 `TestBodyTooLargeWireCodeConvention`（源级钉桩三入口禁现连字符变体，沿用该文件 `TestPromptBudgetWiredInEntryPoints` 的既有风格）
- 可达性备注：默认预算下该分支数学上不可达（estimateTokens ≤ len/4 ⇒ >32MiB body 必然先触发 2M tokens 预算 guard；本轮 33MiB 探针实测先得 prompt_too_large 413），仅 `gateway.max_prompt_tokens=0/off` 部署可达——正是 typo 能潜伏至今的原因

### F2 internal/liveactions typed-nil 崩溃（252 无 Redis 部署启动即 crash-loop）

`cmd/gateway/main.go:2165` 以 `redisClientForCache.Client()` 装配 `liveactions.NewEmitter`；Redis 不可用时 `Client()` 返回 nil `*redis.Client`，typed-nil 装进 `liveactions.Client` 接口后 `write()` 的 `e.client == nil` 守卫失效，首个事件在 `Pipeline()` 空指针 panic（进程退出，Restart=always 每 5s 循环，NRestarts 实抓 41）。245 未触发纯属侥幸——其 boot 窗口内 postgres disabled 使整段装配被跳过。

- 修复：NewEmitter 反射归一化 typed-nil → 非类型 nil（写侧既有 nil 守卫随之恢复语义）
- 回归：liveactions_test.go 新增 `TestTypedNilClientNormalised`

### F3 executors StickyCache.SetRedisStore typed-nil（252 公网流量每请求 500）

`main.go:1229` `var stickyStore *ursmcache.StickyStore` 在 Redis 不可用时保持 nil，`SetRedisStore(stickyStore)` 把 typed-nil 塞进 `StickyRedisStore` 接口；`GetMultiLevel` 的 `store != nil` 快照守卫失效，`StickyStore.GetLevel`（sticky.go:72）nil receiver panic。252 vhost llmgo.itestu.cn 公网可达，实抓多条 chat 请求 500（chat handler recover 兜底转 500，每请求一次）。

- 修复：SetRedisStore 反射归一化（sticky.go）+ main.go 装配点显式判空双保险
- 回归：sticky_typednil_test.go 新增 `TestStickyCacheTypedNilRedisStoreNoPanic`

## 四、部署验证

| 环境 | 方式 | 结果 |
|---|---|---|
| 本地 | `deploy-local.sh`（蓝绿 8781/8782） | ✅ 双实例 healthz ok + readyz ready（database/redis connected）；期间 PG 容器短暂崩溃恢复（57P03，local-deploy-test 已知 pitfall #7）导致一次 cutover 重试，PG 自愈后恢复 |
| 本地 413 复现 | 33MiB POST /v1/chat/completions（sk-e2e-test key） | ✅ 413 `prompt_too_large`（estimated 8650773 > 2097152），与用户报错同型 |
| 252 | 新增 `scripts/deploy-252-gateway.sh`（systemd `llmgo-252-dev`，127.0.0.1:8780，挂入既有悬空 vhost llmgo.itestu.cn） | ✅ 三修二进制部署后服务稳定：restarts=0（修复前 crash-loop NRestarts=41）、近 2 分钟 0 panic、healthz ok（git_sha 2f3151a2）、database connected；readyz 因首启 EnsureSchema 在 600s 预算窗口内继续收敛 |

252 首启踩坑（均已修入脚本，供后续同类部署对照）：
1. SSOT `LLM_GATEWAY_DATABASE_URL` 是 podman 网络视角 `127.0.0.1:5432`，宿主机需改写为 `172.16.2.210:5432`（pg-252-pg17 容器 IP）；
2. CORS fail-closed panic（middleware.NewCORSMiddleware）——必须显式 `LLM_GATEWAY_CORS_ORIGINS`（deploy-local 已知 pitfall #2 同款）；
3. 共享 PG 首启 EnsureSchema >20s 触发 `postgres disabled: context deadline exceeded`，`LLM_GATEWAY_DB_BOOT_RETRY_SECONDS` 放宽到 `600s`——**必须带单位**（time.ParseDuration，裸 `600` 静默回退 20s，实抓一整轮 crash-loop 后定位）。

252 说明：252 此前**无** llm-gateway-go 实例（8780 悬空 vhost、/opt 仅 schema 脚本、:18082 的 "gateway" 进程属 kxmemory-go 产品），本次按用户指令以脚本方式重建 dev 实例；local→245→154 晋级路径不因此改变，本修复进入 245/154 仍走各自晋级门禁。413 探针在 252 因共享生产 DB 无测试 key 返回 401（best-effort WARN 不阻断）；wire-code 行为由 F1 回归测试钉桩。

## 五、后续建议

1. **客户端侧**：ZCode 上下文压缩应在 prompt 接近网关预算（2M 估算 tokens ≈ 8MB body）前主动触发 handoff/压缩；88–109 轮才压已经越线。
2. **网关侧候选（未动，需评审）**：estimateTokens 的 CJK×2 口径对中文大 body 偏保守，可评估与真实 tokenizer 对齐（~1.3×）；或在 413 响应体中回传 `estimated_tokens`/`limit` 结构化字段便于客户端自愈。
3. 245 的 prompt_too_large 属预期防护行为，建议运维面板把 `safety_net_defer_fired attempt_err_code=prompt_too_large` 计数作为「客户端上下文超限」业务指标而非错误告警。
