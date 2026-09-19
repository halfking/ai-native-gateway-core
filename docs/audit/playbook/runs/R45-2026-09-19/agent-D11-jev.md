# D11 auto-model/autoroute/Jev 子代理报告（窗口：51d6147c7..de7efd453）

审计范围核对：窗口内本域相关文件仅被 commit `95a872ff5` 触碰（`git log 51d6147c7..de7efd453 -- autoroute/` 单条）；该 commit 实际改动 5 个文件，本域子集 4 个 + `cmd/gateway/main_types.go`（Warn③④落点）。HEAD == `de7efd453`，工作树对上述文件干净，亲读即终态。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | classify() 头部 Errors 文档块与实际行为相反："Heuristic error → escalate to LLM" 实为直接 `return nil, err`（fallback 根本不会被尝试）；"LLM error → return error" 实为保留 heuristic。R44 在函数体内插入新门但未纠正上方陈旧注释，误导后续维护者对 fail-open 语义的理解 | `autoroute/decision.go:742-745` vs 实际 `decision.go:758-761`（heuristic error 提前返回）与 `decision.go:777-781`（LLM error 保 heuristic） | 改两行注释，与代码对齐 |
| 2 | P3 | 门值与升级阈值共用 `effectiveLLMThreshold()` 且 tuning 通道无任何 clamp：`thresholds.llm_confidence` 经 `applyTuningParam` 原样写入 float（无范围校验，全仓无第二处校验点）。当 tuning 把阈值调到 >0.85 时，升级条件（`decision.go:762` 判 false）仍每次触发 fallback 调用（`decision.go:776`），但 LLM 槽恒返 0.85（`classifier_llm.go:104`）必被 `decision.go:785` 的门拒绝——白付每次 3s 上限的外部调用（延迟+成本）且结果永不生效。方向 fail-safe（保留 heuristic），非正确性破坏 | `autoroute/tuning_store.go:228-233`（无 clamp）＋ `decision.go:762/785` ＋ `classifier_llm.go:104` | tuning setter clamp 到 (0,1]；或对"升级发生但门拒绝"计数沉淀观测 |
| 3 | P3 | 明文 Warn/Info 原样输出 base_url/endpoint：若运维把凭据写进 `TYPESAFE_BASE_URL` 的 userinfo（`http://user:pass@host`），`url.Parse` 不拒绝（scheme 合法即过 `classifier_jev.go:122`），该字符串进 Warn 与后续 Info 日志。注：Go http client 不会用 URL userinfo 做鉴权，故凭据既无效又被落盘。`TYPESAFE_API_KEY` 本身经核实不进任何日志 | `autoroute/classifier_jev.go:126-130`（Warn 带 base_url）、`classifier_jev.go:147-150`（Info 带 endpoint） | 可选：日志前剥离 `u.User`；或维持现状（属自伤式误配） |
| 4 | P3 | R44 钉桩缺口：两个新测试均以 `sessionID=""` 运行，intentCache 粘滞面（门拒绝后缓存应存 heuristic 而非低置信 Jev）无端到端断言；breaker gauge 真实连击（`classifier_jev.go:215`）无测试钉桩——`TestJevClassifier_BreakerOpens` 不检查 gauge 钩子。这两处回归目前只靠 code review 兜底 | `autoroute/decision_test.go:130,154`（sessionID 空）；`classifier_jev_test.go:180-203`（无 gauge 断言） | 补一条带 sessionID 的 Decide 测试断言 `intentCache.Get` 的 TaskType/Classifier；测试内替换 `RecordLLMCircuitBreakerState` 包级钩子断言连击 ≥6 时拒绝路径上报 6 |

## 二、核实为健康的面

