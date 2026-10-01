# Round 44 收口轮：把「计划」变成「已验证的结果」

- 日期：2026-10-01
- 基线：`origin/main` = `651b6cac8`（接手时本地 `main` 落后 **119** 个 commit，R44 的全部数字都已过期）
- 方法：全量重跑 27 个含 integration 测试的包 → 逐条定性 → 修 → 同基线复跑 → 变异验证
- 分支：`audit/round44-closure`

> **纪律声明**：本报告所有 before/after 数字都来自**同一台机器、同一个
> `llm-gateway-pg`（kx-citus-pg17）、同一个 harness**，区别只有代码改动。
> 凡是没有实测支撑的，一律写在「没做的事」里，不写成已完成。

---

## 0. 一句话结论

R44 §9 列了 7 项。**本轮关掉 4 项、修掉 1 个此前完全没人知道的
全新安装可用性缺陷、并把剩下 3 项定性到「为什么还欠着」**。

而且最大的收获不是关掉了哪几项，是**推翻了 R44 自己的一条测量**：

> R44 记的 `autoupdate 26 PASS / 4 FAIL` 是错的。
> 那个包当时**根本没跑完**——它在第 30 个测试上 panic，
> 整个测试二进制被杀掉，**81 个测试从未执行**。
> 「4 个失败」听起来像「差一点就绿」，实际是「崩溃了，且崩得毫无提示」。

---

## 1. 全量重测：R44 的基线已过期，且本身不完整

R44 自己在头部写了「本轮没有合并 origin/main，因此这些数字不构成对最新
main 的结论；合并后必须重跑全量才算数」。本轮照做。

接手时 `main` 落后 119 个 commit。合并到 `651b6cac8` 后全量重跑：

| | 包数 | PASS | SKIP | FAIL | 判定 |
|---|---:|---:|---:|---:|---|
| 接手基线（`651b6cac8`，未改代码） | 27 | 6180 | 101 | 57 | 13 绿 / 13 红 / **1 空跑** |
| 本轮收口后 | 27 | **6295** | 102 | **49** | **15 绿 / 12 红 / 0 空跑** |

**没有任何一个包变差。** 逐包差异（只看 FAIL）：

| 包 | 前 | 后 | 原因 |
|---|---:|---:|---|
| `autoupdate` | 4 | **0** | §2（且执行的测试数 30 → 112） |
| `domains/requestjourney` | 1 | **0** | §5（假红 → 真跑且真过） |
| `admin` | 16 | **11** | §4/§6（修 5 个，推进 5 个） |
| `tests/integration` | 0 | **2** | §3：**不是新增失败，是从「编译不过」变成「能跑并暴露 2 个隐藏失败」** |

`tests/integration` 那 2 个要单独说清，否则「57 → 49」会被误读成
「净修好 8 个」：其中 2 个是**解锁编译后才第一次被看见**的，
它们在修之前根本不存在于任何统计里（该包 rc=3 VACUOUS）。
**真正净消除的失败是 10 个**（autoupdate 4 + requestjourney 1 + admin 5），
另有 5 个 admin 测试被推进到更深的断言层。

**与 R44 记录的数字的差异**（同一批包，基线不同）：

| 包 | R44 记录 | 本轮实测（651b6cac8） |
|---|---|---|
| `autoupdate` | 26 / 0 / 4 | 30 / 0 / 4 **+ 一次 panic** |
| `admin` | 2396 / 29 / 17 | 2420 / 30 / 16 |
| `bg` | 1029 / 15 / 13 | 1040 / 14 / 14 |
| `durable` | 57 / 0 / 0（作废重跑） | 57 / 0 / 0 |
| `tests/integration` | 0 / 0 / 0 VACUOUS | 0 / 0 / 0 VACUOUS（同样断编译） |
| `scripts/audit` | 4 / 0 / 2 | **该包已不在 integration-tagged 列表里** |

> `scripts/audit` 从 28 包变 27 包，是因为它现在没有仅由 integration tag
> 引入的测试文件了。harness 的 `[coverage]` 守卫会直接拒绝跑它——
> 也就是说 harness 本身已经能发现「这个包没有 integration 覆盖」。

