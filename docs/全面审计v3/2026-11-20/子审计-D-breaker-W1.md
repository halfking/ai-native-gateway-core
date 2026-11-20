# 子审计 D：breaker W1 列车（2026-11-18 后 48h 窗口）

> 审计对象：`21d5faf3c`（merge W1-A 熔断耗尽保真 kind + provider_circuit_open）→ `4eac2b8f1`/`b9f22c823`（per-model 阶段1/2a）→ `ebb08441f`（context_summarize per-model）→ `d8b3b04da`（阶段3 DB 上卷）→ `7e2236089`（阶段4 测试）→ `15c54de91`（验收清单+脚本）→ `4a0bc1ac5`（P0-a 键空间统一）→ `e13a43aa4`（P0-a 测试回归）。
> 判定基准＝HEAD（`6da55b177`）。验证：`go build ./domains/credential/ ./domains/streaming/... ./admin/ ./cmd/gateway/` rc=0；`go test -run 'TestBreakerScope|TestCircuitDegradationSwitch' ./domains/credential/` ok；`-run 'TestCircuitScopeGuard|TestDispatchCircuitAdmission' ./domains/streaming/executors/` ok。

## 一、结论总表

| # | 审计问题 | 判定 | 一句话结论 |
|---|---|---|---|
| 1 | 键空间一致性（P0-a 终态） | **已实现** | 四面全部经唯一构造点 `breakerScopeKey` → `"providerID/credentialID/model"`；两道门禁（反射+AST）实测绿 |
| 2 | context_summarize per-model | **已实现** | 全部 9 处调用点均带 `circuitScopeModel(cand)`；无漏带 model 的旧调用 |
| 3 | provider_circuit_open 三协议面 | **部分** | 三面均映射 503+专属码+Retry-After；但 anthropic 预热 SSE 形态丢专属码、chat 面 gateway_debug 泄漏 attempts 内部 ID（三面泄漏面不一致） |
| 4 | DB 上卷（d8b3b04da） | **部分** | 单行 UPDATE credentials 非事务、异步队列；**half-open 分支绕过上卷**（直接写 'half_open' 丢最差态）；崩溃窗口＝重启内存态全丢、DB 自愈向 closed |
| 5 | fail-open 特性开关 | **已实现** | 子进程真读 `KILL_CIRCUIT_DEGRADATION` 钉「关⇒跳过 breaker 不拒绝」；镜像注释的生产式已陈旧（P3） |
| 6 | 测试作用域回归（e13a43aa4） | **已实现** | 三处测试改与生产同源 model 分量；残余空模型测试是合法 legacy 语义测试，非第二键空间 |
| 7 | 验收脚本/清单 | **未实现（活体验证缺位）** | `verify-per-model-breaker.sh` 四处硬伤不可运行；清单键格式判据被 P0-a 推翻且未更新；阶段2–5 全 ⏳ |
| 8 | 并发安全/键增长 | **已实现（附 P3 登记）** | Manager 单 RWMutex+双检锁，Breaker 原子量+单 mu；map 无淘汰无上限（实际被 offer 清单约束） |

## 二、证据段

### 1. 键空间一致性（P0-a 终态）

