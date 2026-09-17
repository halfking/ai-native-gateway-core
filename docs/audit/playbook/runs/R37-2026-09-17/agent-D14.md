# D14 安全场景横切 子代理报告（R35-R1 定向任务；窗口 876302d5e..67f78247c + 遗留核实）

## 〇、R35-R1 前置拓扑核实（任务核心交付）

### 结论先行

**auto-title / auto-summary 回环默认恒为 127.0.0.1:<自身监听端口> 直连本进程，但存在逃生门 env `LLM_GATEWAY_ENDPOINT` 可指向任意主机；goal 影子轮为纯进程内直调不经网络。** token 方案可安全实施，token 校验放在"剥离开关"上。

### 1. 三头生产者/消费者全图

头名常量：auto_route.go:62/:69/:76；admin/auto_title_generator.go:37-38 平行定义。

生产者：
- auto-title 回环（HTTP 自调）：admin/auto_title_generator.go:1051/:1054/:1057（三头全写）
- auto-summary 回环：admin/auto_summary_generator.go:705/:707/:708（三头全写）
- goal/audit 影子轮（进程内直调）：response_interceptor_helpers.go:240/:243（两头，刻意不写 Is-Auto，:184-190 注释钉死）

消费者：
- handler 入口（唯一 r.Header 读点）：handler.go:1718/:1726-1728/:1729-1731 → logCtx
- executor 续写检测豁免：executors/executor.go:2115-2116（第二直读点）
- 落库管道：request_log_pipeline.go:481-501 → handler.go:7026/:5648/:1033
- session_turns 镜像排除门：sessionv2mirror/hook.go:92 → internal_loopback.go:23-40
- auto-title/summary 链式抑制：handler.go:6297/:6347（触发 :6217/:6241）
- admin 运维面回退读：admin/handler.go:1147、session_turns_tree.go:347

另有 handler.go:2816 / responses.go:324 在 model="auto" 且 decider 无 rewrite 时置 IsAutoRequest（非 header 来源，剥头不影响）。

### 2. 回环 URL 构造

- 发起：AutoTitleGenerator.callAutoTitleLLM（admin/auto_title_generator.go:908/:921/:1021/:1064-1065）；summary doCallAutoSummaryOnce（:685/:713-714）。
- URL 解析：`LLM_GATEWAY_ENDPOINT` env 优先（:1157-1162/:899-904，无 scheme/host 校验）；否则 loopback.GatewayBase()（internal/loopback/loopback.go:35-45，恒 127.0.0.1:LLM_GATEWAY_LISTEN 端口，缺省 8781）。
- 跨实例可能：条件性——当前仓内所有部署面未设 LLM_GATEWAY_ENDPOINT（grep 仅 3 个 Go 读取点），默认拓扑恒 127.0.0.1 直连。
- 鉴权面：回环用租户真实 API key（pickFirstAvailableAPIKeyForAuto :1165-1190），攻击者需持有效 key。

相邻自调链（不携带三头）：admin postAdminLLMChat（admin_llm_task.go:222）用 gatewayEndpointFromRequest（logs_summary.go:381-394）**基于 r.Host 构造**——admin 请求经 LB 进来时自调打公网域名（值得单独登记）；self-check workers 默认 loopback.GatewayBase()，可被 LLM_GATEWAY_SELF_CHECK_BASE_URL 覆盖（bg/self_check_worker.go:87、credential_selfcheck.go:122）。死代码备案：admin/session_summary_v2.go:488 硬编码 localhost:8080 带 TODO。

### 3. goal 影子轮进程内直调

defaultDispatchFollowUp（response_interceptor_helpers.go:194-223）：buildFollowUpRequest → httptest.NewRecorder → 直接 h.ServeHTTP（:219）。无 socket。token 化不受影响；若未来走真实 HTTP 需带 token（goalrun_action_scheduler 演进方向）。

### 4. 剥离点与影响面

- 推荐剥离点：RequestIDMiddleware.Wrap（middleware/requestid_mw.go:42-63）——链上最外层段（main.go:6825-6838：Recovery→**RequestID**→Locale→CORS→Prometheus→Tracing→Auth→Origin→Logging→SecurityHeaders→mux）；已有覆写受信关联头先例（:57 覆写 X-Request-Id）；剥离时三头尚未被任何读者触碰。
- 进程内直调不经中间件——剥头零影响；auto-title/summary 走真实 HTTP 会经过——必须 token 方案。
- executor.go:2115 读剥后 header，自动继承正确语义，无需改动。

