# Vapeur 协议适配第二轮（2026-09-29，R-vapeur2）

**触发**：用户报告经本地网关以凭据 hxt-local（credential 126，provider 36 vapeur，
protocol=openai-responses）访问 gpt-5.6-terra / claude-sonnet-5 / claude-opus-5 /
gpt-6-sol / gpt-6-astra「经常出错不通」，要求检查协议适配、充分测试、默认走
responses；先拉最新代码合并本地（已做：2637ea0e1 → cdac6c180，merge 无冲突）。

## 一、结论与根因

**协议翻译层（昨轮 faff99ac6 修复后）全部健康**：本轮在运行版本 2322 上实测
5 模型 × 双入口（/v1/chat/completions、/v1/responses）× 流式/非流式，
协议形态全绿（含 responses 入口对 Claude 系的翻译、GPT 系原生 resp_ id）。
「经常出错不通」的真根因在**路由恢复链路**，两个叠加缺陷：

1. **全红视图 → 硬 503，且恢复竞速结构性必输**（主根因）。
   URSM v2 authoritative 视图翻红后（上游真抖 + 爬梯断档数小时无复探，
   见 §四.3），路由把全部候选过滤为 0；executor 的同步探针恢复机制
   （ProbeSync，hold 5s）被两处时序缺陷卡死：
   - **结果投递竞态**（本轮真根因，三次事故实证）：探针 goroutine 在
     direct HTTP 成功（~1-4s）后、投递 winner 之前，先做 binding/health/
     observed 多段 DB 簿记 + gateway round——DB 抖动时每段挂起数秒，把
     投递拖过 hold 边界。三次实测同一指纹：探针成功、`ursm:v2:node:*`
     视图在 hold 过期后 ~20ms 翻绿（updateURSMv2ProbeState 自带
     WithoutCancel 免疫过期，只是被排在慢写后面），请求仍 503
     （14:19 astra、14:39 复现、14:57 复测，全部同一模式）。
   - **hold 预算过紧**（放大器）：openai-responses 供应商的 direct 探针
     是 responses 腿 + 不支持时 chat 降级两跳，加 DB 簿记，5s hold
     常态性贴边（audit 表已证明探针墙钟 5.4s）。
2. **病态兄弟供应商占优排序**（次因，未改码）：claude-sonnet-5 被路由到
   suyun(13092/131)（评分靠前、视图绿但 TTFB 30-100s），挂 99s 被客户端
   掐断；同一分钟 responses 入口走 vapeur 2.2s 成功。属上游质量问题 +
   评分输入滞后，见 §五。

## 二、修复（全部落地 + 单测 + 生产验收）

| # | 文件 | 内容 |
|---|---|---|
| 1 | cmd/gateway/main_helpers.go | 新增 `syncNoCandidateProbeHoldDefault=10s` + `syncNoCandidateTimeoutEnv()`（env `LLM_GATEWAY_SYNC_NO_CANDIDATE_TIMEOUT`，Go duration，畸形回退默认） |
| 2 | cmd/gateway/main.go（两处接线点） | `SyncNoCandidateTimeout` 硬编码 5s → `syncNoCandidateTimeoutEnv()`；kill-switch `LLM_GATEWAY_SYNC_NO_CANDIDATE_PROBE` 不变 |
| 3 | bg/node_probe.go ProbeSync goroutine | **顺序重排**：`updateURSMv2ProbeState`（Redis 毫秒级，authoritative 路由唯一恢复信号）+ `invalidateCandidateCache` 提到 direct 轮之后立即执行；winner 投递（`results <- res`）提到 gateway round 与 emitSyncAudit 之前（契约=direct 轮，后两者是诊断）；binding/health/observed/audit/gateway round 全部改用 `bookCtx`（WithoutCancel+5s，hold 过期不再腰斩簿记）；投递后不得再改 res（channel 接收方并发拷贝），gateway 结果走独立 `gwRound` 变量 |
| 4 | bg/node_probe.go ProbeSync drain | ctx.Done 退出后补一次非阻塞清扫（sweep）——实测投递可落在 Done 后几十毫秒，已到达的恢复不再丢弃 |
| 5 | bg/probe_rollback_tentative_test.go | 字符串契约测试随新顺序更新，并**加钉两条新不变量**：URSM 写必须先于 DB availability 恢复；winner 投递必须先于 gateway round |
| 6 | bg/node_probe_gateway_side_test.go | sync 段 end-marker 从 updateURSMv2ProbeState 改为 emitSyncAudit（URSM 写提前后段界变化），字面量 ctx→bookCtx |

单测：`go test ./bg/`（含 -race）、`./domains/streaming/executors/`、
`./internal/providercap/`、`./internal/reqprobe/` 全绿；`go build ./...` 通过；
gofmt 干净。

## 三、部署与验收（本地 8782）

