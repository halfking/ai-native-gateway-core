# 2026-09-17 凭据"直连正常但网关判不可用"事件分析与自检/凭据状态管理全面复查

**现象（用户报告）**：apigpt / apiclaude（apicluade）/ hzx-2 / minimax-prod-v2 直连上游全部正常，
但 llmgo.kxpms.cn（245 预发）状态页长时间显示不可用；手动将 hzx-2 置为可用、探测成功后，
很快又进入"冷却中"。

**结论先行**：不是单一 bug，是三层独立缺陷叠加。用户假设"自动探测用了特色模型之外的
模型、找到了错误的模型"——方向正确但机制更具体：探测对**中转商不提供的模型**（含结构性
无法用 chat 探测的图像/realtime/audio 模型）反复发起注定 404 的探测，并把 404 当作与
503/超时同权的"健康信号"，每 5 分钟刷新一次绑定不可用；叠加今晨一次共库错 key 解密风暴
留下的中毒状态在根因修复后 5 小时不自愈。

---

## 1. 证据链（245 / 共享 252 PG，2026-09-17）

### 1.1 解密风暴（05:00–09:08）

`node_probe_runs` 中 24h 内大量 `endpoint_build` 失败，详情全部是：

```
build endpoint failed: decrypt: cannot decrypt: unknown format
```

覆盖 cred 2/25/27/31/35/37/38/39/49/50/58…（全凭据面），时间窗 05:00–09:08。
这是 R37 已立案的"共库错 key 污染"同源问题（20fb4c7a4 修复：网关侧错误拒绝写共享
可用性面 + 实例级解密熔断）。245 canary 于 09:15–09:30 部署了含守卫的 20fb4c7a
（seq 2122），此后 endpoint_build 归零。

**残留问题 A**：守卫阻止了新写入，但风暴期间已写入的存量状态不自愈——
`node_probe_state` 的 endpoint_build 梯子行（cf=7，next_retry 被推到 2–6 小时后）
和被 `probe_endpoint_build` 毒化的绑定，在修复部署后仍让 hzx-2 / minimax-prod-v2
的 UI 状态拖到 ~14:20 手动干预才恢复（≈5 小时）。

### 1.2 404 永动循环（长期故障主体）

apigpt "gpt key"（cred 2）`node_probe_state`：14 个模型 cf=7 / last_direct_ok=f /
err=http_404：`gpt-image-1/1.5/2`、`gpt-4o-realtime-preview`、`gpt-4o-audio-preview`、
`gpt-5.2/5.2-pro/5.2-chat-latest`、`gpt-5.3-codex(-spark)`、`gpt-5.4(-mini)`、`gpt-5.6(-luna)`；
同凭据 `gpt-5.5/gpt-5.6-sol/terra/gpt-6-astra` 等持续成功。130dao-cache / nocahce-1x
类似：`claude-3-7-sonnet/claude-haiku-3-5/claude-sonnet-4-5` http_503 或 timeout cf=7。

`node_probe_runs` 24h 触发源几乎全部是 **request_failure**（真实流量请求了中转商不提供
的模型→失败→阈值 2 触发探测→探测也 404→绑定判不可用 5 分钟→recovery 到期重探→再 404），
apigpt 单凭据单日烧掉数百次注定失败的探测。每轮失败都把绑定打回"不可用/冷却"，
这就是"手动恢复→探测成功→又在冷却"的直接来源。

**回答用户疑问**：
- 自检（selfcheck）本身按 `self_check_settings.model_source=top10`（按用量 top10）取模型，
  featured_model_ids 只是备选集，不是本次主凶；
- 但 `routing_policy.featured_models` 含 `gpt-image-2`、`gpt-5.6-luna` 等，featured 深度
  探测（Layer 4，每 30 分钟）会对**所有绑定了该模型的凭据**发起 chat ping——apigpt 绑了
  这些模型→必 404。特色/用量分级是**全局**的，不区分"该凭据是否真的提供此模型"。
- 真正的驱动者是 request_failure 探测 + 404 被当作普通健康信号。

### 1.3 用户看到的"状态/冷却中"

- 路由总览行级 `available`（model_offers/绑定）与 `availability_state`（credentials）。
  风暴期与 404 循环期，绑定/观察面持续被打成不可用/冷却 → UI 显示凭据不可用。
