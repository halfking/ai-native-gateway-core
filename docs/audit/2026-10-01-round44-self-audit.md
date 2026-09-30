# Round 44 — 批判式自审计：把「全绿」与「真的跑了」的差额量化

- 日期：2026-10-01
- 基线：`origin/main` = `39cd45dbf`（本轮开始时 HEAD 落后 2 个 commit，已 `git fetch` 核对）
- 方法：harness 实跑 + 变异验证 + 端到端复现
- 纪律：报告必须区分「全绿」与「真的跑了」；没证明的事不写成已完成

> ⚠ **本报告所有数字的测量基线**：本地 HEAD `d2af305a0` + 本轮工作区。
> 撰写期间 `origin/main` 又前进到 `8db46f9cd`（并发的压缩/handoff/ir 提交）。
> **本轮没有合并 `origin/main`，因此这些数字不构成对最新 main 的结论**；
> 按既有纪律，合并后必须重跑全量才算数。
> 已核对：上述 7 个改动文件在 `origin/main` 上均未被他人改动，提交无冲突。

---

## 0. 一句话结论

上一轮把 harness 跑通了两个包。本轮把它跑遍全部 **28 个含 integration 测试的包**，
拿到 **6083 PASS / 99 SKIP / 60 FAIL**——**13 个包全绿，14 个包红，1 个包是构建不过的
空跑**。也就是说：**「integration 全绿」这个说法在两包样本上成立，在全仓上不成立。**

同时查实了三件上一轮判错或没判的事：

1. **19 条不落地的启动迁移是真的**（不是 harness 造成的假象），已从「噪音」变成**带棘轮的
   门**，10 项变异 + 1 项端到端复现证明它会咬人。
2. 修掉 **3 个真测试 bug**（其中一个是跨文件污染，一个测试文件的坏清理静默弄坏了另一个）。
3. 挖出 **1 个真产品 bug**：`RecordUpdateReport` 里那句「找不到 release 就用 0」的兜底
   **在现有 schema 下永远失败**，被两个独立测试点命中。

---

## 1. 全仓 sweep：28 个包的真实结果

harness 每包建一个一次性库，注入 15 个 DB 变量名后实跑 `go test -tags=integration`。

| 包 | PASS | SKIP | FAIL | 判定 |
|---|---:|---:|---:|---|
| cmd/gateway | 421 | 0 | 0 | GREEN |
| domains/session/v2 | 389 | 39 | 0 | GREEN |
| modelname | 209 | 0 | 0 | GREEN |
| internal/dbx | 78 | 0 | 0 | GREEN |
| db | 70 | 5 | 0 | GREEN |
| domains/sessionforensics | 70 | 1 | 0 | GREEN |
| domains/ursm/v2/migration | 69 | 1 | 0 | GREEN |
| domains/stats | 65 | 0 | 0 | GREEN |
| durable | 57 | 0 | 0 | GREEN |
| domains/routeincident | 48 | 2 | 0 | GREEN |
| internal/handlers | 36 | 0 | 0 | GREEN |
| **center**（本轮修后） | 15 | 0 | 0 | **GREEN**（原 11/0/4） |
| db/dbxmanifest | 7 | 0 | 0 | GREEN |
| **tests/integration** | 0 | 0 | 0 | **VACUOUS（构建不过）** |
| admin | 2396 | 29 | 17 | RED |
| bg | 1029 | 15 | 13 | RED |
| domains/dispatch | 369 | 2 | 2 | RED |
| sql/migrations/startup | 165 | 0 | 7 | RED |
| licensing | 125 | 1 | 2 | RED |
| domains/hooks/handoff | 112 | 0 | 1 | RED |
| domains/requestjourney | 100 | 3 | 1 | RED |
| internal/outbox | 97 | 1 | 2 | RED |
| internal/sessionv2mirror | 83 | 0 | 1 | RED |
| taskprofile | 35 | 0 | 2 | RED |
| **autoupdate**（本轮修后） | 26 | 0 | 4 | **RED**（原 22/0/8） |
| vibecoding | 5 | 0 | 2 | RED |
| scripts/audit | 4 | 0 | 2 | RED |
| fault | 3 | 0 | 4 | RED |
| **合计（28 包）** | **6083** | **99** | **60** | 13 绿 / 14 红 / 1 空跑 |

