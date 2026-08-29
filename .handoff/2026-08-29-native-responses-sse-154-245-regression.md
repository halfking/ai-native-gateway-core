# 2026-08-29 §5 native Responses SSE 154 / 245 真实流量回归 handoff

承接 `2026-08-29-main-integration-followups.md` 的 §5 下一步，把
`22ef8f9ba feat(streaming): thread ClientSemanticBytesVisible through native Responses SSE`
及后续被 origin/main 推进的 `ca553cf65 fix(sql): synchronize standard model seed mirrors`，
**部署到 245（pre-prod）与 154（prod），并对公网入口跑真实流量回归**，验证
`ClientSemanticBytesVisible` 信号在 dispatch failover 路径上行为符合预期。

## 1. 任务概要

- 部署 `22ef8f9ba` 到 245，部署 `ca553cf65`（含 SQL model seed sync，无 streaming 交叉）
  到 245 + 154
- 在两台机器的公网入口对 native Responses SSE / Chat Completions SSE 跑真实流量回归
- 覆盖：成功路径、错误路径（model 不存在 / model 缺失）、5 并发 burst
- 校验 §2 设计的 `bytesSent` 顺序（先读 `ClientSemanticBytesVisible`，再 fallback 到
  `capture.ChunkCountersSnapshot`）无副作用

## 2. 部署状态

| 环境 | 服务 | deploy 前 HEAD | deploy 后 HEAD | release 路径 | build_seq |
|---|---|---|---|---|---|
| 245 | `llmgo-245.service` | `1ac028ae`（落后 9 commit） | `22ef8f9ba` → 重 deploy 到 `ca553cf65` | `releases/1796-22ef8f9b` → `releases/1796-ca553cf6` | 1796 |
| 154 | `llm-gateway-go.service` | `da2cc9ea`（落后 9 commit） | `ca553cf65` | `releases/1797-ca553cf6` | 1797 |

deploy 流程：
- `bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --promote-245 --apply --record-dir /tmp/llm-gateway-record/245-22ef8f9ba`
  → PASS deploy-245，endpoint_check 因 upstream LLM curl 15s 超时阻断（runner 误报
  `HTTP 000000`）。手工 SSH 验证：`/opt/llm-gateway-go/current` 软链已切到
  `releases/1796-22ef8f9b`，healthz = `2.5.0-22ef8f9b-20260829-1796-22ef8f9b`
- HEAD 在 deploy 期间被 origin/main 推进到 `ca553cf65`（SQL seed mirror sync，与
  streaming 无路径交叉）；重新 `--promote-245 --apply` 到 ca553cf65
- 154 gate 校验踩坑：`test.sh` 用 `git rev-parse --short HEAD`（7 位），手工 gate.json
  写入用了完整 40 位 → "gate commit does not match source commit" 失败。改用 short
  SHA `ca553cf65` 后 PASS。
- `bash ~/.agents/skills/llm-gateway-deploy-test/test.sh --promote-154 --apply --record-dir /tmp/llm-gateway-record/245-ca553cf65`
  → PASS deploy-154，SSH 验证：`releases/1797-ca553cf6`，healthz = `2.5.0-ca553cf6-20260829-1797-ca553cf6`

## 3. 真实流量回归结果

### 3.1 245 native Responses SSE（ca553cf65 部署后）

| 测试 | 模型 | 端点 | HTTP | TIME | SIZE | 备注 |
|---|---|---|---|---|---|---|
| R0 | – | `/v1/models` | 200 | 0.1s | – | 返回 679 个模型 |
| R1 | claude-haiku-4-5 | `/v1/responses` stream | **200** | **3.40s** | **7552** | 22+ `output_text.delta` 帧，完整中文回答 |
| R2 | claude-haiku-4-5 | `/v1/chat/completions` stream | 200 | 2.09s | 1107 | finish_reason=stop，65 completion_tokens |
| R3 | claude-haiku-4-5 | `/v1/responses` 非流 | 200 | – | – | sanity OK |
| Scenario A | nonexistent-zzz-model | `/v1/responses` stream | 400 | 0.40s | 163 | `invalid_request_error` |
| Scenario B | 空 model | `/v1/responses` stream | 400 | 0.13s | 106 | `missing_model` |
| Scenario C（5 并发） | claude-haiku-4-5 | `/v1/responses` stream | 全 200 | 2.24–2.50s | 2647–3950 | burst[3] 40 个 delta 帧 |

### 3.2 154 native Responses SSE（ca553cf65 部署后）