### 1.1 R44 的 19 条启动迁移缺口：已全部修复，棘轮被退场

R44 登记在 `sql/schema/startup_known_gaps.tsv` 的 19 条，实测**全部不再复现**：

```
startup: applied=198 failed=0 missing=0
[populated] relations=435
```

**并且逐条验证过这不是「因为它们不再被执行所以不算失败」**——这是本轮
特意防的假绿：19 个文件在 `embeddata/startup/` 下**都还在**，
在 `StartupFiles` 里**也都还注册着**。它们是真的被应用了。

修好它们的是另一处改动：pre-478 的迁移被加进了 `StartupFiles`。
注册数从 173 涨到 198，applied 从 154/173 变成 198/198。

**处理方式**：把 19 条退场，清单留成 0 条的棘轮，而不是删掉文件。
删掉整个清单只会让门更松（19 条缺口全部变成致命）——这个不对称性是
R44 特意设计的，本轮保留。

**并且顺手修掉了清单自身的一个诊断缺陷**：旧代码在**报告是哪几条失败之前**
就先判「清单解析出 0 条 → 清单坏了」。清单空是 2026-10-01 之后的**健康状态**，
照旧逻辑，一个真正的新缺口会被报成「清单坏了」——把读者指向 paperwork
而不是指向真正的断点。改成先点名失败项再退出。

> **变异验证**（改坏一条真迁移，清单保持空）：
> ```
> startup: applied=197 failed=1
>   ✗ 新增未登记的启动迁移失败（1 条）:
>     - 802_session_turn_details_gw_task_id_index.sql :: ERROR: division by zero
> ERROR: ...且已知缺口清单当前为空（这是 2026-10-01 之后的正常状态）...
> exit=2
> ```
> 空清单**没有**让门变松。恢复后 `applied=198 failed=0 exit=0`。

---

## 2. §9-1 ✅ `release_id = 0` 死兜底 —— 根因比 R44 记录的深一层

R44 定位到「兜底在现有 schema 下永远失败」。本轮把**为什么 schema 是这样**
也挖穿了，四个独立角度互相印证：

1. **376** 建表时 `release_id BIGINT NOT NULL REFERENCES releases(id)`。
2. **379** 把它改写成可空，并建了 `WHERE release_id IS NOT NULL` 的偏索引
   ——**偏索引只有列可空才需要**，这是原始设计意图的物证。
3. 但 379 的 `CREATE TABLE IF NOT EXISTS` **在表已存在时是空操作**。
   它只写 CREATE，不写 ALTER。**哪怕 379 被注册并执行，也改不动一个已存在列的
   NOT NULL。** 换句话说 379 是个**潜伏的 no-op**，不是「没跑到」。
4. 379 **根本没注册**进 `StartupFiles`（注册下限是 388），所以连执行机会都没有。

**真库实测三处互证**：`release_id` 仍是 `NOT NULL`；外键
`instance_release_status_release_id_fkey` 仍在；而 379 想建的
`idx_irs_release` 偏索引**不存在**。

**修法**（用户拍板走 (a)：恢复原始设计意图）：

- 新增迁移 **809**：`ALTER COLUMN release_id DROP NOT NULL` + 补建
  `idx_irs_release` 偏索引。**外键保留**——外键本就允许 NULL，
  保留它意味着 release_id 非空时引用完整性照旧被强制。
- 代码 `RecordUpdateReport` 查不到 release 时改写 **NULL**（不是 0）。
- `ReleaseStatus.ReleaseID` 改成 `*int64`。
  这里刻意**没有**用「读路径把 NULL 折成 0」的省事做法：`ReleaseStatus`
  同时是读模型**和**写命令（`UpdateInstanceStatus` 收它），
  折成 0 的话一次 get→update 往返就会把 0 写回去、再撞一次外键。
- 顺带修掉同处的第二个隐患：旧代码 `if err != nil` 吞掉**所有**查询错误，
  连接中断/权限不足这类真实故障会被当成「没查到 release」静默降级。
  现在只对 `pgx.ErrNoRows` 走 NULL 分支，其余上抛。

**触发它的是真实业务流**：`rollback_report` 的 `ToVersion` 是
「回滚到的那个旧版本」，天然可能没有对应的 releases 行。不是 contrived 夹具。