- 键格式字符串：**`fmt.Sprintf("%d/%d/%s", providerID, credentialID, model)`**（`domains/credential/breaker.go:222`，构造函数 `breakerScopeKey` :217-223）；模型名为空/纯空格 ⇒ 第三段落 `_legacy_`（:200、:218-220）。`NewPerModelWithPolicy` 同步把空模型改写成 `_legacy_`，保证「键与字段一致」（:276-282）。
- **写面（记账）**：`RecordFailurePerModel`/`RecordFailureWithBillingMode`/`RecordFailureWithReset`/`RecordSuccessPerModel` 全部经 `GetOrCreatePerModel` → `breakerScopeKey`（breaker.go:1178/1197/1209/1244）。
- **读面（准入）**：`AllowPerModel`（:1256）同键；dispatch 准入 `executor_dispatch.go:973` 用 `circuitScopeModel(cand)`（= `Candidate.BindingRawModel()`，offer 身份，`executor.go:3222`）。
- **恢复面（half-open→close）**：探针在 `Allow()` 内消费（breaker.go:373-388，单探针 CAS+5min `halfOpenProbeTimeout` 回收）；释放 `Manager.ReleaseProbe(providerID, credentialID, model)`（:1096）与消费同键；成功关闭 `RecordSuccessPerModel` 同键；admin 紧急重置 `Manager.Reset`（:1152-1171）model 非空重置单模型、为空重置该 (provider,credential) 全部模型作用域（含 `_legacy_`）——`admin/routing.go:2047` 传 `req.RawModel`，两调用点（:1909/:5966）都把 rawModel 传下去。
- **DB 上卷面**：`GetCredentialState(credentialID)` 按 `b.credentialID == credentialID` 过滤取最差态（:1053-1070）——正确性依赖实例的 `credentialID` 归属真实凭据，而键含 credentialID 后成立（P0-a 修的正是「谁先创建谁归因」）。
- **model 分量单一来源**：`circuitScopeModel` = `BindingRawModel()`（offer 身份优先 `mo.raw_model_name`，`provider/client.go:102-107`）；nodehealth 路径由 `Observation.BreakerScopeModel = circuitScopeModel(cand)`（`executor_nodehealth.go:123`）→ `Decision.BreakerScopeModel`（`nodehealth/reducer.go:324`）→ 适配器 `scopeModel := decision.BreakerScopeModel`（executor_nodehealth.go:222）逐段透传，与准入逐字节同源。
- **门禁**：`TestBreakerScopeManagerExposesNoModelLessAPI`（反射：Manager 禁止无 model 方法，`breaker_scope_test.go:215-266`）＋ `TestCircuitScopeGuardProductionCallSitesUseSameScopeSource`（go/ast 扫 executors 生产文件，断言每个熔断调用点 model 实参来自同一来源集，`executor_circuit_scope_guard_test.go:31-90`）。两者 HEAD 实跑绿。coverage 守卫 `routeguard`（禁止 circuit_state 进 is_routable 合取）仍活。

### 2. context_summarize per-model

- `context_summarize.go` 9 处熔断调用（:490 准入、:502/:528/:543/:556/:569 探针释放、:539/:552 记失败、:560 记成功）**全部**带 `circuitScopeModel(cand)`，与主链路同一构造函数。
- 全仓 grep：executors 外无任何 `PerModel` 调用点漏 model；`RecordModelScopedFailure`（breaker.go:1218）**生产零调用方**（死面，不构成第二键空间）。

### 3. provider_circuit_open 三协议面

- 判据：`dispatchExhaustedByCircuitOpen`（`executor_circuit_open.go:41-58`）——**尝试摘要里每一条 Reason 都是 circuit_open 才算**（防混合场景误报）；`dispatchErrToExecuteError` 据此把 LastKind 从塌缩的 `KindTransient` 保真为 `KindCircuitOpen`（`executor_dispatch.go:612-621`）。
- 三面接线：chat `handler.go:6280`；messages `messages.go:1079`（落库 errCode）+`:1147`（客户端面）；responses `responses.go:1065`+`:1130`。均位于通用兜底 `model_not_found` 之前。
- 客户端形态：HTTP **503**；`Retry-After`（取熔断器**最早**冷却截止，`circuitOpenRetryAfterSeconds` 读 `GetPerModel` 同键 breaker，:90——P0-a 修前读 legacy 键恒 0）；`X-Gateway-Last-Kind: circuit_open`；body `error.code=provider_circuit_open`。Chat 面信封含 kind+gateway_debug（attempts/tried/retry_after）；Anthropic 信封 code 走 extras 注入（`writeAnthropicError`，messages.go:1153-1157）；Responses 面 `writeResponsesError(...surf.Code)`。
- 泄漏面：专属 Message 只含候选数与 clientModel（`provider_circuit_open.go:88`），无供应商/凭据名；**但 chat 面** gateway_debug 的 `attempts`（`handler.go:6302`）经 `AttemptRecord` JSON 序列化携带 `provider_id/credential_id/raw_model/supplier/http_status/latency`（`executor.go:1885-1896`）——内部凭据 ID 直接进客户端响应，且 **messages/responses 两面没有同等披露** ⇒ 三面泄漏面不一致。
- 形态缺口：anthropic **预热流**分支 `writeAnthropicStreamError(w,"overloaded_error",surf.Message)`（messages.go:1150）只写 type+message，**专属码丢失**（chat 预热 `writePrewarmedStreamErrorFull` 与 responses 预热 `writeResponsesStreamError` 均带 code）。