### 5. 结论：token 方案可实施，附 3 个实施约束

1. 默认拓扑下安全（token 只在本进程内存）。
2. LLM_GATEWAY_ENDPOINT 分支：指向他实例则 token 不匹配被剥 → 只有可观测回归（parent/origin 落 NULL、title 链式抑制失效），无安全回归。处置：校验失败记 Warn；部署文档钉死"LLM_GATEWAY_ENDPOINT 只允许指向自身"；未来跨实例需部署面共享 token。
3. token 校验失败剥头即覆盖 executor 第二读点语义，无需改 executor。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1 | deploy-252-gateway.sh `set +e`（L42）后从未恢复（L44 只补了 `set +u`）——L44 起全脚本裸奔，GEN_KEY 失败/scp 失败/远端安装失败/healthz 12 次全败任一发生都继续跑并打 VERIFY_RESULT=pass | scripts/deploy-252-gateway.sh:42-44, 88, 112-121, 124-154, 158-164, 185 | L44 后补 set -euo pipefail |
| 2 | P2 | LLM_GATEWAY_TEST_KEY 以 -H 内插进 ssh 远端命令行（/proc 与本地 ssh argv 可见窗口）；同密钥亦为长期 SSOT 复用 | scripts/deploy-252-gateway.sh:178 | 远端经 stdin 传 key 或一次性探针 key |
| 3 | P2 | 密钥文件宽权限窗口：本地 heredoc 默认 644 后 chmod 600；远端 scp 不带 -p 落地 644 到 chmod 600 有间隔 | scripts/deploy-252-gateway.sh:90-103, 121, 131-132 | 写前 umask 077；scp -p |
| 4 | P3 | SECRET_KEY 与 CREDENTIAL_ENCRYPTION_KEY 复用同一随机值（单钥跨密码学域） | scripts/deploy-252-gateway.sh:88, 96-97 | 两把独立钥 |
| 5 | P3 | CORS_ORIGINS=* 挂公网 nginx 暴露的网关 | scripts/deploy-252-gateway.sh:93 | 收敛或注释钉死 dev-only 风险接受 |
| 6 | P3 | summary 回环超时取常量 vs title 硬编码 30s；gatewayEndpointFromRequest 从 r.Host 构造自调 URL（经 LB 绕公网）——非 127.0.0.1 第二条自调路径 | admin/auto_summary_generator.go:713 vs auto_title_generator.go:1064；admin/logs_summary.go:381-394 | 备案至 D14 历史回归点；统一走 loopback.GatewayBase() |
| 7 | P2（供 R35-R1 修复登记引用） | 三头注入面完整确认：入口读点上方无任何剥头/校验中间件；伪造 Is-Auto → mirror 剔除；伪造 Source-Actor:goal-% 污染对账。前置拓扑核实通过，token 方案可动工 | handler.go:1718-1731；response_interceptor_helpers.go:184-190；sessionv2mirror/hook.go:92 | 按 §〇.5 三约束实施 X-Gw-Loopback-Token |

## 二、核实为健康的面

- goal 影子轮纯进程内直调，剥头中间件天然无感；"刻意不写 Is-Auto"契约有注释与测试锚。
- loopback.GatewayBase 默认解析正确（端口取自身 LLM_GATEWAY_LISTEN；blue-green 8781/8782 均直连本进程，无打到对端的默认路径）。
- 回环自带重试与结构化失败日志，endpoint 落日志不含密钥。
- session_turns 排除门双门一致（IsInternalAutoEntry 统一实现）。
- deploy 脚本本地密钥文件 EXIT trap shred；远端 .env.dev chmod 600；远端安装段自带 set -euo pipefail。
- admin postAdminLLMChat 不携带三头，不构成注入通道。

## 三、未覆盖项与原因

- bg/goalrun_action_scheduler.go 实际发送路径（进程内还是 HTTP）未展开——死脚手架（R35-R5），若走 HTTP 需并入 token 影响面。
- v2DispatchHandler Pipeline 包装层内部独立 header 读点未逐插件排查（v2 pipeline 默认关闭）。
- 252 真机上 LLM_GATEWAY_ENDPOINT 是否被宿主机遗留 unit 设置需真机核实。