### 2.1 实测：4 个哨兵测试全绿，而且顺带救回了 81 个没跑的测试

| | 执行的测试数 | PASS | FAIL |
|---|---:|---:|---:|
| 修前 | **30**（然后 panic） | 26 | 4 |
| 修后 | **112** | **112** | **0** |

修前的日志尾部：

```
--- FAIL: TestPgxStore_RecordUpdateReport/rollback_report
panic: runtime error: invalid memory address or nil pointer dereference
        store_pgx_test.go:291
FAIL  github.com/kaixuan/llm-gateway-go/autoupdate  0.935s
```

成因：`assert.NoError` 是**非致命**的，紧接着的 `status.Status` 在
`status == nil` 时解引用 → panic → **整个测试二进制被杀** →
排在它后面的 81 个测试**一个都没跑**，而 summary 依然报得整整齐齐
「26 PASS / 4 FAIL」。

> **这比 R44 的 4 个失败严重得多，也是一个可复用的教训**：
> **一个包红了，它的 PASS 数不能当覆盖率读。**
> panic 会静默吞掉同二进制里后面所有测试。
> 已把那处 `assert` 改成 `require`，并加了 `require.NotNil`。

`TestUpgradeRetry` 是第二个命中点，它的 `ReleaseID: 1, // Dummy release ID`
是个**悬空外键**（一次性库里 `releases` 没有 id=1）。已改成先建真 release
再引用——这与 R44 修的 `VersionComparison` 漏发布是同一族夹具漂移。

新增回归锁 `TestPgxStore_RecordUpdateReport_UnknownVersion`：
显式断言「未知 to_version 的上报被**存下来**、且 `release_id IS NULL`」。

---

## 3. §9-2 ✅ `tests/integration` 断编译 59 天

R44 把这条记成「需要决定是补 `NewIRTransport` 还是删/改这份夹具，
属于功能取舍」。**取证结论是：它不是待拍板的取舍，是一次没做完的清理。**

`d206ca771`（审计 R3#1，2026-09-09）有 ADR
（`docs/adr/2026-09-09-ir-transport-layer-retirement.md`），
明确判定 `IRTransport`/`LegacyTransport` 零生产引用、下线，
删除清单**逐个列了** 9 个 `domains/transformation` 测试 +
`tests/integration/ir_default_switch_test.go` —— **唯独漏了
`tests/integration/protocol_e2e_test.go`**，它有 3 处 `NewIRTransport()`
调用（353/485/517 行）。

该文件**完全自包含**（包内其余文件不引用它的任何符号），只测已下线的死工厂，
故按既有 ADR 决策删除，而不是改写回某个「等价 API」。

**为什么能潜伏 59 天**（两个条件叠加）：

1. 所有 integration 测试文件都带 `//go:build integration`，
   无 tag 的 `go build ./...` / `go vet ./...` **根本不编译它们**；
2. 唯一会看见它们的那条 CI job 常年红 ——
   **常年红的门和没有门在观察上不可区分**（R44 §1.2 已指出，本轮再次坐实）。

### 3.1 防复发的守卫（本轮真正的交付）

`TestIntegrationTaggedTreeCompiles`（`sql/schema/integration_gate_test.go`）：
**无 build tag**，会在默认 `go test ./...` 里 `go vet -tags=integration ./...`，
type-check 整棵 tag 树（含 `_test.go`）。选 `go vet` 而不是 `go test`：
它只做类型检查，不执行 `TestMain`、不为每个包链接二进制、不碰数据库。

**变异验证**（删文件之前跑的那一次）：

```
--- FAIL: TestIntegrationTaggedTreeCompiles (10.91s)
    type-checking 367 packages with -tags=integration
    1 reference error(s) in the tagged tree:
      vet: tests/integration/protocol_e2e_test.go:353:23: undefined: transformation.NewIRTransport
```

删掉文件后转绿。**并带扫描量地板**（`go list ./...` 少于 50 个包就 fatal），
防止「`go` 跑挂了 / 根目录错了」时这个守卫恒绿——R44 §4.5 吃过一次亏。

