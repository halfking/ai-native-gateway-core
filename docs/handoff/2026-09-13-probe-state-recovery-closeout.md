# 探测与状态恢复闭环审计 + 实施记录（2026-09-13）

> 目标（用户需求原文）：检查 245/154 日志，找出为何仍需人工强制恢复供应商凭据及节点状态；
> 从代码层面让状态探测流程清晰、及时、成本优化——不重复探测；可恢复错误/优先节点加快频度；
> 成功后不重复探测；失败/网络不通时加快但有度，不被供应商视为攻击。
> 分支：`fix/probe-recovery-closeout`（基于 main 9d8b65d96）。

## 1. 生产取证（2026-09-13 02:00–03:00 CST）

### 1.1 部署拓扑
- 154：`llm-gateway-go-canary@8781`，`LLM_GATEWAY_RUNTIME_ROLE=traffic-only` —— **无任何后台 worker**。
- 245：`llmgo-245-canary@8781` —— **全舰队后台 worker 唯一运行地**；`LLM_GATEWAY_USE_NEW_PROBE_MODE` 未设（默认 true）。
- 两台 v2.5.4-5c58bf34（seq 2086，2026-09-11），共享 252 的 `llm_gateway` 库与 Redis。

### 1.2 日志证据（245，24h）
- ~578 次/小时 unique 探测请求（2h 样本 1,156 次 unique probe-direct）。
- 失败探测集中在高 attempt（a7–a15，退避阶梯 6h 封顶后仍无限续探）。
- `credential probe v2: ProbeNow failed` 持续打 auth_failed 凭据（57/58 GROUP_NOT_ALLOWED、48 token 无模型权限——永久性 403）。

### 1.3 数据库证据（252 pg-252-pg17）
- 凭据：ready 38（12 manual_disabled）、**suspended 17**（2 manual_disabled）、auth_failed 4、unreachable 3。
- **suspended + quota_state='ok' + recover_at=NULL 的自相矛盾行**：cred 9（reason=rate_limited，detail=passive_probe_review_failed: transient）、cred 23（无 reason）、cred 24（reason=rate_limited）——无任何自动恢复路径。
- cred 21：permanently_exhausted + reason_code=network + transient 证据（分类存疑）；cred 38：余额不足（403 带"剩余额度"文案）被分类为 auth_failed。
- 绑定（cmb）available=FALSE 计 58：auto_credential_quota_permanent 32、auto_discovery_expired 12、probe_http_401 6、**model_probe_broken 6**、probe_http_429 2。
- **model_probe_state：113 行 state='recovering' 全部 next_retry_at 已到期（最早 2026-07-20），零 driver**；其中 64 行挂在完全 ready/ok/active 的凭据上。broken_confirmed=0（reviver 已翻成 recovering 后成死端）。
- node_probe_state：579 节点，373 个连续失败≥5（最高 15），72 个 next_retry>1h。
- 近 7 天 request_state_transitions 无 admin 强制恢复审计行。

## 2. 根因（代码级）

