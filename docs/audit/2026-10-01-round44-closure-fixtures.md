# §9-3 夹具形态：本轮交付了机制，也**推翻了这项任务自己的前提**

- 日期：2026-10-01（续 Round 44 收口轮）
- 基线：`origin/main` = `e3406f9e2`（已含 R44 收口 + 他人对那 8 笔的独立复核）
- 分支：`audit/r44-fixture-shapes`
- 承接：`docs/audit/2026-10-01-round44-closure.md` §7 的 §9-3

---

## 0. 一句话结论

§9-3 写的是「两套夹具形态拆开：给每个 integration 文件一个声明，
harness 据此建对应形态的库」。

**我照着做了，然后实测发现这个前提是错的**：两种夹具不是按包分开的，
而是**混在同一个包内部**的。已测的 8 个包**没有任何一个**在任一形态下全绿，
其中一个（`db`）在 prereqs 形态上直接**挂死**。

所以本轮的交付不是「拆开了」，而是：

1. harness 具备按形态建库的能力（`GATE_DB_SHAPE`，三种形态）；
2. 形态登记表 + **逐形态实测失败数**（防止下一个人把「选了某形态」误读成「变绿了」）；
3. 形态不匹配**被点名**（42P07 / 42P01 不再原样抛给读者）；
4. 一条**经过四轮变异**才站住的守卫；
5. 一份**否证结论**：整库形态是错的轴，真正的解法是 per-test schema 隔离。

---

## 1. 实测：换形态不是修复

同一台机器、同一个 `llm_gateway-pg`、同一份 harness，只改 `GATE_DB_SHAPE`。
`prereqs` 形态实测 `relations=4`（几乎全空，正是自建表族需要的起点）。

### 1.1 两种形态都测过的包

| 包 | installer FAIL | prereqs FAIL | 42P07 |
|---|---:|---:|---|
| `taskprofile` | 2 | **1** | 2 → 1 |
| `bg` | **14** | VACUOUS（0 PASS） | 9 → 7 |
| `sql/migrations/startup` | 6 | **5** | 3 → 2 |

### 1.2 只测了 prereqs、与上一轮 installer 全量对比的包

| 包 | installer | prereqs |
|---|---|---|
| `admin` | 2425 PASS / **11** FAIL | 2372 PASS / **66** FAIL |
| `autoupdate` | 112 PASS / **0** FAIL | 90 PASS / **16** FAIL |
| `center` | 15 PASS / **0** FAIL | 5 PASS / **10** FAIL |
| `cmd-gateway` | 423 PASS / **0** FAIL | 421 PASS / **0** FAIL（唯一没变差的） |
| `db` | 73 PASS / **0** FAIL | **挂死** |

> **`db` 在 prereqs 形态上挂死**：日志在 23:29:16 停止增长，测试进程停在
> 0% CPU，手工杀掉。比「失败」是更强的负面信号——某个 `db.ensure*` 用例在
> 空库上持锁、另一个等锁。它是「这个形态不安全」的又一条证据，
> 不是「再等等就好」。

### 1.3 测量范围（照实说，不往宽里读）

全量 27 包 prereqs 扫描**跑到 6/27 主动停掉**，原因就是上面那个挂死。所以：

- 两种形态都测过：**3 个包**
- 只测 prereqs 并与上一轮 installer 对比：**5 个包**
- **其余 19 个包未在 prereqs 上测过**——不要替它们假设数字

结论由已测的 8 个包支撑，**没有外推到未测的 19 个**。登记表与本节都按这个范围写。

**读法**：`prereqs` 确实消掉了一两个 42P07，但**同一个包里**另一半测试
假定库已迁移，于是新增了别的失败（`relation ... does not exist`）。
**换形态只是把失败挪位置。**

> **这否证了 R44 §7 的隐含前提**。R44 写「两套互斥的 integration 夹具」，
> 读起来像 32 vs 36 的干净二分。R44 自己其实已预警过它的分类不准
> （「真值是本轮 28 包的实测结果，不是我的 grep」），但结论仍写成了二分。
> 实测结论是：**二分不成立，粒度错了。**

---

## 2. 为什么整库形态是错的轴

harness 的调用单位是**包**，而夹具的分野比包更细。要让自建表族和已迁移族
在同一趟运行里共存，正确的隔离单位是**每个测试自己的 schema**：

```sql
CREATE SCHEMA t_<random>;
SET search_path = t_<random>, public;
```

