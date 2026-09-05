# 15 · URSM delimiter-safe key 迁移实施计划(交 Migration owner)

> **状态**:实施计划文档;不改变 [14 号冻结决策](./14-URSM%20Redis%20delimiter-safe%20key兼容迁移冻结决策.md) 的任何裁决,不解除 `M5-0/T0 = BLOCKED / NO-GO`。
> **事实基线**:`51223fd37a076474c52456bd9e9f1de32845ee8d`(`main` 与 `origin/main` 一致;`domains/ursm/**` 与 `scripts/rollback/**` 自 14 号基线 `df6c65463` 起无代码变动,本文件全部 `path:line` 引用均按该基线核验)。
> **边界重申**:在 Migration owner 名称、migration ledger ID 和目标 Redis 拓扑被记录之前,禁止任何实现或切换(14 号 §0)。本文件是供 owner 使用的可执行计划与代码事实索引,本身不构成 Redis、PostgreSQL、provider 或发布证据。
> **当前实现状态**:全仓无任何 schema mode / `k2` 代码;迁移链路为绿地。

## 1. 可复用的 migration / ledger / checkpoint 模式

| 模式 | 现有实现(path:line) | 复用点 |
|---|---|---|
| checksum ledger + fail-closed | `scripts/run-migrations-strict.sh:77-83`(sha256)、`:114-121`(已应用则比对 checksum,不一致 exit 4)、`:98-103`(记账) | Redis ledger 重跑 = skip applied + checksum 校验;任何 checksum 漂移直接中止 |
| 空 ledger 拒绝重放 / baseline | `scripts/run-migrations-strict.sh:60-75` | Redis ledger 首跑必须显式 bootstrap,禁止对未知历史自动重放 |
| 部署前 history gate(4 项判定) | `scripts/deploy-lib/db-changelog.sh:145-183`:重复版本数、重复行字段不一致数、version 唯一约束、checksum ledger 存在性 | cutover gate 判定骨架;`db-changelog.sh:352-358`"repair 是唯一允许建 ledger 的路径,普通部署 fail-closed"照搬到 Redis metadata |
| gate 回归测试 | `tests/migration_ledger_gate_test.sh`(fake-ssh 4 模式,期望 rc 1/1/1/0) | Redis cutover gate 的 CI 回归模式 |
| 修复必先备份 | `scripts/repair-252-migration-ledger.sh`:默认只读,`--apply` 先 pg_dump、单事务修复、带 `--ledger-from` 边界 | Redis ledger repair 工具形态 |
| dry-run 默认 + `--apply` 才写 | `cmd/migrate-ursm-v2/main.go:46-49` | preflight/copy/cleanup 全部命令的默认姿态 |
| 幂等跳过(manual_hold / generation>1 不覆盖) | `domains/ursm/v2/bootstrap/bootstrap.go:190-237`;批量 pipeline 500/批 `:89-102` | copy 的"live state 不被覆盖"规则与批处理骨架 |
| coverage manifest 事务发布 | `bootstrap.go:240-267`:先写 `:pending` → staging SET → count 校验 → `RENAME`;失败 defer 删 pending | canonical coverage 发布复用;fail-closed 读取在 `domains/ursm/v2/recovery/manager.go:191-195`(pending 存在即拒绝)与 `:213-224`(逐 key exists + generation/available 字段校验、必须 tenant-aware) |
| epoch fencing Lua | `domains/ursm/v2/recovery/transition_recovery.lua:9-27`(close 自增 counter;`open_if_epoch` 比对观察值,不等返回 `superseded` `:16-19`);Go 侧 `recovery/manager.go:144-183` | metadata `cutover_epoch` 的 CAS/单调推进机制 |
| 集群级 debounce | `recovery/manager.go:101-122`(SetNX+TTL,全 fleet 每 window 只 bump 一次) | copy/cleanup job 单实例互斥锁 |
| generation 字段围栏 | node hash `generation` 由 Lua 每次写自增(`store/record_request.lua:210,226,236`、`store/apply_probe.lua:42`);bootstrap 遇 `generation != "1"` 跳过(`bootstrap.go:231`) | copy 的 per-key 版本围栏:Lua 内 `HGET generation` 比对后才 HSET |
| 幂等 marker(SET NX EX) | `record_request.lua:65-69` dedup;`requestDedupKey` = sha256(`store/record_request.go:102-105`) | per-key copy idempotency marker |
| 非破坏性回退 + 持久 env + L1-L4 验证 | `scripts/rollback/ursm_v2_to_legacy.sh:14-16`(不 DEL)、`:102-141`(改真实 EnvironmentFile,禁 transient set-environment)、`:143-189`(L1-L4)、`:191-207`(summary + 保留现场) | key-schema rollback 脚本模板(语义不同,见 §4) |
| 证据工具 | `scripts/verify-ursm-v2-rollout.sh`(只读 /metrics,断言 label 契约、`require_zero`) | observation 阶段机械验收工具,需扩 schema 维度 |
| SCAN 限速清理 | `scripts/clean_redis_leaks.sh`(dry-run 标志 + EVAL) | cleanup job 的 dry-run/限速形态;但必须按 ledger exact-key 删,不得 pattern-DEL |

