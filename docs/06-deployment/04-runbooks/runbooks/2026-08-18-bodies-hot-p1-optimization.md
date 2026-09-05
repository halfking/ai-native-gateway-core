# Runbook — request_logs_bodies_hot P1 存储优化（2026-08-18）

> 状态：**待运维授权执行**。本 runbook 与配套脚本已入仓，未对任何环境执行破坏性操作。
> 关联审计：2026-08-18 本地↔252 结构对比 + hot/columnar 分区审计（会话产物）。

## 0. 背景（为什么做）

2026-08-18 审计实测（252，via 隧道 localhost:15432）：

| 对象 | 大小 | live rows | 累计 ins/upd/del |
|---|---|---|---|
| `request_logs_bodies_hot`（表体） | ~339 MB heap + **~35 GB TOAST** | **~1,800** | 22.9w / 18.8w / 31.3w |
| 对比 `request_logs_bodies_2026_08`（columnar 分区） | ~2.5 GB | 归档态 | — |

1,800 个活行对应 35GB TOAST，即膨胀（bloat）几乎全部来自 31 万次删除留下的死 TOAST 元组——
autovacuum 对 TOAST 的回收无法归还空间给 OS。同时 18.8w 次 UPDATE 说明应用对该表做
整行 upsert（正文列被反复重写→新 TOAST 版本）。

**结论：这是写入模式问题在存储层的放大，治标 = 重写回收空间（本 runbook P1），治本 = 字段拆分（§5 草案，需应用侧改动）。**

## 1. 执行前置

- [ ] 凭据已加载：`source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go`
- [ ] SSH 隧道已建：`source configs/env-252.sh; source scripts/lib/252-db-tunnel.sh; db252_tunnel_ensure`（helper 动态解析当前容器地址，只复用健康 listener；结束时执行 `db252_tunnel_teardown`）
- [ ] 先跑只读诊断并留档：
  ```bash
  ./scripts/partition/bodies-hot-diagnose.sh --env=252 | tee /tmp/bodies-hot-before-$(date +%F-%H%M).log
  ```
- [ ] 确认目标时刻在 02:00–04:59（脚本内置守卫，窗口外需 `--force` + 运维确认）
- [ ] 确认磁盘余量 ≥ 当前 DB 大小 10%（swap 期间新旧并存，峰值约 +活行数据量，非 +35GB）

## 2. P1 重写（推荐 swap 模式）

```bash
# 1) 预览（默认 dry-run，打印事务不执行）
./scripts/partition/bodies-hot-repack.sh --env=252 --mode=dry-run

# 2) 窗口内执行 swap
./scripts/partition/bodies-hot-repack.sh --env=252 --mode=swap

# 3) 后置诊断留档
./scripts/partition/bodies-hot-diagnose.sh --env=252 | tee /tmp/bodies-hot-after-$(date +%F-%H%M).log
```

### 模式选择

| 模式 | 锁时长 | 空间峰值 | 回滚 | 适用 |
|---|---|---|---|---|
| `swap`（推荐） | ≈ 活行复制（秒级） | +活行数据 | 备份表换名，秒级 | 本例（活行极少） |
| `vacuum-full` | ≈ 全量重写（35GB 级，分钟级） | +新表全量 | 无（靠 pg_dump） | swap 不可用时 |
| `pg-repack` | 在线 | 约 2× | 工具内建 | 需先装 PG17 匹配版 |

swap 语义说明：
- 单事务内会禁用该维护会话的 statement/idle-in-transaction timeout，随后 `LOCK ... ACCESS EXCLUSIVE` →
  建新表（`LIKE INCLUDING ALL`）→复制活行→行数校验→旧表换名 `*_bloat_backup_<ts>` →
  新表换回原名→恢复源表 reloptions、规范化新旧索引名称、重建已知依赖 VIEW → COMMIT。
- 执行器只允许已登记的 `request_logs_bodies_with_current_month` 依赖 VIEW；发现未知依赖 VIEW/MATVIEW
  会 fail-closed 中止，防止 rename 后视图仍指向 backup 表 OID。
- 行数校验不等即抛错整体回滚。

### 回滚（仅限 swap 后立即发现异常）

```bash
./scripts/partition/bodies-hot-repack-rollback.sh --env=252 \
  --backup=request_logs_bodies_hot_bloat_backup_<ts>
```
⚠️ swap 后新表上已产生的写入会被丢弃——超过数分钟的漂移请人工合并，勿用此脚本。