这样它既不撞基线，也不需要第二个数据库——**在同一个满库里就能跑**。

仓内目前**没有**这个共享 helper，只有三处手搓 `CREATE SCHEMA`：

- `tools/benchreport/main.go`
- `domains/reportrollup/grainreport_e2e_test.go`
- `sql/migrations/startup/migration_536_540_integration_test.go`

**我没有在本轮建它。** 理由：它要迁移约 30 个文件，并逐个处理写死 `public.`
限定符的语句（`search_path` 治不了限定符），是独立的一批工程。
硬塞进一个「收口轮」会重演 R44 提醒过的撞车。

登记表 `sql/schema/integration_fixture_shapes.tsv` 的文件头把这个结论、
实测范围、以及后续路径全部写清楚了。

---

## 3. 交付的机制

### 3.1 `GATE_DB_SHAPE`（三种形态）

| 形态 | 建库方式 | 实测 relations | 服务谁 |
|---|---|---:|---|
| `installer`（默认） | prereqs + 基线 + 198 条启动迁移 | 435 | 假定已迁移、读生产形态数据的测试 |
| `baseline` | prereqs + 基线 | ~328 | `db.ensure*()` 族 |
| `prereqs` | 仅 prereqs | 4 | 自建 schema 的测试 |

population floor **按形态分档**（400 / 300 / 0）。用 installer 的地板去卡
`prereqs` 形态，会把「刻意为空」误判成「起始库没建全」——同一个错误在
另一个方向上的形状。

默认值仍是 `installer`，所以**加一张登记表本身不改变任何既有包的建库方式**；
没有登记行的包继续走 installer。登记表可以逐行填，不是一次性生效。

### 3.2 形态不匹配被点名

原来 `bg` 的失败输出是一坨 `relation "X" already exists (42P07)`，读起来
**完全像产品缺陷**——R44 §7 说这个分裂「隐了一个月」，原因就在这里。

现在 harness 会直接说：

```
── 形态不匹配诊断 ──
  本形态 shape=installer 已被本包自己的 CREATE TABLE 撞了 9 次
  （SQLSTATE 42P07 relation ... already exists）。这说明该测试自建 schema，
  而门禁库是满的。它不是产品缺陷。
  ⚠ 实测（2026-10-01）已测的 8 个包里没有任何一个在两种形态下都全绿，
    本包内两种夹具是混着的；换形态通常只是把失败挪位置，不是修复。
```

反向也诊断（`prereqs` 形态下大量 `relation ... does not exist`）。

**注意措辞**：它明确写了「换形态不是修复」。一个只会说「请用 X 形态重跑」
的诊断，会诱导下一个人相信重跑就好了。

### 3.3 守卫 `TestGateSupportsPerPackageFixtureShapes`

钉住：三种形态可选 + 默认 installer + 登记表被**绑定**且被**消费** +
诊断文案在位 + population floor 分档 + 登记表每行必须带**两个实测失败数**。

---

## 4. 这条守卫自己先红了两次（必须记）

第一版有两处**假绿**，是变异测试逼出来的，不是我想出来的。

### 4.1 空变异（又一次）

第一次用 `python3 -c` 做变异，模式串里写了 `\$GATE_DB_SHAPE`。
双引号里的 `\$` 被 bash 吃掉，python 拿到的模式与文件不匹配，
**替换是 no-op，门照常绿**。

如果当时把「门是绿的」读成「门没承重」，下一步就会去**放宽判据**——
真正的问题在变异脚本里。这与 R44 §10 第 2 条、
以及 V7i「备份判重」同族：**装置坏了，输出却是一个干净的绿灯。**

**改法**：变异一律走 heredoc（不经 shell 插值），并且**先确认变异真的落进文件**
（打印替换前后的出现次数），再看门色。

### 4.2 真·假绿：裸子串分不清「绑定」与「提及」

改用 heredoc 重做，变异确实落进去了（出现次数 1 → 0），**门还是绿的**。

第一版断言写成 `strings.Contains(act, "integration_fixture_shapes.tsv")`，
但那个字符串**同时出现在 echo 文案和注释里**。把 `SHAPE_MANIFEST` 改成
`/dev/null`（登记表真的不再被读），门照样绿。

population floor 那条更隐蔽：原断言查 `GATE_DB_SHAPE" != "prereqs"`，
而**形态诊断块里同样的条件出现了两次**——删掉 floor 块，诊断块还在，
断言照样被满足。