> `center`、`autoupdate` 两行的数字是**本轮修复后**的值；括号里是修复前。
> `durable`、`admin` 是重跑后的值（原因见 §5）。

### 1.1 「全绿」的 13 个包里，有多少是真跑了

13 个全绿包合计 1594 PASS。但其中 `domains/session/v2` SKIP=39、
`domains/sessionforensics` SKIP=1、`domains/ursm/v2/migration` SKIP=1 —— 按 harness 自己的
标准，这些只能报「**绿但有跳过**」，不能报全绿。真正 0 SKIP 的只有 9 个包。

### 1.2 `tests/integration` 是空的，而且空了两个月

```
tests/integration/protocol_e2e_test.go:353:23: undefined: transformation.NewIRTransport
```

- `NewIRTransport` **全仓不存在**（grep 零命中），不是被改名。
- 该文件最后一次改动是 `193189c75`（2026-08-03），**已断 59 天**。
- 为什么没人发现：文件带 `//go:build integration`，所以无 tag 的 `go build ./...` 和
  `go vet ./...` 都看不见它；只有 `integration-testcontainers` 那条 job 的
  `go test -tags=integration ./...` 会看见——而那条 job 从上一轮判定起就是**常年红**的
  （不注入 DB URL，67 个文件里 27 个 SKIP、至少 1 个必然硬失败）。
  **常年红的门和没有门在观察上不可区分。** 这正是本轮要记的第一条教训。

harness 把它正确判成 VACUOUS 并 exit 3（而不是报「0 失败 = 通过」），这是门禁该有的行为。

---

## 2. P0：19 条启动迁移——判为真缺口，并装上棘轮

### 2.1 先证伪「是 harness 造出来的」

上一轮把这 19 条记为「已知缺口」，但没证明它不是 harness 自己造出来的。
harness 当时有两个可疑点，本轮查实：

| 可疑点 | 查证结果 |
|---|---|
| harness 用 `sql/schema/01-schema.sql`，installer 用 `embeddata/01-schema.sql` | **两个文件不同**：除空白外差 4 个对象，installer 那份**缺** `bump_credentials_governor_revision()`、`notify_credentials_governor_revision()`、`credentials_governor_revision_seq`、`credentials_revision_idx` |
| harness 完全没应用 `02-seed.sql` | installer 确实会应用（`00-prereqs → 01-schema → 02-seed`） |

于是跑了**真正的 installer 路径**（用 installer 自己 embed 的三个文件 + 全部 173 条注册启动迁移）：

```
00-prereqs ok / 01-schema ok / 02-seed ok
startup: applied=154 failed=19        ← 与 harness 路径逐条相同
relations=421  indexes=1306
```

**同样 19 条、同样的报错文案。** 结论：19 条是真实的新鲜安装缺口，harness 无罪。

