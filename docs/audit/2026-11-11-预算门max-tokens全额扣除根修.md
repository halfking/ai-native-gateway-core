# 2026-11-11 context_budget_exceeded「input cannot fit the selected bucket budget」——body max_tokens 全额扣除根修

> 用户报障（ZCode）：`Turn execution failed / provider_code=context_budget_exceeded
> model=claude-sonnet-5 reason=unknown retryable=false`，错误信息
> `input cannot fit the selected bucket budget`（TraceID 824e8e19…，request=6676b0fa…）。
> 结论先行：**网关把 body 的 max_tokens（输出上界）当成必然同时占满的输入预留全额扣除**，
> claude-sonnet-5（200k 窗）× ZCode max_tokens=128000 ⇒ 输入预算只剩 **72000**，
> ~76.8k 的校准估计被拒 ⇒ 压缩链字节目标 216KB 装不下受保护区 ⇒ 连续
> ErrProtectedContext ⇒ 422 retryable=false。已根修 + 门钉死。

## 1. 实证（211，2026-11-11 00:46–00:50 +08，新部署容器 b）

- `executor failed: all 0 candidates failed: context_budget_exceeded: model
  claude-sonnet-5: context budget cannot contain the protected task, instructions
  and active tool chain` ×4（16:46:34 / 16:47:22 / 16:47:55 / 16:48:11 UTC，同一
  ZCode 会话连续重试）——压缩器（sessioncontext.Fit）报受保护区（系统指令+工具链+
  最近轮）本身超出 fit 字节目标。
- 随后 16:50:19：`final model input exceeds bucket budget (actual=76792
  limit=72000)`——**limit=72000 就是根因的量化指纹**：200000 − 128000 = 72000
  ⇒ 该会话 body 带 `max_tokens=128000`（ZCode 对 sonnet 的输出上界 ask）。
- 请求面：POST /v1/chat/completions，request_bytes=298407，duration 641ms（本地
  快速拒绝、未耗任何上游尝试），ZCode/3.17.2。
- 历史同族（session_turns，session_bodies/turns 存储流 11-10 16:44 +08 后停写，
  取到此前样本）：claude-sonnet-5 上 231746 / 163034 / 158433 / 48342 / 47172 /
  42008 估计值全部以 context_budget_exceeded 终结——48k/42k 一族同样小于半窗
  100k 却被拒，与「更大 max_tokens ask ⇒ 更小 limit」的全额扣除算术一致。

## 2. 机制链

1. `internal/contextbudget` 的 `Check`：`inspect` 从 body 提取 max_tokens /
   max_completion_tokens / thinking.budget_tokens 等作为 `outputReserve`，然后
   `spec.OutputReserve = max(spec.OutputReserve, info.outputReserve)` **全额**并入。
2. `Resolve`（已知窗口臂）：`limit = min(MaxInputTokens, window/2,
   window − OutputReserve − SafetyMargin)`。max_tokens=128000 > 100000（半窗）
   ⇒ 第三项 72000 成为绑定项。
3. 校准估计器（runes/3.5 + blob/5 + 每节点 16）对 298KB 会话估出 ~76.8k > 72000
   ⇒ TokensExceeded。
4. 压缩链（executors.fitCandidateContextBudget）：字节目标 = InputTokens×3 =
   216000B；sessioncontext.Fit 只裁**旧历史**，受保护区（系统+工具链+尾组）>
   216KB ⇒ `ErrProtectedContext` ⇒ `retryable=false` 422。
5. 语义错位：**max_tokens 是「本轮允许的输出上界」，不是「必然同时占满的预留」**。
   Anthropic/OpenAI 请求期都不校验 input+max_tokens ≤ window——生成到窗口耗尽
   自然收尾（end_reason=length）。把上界当预留 ⇒ 窗口被输出 ask 吃掉 64%、
   合法长会话全灭。这也与 2026-11-10 分层执法的既定哲学一致：「容量裁决归上游，
   其 context_length 错误由网关既有恢复链处理」。

## 3. 修正（internal/contextbudget/budget.go CheckWithCounter）

body 侧输出上界**按半窗封顶后**再并入预留：

```go
if info.outputReserve > 0 && spec.ContextWindow > 0 {
    capped := min(info.outputReserve, spec.ContextWindow/2)
    spec.OutputReserve = max(spec.OutputReserve, capped)
} else if info.outputReserve > 0 {
    spec.OutputReserve = max(spec.OutputReserve, info.outputReserve)  // 未知窗口臂不参与预算
}
```

