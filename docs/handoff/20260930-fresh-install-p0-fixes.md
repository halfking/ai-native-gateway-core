# 接力 handoff — fresh-install P0 修复（四十二轮）

**日期**：2026-09-30
**分支 / HEAD**：`main`
**工作目录**：`__DEV_HOME__/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`

---

## 一、结论

本轮修完 `InitSchema` 路径上的 **5 个独立阻断缺陷**。三份 `01-schema.sql` + 三份 `02-seed.sql`
现在都能在**全新空库**上以 `psql -v ON_ERROR_STOP=1 --single-transaction` 走到 **exit=0**（各 328 relations），
这是本轮开始时从未达到过的状态（起点是 exit=3，`relation "request_logs_hot" does not exist`）。

起点上交接文档列的 3 个 Go 失败**已全部由其他轨道修绿**（`sqlreadguard` / `proxy` / `discovery`），
本轮不重复处理；全量并发下偶发的 `admin` 与 `domains/streaming` 两处失败，单跑均绿，判为本机端口资源偶发。

---

## 二、修掉的 5 个阻断缺陷

### 1. `01-schema.sql` 前向引用（3 种形态，canonical + 2 份派生拷贝）

psql 在 `CREATE` 时校验 `LANGUAGE sql` 函数体、视图定义和 `COMMENT ON`，因此以下三类顺序错误会中止整个 apply：

| 形态 | 实例 | 分布 |
|---|---|---|
| A. sql 函数体引用后定义的表 | `credential_most_used_model` → `request_logs_hot` | 三份 |
| B. 函数引用后定义的函数 | `columnar_drift_report` → `columnar_healthcheck` → `columnar_insert_only_parents` | 2 份派生 |
| C. `COMMENT ON` 早于被注释对象 | `system_health_status`（comment @4337，函数 @28790） | 三份 |
| D. sql 函数体引用后定义的表 | `get_model_state_summary` / `model_probe_credential_concurrency` → `credentials` / `model_probe_state` | 三份 |

修法一律是**整块搬移**（`-- Name:` 块头到下一个块头之前），不新增/删除任何语句；
canonical 与 embeddata 各自独立处理（三份相差 612 行 diff，不能盲目 `cp`）。

> 教训：只扫「表引用」会漏掉形态 B/C。最初两版扫描器都只查表，报「零前向引用」后 apply 仍红，
> 剩下的分别是函数间依赖和 `COMMENT ON`。**扫描器必须覆盖函数调用与 COMMENT**。

### 2. `default_table_access_method` 区间错位（真缺陷，与前向引用无关）

`:12943` 的 `SET ... = columnar` 覆盖了 `request_logs_2026_07/08` 与 `request_logs_archive`，
使它们建成 columnar；而这些分区带 hash/gin 索引，Citus columnar 只支持 btree，于是：

```
ERROR:  unsupported access method for the index on columnar table request_logs_2026_07
```

生产 252 实况：`request_logs_2026_07` = **heap**，`request_logs_bodies_2026_07` = **columnar**。
修法：在 `request_logs_2026_*` 前插 `SET heap`，在 `request_logs_bodies` 前插回 `SET columnar`。
修完三份新建库的 AM 与健康库完全一致。

### 3. 括号缺陷（预先存在，2 份派生拷贝各 2 处）

- `trg_notify_auto_route_cmb_update` 的 `WHEN` 少一个右括号 → `syntax error at or near "EXECUTE"`
- deploy 的 `self_check_runs_selection_strategy_check` 丢了 `ARRAY[...]` 的闭括号 `]`

两者在**未改动的 HEAD 副本上即可复现**，与前向引用无关。canonical 两者都正确。

> 教训（代价换来的）：**读 SQL 推理括号必错**。本轮先后按「净深度」「正则切尾」「改 `CHECK ((` 为 `CHECK (`」
> 猜了 4 次，全都错。最终靠向真库穷举候选形态才定值。凡是括号/括号数类问题，直接让服务器判。

### 4. `02-seed.sql` 的两处陈旧对象

- `setval('public.model_offers_id_seq')` 与 `..._ops_model_offers_backup_backup_id_seq`：
  这两个序列**全仓从未定义**，生产库也没有（`model_offers` 是 VIEW 不是表）⇒ 23 个 setval 减为 21。
- 一行 `key_applications` 占位记录（`'__REDACTED_IP__'`）：commit `1e8443c2e` 脱敏时本意删除该行，
  却被替换成了带占位符的假 INSERT，与紧邻注释「intentionally NOT seeded here」自相矛盾，
  且 `applicant_ip` 是 `inet`，`'__REDACTED_IP__'` 不是合法地址 ⇒ fresh install 挂在 02-seed:483。

### 5. `model_aliases` 缺 UNIQUE 约束 + `credentials` 缺 4 列

- `02-seed` 有 4 处 `ON CONFLICT (canonical_id, raw_name)`，但 `model_aliases` 无对应唯一约束
  ⇒ `there is no unique or exclusion constraint matching the ON CONFLICT specification`。
  健康库有 `uq_model_aliases_canonical_raw UNIQUE (canonical_id, raw_name)`，按此补齐。