> 附带查实（不是本轮引入，但也没人记过）：`STARTUP 迁移链不是自足的**。
> 纯从零跑（只有 prereqs + 173 条启动迁移、不应用基线）会挂 **102/173**，
> 最终只有 64 relations / 216 indexes。所以 installer 依赖基线是设计如此，不是缺陷——
> 但它意味着「注册启动迁移」这个列表**单独不构成可安装的 schema**，谁要复现全新安装必须带上基线。

### 2.2 从「噪音」变成「带棘轮的门」

原来 harness 每次都把这 19 条列出来、但**从不判红**。这既不是门也不是记录——它是噪音：
清单可以无声地长到 100 条，没有任何一次构建会因此变红。

本轮改成 `sql/schema/startup_known_gaps.tsv`（19 行，每行 `文件名<TAB>原因`），
harness 行为变成：

- **未登记的失败 → 致命退出**（rc=2，并点名是哪一条）
- 登记了但本轮没复现 → 报「stale，请从清单删除」（不致命：真修好了不该被 paperwork 挡住）
- 清单缺失 / 解析出 0 条 → 致命退出

关键的不对称性：**豁免是逐条枚举的**，所以删掉整个清单不会让门变松，
只会让每一条缺口都变成致命。清单无法静默退化成一张万能免责条。

### 2.3 验证（静态 10 项 + 端到端 1 项）

| 变异 | 期望 | 结果 |
|---|---|---|
| M1 未登记缺口从 die 改成 echo（**核心棘轮**） | 红 | ✅ 经「未登记的启动迁移失败没有致命退出」 |
| M2 `grep -qxF` 降级成 `grep -q`（前缀撞车） | 红 | ✅ |
| M3 清单绑定变量改名 | 红 | ✅ |
| M3b 绑定了但清单从没被读 | 红 | ✅ |
| M4 去掉 stale 检测（清单只进不出） | 红 | ✅ |
| M5 清单清空 | 红 | ✅ 经「已知缺口清单为空」 |
| M6 某行真的丢掉原因 | 红 | ✅ |
| M7 清单指向未注册的文件 | 红 | ✅ |
| M8 TSV 分隔符被换成空格 | 红 | ✅ |
| N1 只改注释（反向对照） | 绿 | ✅ |
| **端到端**：从清单删掉 `622` 后真跑 harness | rc=2 | ✅ 点名 `622_provider_error_aggregator_state.sql` |

M3 值得单说：守卫第一版只判 `strings.Contains(act, "GAP_MANIFEST")`，
**变异证明它是假绿**——把变量改名后守卫仍然绿，因为 `GAP_MANIFEST` 这个字符串还出现在两条
`die` 消息里。**裸子串分不清「绑定」和「提及」。** 改判 `GAP_MANIFEST="$REPO_ROOT/..."` 的
赋值形态，并额外要求它真的被 `sed` 消费，M3/M3b 才都转红。

---

## 3. P0：`RecordUpdateReport` 的兜底是死代码（**真产品 bug，本轮未修**）

`autoupdate/store_pgx.go:361-367`：

```go
// 1. 查找 release_id（通过 to_version）
if err := tx.QueryRow(ctx, releaseQuery, report.ToVersion).Scan(&releaseID); err != nil {
    // 如果找不到 release，使用 0（允许主控端先上报，release 记录稍后创建）
    releaseID = 0
}
```

但 schema 是：

```sql
release_id bigint NOT NULL,
CONSTRAINT instance_release_status_release_id_fkey
  FOREIGN KEY (release_id) REFERENCES public.releases(id) ON DELETE CASCADE