效果（`window/2` 本就是 min 独立项，故收敛为**唯一行为变化**）：

| 场景（window=200k） | 旧 limit | 新 limit |
|---|---|---|
| 无 max_tokens | 100000 | 100000（不变） |
| max_tokens ≤ 100k | 100000 | 100000（不变） |
| **max_tokens=128000（事故）** | **72000** | **100000** |
| max_tokens=190000 | 10000 | 100000 |

- 不变量：输入永远 ≥ 半窗；输出至少保住 min(max_tokens, 半窗)。
- **桶侧显式配置的 `spec.OutputReserve`（spec 直配）不做封顶、仍全额扣除**——
  配置语义不变（Resolve 一行未动，Resolve 级既有断言全部原值通过）。
- 事故请求在新预算下：76792 ≤ 100000 ⇒ Check 直接放行，不进压缩链。

## 4. 门（有牙）

- 新增 `TestBodyOutputCeilingCappedAtHalfWindow`：① 事故形状（200k 窗 ×
  max_tokens=128000）预算必须 = 100000；② ~77k 估计的事务形状 body 必须**放行**；
  ③ 小于半窗 ask 与无 ask 同形（半窗，与旧版一致）；④ 桶侧配置 128k 窗 ×
  100k 预留 × 1k margin 仍 = 27000（配置语义不变）。
- 同步三处钉旧语义的断言（均注明改判理由）：
  - `TestCheckFinalBodyAndOutputReserve`：1192→4096（body ask 无法把输入压到
    半窗以下——这正是本修要消除的行为）；
  - `TestThinkingIsIncludedInOutputLimit`：2192→4096 + 「带/不带 thinking 同形」
    断言（取 max+封顶的结构保证不重复扣除）；
  - `request_test.go` 耗尽检测：单靠 body 预留不再可能耗尽窗口（封顶即保证半窗），
    改由**桶侧配置**预留触发（120k 配置 + 10k margin），检测门保留。
- **变异验证**：临时禁用封顶 ⇒ 事故门红（还原出生产数字 72000）+ thinking 门红
  （2192）⇒ 恢复后全绿。

## 5. 验证

- `go test ./internal/contextbudget/ ./domains/streaming/executors/
  ./domains/streaming/ ./domains/hooks/compression/ -count=1` 全绿
  （streaming 108.8s）；`go build ./...` rc=0；vet/gofmt 干净。

### 5.1 211 生产实测（部署 199c20a00，槽 a，build_seq 3002，公网版本指纹对拍 OK）

- 铸临时验证 key（DB 直插 + HMAC-SHA256(SECRET_KEY, raw) 现算，验后已
  revoked：api_keys id=240，owner=verify-script）。
- **形状 A（算术判别，4200B 文本 × max_tokens=199000）**：旧算术 limit=
  200000−199000=1000 ⇒ ~1.2k 估计必被本地 422；实测 **HTTP 200，模型正常
  流式返回** ⇒ 预算门放行 ✔。
- **形状 B（事故复现，290KB 文本 × max_tokens=128000 = ZCode 事故形状）**：
  旧算术 limit=72000（生产实证 actual=76792 limit=72000 被拒）；实测
  **HTTP 200，模型正常流式返回** ⇒ 预算门放行、上游也接受该请求（证实它
  从来就是合法的——上游裁决如期成为权威）✔。
- 对照：小请求（6 tokens）两种 stream 形态均 200；真实流量 3 分钟 199×200
  无回归。

## 6. 边界与登记（本轮明确没做）

- **Resolve 算术一行未动**：桶侧配置语义、未知窗口回落全局上限、8MB 字节闸、
  「绝不当无限」全部不变；本修只改 body 侧预留的并入方式。
- 估计器标定（chars/3.5、blob/5、nodeOverhead=16）不动：11-09 的 231746 估计
  （约 1.2MB 会话）在新预算 100k 下仍会被拒——那是真·超窗（真实输入 ~230k >
  200k），拒绝对，压缩链若受保护区超目标仍 422（诚实拒绝，比放行后上游截断
  可预期）。
- 不动 fit 字节目标（InputTokens×3）与 fit 迭代（0.75× 收缩）：与本事故无关。
- `session_bodies` / `session_turns` 存储流 2026-11-10 16:44 +08 后停写（另一条
  工作线的观察项，本轮只登记不修）。
- 42-48k 估计值被拒的历史样本与本事故同根（更大的 max_tokens ask ⇒ 更小 limit），
  本修一并覆盖；无独立处置。