- **门的位置（修复①核心）**：`d.fallback.Classify` 全仓唯一调用点在 decider 层 `decision.go:776`，`Decide`（`decision.go:465`）与 `DecideV2`（`decision_v2.go:160`）双路径都汇入同一 `classify()`，门对 V1/V2 全覆盖。Jev 低置信响应在 classifier 层仍按 success 处理（`classifier_jev.go:231-232`：`breakerSuccess()` 清零连击 + metric "success"），不会被计入熔断失败——若按错误做法把门放 classifier 层返 error，`classifier_jev.go:228` 的 `breakerFailure()` 会累计 5 次后误开熔断，现设计无此缝。
- **fail-open 分支无缝**：fallback error → `decision.go:777-781` Warn 后 `return cls`；heuristic 自身 error → `decision.go:758-761` 提前返回，由 `decision.go:466-474` / `decision_v2.go:161-172` 兜 default chat 0.3。不存在"fallback 失败阻断路由"或"error 传播到调用方"的路径。理论缝（Classifier 返 `(nil,nil)` → `decision.go:785` nil deref）在两棵实现树中均不可能出现。
- **LLM 槽 0.85 恒过门（默认态）**：`classifier_llm.go:104` 常量 0.85，`effectiveLLMThreshold` 默认 0.7（`decision.go:222`、`tuning_store.go:99`），0.85 ≥ 0.7 恒过——LLM 槽旧行为零变化（唯 tuning >0.85 时语义变化，见发现#2）。
- **breaker 连击 -race 面（修复②）**：`consecutive` 的全部读写都在 `c.mu` 内——`classifier_jev.go:348-352`（breakerOpenConsecutive 读）、`354-367`（breakerFailure 写）、`369-374`（breakerSuccess 写）；`breakerAllow`（340-344）同样持锁读 `openUntil`。无锁外访问。
- **回跳消除**：`openUntil` 只在 `consecutive >= 5` 时置位（`classifier_jev.go:358-359`），拒绝路径连击必 ≥5；冷却到期后再失败连击 6/7… 单调递增至 success 清零，拒绝路径上报真实值（`:215`）——旧版恒报常量 5（`jevBreakerFailures`）造成的 6→5 回跳不再存在。已知良性交错：拒绝路径上 `breakerAllow` 与 `breakerOpenConsecutive` 两次持锁之间若冷却恰好到期且并发调用成功清零，会采到一次 `(0, open)` 不一致样本——需调度停摆级延迟才触发，仅影响单次 gauge 观测，非 race。
- **Prometheus 全固定词表**：`llm_gateway_llm_classifier_total` 唯一 label `outcome`，取值全部字面量（`classifier_jev.go:212` "breaker_open"、`224` "timeout"、`226` "failure"、`232` "success"；`classifier_llm.go:96` "disabled"；`llm_caller.go:237-246` InstrumentedCaller 同词表映射）；breaker 两个 gauge（`telemetry/tuning_metrics.go:107-116`）**无 label**，consecutive 是 gauge 数值（`:183-195`）非 label——无基数风险。
- **Warn 不泄 key 材料（修复③）**：两处 Warn 只携带 base_url 与固定文案（`classifier_jev.go:128-129`、`main_types.go:268-269`）；`apiKey` 字段仅进 Authorization 头（`classifier_jev.go:272`），全文件无第二个日志消费点。触发条件也正确：明文 Warn 仅在 gate 开 + key 在 + scheme==http 显式配置时出现（默认 https 不可达）；gate 关时不构建、不告警。
- **双配置 Warn 条件（修复④）**：`main_types.go:267` 只查 `LLMGatewayAutoLLMEndpoint` 是对的——Endpoint 是 LLM 槽唯一必需变量（`main_types.go:236-239`：无 Endpoint 即 DisabledCaller），ApiKey/Model 无 Endpoint 本就从不生效，不漏报真实配置冲突。且 Jev 胜出分支里 `RecordLLMMetricCall/RecordLLMCircuitBreakerState` 钩子已镜像接线（`main_types.go:262-263`），`buildAutoLLMCaller` 被跳过也不会断指标。
- **intentCache 粘滞语义（任务项4）**：门拒绝后 `classify` 返回的是 heuristic `cls`，`Decision.TaskType/Classifier` 由它派生（`decision.go:565-577`），缓存 Put（`decision.go:584-594`、V2 同型 `decision_v2.go:345`）存的是 **heuristic 结果**（Classifier="heuristic"、原低置信度），不是低置信 Jev 结论。会话粘住 heuristic winner 即修复意图；且有 drift 兜底（`session_intent_cache.go:17` hitCount≥50 强制 reclassify，`:326-328`）不会永久锁死。同型先例：fallback error 的 fail-open 一直就是这个缓存语义，无新分叉。
- **新测试真实钉住（任务项5）**：`TestDecide_FallbackBelowThreshold_KeepsHeuristic`（`decision_test.go:147-164`）用 jev stub 返回 `(out{TaskCode,0.4}, nil)` 走的是**门分支**而非 error 分支，断言 Classifier+TaskType 双字段——若删掉 `decision.go:785` 的门，得到 "jev"/TaskCode 必挂；`TestDecide_FallbackError_KeepsHeuristic`（`:123-140`）用 err 走 fail-open 分支，二者互斥覆盖两条"保留 heuristic"路径，且与既有 `TestDecide_LLMFallback_TriggersOnLowConfidence`（0.85 过门）三向钉死门的行为边界。`classifier_jev_test.go:6-8` 头注纠偏属实（fail-open 钉桩此前确实不存在）。

## 三、未覆盖项与原因

- 未运行 `go build / go vet / go test -race`——本代理受只读纪律约束（Bash 仅限只读命令）；建议主代理对 `autoroute` 包补跑三件套（涉并发包按规加 `-race`）。
- 真 TypeSafe endpoint 的行为（70–500ms 延迟、真实 confidence 分布、低置信出现频率）无法离线验证——需带 `TYPESAFE_API_KEY` 的真机环境。
- `effectiveLLMThreshold()` 在 `decision.go:762/785/789` 三次读数间与 tuning 热更的并发交错仅做静态推理：比较分支单次读数自洽，最多出现日志 threshold 字段与判定值瞬时不一致（纯文案面），未做动态验证。
- `llm_gateway_llm_circuit_breaker_*` 两个无 label gauge 由 Jev 与旧 LLM 槽共用（谁占槽只能靠启动日志区分）——可读性问题留给运维面确认，非本窗口代码缺陷。
