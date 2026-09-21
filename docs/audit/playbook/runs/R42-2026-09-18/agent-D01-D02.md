# D01(IR 生命周期)+ D02(协议适配)联合子代理报告(窗口:2026-09-17 05:00 → 2026-09-18 05:02)

> 边界说明:ff9e6f27d(R37 typed-nil 修复,09-17 11:11)在墙钟窗口内但 git 区间外,已按协调者指名一并复核。窗口内无 internal/ir、internal/irconv、domains/streaming 生产代码改动,D01/D02 触碰面集中在 errorsx/classify.go、errorsx/failover_policy.go、autoroute/*、bg/auto_route_settle_worker.go。

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **anthropic_bridge 流内 SSE 错误事件路径对 MiniMax bad_request_error 无分支,status=0 使全部状态门分类器失效**。若 thinking.type 400 以 SSE error event 在流内送达:errType switch 无该分支 → ClassifyErrorWithBody(0, payload);status=0 使 invalidRequestFormatRe(新修复)、contentFilter、contextLength、modelNotFound、modelDeprecated 的状态门全部跳过 → KindTransient → bridge 侧升格 KindUpstreamDown(可重试,且在 credstate probeImmediately 集合)→ 流内错误事件形态下同类"冷却↔探测"风险未修复。修复实际只覆盖准入 400(首帧前)路径 | domains/streaming/anthropic_bridge.go:578-608(switch 缺 bad_request_error;604 传 status=0;605-607 transient→UpstreamDown)、errorsx/classify.go:996-1033(各状态门)、:1078-1084(status=0 无 case → Transient)、domains/credentialstate/manager.go:369-374 | bad_request_error 加入 errType switch(与 invalid_request_error 同映射 KindClientBug),或 ClassifyErrorWithBody 对 status==0 放行 4xx 族门;补流内 error event 分类钉桩 |
| 2 | P3 | **MiniMax 原生 base_resp 信封的 HTTP 400 会先被 toolCallIdMismatchRe 吸走**:pattern 匹配 "status_code":2013 且 toolCallIdMismatch 检查先于 invalidRequestFormatRe → 分类为 tool_call_id_mismatch。行为完全等价(两 kind 均 IsClientBug/不冷却/不探测),仅 error_type 与 error_kind 词表漂移;新增钉桩测试只覆盖 Anthropic 兼容壳形态,原生信封 400 形态未钉 | errorsx/classify.go:676-681(2013 pattern)先于 :1030-1035、executor_chat.go:898(原始 body 分类入口)、classify_minimax_test.go:26(仅壳形态) | 补 base_resp 信封 400 的分类断言(接受现状),防重构翻转 |
| 3 | P3 | **failover_policy.go 的 KindClientBug 行零生产消费者**:DecideFailover/FailoverDecision/EnqueueProbe/ProbeFanout 全仓零引用,注释与现实不符;真实 flap 修复载体是 classify.go 正则 → IsClientBug → UpdateOnFailure skip 与 writer no-op(均已存在) | errorsx/failover_policy.go:117-128;grep 零命中;真实载体 manager.go:282、credential/writer.go:406-412 | 标注 test-only contract 或接线,防误认为承载生产行为 |
| 4 | P3 | 窗口内 e9d46b37e 与 6276a3ff9 为同一修复的重复提交(内容一致),归并后无代码影响 | 两 commit 的 auto_route.go diff 相同 | 轮文档记一笔 |

## 二、核实为健康的面

1. **MiniMax 2013 准入路径分类修复正确、误吸面窄**:两个新 pattern 都要求 invalid thinking.type 字面量,400/422 状态门在位;负例断言在位;zhipu 形态由既有 code:1214 覆盖;anthropic 原生 invalid_request_error 由 bridge errType switch 覆盖。发现 #1 是残余流内面,非本修复回归。
2. **KindClientBug 与既有 client bug 家族在全部消费点行为一致**:凭据状态 no-op、动作策略 terminal、attempt_outcome FailTerminal、stream_recovery 不进恢复、error_kind→client_cancel、非流式终态 kind 化信封返回、embeddings 侧同构、failover credential-healthy、nodehealth 不罚;MiniMax 原生 HTTP-200 包裹路径 vendorstrip 2013→KindClientBug 有钉桩。
3. **typed-nil 修复正确且三副本全归一化(R37 孪生排查闭环)**:reflect 归一化+三态钉桩;sticky.go dormant 副本已同步归一化;main 注入点分支门在位。
4. **settle worker synthetic-actor 过滤正确**:LEFT JOIN 后 Go 侧过滤,originActor NULL 走既有 abandon 地平线;abandon 盖 settled_at+NULL reward 使行脱离未结算索引;Scan 列序对齐;三条集成测试钉三面;SQL 不 BTRIM ↔ Go 侧 trim 配对不变量有 R39 注释且 sole writer trim 证实。与 intent cache/outcome 侧口径一致,合成轮五面封闭。
5. **supplier_errors→admin 呈现链完整**:persistSupplierError 无 kind 过滤,error_type 低基数词表,JSONB 走参数化;读侧按 error_type 通用折叠无白名单,client_bug 新词表行可直接呈现;双向 SanitizeErrorText 脱敏。
6. **窗口未触碰 D01 核心 IR 面**:无 internal/ir/irconv/paramreg 改动,清单 3/4/5 无窗口新增写入点可破例。

## 三、未覆盖项与原因

D01 清单 1/2/5/6/8(窗口零触碰,建议全量轮处理);D02 清单 3/4/6/7(窗口无相关改动面,R30 遗留 #7 状态未变);四协议空流门矩阵未跑流式桥用例;MiniMax base_resp 真机形态未实抓(需真实凭据)。