- 手动恢复后再次"冷却"：404 循环每轮失败刷新 5 分钟冷却（130dao claude-opus-5 于
  14:25 probe_timeout 冷却到 14:30 即真实例证）；hzx-2 自身 14:00 后探测全成功，
  其"冷却"观感来自中毒残留 + 同提供方其它模型的冷却展示。

### 1.4 附带发现的部署阻断（已修）

HEAD 含 R36/R37 审计新增的 `ensureFreediscoveryTemplateHealth`（087），假定
`provider_templates` 存在；共享库未跑过 freediscovery bootstrap→42P01→启动重试循环
把 75s 预算烧光→部署 healthz 60s 窗口超时（14:55 候选被拒，旧实例无中断）。
修复：`to_regclass` 预检，表缺失即跳过（70672a052）。

---

## 2. 修复内容（67d8629d2 + 70672a052）

| # | 缺陷 | 修复 |
|---|------|------|
| 1 | 404 与 5xx/超时同权，5 分钟冷却永动 | `unavailableBindingHorizon`：直连轮 404 且 attempt≥2 → 绑定与观察面冷却升为 **6h**，reason=`probe_model_not_served_404`（保持 `probe_%` 前缀，expired-binding recovery 仍拥有 6h 后复验权）；首次 404 保持 5 分钟（防聚合器偶发 404 误终判） |
| 2 | 退避梯每次 request_failure 从头走 | `ProbeBackoffForErrCode`：确认性 404 停泊 6h；legacy runOne 失败分支改用与队列路径同一 err-code 感知策略；featured 回退倍率不再缩短 404 停泊 |
| 3 | 中毒存量不自愈 | `deescalateGatewaySideProbeState`：启动时 + 解密熔断"开→合"时一次性扫除——endpoint_build/request_build 梯子行 next_retry 拉回 now()，被 probe_endpoint_build 毒化的绑定恢复 available |
| 4 | featured 深探白打已判死绑定 | featuredCycle SQL 跳过 `cmb.available=FALSE` 行（该周期对状态只读，探了也不能恢复，只烧请求+记必败 run） |
| 5 | 部署阻断 | 087 ensure 表缺失跳过 |

测试：`bg/probe_model_not_served_test.go`（策略 + 接线钉桩）、既有源码钉桩同步更新；
`go test ./bg ./admin ./internal/probeutil ./domains/credentialstate` 全绿。

**不做的选择**：按模型名（image/realtime/audio…）在入队时预判跳过——会让图像类
绑定失去唯一自动恢复通道（expired-binding recovery 也被挡→永不自愈），风险大于收益；
404 终判 + 6h 复验已把循环频率从 ~288 次/天/对降到 4 次/天/对，且目录变更后当天可自愈。

---

## 3. 自检与凭据节点状态管理功能面复查（用户要求的"全面检查"）

体系现状（职责边界 + 本轮评价）：

| 组件 | 职责 | 状态 |
|------|------|------|
| NodeProbeWorker（队列+legacy双路径） | 直连+网关双轮探测、梯子、绑定/URSM/观察面写入 | 本轮修复主体；直连轮为恢复权威（2026-09-10 hzx-2 doctrine 保持） |
| ProbeService.Run（统一执行） | 必要性门、租约心跳、结果落库 | applyOutcome 404 语义对齐（本修） |
| probe necessity gate | 探前 Redis 证据省探 | 健康；镜像删除有一跳重试 |
| 解密熔断 + 共享态守卫（20fb4c7a4） | 错 key 实例不再毒化共享面 | 本次补上"熔断闭合→扫残留"半环（本修） |
| CredentialRecovery（expired/fresh-degraded） | 冷却到期重探 | `model_probe_broken` 行 `NULLS LAST` 排序致饿死倾向——已有 sweeper 兜底，暂不改 |
| ModelProbeRunner featured 深探 | 常用模型 30 分钟深 ping | 本修过滤不可用绑定；**遗留**：特色集是全局的，gpt-image-2 这类非 chat 模型仍会进列表打其它凭据（有 #1 的 6h 终判兜底，影响=每 6h 一次必败 run 记录） |
| ModelProbeRunner recovering sweeper | recovering 死端行清空 | 健康 |
| CredentialSelfcheckWorker | top10/featured 定期自检 | model_source=top10 与用量联动，正常 |
| credentialstate Manager | 请求驱动成败计数 + 阈值触发主动探测 | 正常（request_failure 入口） |

