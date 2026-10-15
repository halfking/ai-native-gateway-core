# r1026 —— grok-4.6「不可靠、总是失败」双根因：URSM 键 TTL 被流量砍缩 + tentative restore 必然回环

> 会话轮：2026-10-13 16:00–18:00（本仓 llm-gateway-go-4）。
> 用户报告：经 llm.kxpms.cn 调 grok-4.6 不可靠/总失败；直连
> `https://api.vapeur.ai/v1`（responses 模式）正常；怀疑「网关调供应商的模式不对 /
> 供应商上设置的模式没生效」。
> 结论先行：**模式没有问题**——R-vapeur4 的 proactive chat→responses 升级
> （ba1987a4d）已在生产生效（本次实测出站 URL =
> `https://api.vapeur.ai/v1/responses`、非流式响应 id=`resp_*` 回转 chat 信封）。
> 真正的「不可靠」是**可用性层的两个结构性缺陷叠加**，本轮修复：
> ① `record_request.lua` 把 probe 优先级节点键的 7h TTL 砍回流量 TTL；
> ② `ProbeSync` 的 tentative restore 在 park 政策下必然 15 分钟后被 revert，
>    形成自持翻红循环。

## 一、排除项：模式与参数（用户问题的直接回答）

| 检查项 | 结果 | 证据 |
|---|---|---|
| providers 表模式 | `vapeur`(36) protocol=`openai-responses`，enabled | 生产 DB 实查 |
| proactive 升级是否生效 | **生效** | 部署后实测 `upstream_url=https://api.vapeur.ai/v1/responses`（154 log `upstream_http_attempt`）；非流式响应 id `resp_*` |
| capability 位 | grok-4.6/cred63：`native_responses_nonstream=t`（10-13 实测）、`native_responses_stream=t`（09-25） | credential_model_capabilities |
| 直连 vapeur vs 经网关 | 经网关同样 200 | 本轮 7+ 次实测（chat/responses × 流式/非流式 × tools） |

## 二、根因 1：record_request.lua 砍缩 probe 键 TTL（file: domains/ursm/v2/store/record_request.lua）

机制（代码级，旧 224-228 行）：

```lua
if current_priority > 10 then
  redis.call("EXPIRE", node_key, node_ttl)   -- ← 把 7h 砍到 15min
  return {"applied", "0", "0"}
end
```

- UrsmlHealthyKeyReconciler（bg/ursm_healthy_reconcile.go）按 probe 语义写节点键：
  `ApplyProbeForTenant` → `apply_probe.lua`，TTL = max(NodeTTL, **7h floor**)
  （domains/ursm/v2/probe.go probeWriteTTLFloor）。
- 但只要有一次真实流量，遥测写（prio>10 早退分支）就 `EXPIRE node_ttl`
  把 TTL 重置为 NodeTTL（默认 15min，config.go:201 T5 调整）。
- grok-4.6 流量稀疏（生产热表 48h 仅 27 行）→ 每次空闲 >15min 键即过期 →
  URSM v2 T4 读契约把缺失键判 `Available=false` → 路由 fail-closed 滤掉
  唯一候选（apigpt(314) 两凭据已余额耗尽，Vapeur cred63 是唯一活候选）
  → `no candidates after router (availability_check_failed)` →
  热路径 `sync_no_candidate_probe`（~9s hold）→ 恢复。
- **实测**（154 生产，2026-10-13）：
  - 16:31 读键 `ursm:v2:node:default:63:grok-4.6`：prio=20 但 TTL=291s
    （7h 键被 16:25 的请求遥测+探测网关轮遥测砍缩后的残量）；
  - ~16:36 键过期（TTL=-2），reconciler 14+ 个 tick 未修（见 §五开放项）；
  - 16:53 部署前对照请求：**15.8s**（9s 挂起 + 正常耗时）；热请求 7-9s。
