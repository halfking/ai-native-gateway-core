# D11 auto-model（Jev 实验 fallback 分类器面）子代理报告（窗口：468a1ce82..HEAD）

> R44 轮原文落盘（子代理只读报告，主代理已逐条亲读复核，处置见轮文档）。
> 审计对象：bcda0f26c 引入的 `autoroute/classifier_jev.go`（359 行）+ `classifier_jev_test.go`（274 行）+ `cmd/gateway/main.go` / `main_types.go` 接线。横切 D04（超时/熔断/并发）与 D14（凭据/资源）。子代理自测：`go build ./autoroute/ ./cmd/gateway/`、`go vet ./autoroute/`、`go test ./autoroute/ -run Jev -race -count=1` 全绿。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P2** | **Jev 低置信结果无条件覆盖 heuristic winner，无置信度下限门**。触发路径：请求走 auto → heuristic 置信 < 0.7 → `d.fallback.Classify` 返回 Jev 结果，`decision.go:782` **不做任何置信度检查直接 `return llmCls`**；`classifier_jev.go:292-299` 只把越界值钳回 (0,1]（0.01 的 confidence 也被接受），从不与阈值比较。而 LLM 槽此前恒返硬编码 0.85（classifier_llm.go:104），该缺口在 Jev 之前不可达——Jev 是第一个能返回任意低置信的占位者。放大面：低置信 Jev 答案还会经 `d.intentCache.Put` 粘住整个会话 10 分钟（decision.go:584-585）。与派发契约"置信度低于阈值时无条件回退既有 winner"直接相悖。**契约权威性存在两种解读**（派发词的"低于阈值回退" vs commit message 的"全继承 LLM 契约"——后者字面上不含置信度门） | autoroute/classifier_jev.go:292-309；autoroute/decision.go:776-782；autoroute/decision.go:584-585 | 若按派发契约定案：在 decider 层对 fallback 结果加阈值门；若按"继承契约"豁免，需在 classifier_jev.go 头注释钉死该裁决防止下轮重查 |
| 2 | P3 | **breaker_open 拒绝路径的 gauge 上报硬编码 5**。熔断开启后每个被拒请求 `RecordLLMCircuitBreakerState(jevBreakerFailures, true)`（恒传常量 5）；重开周期后 `consecutive` 可达 6、7…，gauge 在拒绝时从真值回跳到 5（抖动/少报）。与 LLM 槽形态不同构：CircuitBreakerCaller 拒绝路径 early-return 不动 gauge（llm_caller.go:121-124） | autoroute/classifier_jev.go:208 | 改为在 mu 内读取当前 `c.consecutive` 后上报 |
| 3 | P3 | **TYPESAFE_BASE_URL 允许 `http://` 明文 scheme**。误配 `http://...` → Bearer key 明文出网（classifier_jev.go:265）；该行为被测试钉成契约（classifier_jev_test.go:250）。默认 https，属误配门控型缝隙 | autoroute/classifier_jev.go:121-125 | 非 https 时升 Warn |
| 4 | P3 | **Jev 胜出时 LLMGatewayAutoLLMEndpoint 配置被静默忽略**，双配置并存只有一条 Jev 启用 Info，无 Warn 点名 | cmd/gateway/main_types.go:260-266 | Jev 命中且 LLM endpoint 非空时补 Warn |
| 5 | P3 | **classifier_jev_test.go 头注释对 fail-open 钉桩位置的声明不实 + 该分支无任何测试**：decision_test.go 无任何构造带非 nil failing fallback 的 Decider（grep `fallback:` 零命中）；decision.go:777-781 零覆盖 | autoroute/classifier_jev_test.go:4-7；autoroute/decision.go:777-781 | 补 `TestDecide_FallbackError_KeepsHeuristic` 钉桩并纠偏头注释 |

## 二、核实为健康的面

- **门控语义零开销**：双 env 关闭只做两次 Getenv 即返回（:106-110）；单开一个 env 按完全禁用处理，**无半启用态**。
- **fail-open（除 #1 置信度面）健康**：全部失败形态（transport/非200/坏JSON/缺answer/未知任务/熔断）返回 error → decision.go:777-781 保留 heuristic；最坏耗时被 timeout 钳位（≤10s）；classify 总失败还有 default-chat 兜底（decision.go:456-462）。
- **熔断与 classifier_llm.go 同构、无数据竞争**：5 连续失败/30s 冷却，状态全在 mu 内；`-race` 全绿。`context.Canceled` 计入 breaker failure 与 InstrumentedCaller 映射同构，系继承非分歧。
- **资源健康**：http.Client 复用默认 Transport；每请求挂 callCtx 超时；body 全分支 defer drain+Close；1MB LimitReader；无 goroutine 产生。
- **指标基数健康**：label 均固定词表，model 名不进 label。
- **接线正确**：buildAutoFallbackClassifier 精确替换 NewDecider 第二参；默认关回落既有 LLM 槽实例非空壳；guard 测试锚点未断。
- **命名卫生**：无重复声明/无 init 注册；build+vet 编译级实证。
- **凭据卫生（D14）**：apiKey 仅进 Authorization 头；deploy/文档 grep 零命中，无落盘回显面。

## 三、未覆盖项与原因

- 发现 #1 的契约权威性裁决需主代理定案（子代理已给双方证据）。
- 真 TypeSafe /v1/systemone 端点实际协议行为需真实凭据，未联测。
- decision_test.go 未全量逐行；deploy-252 面新增 Jev 开关注入通道超出窗口改动面。
- 长时间高并发下 breaker gauge 行为未压测（单测 -race + 亲读代替）。