### 4. DB 上卷（d8b3b04da）

- 写哪张表：`credentials`（`circuit_state`/`circuit_opened_at`/`cooling_until`，`state_sync.go:154-169`；schema 列名 `cooling_until`，`sql/objects/tables/credentials.sql:30`）。**非事务**——单条 UPDATE，无与内存态的原子边界。
- 一致性顺序：**内存先开闸、DB 后写**。状态迁移在 Breaker 内同步完成，`SetObserver` → 128 容量异步队列 → `syncOne` 落库（`cmd/gateway/main.go:2497-2515`）；队列满即丢（`Observe` default 分支 + `dropped` 计数，state_sync.go:59-73）；关停有界排水 30s（:101-119）。**DB 列被仓内定位为「异步可丢的近似信号」**（`internal/routeguard/routeguard.go` 头注），候选 SQL 只投影不过滤（`provider/client.go:1910`），路由不消费 ⇒ 一致性弱化是有意取舍。
- 上卷逻辑：`syncOne` 在 `manager != nil` 时写 `GetCredentialState` 最差态（Quarantined>Open>HalfOpen>Closed，State int 枚举 breaker.go:32-35）；manager 为 nil 时退回单事件态（向后兼容）。**缺陷见 D-1：`sc.To == StateHalfOpen` 分支无视上卷结果直接写 'half_open'**（state_sync.go:140-152 在算完 dbState 之后的分支里硬编码 `'half_open'`）——模型 A Open、模型 B half-open 时 DB 镜像被降级为 half_open，违反「上卷=最差状态」的自述。
- 崩溃窗口：内存开闸但 DB 没写上 ⇒ 重启后内存 breaker 全部回 CLOSED（**无 DB→内存播种**，breaker.go/main.go 均无 rebuild 逻辑）＝重启即 fail-open；DB 侧残留的 open 行由 `bg/credential_recovery.go:758-776` 的 `circuit_close` 任务在 cooling_until 过期（或为 NULL）后收口为 closed——自愈方向恒向 closed，不会把陈旧 open 变成路由输入（routeguard 守卫）。

### 5. fail-open（breaker_feature_switch_failopen_test.go）

- 钉住语义：`circuit_degradation` 开关关（`KILL_CIRCUIT_DEGRADATION=1`）⇒ **整段跳过 breaker（连 Allow 都不调）⇒ 请求不被熔断拒绝**；开关开 ⇒ 正常拒绝。判定条件是**双重条件** `circuitOpen && IsEnabled(...)`（executor_dispatch.go:971/978），镜像函数 `circuitAllows`（failopen_test.go:64-71）逐字复刻并用负控证明非恒真。
- 形态：父进程拉子进程跑同一测试二进制（settings 开关进程内 sync.Once 只读一次，只能子进程拿「关」态）——这是对量具约束的正确处理。
- 生产式已 P0-a 改 `AllowPerModel(...三参)`，镜像注释仍写 `e.Circuit.Allow(cand.ProviderID, cand.EmployeeID)` 两参旧形（failopen_test.go:44-47）——语义未漂移，注释漂移。

### 6. 测试作用域回归（e13a43aa4）