- 修复：prio>10 分支改为**只延不砍**（extend-only）：
  `existing_ttl >= 0 and existing_ttl < node_ttl` 才 `EXPIRE`。
  语义：流量可以续命将逝的键，但永不把 probe 的 7h 地板砍到流量 TTL；
  TTL=-1（持久键）不动；prio≤10 键保持原全路径刷新语义（traffic-owned）。

## 三、根因 2：tentative restore 确认机制被 park 政策杀死（file: bg/node_probe.go ProbeSync）

机制（两份代码注释的时间线互搏）：

- 2026-08-24（需求 6 bullet 6）：`ProbeSync` 热路径恢复成功后
  `MarkTentativeRestore` 盖章 `probe_revert_at = now()+15min`，
  依赖「下一次确认探测成功」（tick/queue 触发）清除盖章
  （`updateBindingAvailability(true)` → healthyBindingSQL 置
  `probe_revert_at=NULL`）。当时假设确认探测会自然到来。
- 2026-09-20（probe-volume 政策）：探测成功后行 park 30 天
  （`next_retry_at = now()+30d`），**所有探测调度器跳过 parked 行**。
- 两者叠加 ⇒ 盖章后确认探测**结构性永不到来** ⇒ 15 分钟后
  `ProbeRollback.revertDue` 必然把 binding 翻红
  （`unavailable_reason='probe_revert_timeout'`，5 分钟窗）
  ⇒ 翻红窗内新请求 no_candidates → ProbeSync 再救 → 再盖章 ⇒ **自持循环**。

实测锚点（154，2026-10-13）：

- 16:23:11 本会话首个测试请求触发 ProbeSync（tentative 盖章 +15min）；
- **16:38:08**（恰 +15min，tick 粒度 ±3s）binding 3095366 被
  `probe_revert_timeout` 翻红，`unavailable_recover_at=16:43:08`；
- 16:43 自动恢复后进入下一轮循环；`probe_rollback: reverted tentative
  restores` 每 tick 都在发生。
- 附带损害：binding 翻红期间 reconciler 的 `healthyCandidateSQL`
  （`cmb.available=TRUE` 谓词）把该对排除在修复范围外——两个根因互相放大。

修复：删除 ProbeSync 成功分支的 `MarkTentativeRestore` 盖章——
`updateBindingAvailability(true)` 清 `probe_revert_at`，恢复为**最终态**。
理由：直连轮成功本来就是恢复信号（gateway 轮仅诊断，2026-08-18 教义）；
真实回归由请求失败阶梯、request_failure 探测与 tick 调度兜底，均不依赖
revert 死线。`MarkTentativeRestore`/`ProbeRollback` 机制保留：滚动部署期
存量已盖章行仍会被 revert 一次，随后由首次救援永久修复（一次性残留，
自愈）。env `LLM_GATEWAY_PROBE_TENTATIVE_REVERT_AFTER=0` 原本可整体关停
该机制，本轮改为代码级默认关停（删除调用点）。

## 四、判据与变异验证

新增/改写：

- `domains/ursm/v2/store/record_request_test.go`
  `TestRecordRequestProbePriorityKeyTTLNeverShrinks`（3 子用例）：
  ① prio=20 键 + ~7h TTL → 流量后 TTL 保持 ~7h、prio 不变；
  ② prio=20 键 + 30s 残量 → 流量后续到 ~15m；
  ③ prio 缺省（≤10）键 → 保持原 NodeTTL 刷新语义（traffic-owned）。
- `bg/probe_rollback_tentative_test.go`
  `TestProbeSyncMarksTentativeRestore` →
  **`TestProbeSyncRestoreIsFinalWithoutTentativeStamp`**：钉住 ProbeSync
  不得再调 `MarkTentativeRestore` + 保留 R-vapeur2 的两条恢复竞态不变量
  （URSM 写先于 DB 簿记、winner 投递先于 gateway 轮）。
- `preload_scripts_test.go` 脚本长度钉子随更新（13089 / 11579）。

变异验证（恢复一律 cp 备份，不用 git 命令）：

