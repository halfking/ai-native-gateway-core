# 无 Redis 启动与依赖崩溃时继续服务（2026-09-04）

> 第三批（同日）：冷启动快照、Redis 丢数据自愈、router 集成测试 —— 见文末
> 「第三批补充」。

## 背景

生产要求网关在以下两种情况下仍能正常服务，而不是退出进程或对所有请求返回 5xx：

1. 部署环境没有 Redis（`LLM_GATEWAY_REDIS_ADDR` 为空或 Redis 不可达）。
2. 运行中 PostgreSQL 崩溃（或 Redis 运行中崩溃）。

排查结论：大量降级路径早已存在（内存限流、fail-open 缓存、dbdegradation 监控等），
但存在四个硬缺口：

- URSM v2 默认 `authoritative`，启动时无 Redis/无 DB 直接 `return` 退出进程
  （`cmd/gateway/main.go` 的 mode 依赖检查）。
- 默认部署下 Redis 运行中崩溃 = 所有路由决策 fail-closed → 全量 503。
  authoritative 模式不构造 legacy credentialstate，router 明确拒绝替换。
- DB 启动时不可达 → `dbConn=nil` 永久禁用，即使 DB 随后恢复也无法自愈；
  Redis 启动 ping 失败同理。
- 热路径硬依赖：API key 验证缓存 60s 过期后 DB 错误即 503；候选路由缓存
  30s+30s 宽限后过期即失败；凭证明文缓存 5 分钟过期后 reveal 失败；
  `/v1/models` 直接查库。

## 修改

### 1. URSM v2 启动降级（`cmd/gateway/main.go`）

依赖缺失（无 Redis / 无 DB / 缺 systemmonitor env / authoritative bootstrap
或 coverage 校验失败）不再退出进程，改为降级 `URSM_V2_MODE=off`（legacy 路由，
等价于既定的 rollback 路径）并以 `slog.Error` 明确标注降级原因。

逃生开关：`URSM_V2_STRICT_DEPS=true` 恢复 2026-09 之前的 fail-fast 行为。
`Validate()`（配置拼写错误）仍然 fail-fast。

### 2. Redis 运行中崩溃：outage mirror 降级（URSM v2）

新增可用性档位 `FilterAndScoreOutageFallback`（`domains/ursm/v2/manager.go`）：
authoritative 读路径因 Redis 不可达而拒绝时，router 改为从进程内 NodeMirror
以只读方式提供路由决策（部分命中：镜像未覆盖的候选被剔除）。

- 窗口：`URSM_V2_OUTAGE_GRACE_SECONDS`（默认 1800s，上限 24h，`0` 关闭恢复
  fail-closed）。
- 安全边界：方法内部先 PING 确认 Redis 确实不可达；Redis 可达（例如
  recovery gate 被人为关闭）时一律拒绝，**该档位不可能绕过 ready gate**。
- 观测：新 `routing_state_source=outage_mirror` 标签；router 在档位
  engage/disengage 时各打一条 Warn/Info。
- 写路径不变：outage 期间 RecordRequest 仍是 best-effort（仅告警日志）。

### 3. 启动有界重试（`cmd/gateway/main_helpers.go`）

- `LLM_GATEWAY_DB_BOOT_RETRY_SECONDS`（默认 20s，0=单次）：DB 配置了但不可达
  时在预算内重试 `db.Open`，避免进程终身无 DB。预算在尝试之间检查，保持
  systemd TimeoutStartSec=90s 内。
- `LLM_GATEWAY_REDIS_BOOT_RETRY_SECONDS`（默认 30s，0=单次）：Redis 启动
  ping 有界重试，覆盖容器启动顺序竞态。

### 4. 热路径 DB 故障兜底