| # | 根因 | 位置 |
|---|---|---|
| R1 | suspended+NULL+quota ok 卡死：挂起类写入（auth_revoked/quota_permanent/quota_balance）recover_at=NULL；30s 恢复 tick 的 availSQL 对 suspended 要求"recover_at 到期"，NULL 永不满足；quota_state 被其它路径清回 ok 后状态矛盾且无探测来源刷新证据 → 只能人工 force-enable | `bg/credential_recovery.go` availSQL(:504-577)、`domains/credential/writer.go`(:233-272) |
| R2 | auth_failed 30 秒盲翻回 ready：availSQL 的 `availability_state='auth_failed'` 分支不检查 recover_at，新鲜 15min 冷却被短路；probe v2 的 401/403 分类写 auth_failed+NULL → ≤30s 翻 ready → 下轮 cycler 再探再败，永久振荡；真实流量 403 的凭据被过早放回路由 | `bg/credential_recovery.go`(:511-515)、`bg/credential_probe_v2.go` classifyProbeFailure(:919-923)、writer.go KindAuth(:273-298) |
| R3 | NEW_PROBE_MODE 下 model_probe_state 生命周期无 driver：ModelProbeRunner 共识循环（唯一 mps writer/重试 driver）被 skip，StartFeaturedOnly 只剩常用模型 deep-ping + 时间戳 watchdog；broken_probe_reviver 把 broken_confirmed→recovering 后成死端；RecoverExpired 又显式排除 model_probe_broken 绑定 → 113 个 recovering/6 个 model_probe_broken 绑定永久滞留、模型不可路由 | `cmd/gateway/main.go`(:3880-3891)、`bg/model_probe.go`(:153-168)、`bg/broken_probe_reviver.go`(:69)、`credentialhealth/checker.go`(:555) |
| R4 | BalanceQuotaProbe 每 2min 全量探测所有 balance/permanently_exhausted 凭据（~10 个死凭据 × 720 次/天/凭据的真实上游请求）：充值探活有价值，但固定高频=纯浪费且易被供应商判为滥用 | `bg/balance_quota_probe.go`(:46,329-380) |
| R5 | 成功后仍重复探测：probe v2 cycler 对流量健康、近 30min 有真实成功流水的凭据仍每小时发探测；node probe 成功后 next_retry=+1h 仍滚动态探测 | `bg/credential_probe_v2.go` cycleAll(:412-456)、`bg/node_probe.go`(:1874-1890) |
| R6 | state_reason 覆写：软降级路径（network/timeout/transient/overloaded）只写 reason 不看当前状态，把 suspended/auth_failed 的真实挂起证据覆写成 transient 痕迹（cred 9/21 实证），运维看不到真实原因 → 误判"莫名挂起只能人工捅" | `domains/credential/writer.go`(:299-321,333-356) |
| R7 | Retry-After 无上限：coolingDuration 首行直接采用上游 Retry-After，异常上游可把绑定钉住任意时长且无自动复核 | `domains/credential/writer.go`(:471-474) |

## 3. 方案（P1–P7，与根因一一对应）

- **P1a（R2）** availSQL auth_failed 分支加守卫：`availability_state='auth_failed' AND (availability_recover_at IS NULL OR availability_recover_at <= now())`——NULL 兼容保留（老行/探测行），新鲜冷却必须等满。
- **P1b（R1）** availSQL 新增 suspended 证据恢复分支：`suspended AND quota_state='ok' AND health_status='healthy' AND health_checked_at > now()-2h AND availability_recover_at IS NULL` → ready（证据=近 2h 内探活成功；硬配额/manual/lifecycle 守卫不变）。
- **P2（R2+R4 配套）** classifyProbeFailure 401/403 写指数退避 recover_at：`15m × 2^min(probe_consecutive_failures,5)`，封顶 24h；writeHealth 维护 `probe_consecutive_failures`（失败+1/成功清零）与 `last_probe_at`。探测型 auth 失败从"每小时振荡"变为衰减重探（24→12→…→1 次/天），成功即停。
- **P3（R4）** balance/periodic 探测目标 SQL 加到期闸：`last_probe_at IS NULL OR now()-last_probe_at >= LEAST(2min×2^probe_consecutive_failures, 1h)`；同时把"suspended 矛盾行"（quota ok + suspended + recover_at NULL + 非 manual）纳入 balance probe 的慢速复验目标，为 P1b 提供证据源（首次 30min 底频）。固定 2min 轰炸 → 衰减到 ≥1h，充值后仍能在 ≤1h 内自动恢复。
- **P4（R3）** StartFeaturedOnly 增加 recovering 补偿 sweeper：30min 一轮、批量 10、仅取 `mps.state='recovering' AND next_retry_at<=now()` 且凭据 ready/ok 的行，走既有共识 applyResult 写回（成功清 cmb.model_probe_broken）。不与 NodeProbeWorker/常用模型 deep-ping 目标重叠。
- **P5（R5）** probe v2 cycler 跳过"近 30min 有真实成功流水"的凭据（`last_used_at > now()-30min AND health_status IN ('healthy','unknown')`），env `LLM_GATEWAY_PROBE_SKIP_RECENT_SUCCESS`（默认 on）可关。真实成功流量即探测，成功后不再重复探测。
- **P6（R6）** 软降级写加守卫：`availability_state NOT IN ('suspended','auth_failed')` 才允许覆写 reason，保住挂起证据。
- **P7（R7）** coolingDuration 对 Retry-After clamp 至 24h 上限。