### 3.2 副作用：解锁编译立刻暴露出 2 个此前**完全不可见**的失败

```
修前：tests/integration  rc=3  RUN=0   PASS=0  SKIP=0  FAIL=0   VACUOUS
修后：tests/integration  rc=1  RUN=26  PASS=23 SKIP=1  FAIL=2
```

即：这个包空了两个月不是因为它没写测试，是因为**它连编译都过不了，
所以没人知道它里面还藏着 2 个失败**。这正是 §1 要区分
「0 失败」与「真的跑了」的又一个实例。

已在 ADR 里补记这次清理遗漏及其成因。

---

## 4. ✅ 新发现：全新安装的 `request_logs` 母表写不进任何一行

**这是本轮发现的最严重问题，R44 完全没提到过它。**

在真实 installer 形态的一次性库上实测：

```sql
SELECT c.relname FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid = c.oid
  JOIN pg_class p   ON p.oid = i.inhparent
 WHERE p.relname = 'request_logs';
-- request_logs_2026_07, request_logs_2026_08      ← 只有 2 个月，无 DEFAULT
```

**全新安装产出的 `request_logs` 母表没有任何 DEFAULT 分区**，
于是 `ts` 落在 2026-08 之外的任何 INSERT 直接失败：

```
ERROR: no partition of relation "request_logs" found for row   SQLSTATE 23514
```

**母表从 2026-09 起就写不进任何一行。**

为什么 baseline 与迁移链都没建它：

- `sql/schema/01-schema.sql` 是 2026-08-04 的 pg_dump，dump 里
  `request_logs_default` **只以函数体引用出现**，没有对应的
  `CREATE TABLE ... PARTITION OF ... DEFAULT`；
- `embeddata/startup/` 下 267 个启动迁移里**没有任何一条创建它**；
  705 只负责把 337 摘下的月分区 ATTACH 回来，**它自己的 repair 流程
  假定 DEFAULT 已存在**。
- 本机 `llm_gateway` 之所以有，是历史上有人手工建过 ——
  所以「生产有、全新安装没有」这个差异一直没被发现。

DEFAULT 分区是**承重**的，不是可有可无：4 个函数引用它
（`ensure_request_logs_partition`、`promote_request_logs_default_batch`、
`archive_request_logs_default`、`repair_request_logs_detached_partitions`），
而 `bg/partition_manager.go:1238` 在**常驻循环**里调用其中一个。
全新安装上它们必然报错。

**修法**：新增迁移 **808**，幂等补建 DEFAULT 分区（`IF NOT EXISTS` 语义）。

**实测前后**：

| | 结果 |
|---|---|
| 修前 | `INSERT ... ts=now()` → `ERROR 23514 no partition of relation "request_logs" found for row` |
| 修后 | 同一语句**路由进 `request_logs_default`**（报错推进到该表自身的 NOT NULL 校验，说明分区路由已生效） |
| 重跑 | `NOTICE: 808: public.request_logs_default already exists`（幂等） |

### 4.1 我差点把「一个实例」当成「一个类」——对照实验把我拦下来了

修完 808 之后复查 `admin` 剩余失败，发现
`TestFetchRequestBodies_HotMiss_FallsBackToBodiesView` 报的
**是同一个错误形状**：

```
ERROR: no partition of relation "request_logs_bodies" found for row  SQLSTATE 23514
```

在「这是一类问题、应该一次性都补上」的冲动下，我在一次性库上把
**全部 25 张分区母表**列了一遍，再对**生产形态库**跑同一查询做对照：

| 分类 | 数量 | 含义 |
|---|---:|---|
| 全新安装**无**、生产**有** DEFAULT 分区 | 11 | **真正的全新安装缺口** |
| 全新安装无、**生产也无** DEFAULT 分区 | 9 | **无证据表明是缺陷** |
| 两边都有 | 5 | 正常 |

**11 张真正缺口的清单**（含本轮已修的 `request_logs`）：

```
credential_model_index   dashboard_access_events   model_probe_runs
request_logs             request_wal                routing_decision_log
session_bodies           session_module_executions  session_turns
sessions                 usage_ledger
```