## 2. Preflight scan 必须采集的 exact fields 与判定

扫描对象:`prefix + "node:*"`、`prefix + "win:*"`、`prefix + "idx:model:*"`、派生 `:request_dedup:` 后缀 key(`store/record_request.go:102-105`)。只读 SCAN(复用 `recovery/manager.go:270-287` 的 cursor 500 循环)。

每个 source key 记录:

- `source_key`(exact bytes,不 normalize)、`key_type`(HASH/ZSET/STRING)、`pttl_ms`(读取时刻)、`generation`(HASH `generation` 字段)、`field_checksum`(HGETALL 全字段按 field-name 排序后的 `name\x00value` 序列 sha256;序列化规则一次冻结,cleanup 比对依赖它);ZSET 另记 `member_count` + `member_checksum`(score+member 排序序列化)。
- `logical_tuple`(tenant、credential-id、raw-model;window 含 bucket;index 含 canonical/profile/modality)、`schema_source`(legacy/k2)、`classification`、`classification_reason`、`scan_batch`、`scan_run_id`。

classification 判定(14 号 §4 的操作化):

- `migratable`:`ParseNodeKey`(`store/keys.go:33-66`)解析成功,segment 数严格匹配已知 grammar,tenant 与 raw-model 两个 variable segment 内部无 `:`。现有 parser 对 tagged numeric tenant 与 untagged string tenant 都做 `:` rejoin(`keys.go:50,54,64`)——"能 parse 但无法证明唯一"的 key 必须判 `ambiguous`。判定必须加一道硬闸:**用解析出的 tuple 重新 `NodeKeyForTenant`(`store/keys.go:15-27`)编码,round-trip exact-byte 相等才算 migratable**;不等即 `ambiguous`(这是 `a:7:b:8:c` 双解释碰撞的机器判定)。
- `canonical_present`:k2 target 已存在 → 比对 logical tuple、generation、field checksum;任一不等 = `conflict`(直接 NO-GO,不属于第四类)。
- `ambiguous`:多余 delimiter、未知 tag(非 `t`)、credential ≤ 0 或非十进制、parse 失败、round-trip 失败、bucket 不在 `{1m,5m,30m}`。
- `excluded_non_authoritative`:仅限有经审计 operator/PG/config mapping 且确认非权威的 key(如 `request_dedup:` 派生 key、`binding:`/`credential:`/`provider:` 非本迁移对象);保留 source 与排除原因。
- 默认规则:`ambiguous>0` 或 `conflict>0` → 整体 `NO-GO`;只有 owner 提供可信 identity mapping(记录 source、映射依据、操作者、复核人)才可定向迁移,禁止字符串启发式。

产出带 `preflight_checksum` 的 ledger(全量分类结果的确定性 checksum,序列化规则同上),写入 metadata。中断恢复 = 重跑时已分类且 checksum 一致的跳过(复用 `run-migrations-strict.sh:116-121` 模式)。

PTTL 采集语义(14 号 §6.1):`-2` 不复制;`-1` 仅当确认 source 无 TTL 才保留 persist;正值记录读取时间戳,copy 时扣减经过时间,`PEXPIRE` 不得超过读取时刻剩余值;扣减后 ≤0 跳过并记录。