- 2 份派生拷贝的 `credentials` 缺 `concurrency_mode` / `tpm_limit` / `max_queue_depth` / `max_queue_wait_ms`
  （canonical 有；这 4 列正是 startup/479 添加的），而 startup/566 直接对它们建触发器
  ⇒ `column "concurrency_mode" of relation "credentials" does not exist`。按 canonical 形态补齐。

### 6. 测试工具缺陷：`psqlScalar` 吞掉 stderr

`fresh_installer_integration_test.go` 的 `psqlScalar` 用 `CombinedOutput()`，
citus 的 background-worker WARNING 落进 stdout 之外的同一 buffer，导致空库前置检查把
`0 non-extension public relations` 读成 `WARNING: ... 0 non-extension public relations` 而误判非空。
改为只取 stdout，失败时用 stderr 报错。

---

## 三、验证证据

```bash
# 三份 01-schema + 02-seed 各自在全新库 exit=0（00-prereqs 先行，columnar AM 由它注册）
for f in sql/schema/01-schema.sql \
         installer/cmd/llm-gw-installer/embeddata/01-schema.sql \
         deploy/sql/schemas/baseline/01-schema.sql; do
  createdb ...; psql -d $db -f sql/schema/00-prereqs.sql
  psql -v ON_ERROR_STOP=1 --single-transaction -f $f   # exit=0, 328 relations
done

go build ./... && go vet ./...            # 根 + installer 均干净
(cd installer && go test ./... -count=1) # 13 包全绿
go test -tags=integration ./cmd/llm-gw-installer/ -run TestFreshInstallerSessionTurnsHotBootstrap
  # 前进到 startup/568 才停（见下），01-schema / 02-seed 阶段已全通过
```

AM 与生产实况对齐（`request_logs_2026_07=heap`、`request_logs_bodies_2026_07=columnar`、`request_logs_hot=heap`）。

---

## 四、🔴 未修：startup 迁移登记缺口（需架构裁定）

fresh installer 已能跑完 00-prereqs / 01-schema / 02-seed，**在 startup 迁移阶段仍停**：

```
startup/568_credential_priority_flag.sql:49: ERROR:
  relation "public.candidate_binding_scope_revision" does not exist
```

根因：**`541_candidate_binding_scope_revision.sql` 未登记进 `Runner.StartupFiles`，且 embeddata 无该文件副本**。
568（已登记）依赖 541 创建的表。这是与迁移 759 撞号同型的「登记缺失」缺陷。

规模：`sql/migrations/startup` 有 465 个迁移，`StartupFiles` 只登记 170 个。
`/tmp/find_missing_registrations.py` 列出 155 个未登记且创建新关系的迁移。

**为何本轮不做**：修 541 就必须五点同步（runner.go StartupFiles + embeddata 副本 + canonical +
deploy baseline + go:embed），而 155 个未登记迁移是**整体架构决策**（哪些对象走 01-schema 基线、
哪些靠 startup 增量收敛），单点登记 541 会改变现有 fresh install 的对象集合，属需你拍板的范围。

---

## 五、纪律（本轮付出代价换来的）

1. **括号/语法问题不要读 SQL 推理**，向真库穷举候选形态，让服务器判值。本轮猜错 4 次。
2. **扫描器要覆盖全部形态**：表引用之外，还有函数间调用、`COMMENT ON`、Citus 访问方法区间。
3. **净深度为 0 不等于语法合法**：`CHECK ((` 多开一层时，净 0 仍被 PG 拒收。
4. **「用 `-a` 回显定位」会被文件末尾的 `COMMIT;` 带偏**，报错行号常是最后一行而非真实位置；
   用 `head -n N` 二分 + 每行净深度扫描才可靠。
5. **修派生拷贝不能盲目 `cp` canonical**：三份相差 612 行 diff，缺陷分布也不同
   （canonical 早已修好 `columnar_healthcheck` 顺序，却仍卡在 `request_logs_hot`）。
6. **修法必须用真库 apply 实测**，且用 installer `applySQL` 的同参形态
   （stdin + `ON_ERROR_STOP=1` + `--single-transaction`）；`-c` 与 `-f` 混用会让 stdin 被吞、报出假绿。
7. **库名长度**：PG 标识符上限 63 字节，`probeFINAL_canonical` 这类长名会让 `CREATE DATABASE` 静默失败，
   后续连接报 `database does not exist` —— 极易误判为脚本问题。
8. **回归「单跑绿、全量红」先怀疑环境**：本轮 `admin`、`domains/streaming` 两处
   （`connect: can't assign requested address`）单跑均绿，判为本机端口资源偶发，未改代码。

---

## 六、下一轮建议

1. **裁定 startup 登记策略**（阻塞 fresh install 的最后一段）：是补齐 541 等关键迁移，
   还是明确「fresh install 只保证 01-schema 基线 + 已登记的 170 个」并在文档中固化。
2. 若选补齐：为「已登记迁移引用了未登记迁移创建的关系」建常驻门
   （本轮 `/tmp/scan_startup_order.py` 的思路，但需修掉 CTE 别名/prose 噪音），
   否则同类缺口会继续静默存在。
3. 交接文档原列的 hotzone P5 文档子项、E2E 部署级验证、F3/F4/F5 三项留档，本轮未触及。