- M1：lua 恢复旧无条件 `EXPIRE node_ttl` → 子用例①红
  （`traffic_does_not_shrink_a_long_probe_TTL`）✅
- M2：node_probe.go 重新插回 `MarkTentativeRestore` 调用 →
  `TestProbeSyncRestoreIsFinalWithoutTentativeStamp` 红 ✅
- 回归：`go build ./...` rc=0；`CGO_ENABLED=0 go test ./bg/`（30s）全绿；
  `./domains/ursm/v2/...` 全绿。

## 五、部署与端到端验证

- 154：**2576-9e2d77b9**（17:14 切流，active_port=8782；含本轮修复 +
  r1025 fp_slot 钉扎豁免——154 此前停在 2602 不含 r1025）。
  二进制指纹：`strings` 命中 `Extend-only`×2（两份 lua）、
  `about to lapse`、`revert loop`（新注释）；旧串
  `tentative restore stamp failed` 已消失（盖章代码确删）。
- 245：并行部署（同构建）。
- 端到端判据（154，llm.kxpms.cn）：
  1. 修复前基线：键过期后的请求 15.8s（9s 挂起），日志
     `no candidates after router` + `sync_no_candidate_probe_recovered
     hold_ms≈9000`；
  2. 修复后：键过期 → 请求触发 ProbeSync → apply_probe 写 prio=20/7h →
     **遥测后 TTL 保持 ~7h**（extend-only 生效的直接证据）；
  3. 修复后 binding 不再出现 `probe_revert_timeout` 翻红
     （`probe_rollback: reverted tentative restores` 计数停止增长）；
  4. 长 idle（>15min）后的首请求不再有 9s 挂起（键存活）。

## 六、开放项（登记不修，本轮范围外）

1. **reconciler 盲区**：16:36 键过期后 14+ 个 tick 未被
   UrsmlHealthyKeyReconciler 修复（pair 满足 `healthyCandidateSQL` 全部谓词、
   无任何 reconciler 错误日志、镜像软 TTL 30s 不解释该盲区）。
   `repaired≈36/tick` 的恒定轮转形态同样未解释（LRU 容量 100k、238 对，
   不应每 tick 轮换）。**修复①落地后键不再周期性消失，该盲区的用户面
   影响大幅缩小，但 Redis 灾难恢复场景仍依赖 reconciler，值得单独一轮
   根修。**
2. fp_slot 在新实例启动后立即 `degradation_ratio=1`（17:15:40，指纹槽
   Redis 持久化跨重启残留）——降级路径正常放行，不影响可用性，但
   `fp_slot_saturation_critical` 的告警噪音值得关注。
3. `persist collect failed ... context deadline exceeded` 每 30s 仍超时
   （170 万键 SCAN，既有登记项）。
4. `expired-binding probe recovery queued pairs:50, unique_credentials:2`
   ——死凭据（72 apigpt / 68 grok-heavy，余额耗尽）的绑定恢复队列持续
   空转，建议 owner 拍板清退。
5. 211 的 Redis 库号 claim 不粘 + UrsmlHealthyKeyReconciler 未起
   （r1024 遗留登记）。

## 七、量具坑（本轮新增）

1. **epoch 毫秒心算连环错**：`updated_at_ms`/`last_ok_ms` 的时刻换算
   连错两次（16:35↔16:49↔17:15），差点把「部署后键形态」推到错误的
   时间线上。凡 Redis 毫秒时间戳对时，必须 `date +%s%3N` 同框比对，
   不许与早前读数做差值心算。
2. **`strings` 找 Go 注释短语要先确认短语真在源里**：两个「未命中」的
   marker 其实来自测试文件名/跨行注释，误判成「改动未部署」；
   用 `existing_ttl`（新引入的标识符，必在 rodata）兜底定性后，
   再回头用准确短语复核。
3. **共享 db2 上 `--scan` 全库 170 万键会挂死 ssh 会话**（两次），
   定位键一律用已知键形态直 GET/HGETALL。