## 3. Standalone vs Cluster:owner 必须裁决的事实清单

当前代码事实:

1. 今天只支持 standalone:全仓只有 `redis.NewClient`(`cmd/gateway/main.go:595`、`cmd/migrate-ursm-v2/main.go:66`);`recovery.Manager`、`persist.Writer`、`store.Store` 持有 `*redis.Client`(`recovery/manager.go:22`、`persist/writer.go:16-17`、`store/store.go:32`)。接 Cluster 需 `redis.UniversalClient` 重构。
2. 多 key Lua 只有一处:`record_request.lua` KEYS[1..5] = node + win1m + win5m + win30m + dedup(`record_request.lua:1-31`、调用方 `record_request.go:78-79`)。legacy 布局下这 5 个 key 无共同 hash-tag(`ursm:v2:node:…` vs `ursm:v2:win:1m:…`),Cluster 下必然 CROSSSLOT;standalone 下天然原子。
3. `transition_recovery.lua` 触碰 2 个 meta key(`meta:ready` + `meta:epoch`,不同 slot);其余 Lua(apply_probe/apply_admin/apply_decision/clear_state)均单 key。
4. `bootstrap.go:266` 用 `RENAME` staging→final(Cluster 下要求同 slot);`recovery/manager.go:203-212` 对全量 coverage key pipeline Exists/HMGet(Cluster 下需分 slot 路由)。
5. PubSub 失效通道 `ursm:v2:meta:node_invalidation`(`store/keys.go:144-146`、`domains/ursm/v2/manager.go:875-889`)。

owner 裁决项:

- 目标拓扑声明(standalone / sentinel / cluster)及证据来源;standalone 下 5-key 原子性可直接承诺,cluster 下不行(14 号 §5.2.4)。
- 若 cluster:hash-tag 位置必须在 k2 grammar 冻结时一起定死(marker 首次生产写入后 grammar 不可变)。选项与后果:静态 namespace tag(如 `ursm:v2:{s}:`)→ 全部 URSM key 单 slot,原子但失去分片意义;per-tuple tag(node 与三个 window、dedup 同 tag)→ 每 key-set 原子、分片友好,但 meta/coverage key 与 node key 不同 slot,`ValidateCoverage` 的非原子读需接受或加 tag。tag 内容不能含 `:` 或 base64url 歧义字符。
- Cluster 专属验证(G4):CROSSSLOT 行为、failover 后 `NOSCRIPT`/script 缓存丢失、SCAN 遍历所有 master、RENAME/pipeline 跨 slot。

## 4. 可审计状态机

全局 checkpoint(存 metadata hash,CAS/epoch-fenced 推进,复用 `transition_recovery.lua` 模式):

```text
T0_BLOCKED
  → preflight      只读 SCAN,产出 ledger + preflight_checksum;ambiguous/conflict>0 ⇒ 停在 NO-GO
  → copy           按 ledger 逐 key:Lua 内 generation 比对 + field checksum + idempotency marker
                   + PTTL 扣减 PEXPIRE;限速、可暂停、可重跑(重跑=skip copied&checksum一致)
  → coverage       canonical coverage 发布(pending→staging→count→rename);ValidateCoverage 全绿
  → dual           schema mode=dual:canonical 首读 + migratable exact-tuple legacy fallback;
                   live 双写由单 Lua 完成;双写失败落 replayable work item(带 request identity)
  → observe        fallback 读取率衰减至 0、无 diff、指标在 runbook §3 阈值内、观察窗覆盖 rollback_deadline 逻辑
  → cleanup        独立限速审计 job,仅删 ledger 中 exact source key 且 checksum 相符;
                   前置:coverage + field checksum + restart/recovery + observation 全部验证
  → done
任意阶段失败 → rollback:ready=false 先行 → 记 ledger 失败 checkpoint → schema mode 回 legacy
                   → 保留 legacy/canonical/coverage/work item 全部证据,不反向合并
```

per-key ledger 状态:`discovered → classified → copied(pttl_ms, generation, checksum) → verified → observed → cleaned(deleted_at)`,旁路 `excluded(reason)`、`ambiguous(NO-GO pending mapping)`、`conflict`、`rolled_back(preserved)`。每步幂等、带 `scan_run_id`/checkpoint,断点续跑。