> **这与 R44 §2.3 记录的 M3 是同一个坑**：
> `Contains(act, "GAP_MANIFEST")` 证明是假绿，因为变量改名后该字符串
> 还出现在两条 `die` 消息里。**R44 记了，我写新守卫时又踩了一次。**
> 纪律的正确形态不是「知道要小心」，而是**断言必须锚在唯一的语法形态上**。

**改法**：断言绑定形态而非裸子串 ——
`SHAPE_MANIFEST="$REPO_ROOT/.../integration_fixture_shapes.tsv"`（赋值形态）
+ 单独断言它被 `-f "$SHAPE_MANIFEST"` 消费；population floor 断言那张
`case` 分档表本身（`installer) GATE_MIN_RELATIONS=` 等三行），
那三行在整个脚本里只有一处。

### 4.3 修正后的变异记录

| 变异 | 期望 | 结果 |
|---|---|---|
| M1 把 per-shape floor 分档表合并回单一全局值 | 红 | ✅ 三条缺失逐条点名 |
| M2 删掉形态不匹配诊断块 | 红 | ✅ 点名缺 `already exists` |
| M3 `SHAPE_MANIFEST` 改指 `/dev/null`（登记表不再被读） | 红 | ✅ 点名未绑定 |
| 恢复 | 绿 | ✅ |

**每一项都先确认变异落进文件，再读门色。**

### 4.4 附带一次自伤：把报告写空了

改报告时用 `open(p,'w').write(s.replace(...))` 做替换。Python 的
`open(p,'w')` **在打开的瞬间就截断文件**，而我给 `write()` 多传了一个
`encoding=` 关键字，抛 `TypeError`——**截断已发生，数据没写进去**，
文件变成 0 字节，且未跟踪，git 无法恢复，只能重写。

正确写法：**先在内存里算好新内容并断言成功，再一次性写入**；或者写临时文件
再 `os.replace`。

> 与 §4.1 同一族：**一次失败的写操作，破坏发生在失败之前**。
> 读-改-写必须让「改」全部成功之后才触碰文件。

---

## 4.5 顺带修掉上一轮我自己留下的真缺陷：登记迁移是**三处**动作

写这一节时我在 `ps` 里瞥见另一个会话在跑
`go test ./cmd/llm-gw-installer/ -run TestStartupFilesAreAllEmbedded`，
当时没跟进。后来该会话的 handoff 记下了结果：
**那条测试在干净的 `origin/main` 上是红的，而红的原因是 808/809。**

上一轮（R44 收口）登记 808/809 时只接了两处：

1. `embeddata/startup/` 里的文件 ✅
2. `installer/internal/dbinit/runner.go` 的 `StartupFiles` ✅
3. `installer/cmd/llm-gw-installer/main.go` 的 `go:embed` 变量
   + `embeddedSQLFiles` 映射 ❌ **漏了**

后果比「测试红」严重：`StartupFiles` 里已经登记了这两条，
但 installer 的 `setupSQLDir` 找不到文件——**fresh-install 路径会静默漏掉
它们**。而门禁 harness 读的是磁盘上的 `embeddata/startup/`，
所以 198 条 applied 里包含 808/809，**门禁完全测不到这个洞**。

> 又一次「门测的不是它声称测的那个东西」：门禁证明的是「迁移链能应用」，
> 不是「installer 能把它打进去」。两者之间隔着 embed 这一层。

已补齐三处，复跑 `TestStartupFilesAreAllEmbedded` 为绿。

**同一个坑在仓内已有前例**，`stats_migrations_test.go` 的注释原文：
「上一版引用 …Migration803——807 由 803 改号而来时测试引用没跟上，
installer 模块测试自那起编译红。」

**规则**：新增或改号一条启动迁移 = ①embeddata 文件 ②StartupFiles
③go:embed 变量 + embeddedSQLFiles 映射。三处缺一，测试会红，
而缺 ③ 的运行时后果是**静默跳过这条迁移**。

---

## 4.6 续作：per-test 隔离 helper 落地并**证明有效**（第二轮）

上一份报告说「per-test schema 隔离是真正的解法，迁移约 30 个文件，留作独立
批次」。本轮把它做了出来，并**实测证明它比换整库形态有效**。

### 交付

