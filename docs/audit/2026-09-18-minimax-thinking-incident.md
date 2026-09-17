# 2026-09-18 MiniMax `thinking.type=enabled` 拒收事故（hzx-2 / minimax-prod-v2 强启即降级）

- 环境：245 预发布（pre-prod），provider 14 = `minimax`（https://api.minimaxi.com/v1）
- 受影响凭据：cred 42（hzx-2）、cred 21（minimax-prod-v2），均绑定 `MiniMax-M3` 等模型
- 修复 commit：`6d474a725`（translateThinking v1）+ 本轮 v2（budget_tokens 剥离）
- 状态：代码已合入 main；部署与验证记录见 §5

## 1. 现象

- 客户端直连 MiniMax 原厂（`MiniMax-M3` + thinking）正常；
  走网关同一模型 100% 失败。
- 管理端对 cred 42/21 执行 force_enable 后，数十秒内再次被降级
  （breaker 开→关循环，冷却 30s 一轮）。

## 2. 根因

MiniMax 上游（2026-09 起）将 `thinking.type` 白名单收窄为
`adaptive | disabled`，对 `enabled` 返回 HTTP 400：

```
{"type":"error","error":{"type":"bad_request_error",
 "message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)",
 "http_code":"400"}}
```

（journald `candidate_failure_alert`，2026-09-18 05:13，cred 21。）

这是生态级变更，非本网关独有：Kilo Code 同期因发送 `enabled` 出现同类故障
（github.com/Kilo-Org/kilocode/issues/11203）。

网关侧的传导链（OpenAI 协议入向，zcode 的路径）：

1. `internal/ir/parse_openai.go`：`thinking` 不在 knownFields →
   进入 `req.Extensions["thinking"]`（`ir.Thinking` 保持 nil）。
2. `internal/ir/serialize_openai.go:182` → `restoreExtensions(..., ProtocolOpenAIChat)`。
3. `internal/ir/extensions_restore.go:78` →
   `paramreg.Apply("thinking", val, src, dst=DialectMiniMax)`。
4. 修复前 `thinking` 登记为 `KindIRHandled`，`Decide()` 返回
   `ActionRestore`（decide.go:117-134 的 2026-08-11 设计决策：Extensions
   里的字段必然是某条 parser 路径未消费的，不能 Skip）。
5. 客户端原值 `{"type":"enabled",...}` 被原样写进 MiniMax-bound body →
   上游 400。
6. `credstate` 连续失败计数达阈值(2) → breaker open → 「强启即降级」。
   force_enable 链（DB + 内存缓存 + URSM v2，`admin/routing.go:5436`
   applyForceEnable）本身行为正确——每个新请求仍携带坏 body，
   所以状态修好又被同一坏请求打回。

关键佐证：事发时 DB 状态全部健康
（`circuit_state=closed / consecutive_failures=0 / availability_state=ready`），
且 400 与 force_enable 无时间耦合，只与请求到达耦合。

## 3. 修复

`internal/paramreg`：

- `translate.go`：新增 `translateThinking(value, src, dst)`：
  - `dst != MiniMax` → 原样透传（DeepSeek/GLM/Kimi/Ark/Anthropic 语义不变）；
  - `dst == MiniMax` 且 `type == "enabled"` → 输出**最小对象**
    `{"type":"adaptive"}`。budget_tokens 等伴生字段一并丢弃：
    MiniMax M3 的 thinking 无 budget/深度控制（官方 Responses API 的
    effort 档位也只是 adaptive 的兼容别名），残留 MiniMax 不认识的字段
    在严格校验下可能再次 400；
  - `type` 为 `adaptive`/`disabled` 或非对象 → 原样透传。
- `registry.go`：`thinking` 从 `KindIRHandled` 升为 `KindTranslatable`，
  绑定 `Translate: translateThinking`，Note 补记上游拒收事实。
- `decide_test.go`：`TestDecide_TranslateThinking_EnabledToAdaptive`
  覆盖 5 个子用例（MiniMax 改写为最小对象 / adaptive 不变 / disabled
  不变 / DeepSeek 透传 / Anthropic 透传）；
  `TestIRHandledFields` 同步把 thinking 移出期望集合。