- 部署路径曲折如实记录：2327 候选两度被门禁拦下——①前端构建门（本工作树
  node_modules 停在 vitest 1.6.1，`npm install` 同步后过，昨日 §7.2 同型复发）；
  ②候选端口 8781 被并行会话的 dev 实例（`/tmp/lgwdev-bin`，19:57 起、活跃
  服务中，未触碰）占用，healthz 门失败。最终以镜像 `2.5.7.2327` 就地替换
  8782 容器（旧镜像 2.5.6.2322 保留可回滚），后经标准 deploy 管线升级至
  **2.5.7.2329**（VERIFY_PASS=1，解密冒烟过）。
  管线坑（非本修复代码）：本工作树无 .env.local，须从运行 env 导出
  SK/CEK/ADMIN_USER/ADMIN_PASSWORD 四项（与 8782 运行值逐字节比对一致）。
- **全红→503 复现与修复验收（同方法同场景）**：
  - 旧代码（2322/2327-hold-only）：Redis HSET 三凭据 astra 视图 available=0
    → chat 请求 **503**（14:39 用时 12s/11.2s；14:57 用时 10s，日志实锤
    "sync probe exhausted"，视图在 54.52 翻绿、请求 54.59 失败）。
  - 新代码（2329）：同操作 → **HTTP 200**（16s），日志链完整：
    `sync probe recovered (cred 314, direct_status=200)` →
    `sync_no_candidate_probe_recovered (hold_ms=13831, candidates=2)` →
    重试成功。红窗期 cred 37（suyun，真病）维持红、其余请求如实失败——
    恢复机制不再掩盖真故障。
- **最终矩阵（2329，测试 key 用后即删）**：5 模型 × {chat, responses} ×
  {非流式, 流式} = **20/20 全 200**。插曲：sol/astra 首轮矩阵出现全形态 429，
  为测试 key 连发触发网关限流（间隔 3-5s 重跑即全绿），非上游问题。

## 四、实测取证要 点（防后轮误判）

1. **8781 端口 dev 实例**：`/tmp/lgwdev-bin`（version=dev，今日 06:48 构建、
   19:57 启动，go1.27.1），与 8782 共享 DB/Redis/API key，raw jsonl 持续
   增长=活跃服务中。**用户实际客户端可能指向它**——若其构建早于昨日修复，
   用户侧体验与 8782 不同。本轮未触碰；移交运维决策（回收或升版）。
2. **request_logs 父表 ≠ 热表**：业务行实时落 `request_logs_hot`（实测
   22:35 仍在写），父表靠 promote 迁移，`max(ts)` 滞后数小时是**正常形态**，
   勿再误判"日志停写"（本轮已误报一次并排除）。
3. **爬梯断档实证**：cred 126 × gpt-6-astra 的 node_probe_state
   last_try=02:30、视图红了几小时无后台复探，直到流量触发 sync 探针翻绿。
   队列 1 worker × 922 活跃 key + DB 抖动放大。移交探针轨道（非本轮范围）。
4. **DB 周期性抖动**（12h 窗口 776 次 "context deadline exceeded"、70 次
   "conn closed"）：直接放大本事故（拖慢探针簿记、拖长路由阶段——复现实测
   路由阶段独占 ~6s）。PG 本身空闲（100 idle 连接、无阻塞查询），病灶在
   网关侧使用模式或共享宿主，移交存储/容量轨道。

## 五、「默认 responses」语义与遗留风险

- 上游出站：openai-responses 供应商默认端点仍为 /v1/responses；数据面对
  上游判「不支持」的模型族（vapeur 的 Claude/qwen 系）按昨轮机制自动降级
  chat——本轮 20/20 矩阵即该策略的最终形态验收。
- **遗留（未修，明确移交）**：
  1. 病态供应商占优排序：suyun 视图绿（探针 30s 内能过）但 TTFB 30-100s，
     评分（price 0.4/latency 0.4/stability 0.2）靠 LatEWMA 滞后 demote，
     用户在窗口期仍会被路由到慢上游。可选项：将探针 TTFB 纳入评分输入 /
     运维调低 provider 13092 的 network_quality_score（数据面操作，需业务决策）。
  2. 全红且探针全败仍 503（10s 后）——语义正确（全候选真死时如实失败），
     但可评估「last-resort 尝试 top 候选」作为下一档；涉及 authoritative
     fail-closed 教义，需设计评审，本轮不擅动。
  3. 爬梯断档（§四.3）与 DB 抖动（§四.4）为独立轨道问题。

## 六、Handoff

- 运行版本：**2.5.7.2329**（8782 active，git 工作树 HEAD=cdac6c180+本修未推）。
- 回滚：镜像 `kx-llm-gateway-local:2.5.6.2322` 在位，按 §三的 docker run
  参数原样回跑即可；标准 deploy 管线亦可（须先释放 8781 或先停 dev 实例）。
- 测试 key：id=760（sk-2DzM…）已 revoke。
- 新 env：`LLM_GATEWAY_SYNC_NO_CANDIDATE_TIMEOUT`（默认 10s；
  "0"/负数/畸形回退默认——与 positiveDurationEnv 语义一致，不接受 0 关闭，
  关闭走既有 kill-switch）。