- **API key 验证**（`domains/authentication/verifier.go` + `keystore_sync.go`）：
  - **全量内存副本（2026-09-04 第二批）**：启动时全量加载有效 api_keys 到内存
    （按 key_hash 键控，不保留原始 key），此后每 5 分钟增量同步。Verify 优先
    命中副本，零 DB IO；DB 宕机时同步停摆但副本持续授权——**宕机时长不再受
    staleGrace 限制**。
    - 增量机制（api_keys 无 updated_at 列，故不依赖 schema 变更）：
      (a) 以 `last_used_at` 为水位拉取最近活跃 key 的完整行（限流/预算/层级
      变更随下一个周期传播）；(b) 独立查询当前失效 key（revoked/disabled/
      过期）并从副本移除（撤销传播 ≤ 同步周期）；(c) 每小时全量对账，兜底
      硬删除。读取时校验 `expires_at`：key 自然过期无需同步即刻失效。
    - store miss 走原有懒加载路径并回写副本（新建 key 无需等同步）。
    - `InvalidateKeyID` 同步清副本：同进程管理端撤销保持即时。
    - `last_used_at` 写入按 key 60s 节流（与原行为同量级），兼作增量水位。
    - 开关：`LLM_GATEWAY_KEYSTORE_SYNC_INTERVAL`（默认 5m，`0` 关闭回到纯
      懒加载）。
  - 原有 DB 基础设施错误 stale 兜底保留（`LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS`
    默认 600s，0 关闭），覆盖副本未加载/未覆盖的 key。`InvalidKeyError`（真
    无效 key）永不兜底。
- **候选路由缓存**（`provider/client.go`）：DB 可重试错误下的 stale 窗口从
  30s 扩展到 `candidateOutageGrace`（1h）。新诊断事件 `db_outage_stale`。
  `db_empty_fallback`（DB 可达但结果为空）保持短窗口不变。
- **凭证明文 reveal**（`provider/client.go`）：过期正向缓存在
  `revealOutageGrace`（1h）内可继续服务；可重试 DB 错误不再写入 negative
  cache（避免 DB 恢复后 1 分钟内仍拒绝 reveal）。
- **`/v1/models`**（`domains/streaming/models.go`）：last-good 列表在
  `modelsStaleGrace`（1h）内继续服务；从未成功查询过的进程仍返回 503。

## 已知边界（有意为之）

- ~~认证兜底只覆盖本进程缓存过的 key；DB 长时间宕机 + 进程重启 = 冷缓存
  503~~（已由第二批 keystore + 第三批快照关闭：任意时长 DB 宕机 + 重启均
  可认证，唯一前提是启动时存在 ≤7 天的本地快照或 DB 可达其一。）
- keystore 撤销传播延迟 ≤ 同步周期（默认 5 分钟，跨进程）；同进程管理端
  撤销仍即时（InvalidateKeyID 同步清副本）。副本/快照条目在 DB 宕机期间
  冻结。
- ~~Redis 丢数据型重启 = authoritative gate 永久关闭~~（已由第三批自愈
  关闭：monitor 周期重试 + adapter 空清单自动重建。）
- outage mirror 冻结在宕机瞬间的路由状态；节点冷却/恢复在窗口内不生效。
- dispatch `redis_enforce` governor 仍为设计上的 fail-closed（默认 `local`）。
- `/readyz` 语义不变：DB/Redis 任一不通仍返回 503（供 LB 摘流），
  `/healthz` 保持 200。

## 测试

- `domains/ursm/v2/manager_outage_test.go`：outage fallback 服务/拒绝/禁用/
  冷镜像/部分命中 + env 解析。
- `domains/authentication/verifier_stale_test.go`：stale 兜底、InvalidKey 不
  兜底、宽限边界、0 关闭、冷缓存仍失败。
- `domains/authentication/keystore_sync_test.go`：全量加载后 Verify 零 DB IO、
  撤销经失效集传播、水位增量刷新行、读取时过期校验、硬删除对账、懒加载
  回写副本、InvalidateKeyID 清副本、未加载不答。
- `provider/client_outage_test.go`：候选 outage 窗口矩阵、reveal stale、
  negative cache 不被 DB 故障污染。
- `domains/streaming/models_stale_test.go`：last-good 服务、nil pool 无缓存
  仍 503、宽限过期。
