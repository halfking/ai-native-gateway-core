# 14 · URSM Redis delimiter-safe key 兼容迁移冻结决策

> **状态**：T0 设计冻结；实施、G1、G4 和发布均未开始（2026-08-18）
> **事实基线**：`df6c6546310ec07ca5abb636cf5d8759bf2c3502`（`main` 与 `origin/main` 一致）
> **裁决**：`M5-0/T0 = BLOCKED / NO-GO`。本文件只冻结兼容设计，不改变生产 key 格式，不构成 Redis、PostgreSQL、provider 或发布证据。
> **唯一执行 owner**：`Migration owner`。在 owner 名称、migration ledger ID 和目标 Redis 拓扑被记录前，禁止实现或切换。

本决策补充 [13-T0契约冻结与所有权](./13-T0契约冻结与所有权.md) 的 delimiter-safe blocker，并约束后续 `NodeKeyForTenant`、`WindowKeyForTenant`、`CandidateIndexKey`、persist/recovery、Lua、coverage 与 cleanup 的唯一可接受迁移路径。

## 1. 问题与不可变约束

当前 legacy key 以 `:` 拼接原始 tenant、model 或 candidate 维度。`tenant="a" / credential=7 / raw="b:8:c"` 与 `tenant="a:7:b" / credential=8 / raw="c"` 可以生成相同 node key；同类边界歧义存在于 window 和 candidate index。`ParseNodeKey` 的 split/rejoin 无法从歧义历史 key 恢复原始 tuple。

以下约束不可放宽：

1. `ready=false` 优先拒绝；authoritative 请求路径的空 tenant 必须 fail-closed，不得以 legacy non-tenant key 兜底。
2. 普通 dispatch 不承诺重启执行；pending 仅结果回放；durable 仅 lease 过期并经 fencing 后恢复；queue mirror 仅 metadata projection。
3. 既有 legacy key 的精确字节输出是兼容契约。禁止就地改变 `node:`、`win:`、`idx:model:` 的布局，禁止只写新 key，禁止凭字符串猜测歧义 key 的 tenant/model。
4. 任何 schema mode 变更先关闭 ready gate；任一冲突、未解析项、coverage 缺口或真实依赖故障都保持 `NO-GO`。
5. miniredis、sqlmock、不可达地址和 SKIP 仅为 L1/L2 或 failure-path 证据，不能作为 G4 或 release-ready 证据。

## 2. 版本化 canonical key grammar

legacy schema 保持现状。新增 schema 只使用新的显式 marker，建议冻结为 `k2`；marker 名称一旦首次生产写入即不可复用。所有 variable string component 使用 `base64.RawURLEncoding`，不得将原文作为 canonical key segment 写入。

```text
<prefix>node:k2:<b64url-tenant>:<credential-id>:<b64url-raw-model>
<prefix>win:k2:<bucket>:<b64url-tenant>:<credential-id>:<b64url-raw-model>
<prefix>idx:model:k2:<b64url-tenant>:<b64url-canonical>:<b64url-profile>:<b64url-modality>
```

- `credential-id` 是十进制正整数；`bucket` 仅允许已冻结的 `1m`、`5m`、`30m`。
- base64url 是 raw、unpadded 形式；decoder 必须严格校验 segment 数量、编码和 credential ID，禁止宽松解析或未标记 fallback。
- canonical node parser 返回 schema 来源（`legacy` 或 `k2`）以及 tenant、credential ID、raw model。persist、recovery 与 coverage 代码必须显式使用来源信息，而非假定任意 parse 成功都可满足 authoritative coverage。
- canonical grammar 可表示 delimiter、重复 delimiter、前后 delimiter、Unicode 和 numeric tenant。空 tenant 仅允许在 legacy compatibility/shadow 规则下表示，不能通过 canonical authoritative read/write 放行。
- candidate index 当前没有 production routing caller；其 grammar 仍是公开兼容契约。启用 routing 前，必须从 provider/config authority 重建并单独验证 member/score coverage。

## 3. Schema mode 与 metadata