关键边界:

- node + 3 windows + dedup 是一个 key set(窗口计数器 `samples_*/successes_*/sr_*` 存在 node hash 里,`record_request.lua:79-146`),copy/dual-write 不许拆开(14 号 §5.2.2)。
- 禁止客户端 `HGETALL` 后无条件 `HSET` 覆盖 live state(14 号 §5.2.3);copy Lua 必须内嵌 generation/checksum 围栏。
- schema-mode 回滚(k2 → legacy)与现有 URSM 运行回退(`URSM_V2_MODE=authoritative → off`,runbook §4 / `scripts/rollback/ursm_v2_to_legacy.sh`)是两层独立开关;key-schema 回滚不改 `URSM_V2_MODE`、不删任何 Redis 数据、不自动把 canonical 合并回 legacy(14 号 §6.3)。
- candidate ZSET(`idx:model:`,无 TTL,`store/keys.go:110-112`)不从 legacy copy:由 provider/config authority 重建后单独验证 member/score coverage(14 号 §2、§5.2.6);其 cleanup 走同一 exact-key 流程。

## 5. G1 前必须冻结的 metadata 与 runbook 条目

metadata(14 号 §3 表格,写入前不得出现在生产 Redis):`owner`、`ledger_id`(不可复用)、`mode`(legacy/dual/canonical)、`cutover_epoch`、`started_at/updated_at`、`preflight_checksum`、`rollback_deadline`、`checkpoint`。另须一并冻结:metadata key 名称与 ACL、schema-mode 启动配置变量名(与 `URSM_V2_MODE` 正交)、marker `k2` 与 grammar exact bytes、field/member checksum 序列化规则、hash-tag 决策(若 cluster)。

runbook 必须新增(`docs/06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md`):

1. 模式表加 schema mode 维度,写明与 `URSM_V2_MODE` 的两层关系。
2. preflight 操作步骤 + NO-GO 判据(ambiguous/conflict>0、coverage 缺口、真实依赖故障)。
3. copy 的限速/暂停/重跑命令与验收(checksum、PTTL、generation)。
4. dual 观察窗判据:fallback=0、错误率/无候选率不超 24h 基线、P99 增幅 <5%(对齐 runbook §3)、双写失败 work item 清零。
5. exact-key cleanup 的前置条件、执行与暂停(严禁 SCAN…DEL)。
6. key-schema rollback 步骤(区别于 §4 authoritative→off):ready=false → 记 checkpoint → mode 回 legacy → 保留证据 → 复盘。
7. 仿照 §7 的 evidence 记录模板(preflight_checksum、ledger_id、操作者、指标链接)。

配套修正:`scripts/rollback/ursm_v2_to_legacy.sh` 头部注释中"`redis-cli --scan --pattern 'ursm:v2:*' | xargs redis-cli DEL`"的指引与 14 号 cleanup 契约冲突,已随本文件一并修正为 exact-key ledger 流程指引(见该脚本变更)。

## 6. 必须留到 G4(隔离真实 Redis)的操作

以下在 G1-G3 只允许 miniredis/sqlmock/不可达地址的 L1/L2 或 failure-path 证据,不得声称为 ready(14 号 §7):

- 真实 Redis 上的 preflight、copy、dual-write、cutover、rollback 演练;
- restart / `NOSCRIPT` / 连接重建 / PubSub(失效通道)下的行为;
- PTTL 实时扣减精度、cleanup 限速对真实 keyspace 的影响;
- coverage warmup 与 `ValidateCoverage` 在真实数据量下的表现;
- cluster 拓扑下的 CROSSSLOT/hash-tag/failover 验证(若选 cluster);
- 真实 PG lease/fencing 与真实 provider/slow-client/chaos(独立必需项);
- 一切对生产 Redis 的首次 `k2` 写入(marker 一旦写入即不可复用)。

## 7. 变更记录

- 2026-08-18:首次提交实施计划;基于 14 号冻结决策与代码基线 `51223fd37` 整理可复用模式、preflight 判定、拓扑裁决清单、状态机、G1 冻结项与 G4 边界;同步修正 rollback 脚本中与 cleanup 契约冲突的注释指引。