- 修的三处：`executor_circuit_mirror_test.go`（`mirrorScopeModel="grok-4.6"` 统一造熔断与 PeekAllowed）、`executor_dispatch_preflight_logging_test.go`（"glm-5.2"）、`context_budget_pipeline_test.go`（"budget-model" + nil-safe 断言：本地拒绝不凭空创建 breaker）——均与生产准入的 `circuitScopeModel` 同源。HEAD 实跑绿。
- 残留排查：`breaker_transition_coverage_test.go`、`executor_circuit_open_test.go:274-284` 等仍传 `""`——这些是 **breaker 自身语义的包内单测**（钉 `_legacy_` 退化映射），且后者用零值 Candidate（`BindingRawModel()==""`）与生产构造函数一致 ⇒ **不是**「测试自建键空间而生产用另一套」的残留。真门禁是反射+AST 两道（见 §二.1），有人加回无 model 方法或调用点换源都会红。

### 7. 验收脚本（verify-per-model-breaker.sh + deploy/W1-per-model-deployment-checklist.md）

- 脚本意图：本地起 18090 网关，造 gpt-4 三连失败 → 断言 circuit_state=open、gpt-3.5-turbo 不连坐、查日志 per-model 键、查上卷态。**但四处硬伤使其不可运行**：
  1. `PORT=18090` 启动（脚本 :102）——网关读的是 `LLM_GATEWAY_LISTEN`（`config/config.go:591`，默认 `:8781`）⇒ 实际起在 8781，脚本探测 18090 永远失败，且会撞真网关端口；
  2. 等待探活 `GET /health`（:112）——路由只有 `/healthz` `/readyz` `/version`（cmd/gateway/main.go:7037-7044），`/health` 不存在 ⇒ 30s 后必然报「启动失败」；
  3. 场景3与清理 SQL 用 **`circuit_cooling_until`** 列（:198、cleanup 段）——schema 列名是 `cooling_until` ⇒ SQL 报错；
  4. 造流量用 `Authorization: Bearer test-key`，但只 seed 了 `credentials` 表、**没有 seed `api_keys`** ⇒ 网关鉴权 401，请求根本进不了 dispatch ⇒ breaker 永不跳闸，场景1断言不可能通过。
- 清单（15c54de91）：阶段1「✅ 单测 13/13 / 推送 commit 7e2236089」，**阶段2–5 全部 ⏳ 未勾**——即 per-model 改造的**部署后活体验证从未完成**；且清单的验收判据已被自己的 P0-a 推翻：它要求日志键格式 `providerID/model`（如 `key=1/gpt-4`，清单 :45-46/:176-181/:282），HEAD 实际是 `providerID/credentialID/model`（breaker.go:222）⇒ 照单验收会得到假红。★ 自述与实现不符：清单自称「准备就绪」，键格式判据与最终实现矛盾，脚本不可执行说明它从未被真跑过（跑过必然立刻撞 /health 404）。

### 8. 并发安全与键增长

- Manager：`map[string]*Breaker` + 单 `sync.RWMutex`；`GetOrCreatePerModel` 双检锁（RLock 快路径 → Lock 慢路径二次检查，breaker.go:1022-1043）。Breaker 内部：state/failCount/consecutive/halfOpenProbes/recoveredAt/freeTier 全 atomic，时间字段归 `b.mu`（:229-261）。无分片锁——竞争点只在 GetOrCreate 的 map 写，量级（候选×凭据）下可接受。
- 键增长：**无淘汰、无容量上限**（全文件仅 :1126 一处 len 引用，无 evict/prune）。实际约束：model 分量来自候选查询（model_offers 清单驱动）⇒ 键上限 ≈ Σ凭据×offer 数（千级，对象小）；但被删 offer/下线凭据的 breaker 条目**驻留到重启**，`Stats()` 全量返回会缓慢膨胀。探针卡死风险已由 5min `halfOpenProbeTimeout` 回收兜底（P0-a 修的「释放走 legacy 键导致永久 HALF_OPEN」即此族）。

## 三、缺陷清单