Migration owner 实现并在启动时固定 schema mode：`legacy`、`dual`、`canonical`。mode 不是现有 `URSM_V2_MODE` 的替代；后者仍定义 legacy/URSM v2 的路由权威。key schema mode 只定义同一 URSM v2 Redis state 的 layout 选择。

Redis migration metadata 至少记录：

| 字段 | 说明 |
|---|---|
| `owner` | 被授权的 Migration owner 身份 |
| `ledger_id` | 不可复用的 migration ledger 标识 |
| `mode` | `legacy`、`dual` 或 `canonical` |
| `cutover_epoch` | 与 ready/coverage 状态关联的单调 epoch |
| `started_at` / `updated_at` | 可审计时间戳 |
| `preflight_checksum` | exact inventory 与分类结果的 checksum |
| `rollback_deadline` | 保留 legacy shadow 的截止点 |
| `checkpoint` | preflight、copy、coverage、observe、cleanup 或 rollback |

metadata 的 key 名称、ACL、写入 Lua/CAS 和启动配置属于 Migration owner 实现范围；在设计冻结阶段不得预先写入生产 Redis。

## 4. Preflight、歧义 key 和 ledger

任何 copy 之前先以只读 preflight 扫描 `prefix + "node:*"` 及其关联 window/index，输出带 checksum 的 ledger。每个 source key 只能归入以下一类：

| 分类 | 行为 |
|---|---|
| `migratable` | legacy parser 可唯一恢复 tuple，segment count 严格匹配已知 grammar，且 variable legacy segment 无额外 `:`；记录 source key、canonical target、field checksum、PTTL、generation |
| `canonical_present` | canonical 已存在；比对 logical tuple、generation 与字段 checksum，canonical 优先；任何不一致为 conflict |
| `ambiguous` | 多余 delimiter、未知 tag、非法 credential、parse 失败或不能唯一恢复 tuple；不自动 copy，不得进入 authoritative coverage |
| `excluded_non_authoritative` | 仅有可信、经审计的 operator/PG/config mapping，且明确确认此状态非 authoritative；保留 source 和排除原因 |

`ambiguous` 或 `conflict` 的默认结论是 `NO-GO`。只有 owner 提供可信 identity mapping，并留下 source、映射依据、操作者和复核记录，才可定向迁移；绝不能由 key 字符串启发式推断 tenant/model。ledger 必须能支持中断恢复、重跑和最终 exact-key cleanup。

## 5. 双读、双写与原子边界

### 5.1 legacy

维持现有 exact-byte 构造、读写和恢复行为。此模式是 rollback target；不允许在其名义下出现 canonical fallback。

### 5.2 dual

1. canonical 是首选读源；只对 ledger 标记为 `migratable` 的**相同完整 logical tuple**允许 exact legacy fallback。
2. node、三个 window、dedup 必须作为一个 key set 读写；禁止 node 来自 canonical、window/dedup 来自 legacy 的未定义混配。
3. live 写入同时维护 legacy 与 canonical。copy 和双写必须由 Lua 或同等 CAS/generation-fenced 原子机制执行，禁止客户端 `HGETALL` 后无条件 `HSET` 覆盖 live state。
4. 当前 request Lua 涉及 node、windows 和 dedup。实施前必须验证真实 Redis topology 对该 key set 的原子约束；若目标是 Redis Cluster，必须先冻结并验证 hash-tag strategy，否则不承诺 atomic dual write。
5. 任一双写失败须记录带 request identity 的可重放 work item、schema 状态和错误；不得让两套 state 无声分叉。
6. candidate ZSET 双读将 member 归并，canonical score 优先；双写后由 rebuild/coverage 验证。candidate index 未获单独授权前不得成为 routing authority。

### 5.3 canonical

canonical 是唯一 authoritative read source。legacy 保留为只读/双写 shadow，直至 observation window 和 rollback checkpoint 关闭。coverage manifest 只收录 canonical node keys；persist/recovery 可识别两种 schema，但只有 canonical、完整且无 pending marker 的 coverage 可打开 authoritative ready gate。

## 6. TTL、copy、cleanup 与回滚

### 6.1 TTL 与复制