**实施中补充（取证驱动）**：生产取证发现 6 个 suspended 矛盾行（9/13/23/24/25/30）全部带有 `lifecycle_status='disabled' + auto_disabled_at`（可用性<50% 自动禁用规则），而 writeHealth 本就允许 disabled+auto_disabled+quota-ok 行写入并自动启用（auto_enabled_reason='periodic_quota_probe_recovered'）——但没有探测目标能到达它们。因此 P3 做了三处配套放宽：
1. balance probe 目标集 admit `lifecycle='disabled' AND auto_disabled_at IS NOT NULL`（operator 手工 lifecycle-disabled 仍排除）；
2. periodic probe 两条 SELECT 同样 admit（对齐 writeHealth 的准入类别）；
3. writeHealth 的 disabled 行准入从"仅 periodic_exhausted"扩展为"periodic_exhausted（到期）或 quota_state='ok'"。
RestoreOnSuccess 与 forceEnableCredentialSQL 增加 `probe_consecutive_failures=0`（+ last_probe_at=NULL）重置，使真实成功/人工恢复立即回到快探测档。

频度总原则（对齐用户要求）：可恢复（cooling/rate_limit/network/新降级）→ 快探测（既有 30s tick + fresh-degraded 冷却中即探）；优先/常用模型 → featured deep-ping 既有通道；成功 → 即时恢复 + 停止重复（P5/P2 计数清零）；失败/网络不通 → 指数退避且封顶（P2/P3），稳态下每死凭据 ≤1 次/小时（quota 类）/≤1 次/24h（auth 类）、每死节点 ≤1 次/6h，远低于任何供应商滥用阈值。

## 4. 审计与模拟测试

- 单元测试：writer（P6 守卫/P7 clamp）、classifyProbeFailure（P2 阶梯）、backoff 表达式（P3）。
- SQL 模拟：在 252 生产库以 READ ONLY 事务回放 availSQL/P3 目标选择的新 WHERE，核对应命中/不命中的行（见 §6 结果）。
- 编译门：`GOOS=linux GOARCH=arm64` zig 交叉编译 `./bg/... ./cmd/gateway/... ./domains/credential/...`；bg 包测试走本地 stub 副本（lgw-bgtest）。

## 5. 实施清单

- [x] P1a/P1b credential_recovery.go availSQL
- [x] P2 credential_probe_v2.go classifyProbeFailure 调用点退避 + writeHealth 计数（`probe_consecutive_failures`/`last_probe_at`/`last_probe_success`）
- [x] P3 balance_quota_probe.go / periodic_quota_probe.go 目标闸 + auto-disabled 行准入 + suspended 复验集
- [x] P4 model_probe.go StartFeaturedOnly recovering sweeper（30min/批 10，走 TriggerManual→共识 applyResult）
- [x] P5 credential_probe_v2.go cycler 近期成功跳过（LLM_GATEWAY_PROBE_SKIP_RECENT_SUCCESS，默认 on）
- [x] P6 writer.go 三处软降级写守卫（KindTransient/KindUpstreamOverloaded/KindNetwork 组）
- [x] P7 writer.go Retry-After clamp（maxCoolingDuration=24h）
- [x] RestoreOnSuccess / forceEnableCredentialSQL 重置探测退避计数
- [x] 单测 + 252 只读事务模拟验证（§6/§7）

## 6. 验证结果

### 6.1 SQL 只读模拟（252 生产库，BEGIN READ ONLY … ROLLBACK）
- **availSQL（P1）新 WHERE**：命中 6 行 auth_failed（38/48/57/58/62/37，全部 recover_at=NULL 的探测写入行）；suspended 矛盾行未被误放（无新鲜健康证据）——语义与设计一致。真实流量 403 写入的 +15min 冷却行不会在新鲜期内被翻回（R2 修复点）。
- **balance probe 目标查询（P3）**：准入 15 个卡死凭据——7 个 permanently_exhausted（39/41/44/26/21/40/61）+ 6 个 auto-disabled+suspended+ok（9/13/23/24/25/30）+ 2 个 canary 测试行；manual_disabled（36/47）与 operator lifecycle-disabled 正确排除。首轮探测后 `last_probe_at` 落笔、`probe_consecutive_failures` 爬梯，稳态 ≤1 次/小时。
- **periodic probe（P3）**：到期闸 SQL 与 balance 同构（POWER 指数 + LEAST 1h 封顶），语法经生产库验证。

