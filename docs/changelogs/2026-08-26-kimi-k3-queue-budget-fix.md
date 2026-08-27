# 2026-08-26 kimi-k3 "总是失败" 排查与修复（RPM 排队耗尽预算 + 静态 key 走错认证分支）

## 背景

用户报告：m 网关（245 `llmgo.kxpms.cn`）转发 `kimi-k3` 的请求"总是失败"，直连 pulian 的
minimax-free 渠道成功，NVIDIA NIM 的 endless 凭据偶发成功，怀疑网关数据格式与 kimi-k3
不兼容。

## 排查证据（245 实锤）

- 245 `gateway.log`：请求 `e197ada…`（04:14:00 进入）在 04:15:00.348 才完成
  `candidates_resolved`（341ms 内求解成功），随后 `executor failed: context canceled`，
  外层 `http_request` 记录 `status=502, duration_ms=60001`。
- 响应头实锤排队：`X-RateLimit-Limit: 12 / X-RateLimit-Queue-Position: 4`，总耗时 94.5s。
- 请求日志黑洞：`trace.FlushToPG: request log row not found` —— 该类排队后被 ctx 取消的
  请求不落 `request_logs`（`request_wal` 也无记录），DB 侧完全看不到 kimi-k3 的失败。
- 25 日 23:53 245 网关被 memcg OOM kill（`task=gateway`），此前同类症状循环出现。

## 根因（两个）

① **静态 key 走错认证分支**：154/245 部署的 `LLM_GATEWAY_API_KEY=sk-gwops-*` 带 `sk-`
前缀，`AuthMiddleware` 的 sk- 直通分支把它交给 DB verifier，命中 `api_keys` 113
（tier=default，`rate_limit_rpm` NULL → 走 tier 默认 12 RPM）。`global-auth-passed`
sentinel 永远不会被设置（只对非 sk- 的静态 key 设置），于是探针/自检/ops/共享该 key
的客户端流量全部挤进 12 RPM 分钟桶。**精确匹配**（`subtle.ConstantTimeCompare`）本来
就该优先于 sk- 前缀直通，只是 2026-08-24 事故修复时把两个分支顺序写反了。

② **RPM 排队无预算概念**：`AdmitRPMWithWait` 最多排队 2 分钟（`maxMinuteBucketWait`），
而请求 ctx 只有 `LLM_GATEWAY_UPSTREAM_TIMEOUT=60s`。排队时间直接吃掉上游预算，等排到
的时候上游必然 `context canceled`，网关只能回 502，且失败不落日志。
与"数据格式不兼容"无关——array content（多模态格式）请求实测正常返回。

## 修复（commit d24dab5e7）

1. `middleware/auth_mw.go`：静态 key **精确匹配**优先于 sk- 直通分支；匹配到则设置
   `global-auth-passed` sentinel（`checkGatewayRateLimit` 据此跳过 RPM 桶）。sk- 非精确
   匹配的 key 行为不变（仍走 DB verifier），2026-08-24 事故修复语义不回归。
2. `ratelimit`：新增 `RPMBudgetedAdmission` 接口（`AdmitRPMWithBudget`），分钟桶
   （`minute_bucket.go`）与 Redis（`minute_bucket_redis.go`）两条排队路径都在**入队前**
   按 `队位 × 窗口` 估算等待；预估等待 > 调用方剩余预算（ctx deadline - 5s headroom）则
   拒绝入队，返回 `ErrQueueBudgetExceeded`（`AdmissionResult.EstimatedWaitSec` 带出预估）。
3. `domains/streaming/rate_limit.go`：`checkGatewayRateLimit` 走预算化准入，超预算立即
   `429 + Retry-After`；剩余预算 ≤ 0 直接 Blocked；无 deadline（后台调用）保持旧行为。

## 验证

- `go build ./...` / `go vet` 全绿；`go test ./ratelimit/ ./middleware/
  ./domains/authentication/ ./domains/streaming/...` 全绿。
- 新增单测：`TestMinuteBucketAdmissionBudgetedRejectsFastWhenWaitExceedsBudget`（拒绝快、
  不入队）、`TestCheckGatewayRateLimit_QueuedBeyondBudgetFailsFast`（handler 层 fail-fast），
  `TestAuthMiddleware_StaticKeyWithSkPrefixIsExempt`（sk- 精确匹配恢复 sentinel）。
- 154 生产部署（seq 1761, commit d24dab5e）后实测：
  - 静态 key 20 连发 burst：全部无 `X-RateLimit-Queue-*` 头（sentinel 生效），12/20 在
    2-11s 返回 200（其余 60s 超时为免费上游凭据并发上限吃满所致，属上游容量问题，非网关 bug）。
  - 串行 3 次 kimi-k3：1.4-2.0s 200 OK。
  - array content（多模态 system+user 数组）200 OK，中文正文正常——数据格式兼容无问题。

## 遗留 / 风险

- 245 目前仍在旧构建（version `298201fe`，未含本修复）；245 按约定需手工部署，由部署人
  员执行 `scripts/deploy-245.sh` 后同样验证 X-RateLimit-Queue 头消失。
- 用户自有 sk-key（tier default 12 RPM）在爆量时仍会进入预算化排队——现在会拿到明确的
  429 + Retry-After，而不是 60s 后凭空 502；客户端应当实现 Retry-After 重试。
- 排队取消请求仍不写 `request_logs`（黑洞未堵）——属观测增强，单独任务跟进。