附带修复：`cmd/gateway/main.go` 补 `net` import —— commit `cd76a7f0e`
（discovery mDNS）引入 `net.SplitHostPort` 但漏加 import，导致该提交之后
main 分支 HEAD 本地构建必失败（`go build ./cmd/gateway` 报
`undefined: net`）。

## 4. 覆盖范围与已知缺口（实事求是）

| 路径 | 状态 |
|---|---|
| OpenAI 协议入向 + OpenAI 形态上游 → MiniMax（zcode 路径） | ✅ 本修复覆盖 |
| OpenAI 协议入向 → 非 MiniMax 上游 | 行为不变（透传） |
| Anthropic 协议入向 → OpenAI 形态上游（**含 MiniMax**） | ❌ **不在本修复内**：`parse_anthropic` 把 thinking 消费进 `ir.Thinking`（不进 Extensions），`serialize_openai` 对 `ir.Thinking` 仅上报 protocol loss、不输出 → thinking 被静默丢弃（不会 400，但推理意图丢失）。属 P5 reasonnorm 统一处理范畴，需单独排期 |
| `reasoning_effort`/`reasoning` 字段的方言翻译 | 不变（本事故未涉及） |

## 5. 部署与验证记录

### 5.1 时间线（含失误复盘）

- 05:28 首次 `deploy-245.sh`：构建 2127（含 v1 修复）成功上传，
  但 step 9.1 sync-admin 撞 252 共享 PG `too many clients`（当时
  38/100 连接占用）→ 脚本按契约自动回滚 nginx，未切流。
- 05:35 手工蓝绿：slots/8782 → 2127，restart，8782 成为 active。
  05:35–05:42 窗口真实流量验证：cred 21 上游 `200×11`、cred 42/21
  的 4xx=0（此前同型请求 100% 400，错误预览均为 thinking.type）。
- 06:14 **并发部署事故**：official-deploy worktree 的另一次部署上线
  2133/2134（构建于 06:14，早于 06:16 的修复 push，**不含修复**），
  并把 active 切到 8782@2134，同时清掉了 slots/8782 → 2127 的指向。
  教训：手工改 slot 无法对抗正规部署流程；修复必须先进 origin/main
  再靠下一次正规部署承接。
- 06:16 修复 push origin/main（`6d474a725`）→ 后续正规部署自动携带。
- 本轮（v2 修复后）：重新正规部署 + E2E 验证，结果见 §5.2。

### 5.2 E2E 验证

（部署后回填：见提交说明与本文件 git 历史。验证方法：有效网关 API key
经 `/v1/chat/completions` 发 `thinking:{type:enabled,budget_tokens:N}`
到 `minimax-m3`，观察 journald `upstream_http_attempt` 的
`upstream_status`——期望 200，且窗口内 cred 42/21 无 400。）

## 6. 遗留风险

1. **Anthropic 入向 thinking 丢弃**（§4）：Claude Code 等Anthropic 协议
   客户端经网关到 MiniMax/DeepSeek 等 OpenAI 形态上游时推理意图丢失。
   当前靠客户端不发 thinking 或走 OpenAI 协议规避。
2. **并发部署竞争**：多人/多 worktree 同时 deploy 245 仍可能互相覆盖
   slot 与 active-port（本轮实测发生）。deploy 锁只锁同机同脚本，
   不锁不同 worktree 的并发 deploy-seamless。
3. **252 共享 PG 连接上限**（max_connections=100）：sync-admin 等部署
   步骤在高占用时段会 FATAL `too many clients` 触发不必要的自动回滚。
   2133 已带 sync-admin 重试，但连接池根因未治。
4. **version.json SSOT 多读路径**：`/api/system/version`（admin 读
   `/opt/llm-gateway-go/version.json`，进程内 5s 永久缓存直到重启）与
   `/healthz`（读 slots/%i/version.json）在手工换版本时可能短暂不一致。
   正规部署路径两者同步，不受影响。