**9 张「两边都没有」的**：`candidate_failure_logs`、`credit_ledger`、
`request_logs_bodies`、`routing_decision_log_archive`、`session_censors`、
`session_memora`、`session_tools`、`session_turn_details`、`tool_usage_stats`。

**这张对照表推翻了我自己的一句话**：`request_logs_bodies` 的那个 23514
**不是全新安装缺陷**——生产上它同样没有 DEFAULT 分区、同样会报同一个错。
它是「测试写入落在未物化的月份里」这一类问题，与 808 无关。
before/after 也印证了：修完 808，该测试**一字未变**地继续失败。

**为什么没有顺手把 11 张全补了**：

- `CREATE TABLE ... PARTITION OF ... DEFAULT` 需要 ACCESS EXCLUSIVE 锁
  并全表扫描。`session_turns` / `sessions` 在生产上是热大表，
  这是**真实的停机风险**，不能凭「形状一样」就批量执行。
- 除 `request_logs` 之外，其余 10 张**没有任何测试失败作为证据**。
  `request_logs` 有三条独立证据（4 个函数按名引用它 + 3 个测试实际失败 +
  705 的 repair 流程以它为骨架），其余没有。
- 批量加 10 个 DEFAULT 分区属于**未经证据支持的 schema 变更**。

所以本轮只修有证据的那一张，其余 10 张**作为普查结果交出**，
交给下一轮按表逐一取证。**这比「形状一样就一起改」诚实。**

> 这也是本轮第二次被同一个纪律抓住：第一次是 §2.1 的 panic 掩盖 81 个测试，
> 这一次是「单侧分布当成结论」。两次的错误形状相同——
> **输出干净、格式整齐，而结论比证据宽**。

---

## 5. ✅ 一个假红伪装成「租户隔离漏洞」

`domains/requestjourney` 有一个测试报：

```
alpha scope saw 1 beta rows, want 0 (RLS leak)
```

**这不是产品缺陷。** 逐层查证：

1. 552 迁移的策略是**对的**（`ENABLE` + `FORCE` + 两条 policy 都建了，
   启动链 applied=198 failed=0 证明它干净落地）；
2. 测试自己写着前提：「The validator superuser pool above bypasses RLS
   even with FORCE; the non-bypass pool is required to assert RLS isolation」；
3. 但 harness 把 `TEST_TENANT_DATABASE_URL` 指向了**同一个 URL、同一个角色**，
   而那个角色是：

```
rolname     | rolsuper | rolbypassrls
llm_gateway | t        | t
```

**superuser 无条件绕过 RLS，`FORCE` 也不例外。** 测试量的是一个超级用户。

> **一个会谎报的安全断言，比没有断言更糟**——它训练读者对 RLS 失败脱敏。

**两处都修了**：

- **harness** 现在建一个真正的非 superuser、非 BYPASSRLS 角色
  `itgate_tenant`，并**先验证 `rolsuper/rolbypassrls` 确为 false 再导出 DSN**；
- **测试**先断言前提（探测 `current_user` 的角色属性），不满足就
  **带完整诊断信息 SKIP**，而不是继续断言一个它没观察到的泄漏。

**双向实测**：

| 接线 | 结果 |
|---|---|
| 新接线（非 bypass 角色） | `--- PASS: TestObservationOutbox_TenantRLSIsolation` —— **真跑了，真过了** |
| 变异（改回旧接线） | `--- SKIP`，并打印 `rolsuper=true rolbypassrls=true` + 指路修复方式；`exit=0`，**不再谎报泄漏** |

包级结果：`100 PASS / 1 FAIL` → `101 PASS / 0 FAIL`。

---

## 6. 逐条定性：把「13 个红包」拆成有主的类别（§9-4）

接手基线 57 个 FAIL，**逐个归因后**：