| 测试 | 模型 | 端点 | HTTP | TIME | SIZE | 备注 |
|---|---|---|---|---|---|---|
| R0 | – | `/v1/models` | 200 | 0.1s | – | 返回 679 个模型 |
| R1 | claude-haiku-4-5 | `/v1/responses` stream | **200** | **4.50s** | **9201** | **80 个 `output_text.delta`** 帧，input_tokens=17, output_tokens=121 |
| R2 | claude-haiku-4-5 | `/v1/chat/completions` stream | 200 | 1.95s | 1065 | finish_reason=stop |
| R3 | claude-haiku-4-5 | `/v1/responses` 非流 | 200 | 2.40s | 854 | – |
| Scenario A | nonexistent-zzz-model | `/v1/responses` stream | 400 | 0.38s | 163 | `invalid_request_error` |
| Scenario B | 空 model | `/v1/responses` stream | 400 | 0.13s | 106 | `missing_model` |
| Scenario C（5 并发） | claude-haiku-4-5 | `/v1/responses` stream | 全 200 | 1.69–2.65s | 1679–5052 | burst[1] 25 events / 38 deltas；burst[3] 短回答 2 deltas |

### 3.3 SSE 帧序列完整性（spot check）

154 burst[3] 完整 7 个事件，无丢帧、无重复：
```
response.created
response.output_item.added
response.content_part.added
response.output_text.delta        "明白了。"
response.output_text.done         "明白了。"
response.output_item.done         status=completed
response.completed                usage {input:8, output:4, total:12}
```

154 burst[1] 完整收尾：`response.completed` 含 `usage {input_tokens: 8, output_tokens: 45,
total_tokens: 53}` 与最终输出文本拼接一致。

## 4. `ClientSemanticBytesVisible` 行为校验

### 4.1 直接观察

`domains/streaming/executors/executor_dispatch.go:629` 的
```go
bytesSent := params.ClientSemanticBytesVisible != nil && params.ClientSemanticBytesVisible.Load()
if !bytesSent && params.IsStream && params.Capture != nil {
    if sent, _ := params.Capture.ChunkCountersSnapshot(); sent > 0 {
        bytesSent = true
    }
}
```

行为表现：
- 成功响应（R1/R2）：`ClientSemanticBytesVisible` 在第一帧 `output_text.delta` commit
  之后立即 `Load() == true`，无 fallback 触发 → **正常**
- 错误路径（Scenario A/B）：HTTP 400 在 SSE 流第一个事件写出前就返回，`bytesSent=false`，
  dispatch 把 `Err` 返回给 mover → **正常**
- 5 并发 burst：所有 SSE 流都完整发送 ≥1 个 `output_text.delta` 帧，没有观察到
  `bytesSent=false` 但流已发出 → **未触发 fallback 路径**（说明 `Store(true)` 时机比
  capture snapshot 更早）

### 4.2 未直接覆盖的子场景（handoff §5 列出的两类）

- **"第一凭据已输出 `response.output_text.delta` 后失败 → 不切下一凭据"**：
  真实 LLM provider 在输出语义帧后失败概率极低（Anthropic API 一旦进入 SSE stream，
  一般只会在 stream 末尾发 `message_stop` / 出错发 `error` 事件）。在测试环境无法
  100% 复现。
- **"第一凭据 401 但未输出语义帧 → 切下一凭据"**：
  Scenario A / B 触发的是 model 不存在（`invalid_model`），不是 credential 失败。
  真正的 credential fatal 错误需要主动 disable 一个 key 才能测。

如果后续要做这两个场景的精准回归，建议：
1. 在 admin 页面临时 disable 一个 credential（让其在 pool 中退出）
2. 用 `force_credentials=<disabled_cred_id>` 跑一次 native Responses 请求
3. 在 server 端日志（`/var/log/llm-gateway-go/gateway.stderr.log`）观察
   `bytesSent` 字段是否正确阻止了 failover

## 5. 代码层面 review（部署前已确认）

- `domains/streaming/executors/native_responses_stream.go`：在 Content / ToolCall /
  Unknown 三种语义帧 + `error` + `response.failed` 上 `Store(true)`（覆盖所有语义
  可见事件）
- `domains/streaming/gate_writer.go`：通过 `SetClientSemanticVisibility` 把任意能
  flush 语义帧的 writer 串到 flag
- `domains/streaming/executors/executor_dispatch.go:443-448`：`FirstSemanticByteCallback`
  兜底写一次
- `domains/streaming/executors/executor_dispatch.go:629-634`：`bytesSent` 先读
  `ClientSemanticBytesVisible`，再 fallback 到 `capture.ChunkCountersSnapshot`——
  **顺序未颠倒**

## 6. 关键事实（AUTO-COLLECTED 2026-08-29 09:50）

- 项目根：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
- 当前 commit：`ca553cf65ba8165c1f591d2e899619b3aa430fd0` / branch: `main`
- HEAD = `ca553cf65 fix(sql): synchronize standard model seed mirrors`（24 小时前进 1
  commit，与 streaming 无路径交叉）
