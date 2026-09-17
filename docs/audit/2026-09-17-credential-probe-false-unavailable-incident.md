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

---

## 7. D+1 复核（2026-09-18 04:42–06:30，§6.5 五项逐条）

验证纪律：双时点+归因（T0=04:42 部署前基线；T5=06:30 部署后终测），全部时区 +08；
DB 为 154/245 共享 252 PG，任何单侧观测都注明写入方归因。本轮**先取证后改码**，
三个修复各自精确路径提交（62853d7b0 / c1263e9af+ffbc6dbc8），无预提交污染。

### 7.0 部署阻塞与修复（§6.5-5 的前置，共 6 次尝试）

| # | 时刻 | 结果 | 根因 / 处置 |
|---|------|------|-------------|
| 1 | 04:52 | 切流后回滚 | sync-admin 单次 psql 撞 252 饱和（too many clients，~90/100 水位） |
| 2 | 05:0x | 迁移中止 | **720 首应用 42P01**：`relation "context_manifest_entries" does not exist`（line 60）——59 段策略改写横跨可选表（兄弟产品表），`IF EXISTS` 只护 policy 不护表 → **62853d7b0**：每段 to_regclass 守卫 DO 块，缺表 NOTICE 跳过；up/down 双副本字节一致；scratch 库三场景实测（零表全跳过/有表仅建存在表/重跑幂等） |
| 3 | 05:16 | 切流后回滚 | 720 修复生效（迁移推进到 722）；sync-admin 再撞饱和 → **c1263e9af**：连接类错误 psql 重试 |
| 4 | 05:26 | 切流后回滚 | 3×5s 重试全落空——饱和是**持续性**的（实测 107 连接、90+ idle，兄弟产品泄漏）→ **ffbc6dbc8**：预算 8×10s |
| 5 | 05:31 | 中止 | [8/9] 期间被并行会话干扰（见下） |
| 6 | 05:40 | **成功** | 采纳已验证 2133 release，切流 24s |

**并行部署竞争事件**：05:31–05:36 另一会话以 in-place 方式（绕过无缝蓝绿与构建锁）
直接替换 `/opt/llm-gateway-go/gateway`（其工作区构建，版本戳 2127-48278818）并重启
8782；且 05:37:52 对 cred 21 执行 force-enable（detail=`force_enable:
minimax-thinking-fix verify 2127`，人工救援动作、不钉住状态）——对方在验同一事故。
run 6 收尾时已 prune 未验证 releases（含该 2127）；06:17–06:19 对方再度部署（见
下方"最终态"）——其 06:20:00 日志显示其工作区含等价 client_bug 分类（未提交态）。

**验证（全绿，05:45 实测 2133 在役时）**：245 active=8781 `releases/2133-ffbc6dbc`
（⊇ c66dbd6c9）；healthz/readyz 200；nginx https 200；background-tasks 401（DB 就绪
非 503）；admin-login 200（sync-admin 生效）；公网 L4 `llmgo.kxpms.cn/healthz` 200；
解密冒烟 6 provider / 4 cred / **0 decrypt_failed**；现役二进制含 MiniMax 分类
regex literal（`invalid[ _-]?params` / `thinking[._ -]?type`）。

**复核收尾时的最终态（06:27，如实记录）**：本会话的 2133-ffbc6dbc 在 05:40–06:17
服役并完成 §7.1 活体验证后，被并行会话再度取代——06:17:13 其无缝部署
`2134-289c880e`（基于 origin/main 的 merge，**不含本会话三个修复提交**——720 守卫/
sync-admin 重试尚未推送；其未再撞 720 是因为共享 PG 已由本会话 run3/run5 登记
720–723）切至 8782，06:19:57 又以 in-place 重启 8781（2127-48278818，工作区含
等价 client_bug 修复，06:20:00 日志实证）。截至复核结束，nginx 现役=8782
（2134-289c880e，二进制含 MiniMax 修复 pattern）；本会话三修复以提交形式待推送
合流。**教训**：多会话同时操作 245 时"部署谁说了算"没有仲裁，必须串行化约定。