| 类别 | 数量 | 性质 | 本轮处置 |
|---|---:|---|---|
| **A 夹具形态冲突**：测试自建表，撞上 installer 形态库 | 10 | 测试基建 | 未修（§9-3 架构项） |
| **B 夹具漂移**：schema 收紧后夹具没跟上（23502 NOT NULL 等） | 10 | 测试 bug | **修 1 个根因关掉 7 个** |
| **C 全新安装缺表**（`*_hot`、`outbox_events`） | 3 | **产品缺陷** | 未修，已定位 |
| **D `request_logs` 无 DEFAULT 分区**（23514） | 2 | **产品缺陷** | **✅ 已修（迁移 808）** |
| **D' `request_logs_bodies` 23514** | 1 | **不是同类**（生产同样缺，见 §4.1） | 未修，已重新归类 |
| **E `release_id` 死兜底**（23503） | 4 | **产品缺陷** | **✅ 已修（迁移 809 + 代码）** |
| **F RLS 断言前提不成立** | 1 | 假红 | **✅ 已修（harness + 测试）** |
| **G 自建 testcontainers**（裸 `postgres:16-alpine` 连不上） | 4 | 基础设施 | 未修 |
| **H 真实行为断言不符 / 依赖生产形态库** | 12 | 待产品判断 | 未修，逐条列名 |

**B 类里最大的一个根因**：`providers.base_url` 是 NOT NULL，
`admin/routing_candidate_binding_test.go` 的共用夹具没写这一列，
**7 个测试、2 个文件、同一个根因**，全部死在
`insert provider: null value in column "base_url"`。一处修好。

> **但必须说清楚：这 7 个并没有变绿。** 修完 `base_url` 后它们**推进了一层**。
> 逐名对比（before / after 的失败名集合做差）：
>
> - **直接转绿 3 个**：`ConcurrentConflict`、`IncompleteSet`、`StaleRevision`
> - **推进到更深的断言 4 个**：`IntegrationHappy`、`IntegrationBumpMonotonic`、
>   `IntegrationNoOpBump`、`IntegrationResolveEcho`，现在统一死在
>   ```
>   routing_candidate_binding_test.go:523: expected at least one audit row for test-reorder-model-...
>   ```
>   即 **HTTP 200、优先级也正确翻了，但 `routing_audit_log` 里没有审计行**。
> - 加上 808 修掉的 `TestHandleModelBreakdown_Success` / `_LongTailMerge`，
>   `admin` 共 **5 个转绿、5 个推进**。
>
> 审计行为空这个根因**未定性，不在本轮范围内**，留作下一轮的明确入口。
> 本轮**不**把它算成「修好了」。

`admin` 剩余 11 个失败中，有 4 个是**测试明确要求一个「生产形态」的库**
（`TestReportRollup_HTTPContract` 自己写着「DSN 已指定却无数据，
多半指错了库。本测试要求有日聚合结果的库」；`TestResolveCandidatesInvariant_Live`
写着「empty models_canonical — the gate would be vacuous」）。
**这是 R44 §7「两套夹具形态」的第三种形态：假定活库。**
它们不是缺陷，但它们说明 harness 需要第三种库形态，不能只有一种。

---

## 7. 没做的事（逐条写明为什么，不粉饰）

| §9 项 | 状态 | 为什么 |
|---|---|---|
| §9-1 `release_id` | ✅ 完成并实测 | — |
| §9-2 `tests/integration` 断编译 | ✅ 完成并实测 | — |
| §9-3 两套夹具形态拆开 | **未做（只做了其中可独立交付的一小块）** | 需要给 ~68 个 integration 文件逐个声明形态并让 harness 建对应库。本轮只交付了它的一部分：**一个真正的第三种角色（非 bypass 租户角色）**，外加把「假定活库」这第三形态在 §6 里点名。C 类缺表与 A 类冲突**仍未解**。 |
| §9-4 逐条定性 | ✅ 完成（57 个全部归类，见 §6） | — |
| §9-5 CI 前置（amd64 `kx-citus-pg17`） | **未做** | 依赖外部镜像构建，本机是 arm64。仍是 `.github/workflows/integration-testcontainers-ci.yml` 常年红的状态。**本轮没让那条 job 变绿，也没有假装它绿了。** |
| §9-6 基线双份统一 | **未做** | R43 已完整对账：已提交基线 2612 个对象 vs 新生成 6354 个，ADDED 3759 / MISSING 17（见 `docs/audit/2026-10-01-baseline-reconcile.md`）。这是多批次工程，**不是本轮能顺手带上的改动**。本轮的 808/809 走**新增启动迁移**而不是改基线，正是为了不碰这个共享产物。 |
| §9-7 `sql/objects/` 定位 | **未做** | 承重 SSOT vs 部署惰性 vs 双向漂移 31/618，仍未决。 |
| **本轮新增：另外 10 张表的 DEFAULT 分区缺口** | **未做（只普查，未动手）** | §4.1 的对照实验确认它们与 `request_logs` 同形（全新安装无、生产有）。**但没有一条测试失败作为证据**，且 `CREATE TABLE ... PARTITION OF ... DEFAULT` 对 `session_turns` / `sessions` 这类热大表要 ACCESS EXCLUSIVE + 全表扫描。批量加 10 个未经取证的 schema 变更，风险大于收益。**清单已交出，逐表取证是下一轮的事。** |
| **本轮新增：`routing_audit_log` 审计行为空** | **未定性** | §6。`base_url` 夹具修好后，4 个 reorder 测试推进到这一层：HTTP 200、优先级正确翻转，但审计表里没有行。需要单独一轮查 `logAuditExec` 的写入路径。 |
| **本轮新增：`request_logs_bodies` 23514** | **重新归类，未修** | §4.1。对照实验证明生产上同样缺 DEFAULT 分区，所以**不是**全新安装缺陷。 |