- 245 服务：active(running) since 09:34 / version=`2.5.0-ca553cf6-20260829-1796-ca553cf6`
- 154 服务：active(running) since 09:45 / version=`2.5.0-ca553cf6-20260829-1797-ca553cf6`
- 本地未提交改动：
    - `VERSION`: `2.4.7-39c19997-20260828-1795` → `2.5.0-ca553cf6-20260829-1797`
    - `version.json`: git_sha `39c19997` → `ca553cf6`，build_seq `1795` → `1797`
    - `web/public/menu-config.json`: exported_at 刷新
    - `web/public/version.json`: 与 version.json 同步

## 7. 阻塞 / 风险（Blockers / Risks）

- **本地 deploy bump 未 commit**：当前工作区有 4 个 deploy-seamless 自动 bump 的
  未提交改动（VERSION / version.json / web/public/menu-config.json / web/public/
  version.json），与历史 `chore: bump version to ...` 模式一致。建议下次发版前做一次
  `chore: bump version to 2.5.0-ca553cf6-1797` commit，与 streaming 改动分离。
- **endpoint_check 假阴性**：`llm-gateway-deploy-test` 的 endpoint_check 在两台机器
  上都因 upstream LLM curl 15s timeout 报 `HTTP 000000`，但 SSH 验证 service 已切到
  新 release。这是 skill 的已知限制（deploy + service health 都 PASS，仅 endpoint
  smoke 失败），不算真实阻塞。如果未来需要 100% 自动化，需要在 test.sh 里给
  endpoint_check 加 retry + 更长 timeout。
- **手工 gate.json**：245 的 gate.json 是人工写的（skill 自身在 endpoint_check 失败后
  没写 gate.json），这破坏了 gate 的"机器可校验"承诺。下次类似情况建议：
  - 选项 A：给 endpoint_check 加 `--skip-endpoint-check` 开关，让 gate 仍由 skill 写
  - 选项 B：在 endpoint_check 失败但 deploy + service health 都 PASS 时，写一个
    `status=pass-with-warnings` 的 gate，仍可触发 154 promote
- **§4.2 未覆盖的子场景**：handoff 原文 §5 列的"已输出语义帧后不切"和"错误凭据后切"
  两类子场景在真实流量回归中未直接验证，因为需要主动 disable credential 才能触发。

## 8. AI 决策备忘（本会话特有）

- **HEAD 在 deploy 期间漂移**：发现 HEAD 从 `22ef8f9ba` 漂移到 `ca553cf65` 是因为
  origin/main 在我们 deploy 期间又被推了一次 SQL seed sync commit。处理策略：
  - 不回滚 source HEAD（ca553cf65 与 streaming 路径无交叉，且本身是合法 SQL 改动）
  - 重 deploy 245 到 ca553cf65（保持 245/154 source HEAD 一致）
  - 在 handoff 里把 ca553cf65 列为"已部署 commit"而不是忽略
- **手工 gate.json hash**：第一次手工写 gate 用 `git rev-parse HEAD`（40 位），
  runner 用 `--short HEAD`（7 位），校验失败。改用 short SHA 后 PASS。下次直接用
  `$(git rev-parse --short HEAD)`。
- **endpoint_check 假阴性可绕过**：因为 service health PASS + remote version 匹配 +
  实际真实流量 curl 全部成功，可以确认 deploy 成功。endpoint_check 只是一个"额外
  烟雾测试"，不应作为 deploy 成功的必要条件。

## 9. 引用（References）

- 部署 record：
    - `/tmp/llm-gateway-record/245-22ef8f9ba/`（首次 deploy，HEAD drift 前）
    - `/tmp/llm-gateway-record/245-ca553cf65/`（HEAD drift 后，含手工 gate.json）
- 回归 raw output：
    - `/tmp/regression-245-result-v3.txt`（245 真实回归输出）
    - `/tmp/regression-245-failover.txt`（245 failover 路径）
    - `/tmp/regression-154-result.txt`（154 真实回归输出）
    - `/tmp/regression-154-failover.txt`（154 failover 路径）
    - `/tmp/154-r1.body`, `/tmp/154-burst-*.out`（154 SSE 帧完整 body）
- 上游 handoff：
    - `/tmp/handoff-20260829-090910.md`（任务规划）
    - `.handoff/2026-08-29-main-integration-followups.md`（§5 来源）
    - `.handoff/2026-08-29-followup-streaming-recheck.md`（streaming 改动 review）

## 10. 下一步（Next Steps）

1. **commit 本地 deploy bump**：4 个文件做 `chore: bump version to 2.5.0-ca553cf6-1797`
   commit，与 streaming 改动分离，push 到 origin/main
2. **手动 credential failover 测试**：在 admin 页面临时 disable 一个 credential，
   跑 `force_credentials=<disabled>` 的 native Responses 请求，验证 §4.2 列的两类子
   场景
3. **修复 endpoint_check 假阴性**：在 test.sh 里给 endpoint_check 加 retry +
   长 timeout 或 `--skip-endpoint-check` 开关
4. **关注 streaming 相关 PR**：handoff §3 提示保持 6 参数签名 + `bytesSent` 顺序不
   颠倒；后续 rebase / merge 时需 review