- 受影响包（ursm/v2 全树、executors、authentication、provider、streaming、
  cmd/gateway、dispatch、systemmonitor）测试全部通过；authentication 包
  通过 `-race` 检测。

## 第三批补充（2026-09-04 同日）

### 5. keystore 本地快照（冷启动 + DB 不可用）

关闭第二批声明的最后一个边界（"重启时 DB 也不可用则无解"）：

- 每次成功的全量加载与增量同步后，将 store 持久化到
  `LLM_GATEWAY_KEYSTORE_SNAPSHOT_DIR`（默认 `./data/keystore`，
  `off/disable/none` 关闭）下的 `api_keys_snapshot.json`（原子写，0600）。
  文件内容为 key_hash（服务端密钥 HMAC，不可逆）+ 元数据，**不含任何密钥
  材料**，信任级别等同 api_keys 行减去密文列。
- 启动时 DB 不可达：`SetSecretKey` + `LoadSnapshot` 进入 secret-only 模式，
  `Enabled()` 语义扩展为"有 DB 池 **或** store 已加载"→ 认证继续工作。
  快照最长 7 天（`maxSnapshotAge`），超龄拒绝加载。
- DB 在但启动全量加载失败：同步循环先落快照兜底，并以
  `keyStoreFromSnapshot` 标记未可信状态——循环每 tick 重试**全量**加载
  （不做增量），DB 恢复后自动收敛并清除标记。
- 撤销时效代价：快照在 DB 宕机期间冻结；DB 恢复后 ≤5 分钟经失效集同步
  收敛。DB 宕机期间管理端本就无法撤销 key（admin 依赖 DB），实际暴露面
  仅"最后一次快照写入到 DB 崩溃之间"的撤销（≤5 分钟窗口）。

### 6. Redis 丢数据型重启自愈（authoritative）

此前：Redis 无持久化重启 → 命名空间清空 → monitor 在 fallback→healthy
切换时**仅做一次**恢复尝试 → `WarmupFromCoverage` 因空清单拒绝 → gate
永久关闭 → 全量 503，只能重启进程。修复三层：

- `recovery.ErrCoverageManifestEmpty` sentinel（`recovery/manager.go`），
  `ValidateCoverage` 空清单错误改为 `%w` 包装，供 `errors.Is` 判别。
- `v2RecoveryGateAdapter`（`cmd/gateway/recovery_gate_adapter.go`）在
  authoritative 模式接入 `withRebuild`（PG pool + Redis + 前缀/冷却/schema
  参数）：restore 因空清单拒绝时自动 `bootstrap.Apply`（对线上命名空间
  安全：跳过 manual_hold 与 generation>1 节点、原子换清单、不触碰 gate）
  并重试 coverage 校验；重建尝试 5 分钟限速，失败错误与原始拒绝可区分。
- `systemmonitor` 健康分支每 4 个 tick（约 1 分钟）重试一次
  `RestoreIfClosed`（open 状态下自守卫为一次 GET），替代原先"仅
  fallback→healthy 切换时的一次尝试"。

### 7. router outage fallback 集成测试

`domains/streaming/executors/router_outage_test.go`：真实 `*ursmv2.Manager`
+ miniredis 驱动完整 `planCandidates` 路径——Redis 挂掉后 outage gear 继续出
候选并记 `outage_mirror`；gate 被人为关闭且 Redis 可达时保持 fail-closed
并记 `fallback`（安全边界不可绕过）。

### 第三批测试

- `domains/authentication/keystore_snapshot_test.go`：冷启动往返（快照→
  secret-only→Verify 零 DB）、不含明文 key、超龄拒绝、缺失目录非致命、
  空目录 no-op、DB 挂启动走快照。
- `cmd/gateway/recovery_gate_adapter_test.go`（追加）：空清单无重建依赖时
  sentinel 透传；有重建依赖时重建确实触发（对不可达 PG 失败可区分）且
  立即重试被限速。
- 全部受影响包（authentication 含 `-race`、executors、cmd/gateway、
  systemmonitor、ursm/v2 全树）回归通过。