**遗留观察项（不阻塞，建议下轮）**：
1. `routing_policy.featured_models` 运营侧清理非 chat 模型（gpt-image-2）——一行 SQL 的
   人工动作，比代码兜底更治本。
2. node_probe_runs / credential_probe_queue 无实例标识列，跨实例溯源靠时间窗推断；
   建议加 `instance_id`（迁移 + 写入点）。
3. `model_probe_state` 与 `node_probe_state` 对同一 (cred,model) 可能给出相反判定
   （今晨 gpt-5.6-luna：前者 healthy_confirmed（8月旧数据）、后者 cf=7）——双体系
   判定收敛是 C11（mirror 退役）的既有规划，维持该方向。
4. 154 生产仍在 367c6545（无守卫）——本轮部署 245 验证后必须同步 154，否则未来任何
   一台共库实例错 key 仍可从 154 侧复现污染（154 自身也是候选污染源）。

---

## 4. 部署过程中追加发现的两个独立缺陷（已一并修复）

### 4.1 087 ensure 假定 provider_templates 存在（部署阻断，70672a052）

R36/R37 审计新增的 `ensureFreediscoveryTemplateHealth` 在共享库上 42P01 死循环
（freediscovery bootstrap 从未在该库跑过），`openDBWithBootRetry` 误判为
"postgres unreachable" 烧完 75s 预算，部署 healthz 60s 窗口超时候选被拒
（14:55 首次部署实证，旧实例无中断）。修复：`to_regclass` 预检，表缺失静默跳过。

### 4.2 154 canary 模板被统一成 traffic-only —— 后台 worker 全集群无主（0a015af51）

154 的 blue-green 模板在 09-17 "与 245 统一"时继承了
`LLM_GATEWAY_RUNTIME_ROLE=traffic-only`。245 是 `bg_mode=data-plane`（设计使然），
**154 是共享库上唯一的 full-mode 实例** —— 154 今晨 09:09 部署换装新模板后，
所有 `!bgDataPlaneOnly` worker 全集群无主：
- 持久探测队列（credential_probe_queue 516 条 ready 行自 09:09 无人认领；
  必要性门、pinned gateway round、integrity_verify 一并失效）
- model discovery、credential cycler、taxonomy sync、weekly rollup、audit trimmer

node 探测靠 legacy cycle 路径苟活，掩盖了损失。修复：从 154 模板移除该行
（空角色归一化为 active，恢复 09:09 前的所有权模型；蓝绿 ~15-30s 双活重叠是
历史稳态，队列 SKIP LOCKED+租约、其余 worker advisory-lock/幂等，均为多实例设计）。
重部署后实证：`runtime_role=active`、`durable probe queue worker started`、
积压 516→483 消化中。

## 5. 验证记录

- 本地：`go build ./...`；`go test ./bg ./admin ./internal/probeutil ./domains/credentialstate -count=1` 全绿。
- 245：2125-70672a05（14:58 上线）——
  - `provider_templates absent ... skipping 087`（部署阻断修复生效）；
  - 404 梯子停泊：gpt-5.4 attempt7 http_404 → next_retry_seconds=21600；
  - 解密恢复扫除无残留行可清（当时已被上午的恢复路径消化）。
- 154：2126-b874368a（15:33，含守卫 20fb4c7a4 + 全部修复）→ 2127-0a015af5（15:33，角色修复）——
  - 解密冒烟 0 失败；`durable probe queue worker started`；队列积压消化中；
  - **404 终判端到端生效**：`writer/palmyra-creative-122b`、`nvidia/cosmos-reason2-8b`
    （cred 19，attempt 7，http_404）→ 绑定 `probe_model_not_served_404` +
    recover_at +6h；同批 timeout→30s 短链、http_429→3600/7200 策略间隔；
  - gpt-5.4 / gpt-image-1（cred 2）在队列重排后被成功探测恢复 available ——
    6h 复验/恢复闭环双向可用。
- 现场状态复核（15:38）：四凭据 circuit closed / availability_state=ready /
  hzx-2 全模型探测 ok；仅剩历史 404 模型按 6h 节奏复检。

---

## 6. 收口审计（2026-09-18 03:00–03:30，批判性复检）

对前述修复逐项重取证，**不采信此前未实证的结论**。结果分三类：已实证生效 / 新发现缺陷（已修）/ 仍未闭环。