- **D-1（P2）syncOne 的 half-open 分支绕过 DB 上卷**：`domains/credential/state_sync.go:140-152`——`sc.To==StateHalfOpen` 时硬编码写 `circuit_state='half_open'`，无视刚算出的最差态 dbState。模型 A 处 OPEN、模型 B 转 half-open 时，credentials.circuit_state 被从 open 降级为 half_open，违背 d8b3b04da 自述「上卷态反映最差状态」；bg circuit_close 只收口 'open' 行 ⇒ 错误态滞留到下一次迁移事件。仅影响诊断/巡检面（路由不消费该列），故 P2 非 P1。修法：该分支同样按上卷结果写（half-open 不是最差态时不写/写最差态）。
- **D-2（P2）验收资产整体不可用（自述与实现不符）**：`scripts/verify-per-model-breaker.sh` 四处硬伤（PORT env 不生效、/health 路由不存在、`circuit_cooling_until` 列名错误、未 seed api_keys ⇒ 401 进不了熔断）⇒ 脚本从未可能跑通过；`deploy/W1-per-model-deployment-checklist.md` 键格式判据（`providerID/model`）被同车 P0-a（`4a0bc1ac5`）推翻后未更新，阶段2–5 活体验证全部未执行。per-model 隔离的线上行为证据实际只有单测（含 scope 守卫），无活体。
- **D-3（P3）chat 面 gateway_debug 泄漏 attempts 内部 ID**：`handler.go:6302` 把 `AttemptRecord`（provider_id/credential_id/raw_model/supplier）无条件塞进客户端 503 响应；messages/responses 两面无此披露 ⇒ 三面契约不一致。至少应将 credential_id 摘除或面间对齐。
- **D-4（P3）anthropic 预热 SSE 丢失专属码**：`messages.go:1150` `writeAnthropicStreamError` 只带 type+message，`provider_circuit_open` 码在该形态不可判别（其余五种形态均带码）。
- **D-5（P3）per-model map 无淘汰无上限**：`breaker.go` Manager——下线 offer/凭据的 breaker 驻留到重启；建议加代际清理或 Stats 披露条目数。
- **D-6（P3）注释级漂移三处**：`provider_circuit_open.go:10` 自述准入粒度仍是「per-(provider, credential)」；`breaker_feature_switch_failopen_test.go:44-47` 镜像注释仍是两参 `e.Circuit.Allow`；`executor_circuit_open.go` 头注引用 `executor_dispatch.go`旧行号/旧式。语义均已改，注释未跟——按本仓「自述也是交付物」口径应随改。

## 四、遗留登记建议

1. **L-D1**（P2）：state_sync half-open 分支上卷绕过——归 state_sync 维护者，随下次 breaker 轮一并修（附 D-1 的 SQL 行为对拍建议：隔离 PG 造 A-Open+B-half-open 断言列值）。
2. **L-D2**（P2）：W1 活体验证缺位——要么修脚本四处硬伤后在测试机真跑一轮（同时把清单键格式判据更新为三元组），要么在清单显式降级声明「活体验证未执行，行为证据=单测+门禁」，删除「准备就绪」表述。
3. **L-D3/D-4**（P3）：三协议面熔断耗尽响应的披露面统一（chat 面去内部 ID 或三面对齐；anthropic SSE 补 code 注入）——归 streaming 契约面。
4. **L-D5**（P3）：breaker map 淘汰策略——低优先，先加 `Stats()` 条目数观测再决定。
5. **正面登记**：P0-a 的「删旧 API+反射门禁+AST 门禁」三层结构（`4a0bc1ac5`/`e13a43aa4`）是键空间一致性的可执行保障，实测绿；「模型名为空 ⇒ `_legacy_` 退化」保证了准入/记账/释放三处空值同键，是防分裂的正确设计；routeguard 守卫使 DB 镜像的陈旧值不进路由合取，是崩溃窗口与异步丢事件安全性的前提，三者共同构成当前终态的可信基线。