## 第四批：审计修复（2026-09-05）

对上述三批实现的独立审计发现并修复以下问题（回归测试同步补充）：

- **认证 nil-DB panic（P0）**：snapshot-only 模式下 `Enabled()==true` 但
  `dbPool==nil`，`LookupKeyMeta`/`VerifyByID`/`CheckBudget` 直接解引用
  nil 接口会 panic。现在 `SetDB(nil,…)` 不再误置 enabled；无 DB 时
  `LookupKeyMeta` 优先从内存副本取元数据（miss 时返回受控错误）、
  `VerifyByID` 受控 fail-closed（durable runner 可重试）、`CheckBudget`
  明确 no-op（副本无消费流水可查，与既有 view-missing fail-open 语义一致）。
- **撤销/过期 key 经旧缓存复活**：`getCache`/`getStaleCache`/store 读取
  统一校验 `expires_at` 与 revoked/disabled 状态；DB 查询返回 ErrNoRows
  时同步清除 raw-key 缓存与 store 副本——DB 已证实无效的 key 不再因
  随后的基础设施故障被 stale 兜底重新授权。
- **快照完整性加固**：`LoadSnapshot` 拒绝未来 `saved_at`（时钟回拨绕过
  7 天上限）、非法 key_hash（非 64 位小写 hex）、无 ID / revoked /
  disabled / 已过期条目；全部条目无效时拒绝加载。
- **outage mirror 误判收紧**：PING 失败不再一律视为 Redis 宕机——
  context 取消/超时、认证/ACL/协议类错误保持 fail-closed；仅明确的
  传输不可达（connection refused/reset、网络不可达、EOF、内部
  ErrRedisUnavailable）才允许降级。同时补齐空 tenant 校验与
  LRU 禁用（`URSM_V2_LRU_SIZE=0`）下的 nil-mirror 防护。
- **coverage 恢复竞态**：`WarmupFromCoverage` 复用 `open_if_epoch`
  条件开门——旧的恢复流程无法再覆盖恢复期间发生的新故障（新 epoch）
  而误开 gate。coverage manifest 非空但节点缺失/不完整时返回可识别的
  `ErrCoverageIncomplete`，adapter 同样触发 PG 重建（此前只有完全空清单
  才会重建，部分丢数据型重启会永久卡死）。
- **rebuild 并发限速**：adapter 的 5 分钟限速检查改为互斥串行，
  transition 与周期重试两条路径并发触达时不会同时执行 bootstrap.Apply。
- **MarkClosedDebounced 失败回滚**：`EnterRecovery` 失败时删除已获取的
  debounce claim，避免后续最长一个 TTL 内的关门重试被静默抑制。
- **`/v1/models` 截断污染**：行扫描错误与 `rows.Err()` 不再把部分结果
  写入 last-good——保留上一份完整列表（或无缓存时 503），不再出现
  DB 断连中截断列表覆盖完整目录并被 stale 窗口放大的情况。
- **启动重试预算硬界**：`openDBWithBootRetry` 的每次 `db.Open` 尝试
  继承剩余预算的 context，迁移（最长 3 分钟）不再突破
  `LLM_GATEWAY_DB_BOOT_RETRY_SECONDS` 总预算与 systemd 启动窗口；
  `URSM_V2_OUTAGE_GRACE_SECONDS` 非法值现在通过 `Validate()`
  fail-fast（与配置拼写错误语义一致）。

### 第四批测试

- snapshot-only 下 `LookupKeyMeta`/`CheckBudget`/`VerifyByID` 不 panic、
  语义符合上述矩阵；`SetDB(nil,…)` 保持 disabled。
- 过期 `KeyInfo` 不经 legacy cache 复活；快照拒绝未来时间戳与非法条目。
- outage gear 在零 tenant/cold mirror 部分命中等既有用例保持通过；
  malformed outage grace 配置 fail-fast。
- 受影响包（authentication、streaming、ursm/v2 全树、cmd/gateway、
  provider、bg/systemmonitor）`-race` 回归通过。
