# auto route rollup 42P10 全失败 —— 154 生产审计与迁移 726 修复

- 日期：2026-09-18
- 触发任务：老板报告"本机请求 kaixuan/auto 出现『重新连接中... 6/10』，确认 auto 策略是否生效"
- 结论：**auto 策略代码链路完好，但其索引数据管道自 09-18 07:35 起完全断裂**；客户端的"重新连接中... n/10"是本地客户端在网关响应异常/超时后的自身重连计数 UI（gateway 日志与前端 i18n 中均不存在该字面量），网关侧的真实根因是下述 schema 漂移。

---

## 一、证据链（全部只读核实）

### 1. 现场日志（154，`/opt/llm-gateway-go/logs/gateway-canary-8782.log`）

```
WARN auto route listener: refresh failed
error="rollup credential_model_index: insert: ERROR: there is no unique or
 exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)"
```

- 部署版本：`2.5.6-73c8a6c5-20260917-2138`（slot 8782，PID 23445，09-18 07:36 起）
- 报错起始于当日 07:35 部署应用迁移 718 之后，每逢 5 分钟 ticker 与
  `credentials:UPDATE` / `credential_model_bindings:UPDATE` NOTIFY 必现。

### 2. 共享库只读核查（154 → PG17 @ 252 内网）

| 核查项 | 结果 |
| --- | --- |
| `pg_indexes` of `credential_model_index_hot` | 仅 3 个**非唯一**索引（canonical_id / credential_id / updated_at DESC），唯一索引**为零** |
| `pg_indexes` of `model_task_index` | `model_task_index_bucket_canonical_task_key` UNIQUE 健在（该 rollup 不受影响） |
| `credential_model_index_hot` | **0 行** |
| `credential_model_index`（parent） | 64286 行，`MAX(bucket)=2026-09-17 14:45+08` |
| `schema_migrations.version` | 混有 `'V359'` 等非数字值（存在账本修复痕迹） |

### 3. 因果链

1. 历史：hot 表三列上先后叠了三套函数等价唯一索引 —— 347
   `credential_model_index_hot_unique_key`、354 `idx_credential_model_index_hot_unique`、
   以及一版 `credential_model_index_hot_bucket_credential_id_raw_model_idx`。
2. 09-17/09-18 的迁移 **718**（R37 冗余索引清理）按"三留一"drop 掉前两者，
   设计上保留 `idx_credential_model_index_hot_unique`；其前置核实②③在审计
   所在的本地库成立，但在共享库上该保留侧**并不存在**（354 的
   `IF NOT EXISTS` 未在共享库生效，或该索引此前已被清理）。
3. 718 应用后 hot 表唯一索引归零 → `bg/auto_index_refresher.go` 的
   `rollupCredentialModelIndexONCONFLICT`（`ON CONFLICT (bucket, credential_id,
   raw_model) DO UPDATE`）每次 INSERT 必然 42P10；DELETE 先行成功（删 0 行）。
4. 8h retention 的 `promote_credential_model_index_hot_to_partition` 把 07:35
   前的最后存量搬入 parent 后 DELETE，hot 归零且再无补充。
5. `credential_model_index_with_current_month` 最新桶冻结在 09-17 14:45；
   `autoroute` 的 `refreshIndexSQL` 取 per-pair `MAX(bucket)` 且**无新鲜度
   下限**，因此 auto 决策继续使用过期快照 —— 服务未中断，但策略指标停止
   进化、无法反映当日凭据健康变化。

### 4. 与用户症状的对应

- `kaixuan/auto`：`kaixuan` 为该网关内一凭据/模型族（154 日志中 21 处命中均为
  auto_summary 对"kaixuan 请求故障"排查会话的摘要，即本任务之前已有多个并行
  会话在排查同一现象）；`auto` 为网关 `model=auto` 自动路由入口
  （`domains/streaming/auto_route.go::maybeResolveAuto`）。
- `重新连接中... 6/10`：gateway 日志 0 命中；前端 i18n 仅有无计数后缀的
  `重新连接中…`（`web/src/locales/zh-CN/dashboard.ts`）。该提示来自客户端侧
  重连 UI，计数为客户端自身重试进度，非网关输出。

## 二、修复（迁移 726）

文件：

- `sql/migrations/startup/726_restore_credential_model_index_hot_unique.sql`
  1. ctid 守卫的防御性去重（对曾在无约束窗口产生重复行的环境先收敛，避免
     `CREATE UNIQUE INDEX` 因冲突失败；对 0 重复库为 no-op）；
  2. `CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_model_index_hot_unique
     ON credential_model_index_hot (bucket, credential_id, raw_model)` ——
     采用 718 选定的保留名；新装库（baseline→354→718 链路）已有同名索引，
     本语句 no-op，完全幂等。事务包裹。
- `sql/migrations/startup/726_restore_credential_model_index_hot_unique.down.sql`
  语义反转（DROP 该索引），头部注明执行后 rollup 将回到 42P10 事故态。
