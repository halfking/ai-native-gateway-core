# D08 供应商错误链与凭据服务质量 子代理报告（窗口：b9d9a8ba5..HEAD）

窗口内核实：`git log b9d9a8ba5..HEAD` 仅 2 个 commit，改动面为 goal 控制指令（cmd/gateway/goal_control.go、domains/hooks/goal/、domains/streaming/handler.go）与 playbook/docs，**无本域直接改动**，与派发说明一致。本报告主体为 R33 遗留 P2「credential_state_log 零读者」的窗口外追溯决策评估（§四）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（决策项） | credential_state_log 全仓**零应用读者**：唯一写者 batch_writer（batch_writer.go:157-203），Go/SQL/前端均无 SELECT；唯一读者是 ops 脚本 `scripts/monitor-245-incident-pending.sh:173,185`（M2a/M2b 监控）。bba08b922 补的 Available/RecoverAt 持久化目前只喂了这张只写快照表 | batch_writer.go:157; scripts/monitor-245-incident-pending.sh:173 | 采方案 A（见 §四），或降级登记为"观测快照表"并修正 R33 措辞"零读者"→"零应用读者" |
| 2 | P2（窗口外追溯） | **RecoverAt 全链零读者/零过期语义**：`State.RecoverAt` 只被写入（manager.go:401,448）与序列化，`IsAvailable`（manager.go:761）只看 `state.Available`，从不比对 RecoverAt 是否已过——任何来源灌入的 Available=false 都不会按时自动翻回，恢复完全依赖 UpdateFromProbe/下一次成功请求。而 L3 的 node_probe_state 水合却有过期门（cache.go:133 `nextRetryAt.After(time.Now())`），两个来源过期语义不一致 | manager.go:753-765; cache.go:133 | 若采方案 A，读表必须自带 `recover_at > now()` 门；此项独立登记（属 legacy 包既有债） |
| 3 | P3 | **探测恢复不落该表**：UpdateFromProbe（probe 权威恢复路径）只写 mem+Redis，无 batchWriter.Add——表行可滞后于探测恢复，方案 A 直接读会把"已探测恢复"的凭据重新灌成不可用 | manager.go:524-611（无 Add 调用） | 方案 A 加 updated_at 新鲜度守卫（见 §四 Q2） |
| 4 | P3 | batch UPSERT 的 `updated_at = EXCLUDED.updated_at` 无条件覆盖（batch_writer.go:199），多实例乱序 flush 可让旧行时间戳回退——仅当方案 A 引入时间戳守卫时才成为实际风险 | batch_writer.go:190-200 | 随方案 A 一并处理（`GREATEST(old, new)`） |
| 5 | P3 | credential_state_log 无 TTL/清理任务（supplier_errors 有 90d 基准，此表无）；行数受活跃 (credential,model) 对数上界约束，风险低 | 全仓无该表 DELETE/cleanup | 顺带清账登记 |

## 二、核实为健康的面

- **「重启后冷却丢失」的主切片已有持久化双道覆盖（R33 措辞偏重）**：
  - 瞬时失败（429/5xx/timeout/network/upstream_down/upstream_overloaded/concurrent）**每次 dispatch 失败**即写 node_probe_state（last_direct_ok=FALSE, next_retry_at=now()+5min，free 档 60s）且**不受 legacyWritersEnabled 门限**（executor_dispatch.go:957-962 → executor.go:2758-2790）；恢复链 L3 优先读它并带 now() 过期门（cache.go:115-137）。
  - model_not_found 自愈排除 5min 亦持久化（executor.go:2648-2694）。
  - auth/auth_revoked/quota_permanent 凭据级 15min 冷却由 domains/credential 状态机落 credentials 表（executor_nodehealth.go:193-205 → credentials.availability_state/recover_at，仪表盘读它：admin/routing.go:403-404）。
  - UpdateOnFailure 会触发冷却的全部错误种类（manager.go:391-395, 416）均被上述三道之一持久化。
- **Redis 层本身跨重启**：llmgw:credstate:* 键 TTL 5min（cache.go:96），外部 Redis 存活时恰覆盖 5min transient 冷却窗口。
- **authoritative 模式隔离在位**：legacy 写者被 `legacyWritersEnabled()` 关断（executor.go:1564-1566），路由侧有显式零调用 guard（router.go:486-489），符合包注释契约（manager.go:33-34）。
- **bba08b922 写侧修复本身正确且有钉桩**：manager_available_persist_test.go:23-178 三测覆盖 paid transient（Available=false+RecoverAt≈+5min）、free 不冷却、permanent +15min。

## 三、未覆盖项与原因

- 各生产库 credential_state_log / node_probe_state 实际行数、Redis 拓扑是否跨重启存活——需真机/DB 访问。
- URSM_V2_MODE=authoritative 在各环境放量占比——决定 legacy 路径权重，需部署配置事实。
- R33「症状未缓解」是否指 Redis 同时丢失的场景，无法从代码侧复核，需事件时间线。

## 四、R33 遗留 P2 决策评估（核心任务）

### Q1 表结构是否适合作为恢复源