```

`releases_id_seq` 从 1 开始，**永远不存在 id=0 的行**。所以注释里承诺的
「允许主控端先上报、release 记录稍后创建」这条路径**在当前 schema 下不可能成立**，
每次都撞 FK 违例（SQLSTATE 23503）。

调用方 `cmd/license-authority/update_report_handler.go:66` 把这个错误直接
`return echo.NewHTTPError(http.StatusInternalServerError, ...)`，
**上报丢失，实例侧拿到 500**。

**两个独立测试点同时命中**，说明不是某个夹具 contrived：

- `TestPgxStore_RecordUpdateReport/rollback_report`（`to_version = v1.4.0`，库里没有）
- `TestUpgradeRetry/RetryMechanism`（autoupdate_integration_test.go:467）

### 为什么本轮不直接改

两个修法都要动别人正在动的东西：

- **(a) 忠于原意**：`release_id` 改 nullable（迁移 + 注册 + 基线重生成）。并发会话此刻正在
  `db` / 迁移链上高提交，且基线是生成产物——现在动会撞车。
- **(b) 最小且安全**：找不到 release 时返回一个可识别的错误，让 handler 回 409/422 而不是 500，
  并把「先上报后建 release」明确标为不支持。代价小，但**改变了 API 语义**，属于产品决策。

我倾向 (a) 才是原设计意图，(b) 是止血。**这个取舍需要拍板，不该由审计轮单方面决定。**
在那之前，这两个测试保持红——**这是刻意的：它们是这条 bug 的哨兵，不该被改绿。**

---

## 4. 修掉的 3 个真测试 bug（全部经变异验证）

这三个都是「写下来就红、但从来没红过」——因为没有 DB URL 时整个文件 skip，
CI 那条 integration job 又常年红，没人分得清。

### 4.1 `center` 大小写（`CommandWithArgs`）

夹具写 `"Script executed successfully"`，断言查 `"Successfully"`（大写 S）。
**同一个测试的两半互相矛盾，永远不可能通过。** 改为断言精确往返
（`assert.Equal(t, result.Output, executed.Result.Output)`）——既表达真实意图
（Output 能穿过写/读往返），也不依赖夹具散文的大小写。

### 4.2 `center` 切片差一位（`AggregateStats`）

```go
if inst.InstanceID[:18] != "test-instance-stats" {   // 字面量是 19 字符
```

`"test-instance-stats"` 长 **19**，`[:18]` 得到 `"test-instance-stat"`，**比较永不成立**，
所有实例被跳过、合计恒为 0，断言失败。顺带是个潜在 panic：任何短于 18 字符的
`instance_id` 会直接越界。改为 `strings.HasPrefix(inst.InstanceID, "test-instance-stats-")`。

### 4.3 `autoupdate` 跨文件污染（本轮最隐蔽的一个）

`autoupdate_integration_test.go` 的清理删的是：

```go
DELETE FROM autoupdate_upgrade_logs ... / autoupdate_gray_rules ... / autoupdate_releases ...
```

**这三张表在 schema 里根本不存在**，产品代码也从未引用过（store 是 `*PgxStore`，
写的是 `releases` / `upgrade_logs` / `gray_release_rules` / `instance_release_status`）。
所以三条 DELETE 全部报错并被 `_, _ =` 吞掉，**这些子测试真正写入的行一行都没清掉**。

危害不局限于本文件：漏掉的行留在 `releases` 的 `ChannelStable` 上、build_seq 很高，
于是 **另一个文件的** `TestPgxStore_GetLatestReleaseAfter` 捡到了它们，三个子测试连败。
**一个测试文件的坏清理，静默弄坏了另一个测试文件。**

另一条同源隐患：清理复用了测试体那个 60s 超时的 `ctx`，慢跑时 ctx 已过期，
每条 DELETE 都会因 context 取消而失败、再次漏行——同一个静默失败换了个路径。
现在清理用**自己的 context**，并且**失败时打日志而不是吞掉**（当初就是被吞掉才没被发现）。

修完跑出来的实测（`t.Logf` 直接打出删了几行）：

```
autoupdate cleanup: DELETE FROM upgrade_logs ...             -> 2 row(s)
autoupdate cleanup: DELETE FROM gray_release_rules ...       -> 2 row(s)
autoupdate cleanup: DELETE FROM instance_release_status ...  -> 1 row(s)
autoupdate cleanup: DELETE FROM releases ...                 -> 10 row(s)
```

**这 10 行以前一行都没删掉过。**

### 4.4 第四处同源：子测试里的单行清理

写守卫时顺带扫出来的：`TestUpgradeRetry` 结尾还有一条

```go
_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_upgrade_logs WHERE id = $1", logID)
```

同样是那张不存在的表，同样被 `_, _ =` 吞掉。改成 `upgrade_logs`（真实表有 `id` 列），
并同样把吞掉的错误改成 `t.Logf`。

> 顺带一个诚实的边界：这条清理在测试**失败**时仍不会执行，因为上面的
> `require.NoError` 失败会 `FailNow` 中止子测试。所以它不能替代外层清理，
> 外层清理才是真正兜底的那一层。

顺带修掉第五个：同文件的 `VersionComparison` 建了 release 却**从不发布**，
而 `GetLatestRelease` 过滤 `published_at IS NOT NULL`，于是查到 0 行。
姊妹夹具 `store_pgx_test.go` 每次 `CreateRelease` 后都调 `UpdateReleaseStatus(id, true)`，
这个漏了。

### 4.5 给这一类 bug 补守卫（`autoupdate/testschema_guard_test.go`）

上面四处都是同一个形状：**测试 SQL 引用了一张 schema 里不存在的表，错误被
`_, _ =` 吞掉，于是清理静默变成 no-op。** 修完代码不补守卫，下次照犯。

新守卫是纯源码分析、无需数据库、不带 build tag：

- `TestTestCleanupTargetsRealTables`：用 `go/ast` 取出测试文件里的 SQL 字面量，
  抽出关系名，逐个对照 `sql/schema/01-schema.sql` 解析出的 474 张表。
- `TestGuardIsNotSatisfiedByItsOwnComment`：剥掉注释后，源码里不得再出现那三个幽灵表名。
  这条是元守卫——**本轮至少有两次守卫被「只是提到目标名字的文本」满足**，
  修复的注释里恰好就写着 `autoupdate_releases`，所以必须有这条。

**这个守卫的判据迭代了四次才站住**，过程本身就是本轮主题的又一次重演：

| 版本 | 判据 | 结局 |
|---|---|---|
| v1 | 对源码做不分大小写的 `FROM\|JOIN\|INTO\|UPDATE` 匹配 | 误报 3 条：英文散文里的 "rows **into** later tests"、"This **update** must be applied"、"Mandatory **update** created" |
| v2 | 「字面量**含有** SELECT/DELETE/INSERT/UPDATE」 | 同样 3 条误报——一句含 "update" 的话就能通过这种过滤 |
| v3 | 「字面量是 `pool.Exec/Query` 的参数」 | 语义最接近，但**只查到 1 条**：要守的四条清理在 `[]string{...}` 切片里，是变量传进 Exec 的，参数位置规则看不见它们 |
| **v4** | 「字面量**以** SQL 语句关键字**开头**」 | ✅ 查到 6 条，且散文全部排除 |

v3 那次是被**「0 条引用」的自检**抓住的——如果没写那条自检，一个恒返回 0 条的守卫
就会带着全绿上线。这正是「0 findings 必须钉住扫描量」那条纪律的又一次应用。

### 4.6 变异验证

| 变异 | 期望 | 结果 |
|---|---|---|
| 退回 `[:18]` 写法 | 红 | ✅ 经 `TestDashboardStats`（PASS=13，确实跑了） |
| 退回大小写敏感断言 | 红 | ✅ 经 `CommandWithArgs`（PASS=13） |
| 退回删不存在的三张表 | 红 | ✅ 经 `TestPgxStore_GetLatestReleaseAfter`（PASS=24） |
| 去掉 `VersionComparison` 的发布调用 | 红 | ✅ 经 `VersionComparison`（PASS=24） |
| G1 把外层清理改回 `autoupdate_releases` | 红 | ✅ 经「不存在的表」 |
| G2 把子测试清理改回 `autoupdate_upgrade_logs` | 红 | ✅ 经「不存在的表」 |
| G3 在**普通 Go 字符串**里放幽灵表名（非 SQL，表引用守卫看不见） | 红 | ✅ **仅**元守卫报红 —— 证明两个守卫互相独立且都有牙 |
| N1 只改注释措辞（反向对照） | 绿 | ✅ |

修后：`center` 11/0/4 → **15/0/0**；`autoupdate` 22/0/8 → **26/0/4**，
剩下的 4 个 FAIL 全部是 §3 那一个产品 bug 的两个命中点，**刻意保持红**。

---

## 5. 本轮我自己制造的一次污染（必须记）

我一边让 sweep 在后台跑 28 个包，一边对 `scripts/audit/run-integration-gate.sh`
做变异（故意改坏）。**两者操作同一个文件。** 后果：

- `durable` 那一次跑出来的日志里有 `行 250: green: 未找到命令` 和
  `行 312: 未预期的记号 "fi" 附近有语法错误` —— bash 是**按字节偏移惰性读脚本**的，
  文件被换掉后它读到了错位的内容。
- 该包结果作废，重跑得到 `57 PASS / 0 FAIL`。

**规则：原地变异共享文件，只能在树静止时做。** 现在
`/tmp/mutate-fixes.sh` 开头会 `pgrep -f run-integration-gate` 拒绝执行。

事后我给全部 28 份日志做完整性体检（每个包必须有 `[populated]` / `[dsn]` / `summary` 三个标记），
确认**只有 `durable` 一份被污染**，其余 27 份有效。

> 顺带记一个我自己当场犯的体检 bug：第一版写成
> `p=$(grep -c "\[populated\]" "$f") || echo 0`——`grep -c` 在零匹配时**既打印 `0` 又
> 退出码 1**，`|| echo 0` 于是追加第二个 `0`，`p` 变成 `"0\n0"`，比较永不成立，
> 体检静默什么都没查。修法是判退出码而不是判字符串。
>
> 还有一次我把条件写反（`pr -ne 0` 意为「grep 找到了」），结果 26 个完整包被误报为 INCOMPLETE。
> 两次都是同一个错误类型：**判据的谓词选错了，于是门恒真或恒假。**

---

## 6. `admin` 的 VACUOUS 是并发编辑，不是缺陷

第一轮 `admin` 报 `PASS=0 FAIL=0` + VACUOUS，日志是：

```
admin/errors_trend.go:28:2: "…/internal/jsoncol" imported and not used
admin/errors_trend.go:348:7: undefined: json
```

一度像是个 P0 构建断。查证：该文件 mtime `04:49:53`，正是 sweep 跑到 `admin` 的时刻；
当场 `go build -tags=integration ./admin/` **通过**。是并发会话正在改这个文件、
sweep 撞上了中间态。**重跑：`2396 PASS / 29 SKIP / 17 FAIL`。**

判据是「现在能不能编译 + 文件 mtime 与 sweep 时刻是否吻合」，不是「日志里有没有报错」。

---

## 7. 结构性问题：两套互斥的 integration 夹具（**本轮最重要的架构发现**）

把 68 个 integration-only 文件按「是否自己建表」粗分：

- **自建 schema（需要空库）**：32 个文件
- **假定已迁移的 installer 形态库**：36 个文件

`bg` 的 13 个失败里，绝大多数是 `relation "X" already exists (42P07)`——
这些测试自己 `CREATE TABLE`，而 harness 给的是一个已经 421 relations 的库。

**一个 harness 只有一种库形态，所以它天然只能服务其中一半。** 这解释了整轮的核心矛盾：
现有 CI job（testcontainers 空库）适合前者、不适合后者；我这轮 harness（installer 形态库）
适合后者、不适合前者。**在两种夹具形态拆开之前，「integration 门」没有唯一解。**

> 我第一版的分类是按 grep `CREATE TABLE` 粗分，`internal/dbx` 被分到「自建」但它在
> installer 形态库上是绿的——**说明这个分类不准**。真值是本轮 28 包的实测结果，不是我的 grep。
> 任何要据此改造的设计，都应该先以实测为准重新分类。

---

## 8. 本轮**没有**证明的事

1. **CI 上跑过一次 integration 门** —— 仍然没有。job 已移除，前置条件见
   `2026-10-01-integration-gate-ci-preconditions.md`。
2. **基线生成器产物 apply 到空库 exit=0** —— 仍无测试保证（上一轮只手工验过一次）。
3. **`tests/integration` 两个月没编译过** —— 已定位到 `undefined: transformation.NewIRTransport`，
   **本轮未修**（需要决定是补 `NewIRTransport` 还是删/改这份夹具，属于功能取舍）。
4. **§3 的产品 bug** —— 已定位、已双点复现，**未修**，等 schema 语义拍板。
5. **14 个红包的失败是否都是真缺陷** —— 本轮只深挖了 `autoupdate` / `center` / `bg`(形态)
   三处。`sql/migrations/startup`(7)、`admin`(17)、`licensing`(2)、`internal/outbox`(2)、
   `fault`(4) 等**尚未逐条定性**，不要默认它们和已修的那几个同性质。
6. **基线双份漂移（4 个对象）** —— 已记录在 `startup_known_gaps.tsv` 头部，**未修**。

---

## 9. 给下一轮的优先级

1. 🔴 **拍板 §3 的 `release_id` 语义**，然后修它并让两个哨兵测试转绿。
2. 🔴 **修 `tests/integration` 的构建断**（59 天了），或明确删除这份夹具。
3. 🔴 **两套夹具形态拆开**：给每个 integration 文件一个声明（自建 / 假定已迁移），
   harness 据此建对应形态的库。这是让「integration 门」成为可能的前提。
4. 🟡 **逐条定性剩下 11 个红包**，别让「14 个红」长期作为一个无人认领的总数。
5. 🟡 **CI 前置**：落实 amd64 `kx-citus-pg17` 镜像，把状态行翻成 SATISFIED。
6. 🟡 **基线双份统一**：`sql/schema/01-schema.sql` 与 `embeddata/01-schema.sql`
   应当同源生成，并给生成器加真库门。
7. 🟢 `sql/objects/` 定位（承重 SSOT vs 部署惰性 vs 双向漂移 31/618）—— 仍未决。

---

## 10. 本轮新增的变异验证记录

| 变异 | 期望 | 结果 |
|---|---|---|
| 启动缺口棘轮：未登记 → 警告 | 红 | ✅ |
| `grep -qxF` → `grep -q` | 红 | ✅ |
| 清单绑定改名（杀裸子串假绿） | 红 | ✅ |
| 清单绑定但不读 | 红 | ✅ |
| 去 stale 检测 | 红 | ✅ |
| 清单清空 | 红 | ✅ |
| 清单行丢原因 | 红 | ✅ |
| 清单指向未注册文件 | 红 | ✅ |
| TSV 分隔符换成空格 | 红 | ✅ |
| 只改注释（反向对照） | 绿 | ✅ |
| **端到端**：删掉 622 后 harness 实跑 | rc=2 | ✅ |
| center 退回 `[:18]` | 红 | ✅ |
| center 退回大小写断言 | 红 | ✅ |
| autoupdate 退回删不存在的表 | 红 | ✅ |
| autoupdate 去掉发布调用 | 红 | ✅ |
| 外层清理改回 `autoupdate_releases` | 红 | ✅ |
| 子测试清理改回 `autoupdate_upgrade_logs` | 红 | ✅ |
| 普通 Go 字符串里放幽灵表名（只有元守卫能抓） | 红 | ✅ |
| 只改注释措辞（反向对照） | 绿 | ✅ |

合计 **19 项变异验证**（10 项棘轮 + 4 项测试修复 + 4 项新守卫 + 1 项端到端复现），
全部按预期转色。

### 五个「守卫本身是假绿 / 验证脚本本身有 bug」的记录

1. **M3 证明 `Contains(act, "GAP_MANIFEST")` 是假绿**——变量改名后守卫仍绿，因为该字符串
   还出现在两条 `die` 消息里。裸子串分不清绑定与提及。已改成锚赋值形态。
2. **变异脚本第一版把 no-op 当成真变异**——备份路径写错（`$BK/$(basename $file)`
   对上 `gate.sh`），`cmp` 永远看到「文件不存在」于是永远判定「已改变」。
   三个假通过全部由此而来。已改成每例显式传备份路径，并让 python 在锚点没命中时
   `exit 9` 判为 **VOID 案例而非通过**。
3. **变异检查器把构建失败叫成「仍然绿」**——C1 只查失败子测试名，没看 PASS；
   退回 `[:18]` 后 `strings` 变成未使用导入 → 构建失败 → `PASS=0` → 被判 GREEN。
   已加：`PASS==0` 单列为 VACUOUS 判定，且该案例判为无效。
   **这正是本轮主题「全绿 ≠ 真的跑了」在我自己的工具里重演了一次。**
4. **新守卫的判据错了三次**（散文误报 → 切片看不见 → 以关键字开头才对），
   v3 那次是被「0 条引用」自检抓住的。
5. **G3 第一次瞄错了目标**——我把幽灵表名加进守卫文件，而元守卫读的是**测试**文件，
   于是报了「守卫是假的」。实际是变异瞄错，不是守卫假。重新瞄到测试文件里的
   普通 Go 字符串后，元守卫如期报红。**「守卫是假的」和「我测错了地方」必须分开。**

> 这五条加起来是本轮最贵的一课：**写守卫的成本不在写，在于证明它会响。**
> 本轮 19 项变异里有 6 项第一次跑是错的（3 项脚本 bug、1 项瞄错目标、2 项判据本身有洞），
> 如果不做变异验证，这 19 项里至少有 4 项会以「已验证」的名义进入仓库。