- `sql/migrations/startup/migration_726_test.go` —— 四不变量契约测试：
  - C1 索引元组与 `bg/auto_index_refresher.go` 的 ON CONFLICT 元组**解析绑定**
    （非拷贝字面量，rollup SQL 改元组时测试必红）；
  - C2 去重守卫必须先于建索引、事务包裹；
  - C3 任何 startup 迁移（.up 侧）不得 DROP 保留侧索引 —— 718 类错误的回归门；
  - C4 installer embeddata 副本与原件字节一致。
- `installer/cmd/llm-gw-installer/embeddata/startup/726_*` —— embeddata 镜像同步
  （721 先例约定）。

不改动项及理由：

- `bg/auto_index_refresher.go`：Go 侧行为正确（DELETE+INSERT+ON CONFLICT 为
  2026-09-10 修复的正确形态），故障在 DB 状态，无需改代码。
- schema 快照（`sql/schema/01-schema.sql`、`deploy/sql/schemas/baseline/`、
  `sql/objects/indexes/`）：为 `sql/scripts/dump-schema.sh` 生成物，按仓库惯例
  不手改，下一次 dump 自然收敛（届时 unique_key 索引消失、canonical 索引入册）。

## 三、验证

```
# 干净 worktree @HEAD(9769a18c4) + 仅叠加本次 5 个新文件
go test ./sql/migrations/startup/ -count=1
→ ok  github.com/kaixuan/llm-gateway-go/sql/migrations/startup  (含 726 契约测试与全部既有迁移契约测试)
```

- 主工作树同等命令全绿（`TestMigration726RestoreHotUniqueContract` PASS）。
- `bg` 包在本机 Windows 构建失败为**既有平台限制**（`storage_retention_worker.go`
  使用 `syscall.Statfs`，Linux 目标不受影响；本次 diff 未触碰 bg）。
- 生产应用方式（下一轮执行）：对共享 PG17 应用迁移 726 后**无需重启网关**，
  5 分钟 ticker / 下一条 NOTIFY 即自动恢复 rollup；预期日志出现
  `auto index refreshed credential_rows=N`，hot 表在首个 5 分钟桶重建。
  回归口径：`gateway-canary-8782.log` 中 42P10 WARN 停止新增。

## 四、遗留风险

1. **共享库口令暴露**：诊断过程中 `/etc/llm-gateway-go/env` 的
   `LLM_GATEWAY_DATABASE_URL` 含明文口令并进入了会话输出 —— 建议轮换该口令
   （内网 172.16.2.210，风险有边界但应轮换）。
2. **schema_migrations 账本脏数据**：`version` 列混有 `'V359'` 等非数字值，
   任何 `version::int` 类查询/校验都会 500；且账本曾被打过修复补丁（存在
   "标记已应用但 DDL 未执行"的漂移形态，本次 354 即疑似案例）。
   建议：账本规范化 + `scripts/repair-252-migration-ledger.sh` 流程加
   "标记≠执行"的复核闸。
3. **718 类"留一"清理无前置探针**：drop 冗余索引前未在目标库断言"保留侧
   存在"。726 的 C3 契约是事后门；如再做同类清理，应在迁移内先
   `DO $$ ... IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='保留名') THEN RAISE EXCEPTION ... END $$;`。
4. **Windows 本地 bg 包不可编译**（既有）：`storage_retention_worker.go` 的
   `syscall.Statfs` 无 build tag 分离，本地 `go test ./bg/` 不可用，只能依赖
   Linux 环境；建议拆分 `_unix.go`/`_windows.go`。
5. **refreshIndexSQL 无新鲜度下限**：索引冻结时 auto 决策静默使用过期快照、
   无告警。可考虑给 decider 加 max-staleness 判定并在超限时走降级日志
   （本轮不改行为，仅登记）。
6. **并行会话工作树污染**：审计期间发现本工作树存在另一并行会话遗留的
   未提交 WIP（`autoroute/session_role.go` + `classifier.go`/`decision.go`
   修改），其与既有代码存在 `containsAnyPhrase`/`lowerASCII` 重复声明，
   当前使 `autoroute`/`bg`/`admin`/`cmd` 在本机编译失败。本提交严格隔离、
   未包含上述文件；该 WIP 归属会话需自行收敛后再合入。

## 五、下一轮建议（提示词草案）

> 在 154 上对共享 PG17（172.16.2.210）应用迁移 726
> （`psql -f sql/migrations/startup/726_restore_credential_model_index_hot_unique.sql`，
> 或随下一次 deploy-seamless 走标准 migration 通道），随后观察
> `gateway-canary-8782.log`：42P10 WARN 应停止新增，5 分钟内出现
> `auto index refreshed` 且 `credential_rows>0`，`credential_model_index_hot`
> 重建行数>0；再用 `model=auto` 打 `/v1/chat/completions` 冒烟并核对
> `X-Gw-Auto-Decision` 头与 `auto_route_selections` 落行。完成后执行
> 共享库口令轮换与 `schema_migrations.version` 脏值规范化，并在
> `bg/storage_retention_worker.go` 拆分 Windows build tag。