copy 对每个 source key 读取 `PTTL`：`-2` 不复制；`-1` 仅在 source 确属无 TTL 时保留持久状态；正值以读取后经过时间扣减、且不得大于 source 当时剩余值的毫秒数 `PEXPIRE` 到 target；若扣减后不再为正则跳过并记录。禁止以默认 TTL 或 copy 延迟延长历史状态。node、window、dedup 的 copy 需要 generation/field checksum 与 idempotency marker；live dual-write 使用同一 request event 和现有 TTL policy。

### 6.2 cleanup

legacy key 在 canonical coverage、field checksum、restart/recovery 和 observation 全部验证后，仍至少保留到 ledger `rollback_deadline`。cleanup 是独立、限速、可暂停、可重跑的审计 job，只按 ledger 中 exact source key 和 checksum 删除；严禁广泛 `SCAN ... DEL`。candidate legacy index 无 TTL，必须在 canonical rebuild 验证后走相同流程。

### 6.3 rollback

mismatch、Redis restart/`NOSCRIPT`/reconnect/PubSub 失败、coverage/recovery 异常或运行指标越线时：先 `ready=false`，记录 ledger 失败 checkpoint，切 schema mode 回 `legacy`，保留 legacy/canonical 数据、coverage 和 work item 供复盘。已有 URSM `authoritative -> off` 运行回退仍按 [URSM v2 直接启动操作手册](../../../06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md) 执行；本 key schema 回退不删除任何 Redis 证据，也不自动把 canonical state 反向合并到 legacy。

## 7. 必需测试和门禁

实现必须按 TDD vertical slice：一项外部可观察行为的失败测试，再写最小实现并转绿。至少覆盖：

1. canonical node/window/candidate golden bytes 和 parser round-trip；`:`, 重复/前后 delimiter、numeric tenant、Unicode、非法/空值。
2. 代表性生成 corpus 的 collision property，以及历史 key collision pair 不能在 canonical schema 复现。
3. colon-bearing tenant/model 的 cross-tenant/model isolation：RecordRequest、评分、mirror、dedup 和 candidate index 均不能交叉读取或更新。
4. legacy-only、canonical-only、both-present/conflict 的 dual-read precedence；node/window/dedup 同一 key set 一致性。
5. Lua 双写/copy 的 generation fencing、idempotency、PTTL preservation 与中断可恢复。
6. persist/recovery 扫描同时识别 schema；ambiguous legacy key 被拒绝；canonical coverage、pending marker 与 ready gate 均 fail-closed。
7. cleanup 的 exact-ledger eligibility、暂停/恢复、rollback 后不删除证据。

G1 前的本地门槛为 migration/static checks、相关包测试、全仓 `go test -count=1 ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、pre-commit、scoped secret scan 与独立双轴 review。G4 必须在隔离真实 Redis 验证 dual-write/cutover/rollback、restart、`NOSCRIPT`、连接重建、Pub/Sub、TTL/cleanup 和 coverage warmup；真实 PG lease/fencing 与真实 provider/slow-client/chaos 证据仍是独立必需项。任何 mock、SKIP 或未验证项均不得写成 release-ready。

## 8. 冻结后的实施顺序

```text
T0 design freeze
  -> L1 canonical key primitives/tests
  -> L2 schema-aware store/recovery/tests
  -> L3 preflight/ledger/copy/cleanup machinery
  -> M5-2/G1 schema and migration gate
  -> M5-3/G2 handoff
  -> M5-4/G3 authoritative local closure
  -> M5-1/G3 merged local regression + mode/revert drill
  -> M5-5/G4 real Redis/PG/provider evidence
  -> M5-6/G5 GO/NO-GO and rollback drill
  -> T8/245 only after GO
```

本文件不会解除 M5-0/T0 blocker。只有 Migration owner 以完整 ledger、G1/G3 本地证据、G4 真实依赖证据和 G5 回滚演练完成所有 exit criteria 后，协调方才能重新评估 `NO-GO`。

## 9. 变更记录

- 2026-08-18：首次冻结 delimiter-safe Redis key compatibility 设计；明确版本化 namespace、base64url grammar、唯一 migration owner、preflight 分类、dual/canonical mode、TTL/cleanup/rollback 与 G1/G4 门禁。