### 收尾

- 稳定运行 ≥48h 后人工 `DROP TABLE public.request_logs_bodies_hot_bloat_backup_<ts>;`
  （回收 ~35GB，这是唯一释放磁盘的步骤）
- drop 前不必再备份：备份表本身就是膨胀死区，活数据已在新表。

## 3. 索引漂移对齐（独立小项，可先行）

```bash
# 本地补齐 252 已有的 4 个 GIN（columnar 分区上 GIN 受 Citus 限制，失败按 WARN 处理）
./scripts/partition/index-drift-align.sh --direction=to-local --dry-run
./scripts/partition/index-drift-align.sh --direction=to-local

# 252 补齐本地已有的 2 个 handoff_logs_hot btree（heap 小表，秒级）
./scripts/partition/index-drift-align.sh --direction=to-252 --dry-run
./scripts/partition/index-drift-align.sh --direction=to-252
```

漂移明细（2026-08-18 审计）：
- 仅 252 有：`request_logs_2026_07/08_{quality_flags,tool_calls}_idx`（GIN 部分索引）
- 仅本地有：`idx_handoff_logs_hot_{created_at,new_session}`

**2026-08-18 实测补充（本地已执行 to-local 方向）**：
- 本地 `_2026_07/08` 为 columnar 分区，GIN 创建被 Citus 拒绝（unsupported access method，
  预期内）。252 上那 4 个 GIN 应为分区转 columnar 前的历史遗留索引。
- 本地 `_2026_09`（heap）本就存在同名 GIN 且 valid → 新分区由模板正常继承。
- **结论：A 方向（to-local）记为「columnar 能力豁免」，不需要也无法补齐；
  真正待执行的是 B 方向（to-252 的 2 个 handoff btree）。**

## 4. 防复发（立即可做的低风险项）

```sql
-- 表级收紧 autovacuum，让 TOAST 死元组更早被标记可复用（不归还 OS，但抑制继续膨胀）
ALTER TABLE public.request_logs_bodies_hot SET (
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_threshold    = 1000
);
```
> 注：此为 DDL（秒级锁），建议与 P1 同窗口执行；仅改 reloptions，幂等可重复。

## 5. 治本草案 — 正文/状态字段拆分（P2，需应用侧配套，本 runbook 不执行）

问题本质：`request_logs_bodies_hot` 的 upsert 把「高频变化的状态列」与「大而不变的正文列」
放在同一行，每次状态更新都触发整行（含 TOAST 指针）新版本。

草案 DDL（**未执行、未编号**，落地时按当时 migration 序号补 536+ 并配套 Go 写入路径改造）：

```sql
-- 状态/元数据行（小行、高频 UPDATE，无大字段）
CREATE TABLE public.request_logs_bodies_meta_hot (
  request_id  varchar PRIMARY KEY,
  ts          timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  body_len    integer,
  body_sha256 char(64)
  -- …仅状态与小元数据列
);

-- 正文行（大字段、写入后不变，仅 INSERT；删除走 TTL/促进归档）
CREATE TABLE public.request_logs_bodies_payload_hot (
  request_id    varchar PRIMARY KEY,
  ts            timestamptz NOT NULL,
  request_body  text,
  outbound_body text,
  response_body text
);
```

应用侧改造点（domains/hooks 与 bg 写入路径，实施时另开分支）：
- upsert 拆为：payload `INSERT ... ON CONFLICT (request_id) DO NOTHING`（正文不变则零重写）+
  meta `INSERT ... ON CONFLICT DO UPDATE`（小行更新，无 TOAST 放大）。
- promote/归档函数按 request_id 对齐两表行。
- 预期效果：TOAST 版本数下降 >90%，hot 表体积回到活数据真实大小，P1 类重写不再周期性需要。

## 6. 验收清单

- [ ] `bodies-hot-after-*.log` 中 TOAST bytes < 2GB（活行真实大小量级）
- [ ] `count(*)` 前后一致；应用请求详情页可正常取正文
- [ ] `pg_stat_user_tables.n_dead_tup` 回落至 < live 数
- [ ] 48h 后 DROP 备份表，`pg_database_size` 下降 ~35GB
- [ ] 索引对齐后 `pg_indexes` 双向差异清零（columnar GIN 受限项除外，记录豁免）