**适合，且比 R33 描述的更简单**：`sql/migrations/startup/324_credential_state_log.sql:39-51` 定义 `PRIMARY KEY (credential_id, raw_model_name)`——尽管名为 "log"，它是**按对 upsert 的最新态快照表**（表注释自述 "UPSERT by (credential_id, raw_model_name)"），不是 append-only 日志。恢复读取就是**单行 PK 点查，无需 ORDER BY 取最新**（updated_at DESC 索引只服务 ops 扫描）。缺口：无 consecutive_fails 列（StateUpdate 无此字段，state.go:31-42）——恢复后分层退避（manager.go:476-484）与 permanent 阈值（:397）计数清零；无 source 列区分 request/probe 写入。

### Q2 方案 A 最小改动点

- **插入点**：`domains/credentialstate/cache.go:146`——getFromDB 在 node_probe_state 查询 ErrNoRows/42P01 之后、回退 getLegacyStateFromDB 之前，插入 `getFromCredentialStateLog(ctx, credID, model)`（新增 helper，单行 `SELECT available, health_status, latency_ms, last_success_at, last_failure_at, last_error, recover_at, updated_at FROM credential_state_log WHERE credential_id=$1 AND raw_model_name=$2`）。
- **灌回结构**：构造 `credentialstate.State{..., Source:"credential_state_log"}` 返回；GetState（manager.go:744-747）已有 setToMemCache + go setToRedis 会自动完成回灌，无需再改。
- **必带的两道门**（否则引入新缺陷）：
  1. `available=false` 仅在 `recover_at IS NULL OR recover_at > now()` 时生效——因为 IsAvailable 不过期（发现 #2）；
  2. 行新鲜度守卫（如 `updated_at > now() - 30min`）——因为探测恢复不写该表（发现 #3）。
- **多实例风险：不成立为阻断项**。表无 node/instance 维（PK 仅 credential_id+raw_model_name），但该状态本来就是 (credential, model) 对的全局属性——**跨实例传播现状已存在**（所有实例共享同一 Redis 键 llmgw:credstate:*，cache.go:64/86）。"别实例的冷却灌进来"与现行为一致，非新风险；真正的风险是 #3/#4 的陈旧性，由上述两道门兜住。
- 影响范围：仅 legacy/off/canary 模式可达（LegacyStateBackend，state_backend.go:207-209），authoritative 下该读路径不可达，不违反零调用契约；但确实延长 deprecated 包（v3.0 计划 2026-Q4 移除，manager.go:16）的存续——好在包内维护原则明确"回退路径必须可用"，A 属合法 bug 修复而非新功能。

### Q3 方案 B 改写权威表

- **B1 改写 node_probe_state**：batch_writer 改 UPSERT 该表（Available→last_direct_ok、RecoverAt→next_retry_at、LastError→last_err_code）。风险大：node_probe_state 由 bg NodeProbeWorker 的 7 步退避梯状态机独占写（bg/node_probe.go:590/876/891/988/1112，表注释"7-step backoff ladder"）——第二个写者每 5s 批量覆写 next_retry_at/consecutive_failures 会踩烂梯子记账（next_retry_seconds/paused/in_flight_until）；且 UpdateOnSuccess 每次成功写 last_direct_ok=TRUE 会**即时清掉** executor 自己布的 5min transient 抑制窗（executor.go:2758+），语义变更。
- **B2 改写 model_probe_state**：更差——该表是 bg/model_probe 单一 writer（main.go:3509/3999）的 3 连击共识状态机，快照 upsert 与 state 字符串语义不匹配。
- **迁移成本**：B 本体不需新表迁移，但废弃/删除 credential_state_log 需新增 >715 号 startup 迁移 + revision-sequence 登记 + baseline/schema/installer embeddata 三副本同步（conventions §5、§7），并**改写唯一现存读者** scripts/monitor-245-incident-pending.sh M2a/M2b。

### Q4 推荐方案与理由

**推荐方案 A（带两道门的最小读回），否决方案 B，无需任何 DB 迁移。**

理由：
- **成本**：A = 1 个 helper + 1 个调用点 + 1 个钉桩测试，限定在已声明"必须可用"的回退包内；B = 与两个在役状态机（NodeProbeWorker 梯子、probe queue）争单写者权，语义冲突不可局部化。
- **风险**：A 的陈旧性风险被插入点天然缩小——getFromDB 先读 node_probe_state（探测权威），表读只在无探测行的窄缝生效；再加 recover_at/新鲜度双门后残余风险有界。B 的风险是结构性的（双写者互相覆盖）。
- **一致性**：A 不制造第二真相源（node_probe_state 仍优先），B 会让请求成功清探测抑制，破坏 2026-09-14 O2 审计建立的不变式。
- **诚实标注**：本审计核实"重启冷却丢失"的主切片已被 node_probe_state（瞬时，每次失败即布防、不受模式门限）+ credentials 表（permanent 凭据级）+ 外部 Redis（5min TTL）三道覆盖；A 真正补的是 **Redis 丢失/过期 + 该对未进探测管道**的窄缝（Redis 事故场景，仓内已有 2026-09-04 outage 先例与 gear）。若主代理认为该窄缝不值得为 deprecated 包加读路径，替代处置是：保留写路径作为 ops 快照（monitor-245 M2a/M2b 在用），把 R33 项按"发现 #2/#3 登记为 legacy 债、随 v3.0 移除包一并清退"结案——此为次选，但两个方案都不需要迁移。