### 6.1 已实证生效

- **404 终判 fleet 级生效**：`credential_model_bindings` 中 `unavailable_reason='probe_model_not_served_404'` 的行从首验时的 2 行增至 **13 行**——随 request_failure 探测持续落新语义，非一次性现象。
- **双路径守卫齐备**：legacy runOne（`gatewaySide` 分支，bg/node_probe.go）+ 队列路径 mirrorNodeProbeState（R39 补的 `CASE WHEN $10` 守卫，bg/probe_service.go）同时在线；`go test ./bg/ -count=1` 全绿。
- **代码现状**：`go build ./...`、`go vet ./cmd/gateway ./bg`、`go test ./errorsx ./bg -count=1` 全绿。

### 6.2 前次验证的盲区（如实记录）

- **"五凭据全 ready"只是时点快照**。03:14 复查：cred 2/31/59 跌为 auth_failed、cred 21 在 cooling↔ready 秒级抖动。
  - cred 2/31/59 的 auth_failed 是**上游真实 403 INSUFFICIENT_BALANCE**（中转账户欠费）——探测如实上报，属正确行为而非误判；直连此刻同样会失败。欠费充值后由探测/流量自动恢复。
  - **教训**：验证凭据状态类修复必须做时间窗对照（≥2 个时点+归因），单时点快照会误报"已修复"。

### 6.3 新发现缺陷并已修（本提交）

**MiniMax 400 invalid thinking.type（2013）被误分类 KindTransient → 凭据秒级抖动**。
链路：客户端请求带 `thinking.type:"enabled"` → MiniMax 400 → 分类器不认识该形状 → KindTransient → UpdateOnFailure 冷却凭据 → 节点探测（规范 ping）直连 200 → 恢复 → 下一个同形请求再冷却。prod 实证：cred 21 minimax-prod-v2 于 03:08–03:19 间多次翻转，credential_state_log 的 MiniMax-M3 行 `last_err=transient, recover_at=+30min`。
- `invalidRequestFormatRe`（errorsx/classify.go）新增两个 MiniMax 专属模式（`invalid params,…invalid thinking.type` 与 `invalid thinking.type…(2013)`）→ **KindClientBug**。
- 下游语义核验：`IsClientBug(KindClientBug)=true` → UpdateOnFailure 直接 return（不冷却）；`action_policy` terminal（不重试）。
- `failover_policy` 新增 `case KindClientBug`：EnqueueProbe=false（请求形状错误对任何凭据都必败，探测是纯浪费）——此前落入 default 分支仍会 fanout 探测。
- 新测试：`TestClassifyErrorWithBody_MiniMaxInvalidThinkingTypeIsClientBug`（含反向断言：裸 invalid params 不误伤）；`TestDecideFailover` 表新增 client-shape 行（probe=false）。

### 6.4 提交记录失实更正（不重写历史）

b484efbac 提交信息称"add missing net import"，但 rebase 时该 hunk 与 R39（78b3acdef，并行会话同修此问题）重复被 git 自动丢弃，**该提交实际只含版本漂移文件**；net import 修复的真实归属是 R39 的 78b3acdef。此处存档更正，不改写已推送历史。

### 6.5 仍未闭环（开放项）

1. **cred 2 的 5 个 `model_probe_broken` 死端行**（gpt-4o-audio-preview/gpt-5.2/gpt-5.2-chat-latest/gpt-5.3-codex-spark/gpt-image-1，recover_at=NULL）——归 recoveringSweeper 清空机制所有， sweeper 运行时行会逐步消化；未逐行验证消化进度。
2. **探测队列积压**：03:14 ready=497（15:38 时为 466）——随失败驱动的入队波动，无法据此断言"持续消化"或"持续增长"，需带窗口的趋势观测。
3. **featured 深探的运营侧清理**（routing_policy.featured_models 移除 gpt-image-2 等非 chat 模型）——报告 §3 遗留项 1，仍未执行（人工动作）。
4. **404 终判 6h 复验闭环的活体观测**：单测已锁行为，但首批 6h 窗口（约 09-18 05:36 后到期）的自动复验-恢复尚未到时，无活体证据。
5. **本修复（MiniMax 分类）未部署**：需随下一班 deploy-245 → 154 晋级；部署后观察 cred 21 是否停止抖动（state_reason 不再出现 `[transient] upstream 400 … thinking.type`）。