### 7.1 §6.5-5：cred 21（minimax-prod-v2）抖动是否停止（①）

部署前基线（旧代码 2125/2127，两台均无修复）：
- T0 04:42:44 credentials 行写入 `[transient] upstream 400 … invalid thinking.type
  (2013)`（ready 但 detail 残留该形状）；24h 内 cred 21 的 request_failure 探测 295 次。
- T2 05:12:16 再次 cooling 写入（05:17:03 恢复）——抖动持续活跃，节奏 ~15-60 分钟。
- 归因：03:05–03:25 抖动窗内 245 journald 0 条 "invalid thinking"（6097 条 minimax
  相关日志中），154 journald 04:10:51 有 `candidate_failure_alert credential_id=21
  MiniMax-M3`（invalid thinking）——**两台旧代码都在产生/放大抖动**（共享 PG，
  任意一侧的请求路径都能写入冷却），单一时点或单侧日志都不足以归因。

部署后（05:40 起，双构建叠加窗口——05:40–06:17 为 2133-ffbc6dbc，06:17 起为并行
会话的 2127/2134 构建，二者经二进制 grep 与活体日志验证均含等价 client_bug 分类）：
- **T5（06:23）**：credentials 行 state_updated_at 仍停在 05:37:52（force_enable）；
  05:40 以来 cred 21 冷却写入 **0** 条，模型级 5 条写入全部为 available 恢复向。
- **活量流量下的修复实证**：06:04:21 与 06:20:00 两波同形请求（`count_in_window=10`
  /5min，MiniMax-M3 thinking.type 400）在 245 日志中呈现
  `candidate_failure_alert … error_kind":"client_bug"`——分类正确、仅告警、
  **无任何 cooling 动作**（"cooled"行检索为空；对比旧代码下同型请求写入
  `[transient]` 冷却）。
- 判定标准达成：部署后 state_reason_detail 不再出现 `[transient] … thinking.type`
  形状（06:27:09 的一次 cooling 为 `[concurrent] overloaded_error`——MiniMax 集群
  过载的合法限流冷却，属另一机制、按 rate_limit 策略自愈，非本事故形状）。

判定：**①在 245 达成**（含活量流量验证）；残余风险 = 154 仍运行 2127（无修复），
154 侧遇到同形请求仍会写入冷却（共享 PG），完全止抖以 154 晋级为完成条件。

### 7.2 §6.5-4：404 终判 6h 到期复验（②）——两个结构性发现

**发现一：§6.5-4 预期的"05:36 到期批"根本没到期。** 03:51–04:43 之间，全部 25 行
`probe_model_not_served_404` 被 request_failure 探测**重写为新的 +6h 窗口**（滚动
续期）；至 05:12 总数 25→32（11 行在 04:43 后再次续期）；**至 06:23 爆发到 77 行**
（06:13–06:23 request_failure 探测波，峰值 16-21 次/分钟，cred 8×27 / cred 18×22 /
cred 19×15 —— 某客户端批量命中中转目录模型）。机制：停泊中的绑定仍被
流量驱动的 request_failure 探测命中（cred 19 停泊模型 6h 内各被探 4-7 次，最晚
04:46），每次 direct 404×2 即 `recover_at=now()+6h`（bg/node_probe.go
unavailableBindingHorizon → updateBindingAvailability 无条件刷新），**死线永远追不上**。

**发现二：`trigger_kind='credential_recovery'` 全时段 0 行（自表建成以来）。**
机制层两个原因：
1. **触发错标**：恢复扫描的提交走 main.go:4302/4461
   `nodeProbeWorker.Submit(credID, model, "default", "expired-binding-recovery")`
   ——第 4 参是 parentReqID 不是 trigger；unified-queue 下 `submitViaQueue` 硬编码
   source="request_failure"（bg/node_probe.go:835）。恢复探测即使发生也被记成
   request_failure，不可观测。