### 6.2 单元测试
- `domains/credential`：新增 3 项（Retry-After clamp / 软降级 suspended 守卫 / RestoreOnSuccess 重置探测计数）全部通过；全包套件除 3 个 `TestRedisRPM*` Redis 时间窗测试外全绿——该族测试在未改动的 HEAD 代码上同样随机失败（复现实证：`TestRedisRPMMultiGateway` allowed=27 want 15；HEAD 上 allowed=18 want 15、allowed=22 want 20），为本机环境性预存抖动，与本次改动无关。
- `bg`：新增 `TestAuthProbeBackoff*` 3 项通过；全包套件除预存 Windows-only `TestWalkDirSafe_ToleratesIsolatedErrors`（symlink 需管理员权限，memory 记录在案的已知失败）外全绿；`TestPeriodicQuotaProbeSkipsDisabledCredentials` 按新策略更新（auto-disabled 行必须带 `auto_disabled_at IS NOT NULL` 守卫准入，操作员手工禁用仍排除）。
- `admin`：`TestForceEnableCredentialSQL_Structural` 扩展断言（probe_consecutive_failures=0、last_probe_at=NULL 重置）通过。

### 6.3 编译门
- `GOOS=linux GOARCH=arm64`（部署目标）zig 交叉编译 `./bg/... ./domains/credential/... ./admin/... ./cmd/gateway/...` 全部通过。

## 7. 预期效果（部署后观察点）

| 指标 | 改动前 | 改动后预期 |
|---|---|---|
| 死 auth 凭据探测频度 | ~24 次/天/凭据（振荡） | 15m→24h 衰减，稳态 1 次/天 |
| 死配额凭据探测频度 | 720 次/天/凭据（2min 固定） | 2min→1h 衰减，稳态 ≤24 次/天 |
| 高流量健康凭据探测 | 每小时 1 次 | 跳过（30min 内有成功真实流量） |
| suspended+ok 矛盾行 | 永久卡死，需人工 | ≤1h 复验一次，探活成功自动恢复 |
| recovering 死端（mps） | 113 行滞留（最早 07-20） | 30min/批 10 逐步排空 |
| cmb model_probe_broken 滞留 | 6 行永久 | sweeper 探活成功即清 |
| suspended 行 reason 证据 | 被软降级覆写 | 保留（P6 守卫） |

观察指标：`llmgw_routing_credential_recovery_total{sql_kind="availability_recover"}`、`credential probe v2: cycle complete` 日志的 `skipped_recent_success`、`recovering sweeper: drained due recovering probes` 日志、journald 中 `ProbeNow failed` 频度。

## 8. 审计轮（2026-09-13 第二轮，F1–F5）

实施完成后按用户要求做了一轮完整自审计，逐条复核 P1–P7 的行为闭环，发现并修复 5 个问题（均已提交至同一分支）：

| # | 问题 | 修复 |
|---|---|---|
| F1 | **cycler 不看退避阶梯**：P2 的 ladder 只挡"恢复 tick 翻 ready"，cycleAll 每小时仍探测 auth_failed 凭据——死 key 实际 24 次/天而非宣称的衰减 | cycleAll WHERE 增加 `(availability_recover_at IS NULL OR <= now())`——未来 recover_at 的行（auth 阶梯/rate_limited/unreachable）跳过，到期后由 tick 释放、下一轮 cycler 复测 |
| F2 | **快速复探击穿阶梯**：每次 auth_failed 还会 SubmitFastProbe（5 分钟后再探一次），阶梯形同虚设 | auth_failed 不再触发 fast reprobe；仅 unreachable（短暂瞬态）保留 |
| F3 | **pre-exhausted 误纳 auto-disabled 行**：replace_all 把放宽打到两条 SELECT；pre-boundary 行的探测结果会被 writeHealth 的 disabled 准入（要求 recover_at 已到期）拒写，纯浪费供应商请求 | probePreExhausted 回退为仅 lifecycle='active'；post-expiry 保留放宽 |
| F4 | **auto-disabled 复验再悬空**：复验集吃 403 后行变 auth_failed，掉出 suspended 复验集，且 availSQL 释放需 lifecycle='active'——再次无通道 | balance 复验集新增分支：auto-disabled + quota ok + 非 ready + recover_at 已到期，统一走慢速复验 |
| F5 | **sweeper 共享预算会掐断在途探测**：2 分钟 tick 预算到期时 cancel 正在执行的 TriggerManual，把可能健康的模型误记为共识失败 | SELECT 用 30s 独立 ctx；每个探测独立 90s 超时（recoveringSweepProbeTimeout）；5 分钟 tick 预算只在探测间隙检查 |