**另外记一条本轮自己踩的**：我第一版 sweep 脚本把完整 import path
（`./github.com/kaixuan/llm-gateway-go/admin`）传给了只认仓库相对路径的
harness，27 个包**全部** rc=2 INCOMPLETE。判据是我自己加的
`[populated]/[dsn]/summary` 三标记体检抓到的——否则这轮会拿一份
「27 个包全红」的假结果去写报告。**体检器和被体检的量具一样需要自证。**

---

## 8. 变异验证记录

| 变异 | 期望 | 结果 |
|---|---|---|
| integration 树断编译（删文件前） | 红并点名 | ✅ `undefined: transformation.NewIRTransport`（扫 367 包） |
| 恢复编译（删文件后） | 绿 | ✅ |
| 启动迁移真的失败 + 清单为空 | rc=2 且点名 | ✅ `802_...:: ERROR: division by zero` |
| 恢复 | rc=0 | ✅ `applied=198 failed=0` |
| RLS 角色改回 superuser | 测试 SKIP 且说明原因 | ✅ 打印 `rolsuper=true rolbypassrls=true` |
| RLS 角色为真非 bypass | 测试真跑且 PASS | ✅ `--- PASS` |
| 把 `TEST_TENANT_DATABASE_URL` 接回 `$GATE_URL` | 注入守卫红 | ✅ 报「会报出一个并不存在的『RLS 泄漏』」 |
| 删掉 harness 里 `rolbypassrls` 的**全部**出现 | 注入守卫红 | ✅ 报「只建角色而不验证它能否绕过 RLS」 |

合计 **8 项变异**，全部按预期转色。每一项都是先看见它红、再修、再看见它绿。

> **一处必须记的自我纠错**：M2 的第一版是**空变异**——我用 `sed` 只替换了
> 查询里的一处 `rolbypassrls`，而脚本里还有第二处（日志文案），于是
> `grep -c` 仍为 1，守卫自然照常绿。**若我当时只看「守卫是绿的就认为变异无效」，
> 就会错误地放宽判据。** 改成统计出现次数、先确认变异真的落进文件
> （2 → 0），再读门色。这与 R44 §10 第 2 条记录的「变异脚本把 no-op 当真变异」
> 是同一个坑，隔了两轮我又踩了一次。

---

## 9. 收尾自查

- 本地 `main` 曾落后 119 个 commit，**R44 的数字全部作废**；本报告的
  before 是在 `651b6cac8` 上**重新实测**的，不是引用旧值。
- 工作区原有的 4 个生成物改动（`VERSION` / `version.json` /
  `web/public/menu-config.json` / `web/public/version.json`）**不是本轮的**，
  已 `git stash` 保存（`R44-continuation: generated build artifacts`），
  **未被本轮提交**，避免把别人的构建产物混进审计提交。
- 另有一路 `go test ./...` 在 `syncfield/llm-gateway-go-2` 目录运行，
  与本仓库无关，已核对 cwd 确认无干扰。