2. 滚动续期（发现一）使 recover_at 极少成熟；且 expiredCmbRecoverySQL 要求
   credential 本身 `availability_state='ready'`，cred 18/19/29 长期 cooling 亦不合格。

结论：§5 所称"6h 复验/恢复闭环双向可用"的活体证据实为 request_failure/periodic
通道完成（gpt-5.4/gpt-image-1），**到期自动复验通道至今无一次可观测运行**。下一个
自然到期窗 09:51–10:43（若不再被续期），留待下轮。结构性修复建议：恢复提交走显式
source（需扩 credential_probe_queue.source CHECK 枚举，随迁移做）；并评估停泊绑定
对 request_failure 探测的免疫，否则 6h 死线形同虚设。

### 7.3 §6.5-1：cred 2 五个 model_probe_broken 行（③）——已消化，机制活体有效

- 五行（gpt-4o-audio-preview / gpt-5.2 / gpt-5.2-chat-latest / gpt-5.3-codex-spark /
  gpt-image-1）至 T0（04:47）**全部 available=t、无 unavailable_reason**。
- broken 行的生灭是动态的：04:53:34（同一微秒=模型探测共识批量写）新产生 8 行
  （creds 2/4/18/21）；至 05:30 cred 2 的 3 行已消化（~20-35 分钟），其余 6 行仍在
  周期中（含 cred 21 的 abab5.5-chat / MiniMax-Text-01）。
- 判定：sweeper/healthy_confirmed 消化活体有效；行存续以小时计，非死端。

### 7.4 §6.5-2：探测队列 ready 24h 趋势（④）

- 时点序列：466（09-17 15:38）→ 497（03:14）→ 474（T0 04:42）→ 531（04:56）→
  526（05:01）→ 520（05:12）→ **524（T5 06:05/06:23 两测一致）**。
- 24h 结构：入队 ~450-660 行/h；消化 ~60-108 行/h，**输出几乎全部失败**——05:00
  前后 2h 窗 210 行失败中 207 行 result_http_status=401（上游无效 key/欠费中转）；
  success 全天仅 4 行。
- 年龄分布（05:56，total 527）：<6h 446（活跃滚动）＋昨日尾 77＋09-11 化石 4。
- 判定：**高位持平/缓涨**，既非持续消化也非爆涨；去化瓶颈是死 key 凭据的运营清理
  （欠费充值/换 key），非代码缺陷。

### 7.5 §6.5-3：featured_models 非 chat 模型（⑤）

- routing_policy.featured_models 共 26 模型，唯一非 chat：**gpt-image-2**（其余 25
  个为 claude/deepseek/doubao/gemini/glm/gpt-5.x/kimi/minimax 等 chat/text 系列）。
- 卫生发现：routing_policy 存在 **4 行物理重复**（ctid 互异，id=1/tenant=default
  内容全同）——id 无唯一约束。
- 运营确认结论：**待运营答复**——本轮已备齐证据与建议 SQL（见下），因"移除会改变
  featured 深探/路由行为"属运营决策，未擅自执行；答复后按建议动作落库即可闭环。
- 建议动作（确认后执行，注意先去重）：
  `DELETE` 重复行后 `UPDATE routing_policy SET featured_models =
  array_remove(featured_models,'gpt-image-2')`。

### 7.6 新开放项（本轮新登记）

1. **404 复验通道结构性修复**（§7.2 两发现）：显式 source 枚举 + 停泊绑定探测免疫。
2. **154 晋级**：MiniMax 修复随下一班 deploy-154，完成后 cred 21 抖动方可完全止息。
3. cred 35 失败率 ~100%（每 5 分钟 38-46 次全败）持续 auto-cool 循环——死 key 候选
   仍被流量反复命中，建议运营下线或修 key。
4. 245 request_failure 探测的 gateway 腿大量 401 invalid_key（同分钟 direct 腿
   200）——探测网关腿的鉴权/路由错位，建议单开排查。
5. **多会话 245 部署串行化约定**：05:31 in-place 绕过无缝蓝绿与构建锁事件。
6. routing_policy 4 行物理重复（id 无唯一约束）。