回归锁定：`TestCycleAllRespectsAuthBackoffLadder`（F1 源码形状 + F2 禁止 auth_failed 触发 fast reprobe）。

### 审计轮验证
- 252 只读模拟：F1 闸门使当前 cycler 目标集 23 行、0 行被跳过（ladder 行由新代码部署后自产自耗）；F4 查询命中集与修复前一致（15 行）且覆盖未来 auth_failed 悬空路径。
- `go test ./bg/`（本地 stub 副本）全绿（除预存 Windows symlink 项）；linux/arm64 交叉编译通过。

## 9. 部署说明（未执行，留待窗口）

- 顺序：先 245（全部 bg worker 所在）后 154（traffic-only，热路径 P6/P7 生效）。走既有 deploy-245 / deploy-seamless 流程 bump seq。
- 回滚：改动均为 SQL 字符串/Go 逻辑，无 schema 变更，直接回滚二进制即可。
- 注意：`probe_consecutive_failures`/`last_probe_at` 列为既有列（此前无 writer 维护），无迁移。部署后首批 P3 复验将立即探测 15 个卡死凭据（15 次真实请求），属预期一次性成本。

## 10. 部署执行记录（2026-09-13，245 完成 / 154 待 24h 观测门）

### 10.1 245（已完成，2026-09-13 07:23 CST）

- **2102-8e9f0072（seq 2102）蓝绿切换 8781→8782，DEPLOY_RC=0**；9 步验证链全绿（healthz/readyz/版本身份/DB/admin 同步/凭据解密冒烟 failed=0/logrotate）；身份三方核验过（current=releases/2102-8e9f0072，bundle git_sha = 二进制 vcs.revision = 8e9f0072）。
- 分支增量（已推送 origin）：`34031a63f` 合并 origin/main——**部署树必须 ⊇ 生产 2092**（其流式修复 b514ec374/4b7fb6bfd 原始 sha 只存在于 go-2 本地检出，已 rebase 为 `5b249f31d`/`158043f93` 进 main；同时带入 balance_floor guard（migration 701）+ 若干 web 修复）；`0d42b4a65` seamless 预编译二进制入口；`56a8eace1` bump-version cygpath 修正；`8e9f00722` 上传后恢复 gateway 执行位。合并解法：balance_floor 豁免（main 侧字面谓词，TestBalanceQuotaProbeExemptsBalanceFloorPulled 钉住）限定在 P3 目标集的 quota 分支内——suspended/F4 复验分支要求 quota_state='ok' 天然不选 floor 行。
- 部署前基线（24h，双槽）：ProbeNow failed=15,358；probe-direct 行=160,215；429/403 行=1,599；mps recovering=113（最早 2026-07-08）；probe_backoff 列维护行=0。基线与 ③ 对比口径已钉定于观测日志 `~\AppData\Local\lgw-closeout-observe\observations.md`。
- 迁移：部署流水线向共享库应用 4 个 pending（699/700/701_credential_balance_floor 等），db-changelog 已记账。
- **Windows 宿主部署配方**（首次从 Windows 完成，见记忆 llm-gateway-windows-deploy-recipe）：zig cc 预编译 + `LLM_GATEWAY_PREBUILT_BINARY` 入口；ssh（mux 剥除 + LogLevel=ERROR 防 PQ 警告污染合并捕获）/python3（Store 空壳 + \r\n 剥除）/sha256sum（-t 文本模式）三 shim；`AIAN_DEPLOY_LIB`/`ENVS_ROOT` 指向 /z/ SSOT（deploy-245.sh 包装层硬编码坏软链，直接调 deploy-seamless.sh）。
- T+10min 早期信号：全部 bg worker 启动（含新 balanceFloorGuard）；recovering sweeper 启动；availability_recover recovered 5→28；probe_backoff 维护行 0→13；recovering 113→112；15 卡死凭据复验开跑（403 死 key 落 auth_failed+退避）；孤发 1 条内部 503（跟踪）。