- `internal/testschema`：给单个测试一个私有 schema。
  `CREATE SCHEMA` + 通过 `RuntimeParams["search_path"]` 下发
  `<schema>,public,pg_catalog`，`t.Cleanup` 里 `DROP SCHEMA ... CASCADE`。
  用 RuntimeParams 而不是 acquire 后 SET，是因为池会把连接再发给别人，
  只改一条连接等于把私有 schema 漏给别的测试。
  迁移前提是被测代码用**非限定名**——本仓这几处成立
  （`JOIN auto_route_selections_all s`），写死 `public.` 的语句则无效。
- `bg/dispatch_postgres_helper.go`：`DispatchPostgresContainer` 在
  `TEST_PG_URL` 且**传了 schema** 时改走隔离；传空 schema 的调用方要的是真实
  库，保持原样（否则会把它们的生产形态读取指向空 schema）。
- 4 个 bg 夹具 + 1 个 taskprofile 夹具去掉 `public.` 限定，让对象落在私有 schema。

### 实测

| 包 | 前 | 后 |
|---|---|---|
| `taskprofile` | 35 PASS / **2 FAIL**（42P07×2） | **37 PASS / 0 FAIL** |
| `bg` | 1040 PASS / 14 SKIP / **14 FAIL**（42P07×9） | **1044 PASS / 14 SKIP / 10 FAIL**（42P07 = **0**） |

`bg` 不再挂死。

**变异验证**（把 helper 改成不隔离，search_path 只留 public）：
`taskprofile` 立刻回到 `exit=1 / 2 FAIL / 42P07×2`。这条证明**测试确实依赖
隔离**，不是碰巧变绿。

### 修掉 42P07 之后暴露出来的三个更深缺陷

这正是「一个浅层失败后面还藏着一个」——**42P07 一直在替它们挡着**：

1. **`credentials` 夹具 NOT NULL 漂移（23502）**：
   `INSERT INTO credentials DEFAULT VALUES`，而该表已长出三个无默认值的
   NOT NULL 列（`provider_id` / `label` / `fp_slot_limit`）。补齐。
   *我第一版把 label 写死，结果撞上 `UNIQUE (provider_id, tenant_id, label)`
   （某个测试在一次里 seed 两次）——那是**我引入的新失败**，已改成每次唯一。*
2. **`availability_state='degraded'` 违反 CHECK（23514）**：
   该列的枚举是 `{ready,cooling,rate_limited,auth_failed,unreachable,suspended}`，
   `'degraded'` 属于**另一列**（`status` / `trust_level`）的枚举。
   改成 `rate_limited`。测试意图只是「同一行 N 次 UPDATE 在 debounce 窗内
   合并成一次刷新」，取值合法且确实改变行即可。
3. **listener 协程泄漏导致整包挂死**：
   `IntegrationStopReturnsPromptlyAfterRealListen` 把 `l.Stop()` 放在**成功
   路径**上（`go func(){ l.Stop(); ... }()`），中间任何 `t.Fatalf` 都会让
   LISTEN/NOTIFY 协程活下来，反复重连、刷日志，**测试二进制永不退出**——
   失败不再被汇报，它之后的测试也不再运行。
   改挂 `t.Cleanup(l.Stop)`。
   > 与 Round 44 的 autoupdate panic 同形：**一个失败让它自己不再被汇报，
   > 并吞掉其后所有测试**。那次是 panic，这次是泄漏的 goroutine。

4 个 listener integration 测试现在全绿（其中一个原先 120s 超时）。

---

## 5. 本轮没有做的事

- **没有建 per-test schema 隔离 helper**（§2）。这是 §9-3 真正剩下的部分，
  约 30 个文件的迁移量，刻意留作独立批次。
- **没有填登记表的数据行**。已测的 8 个包在两种形态下**都**不是全绿，
  填下去每行都得写「两种形态都不行」——这句话对每行都成立，
  **不携带任何路由信息**。等 per-test 隔离落地、某个包真的能被单一形态
  覆盖时再填，否则这张表只会是一张「都不行」的清单。
- **没有测 prereqs 形态下的另外 19 个包**（§1.3，如实标注，未外推）。
- **没有动 R44 §9-5 / §9-6 / §9-7。** §9-5 仍缺 amd64 镜像（CI job 依然常年红，
  本轮没有让它变绿，也没有假装它绿）；§9-6 基线双份统一仍是 R43 对账出的
  2612 vs 6354 对象；§9-7 `sql/objects/` 定位仍未决。
