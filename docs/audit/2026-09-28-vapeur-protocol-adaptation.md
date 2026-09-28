# Vapeur AI 协议适配审计（2026-09-28，R-vapeur）

**触发**：用户报告经本地网关以凭据 hxt-local（credential 126，provider 36 vapeur，
protocol=openai-responses）访问 gpt-5.6-terra / claude-sonnet-5 / claude-opus-5 /
gpt-6-sol / gpt-6-astra「经常出错不通」。

## 一、上游实测（hxt-local 直连 api.vapeur.ai，2026-09-28 12:2x–12:4x +08）

| 模型 | /v1/chat/completions | /v1/responses |
|---|---|---|
| gpt-5.6-terra | 200 | 200 |
| gpt-6-sol | 200 | 200 |
| gpt-6-astra | 200 | 200 |
| claude-sonnet-5 | 200 | **400「该供应商不支持 Responses API」(unsupported_operation)** |
| claude-opus-5 | 200 | **400 同上** |
| qwen 系（node_probe_state 佐证） | 探针记录 | **502「QWEN provider does not support the Responses API (/responses). Please use /v1/chat/completions instead.」** |
| doubao 系 | 同上 | 502 同型 |
| gemini 系 | 同上 | 400 同型 / timeout |

结论：vapeur 是多厂商聚合中转，**/v1/responses 仅对 GPT 系开放**；模型族协议
能力是「按模型」而非「按供应商」的——provider.protocol=openai-responses 的一刀切
假设不成立。

## 二、根因（三层叠加）

1. **探针按供应商协议一刀切打 /v1/responses**
   （providercap.Resolve openai-responses → EpResponses；node_probe/model_probe/
   active_probe/credential_probe_v2 四条链路同源）。非 GPT 模型族全部被上游 400/502
   拒绝 → node_probe_state 红 → URSM v2 节点视图 available=0 → **路由整体排除
   vapeur**（request_logs 实证：claude 系一直走 suyun 13092、terra 走 apigpt 314，
   provider 36 从未被选中）。上游剩余单点（apigpt 90s 挂起）时用户面即 503/超时。
2. **credential_probe_v2 step2 形态错配**：openai-responses 供应商的 step2 把
   chat 体（messages/max_tokens）POST 到 /responses URL——任何合规 Responses 端点
   都回 400 "Unsupported parameter: 'messages'"（实测），凭据级健康判定被污染。
3. **URSM v2 authoritative 全拒静默**：router.go filteredByViews 返回 0 候选时
   直接 return nil，零日志；executor 侧只见推断值 availability_check_failed:N，
   事故无法从请求日志归因。

另：reqprobe 数据面同请求回退（Responses→Chat）已存在，但文案匹配表缺 CJK
「该供应商不支持 Responses API」变体（语序与英文 "responses api is not" 相反），
vapeur 400 不触发回退。

## 三、修复（全部落地 + 单测）

| # | 文件 | 内容 |
|---|---|---|
| 1 | internal/providercap/capability.go | 新增 `ResponsesUnsupportedError(status, body)`：状态码白名单(400/404/405/415/422/501/502) + 「提及 Responses API 且带否定/重定向 chat」文案判定；参数拒绝类（"Unsupported parameter: 'messages'"）不误判 |
| 2 | bg/probe_responses_fallback.go（新） | 共享降级原语 `responsesChatFallbackPing`（chat/completions、max_tokens=10、Bearer）+ 统一注记 `responsesUnsupportedDetail` |
| 3 | bg/node_probe.go probeDirect | responses 探针命中「不支持」裁决 → chat 降级复探；chat 绿则本轮 ok（chatFallback 标记）；红则维持原结论+附注 |
| 4 | bg/probe_http.go probeWithRetry | Layer 4（model_probe 链）同款降级，阻断 broken_confirmed → binding 压红 |
| 5 | bg/active_probe_executor.go Run | 主动探针同款降级（结果回灌 CredentialStateManager 前纠正） |
| 6 | bg/credential_probe_v2.go | step2 对 openai-responses 改发原生 `miniResponses`（{"input","max_output_tokens"}）；命中不支持裁决 → chat 降级 |
| 7 | internal/reqprobe/diagnose.go | modeMismatchHints 补 CJK/中转变体（"不支持 responses api"、"does not support the responses api"）→ 数据面同请求回退可命中 |
| 8 | domains/streaming/executors/router.go | authoritative 全拒补 WARN（view_sample），消除静默 |

单测：internal/providercap/responses_unsupported_test.go（10 例含误判反例）、
bg/probe_responses_fallback_test.go（5 个行为测试）、reqprobe_test.go（CJK/英文
2 例）。`go test ./bg/ ./internal/providercap/ ./internal/reqprobe/
./domains/streaming/executors/` 全绿；`go build ./...` 通过；gofmt 干净。

## 四、部署与验证（本地 8782）

- 部署 2.5.6.2295→2296（2296 修正密钥漂移：deploy 从 .env.local 取到 43 位错误
  SECRET_KEY，凭据解密 12/16 失败；以旧容器真值 64/44 位导出重部署后 failed=0。
  教训：**本地部署密钥必须显式导出，.env.local 的 SECRET_KEY 是错值**）。
- 探针生产证据（node_probe_runs，2296 上）：
  - claude-opus-5-5 / claude-haiku-4-5：direct_ok=t，
    detail="responses probe rejected (HTTP 400: Responses API unsupported for this
    model); chat fallback probe OK (HTTP 200)"；URSM 视图翻绿；claude-opus-5 视图
    亦翻绿。
  - credential_model_bindings 8 个目标模型 available 全 t。
- 数据面矩阵（2296，测试 key 用后即删）：
  - /v1/chat/completions 入口：5 模型全 200（gpt-6-astra 经 vapeur chat 上游
    200——修复前该路径被探红排除）。
  - /v1/responses 入口：5 模型全 200（GPT 系 resp_ id、Claude 系经翻译 msg_ id）。
- 遗留（非阻断）：URSM 视图 claude-sonnet-5/qwen 系随探针队列（1 worker）慢速
  自然翻绿；apigpt(apiclaude.cc) 上游偶发 60-90s 挂起为独立供应商问题；
  glm-5「Agent capabilities are not enabled」为 GLM 上游对探针的真实拒绝；
  gemini 系 responses 超时为上游真慢——均与本修复无关。

## 五、「默认使用 responses」的语义澄清

用户期望「默认走 responses」在网关侧的现状与边界：
- **上游出站**：openai-responses 供应商默认端点=/v1/responses（保持）；探针/数据面
  在被上游判「不支持」时自动降级 chat（本修复）。原生 Responses 传输（保留
  Responses 信封不翻译）仍由 credential_model_capabilities 能力闸门控制
  （SupportsNativeResponses(Stream)，目前仅 grok-4.6 有记录）。
- **vapeur 的现实**：responses 端点仅 GPT 系可用，Claude/qwen/doubao/gemini 必须
  chat——「默认 responses + 按模型降级 chat」即最优策略，已实现。
