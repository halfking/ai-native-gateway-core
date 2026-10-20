# R74 轮：全新安装形态门补自检 ＋ integration 门的三处过期断言

> 日期：2026-10-21
> 树：`367c71e54`（本轮起点）→ 提交见 §7
> 库：`itgate_r74_hot`（probe 脚本产物）/ `itgate_r74_integ2`（integration 门自建）/
> `itgate_r69_final`（R69 轮那个「probe 形态」库，**本轮证明它不是脚本产物**）
> DSN 集：全量（`scripts/audit/datasource_env.sh` 40 个变量名）
> 容器：`llm-gateway-pg`（本机 127.0.0.1:5432）

---

## 0. 一句话结论

★ R69 轮留下的「下一轮第一件事」查完了，结论是**两件事都不是当时以为的那件事**：

1. **「probe 库不是产品形态（缺 `session_turns_hot`）」⇒ 假命题** ——
   那个库**根本不是 probe 脚本的产物**（证据：它连 2026-07-05 就进基线的
   `providers` / `credentials` 都没有 ⇒ 它是手工搭的**子集库**）。
   ★ **最新树上，同一个脚本、同一个容器 ⇒ 链 296 条全过 0 失败 ⇒ 库形态完整**
   （`session_turns_hot` 在、108 列、与父表逐列同形）。
2. ⇒ 但**顺着这条线挖出了两个真缺陷**：
   - **F1**：安装链的形态守卫 `TestFreshInstallerSessionTurnsHotBootstrap`
     **今天真跑必然红**（3 处过期断言）⇒ 而它**从未被真跑过**（R73 已证官方 runner
     在 startup 重跑阶段就 abort）⇒ ⇒ **安装链形态今天没人守**。
   - **F2**：`TestFreshInstallRuntimeShape_EnsureChain` 是**漏检门**
     —— 自己缺关键对象时仍 PASS ⇒ R69 轮据它得出了「库形态不可信」的结论，
     门自己却一声不吭。

---

## 1. 触发

R69 轮 §9.4 留的题：

> ★ 查 `session_turns_hot` 为什么在 probe 库里缺失：① probe 脚本的启动迁移链是否漏了
> 建 hot 表的那条（bisect **同时**发现链跑完连 `session_turns_hot` 都没有 ⇒
> ⇒ **链本身就没建出来** ⇒ ★ 与 ensure 链无关）；② 然后给
> `TestFreshInstallRuntimeShape_EnsureChain` 补一条「`session_turns_hot` 必须在」的断言
> ⇒ ★ 它现在**漏检**这件事本身就该修。

---

## 2. ★★★★ 订正 R69 轮的核心结论：**量具用错了对象**

### 2.1 那个库不是 probe 脚本的产物

| 证据 | `itgate_r69_final`（R69 轮据此下结论） | `itgate_r74_hot`（最新树 · 同脚本 · 同容器） |
|---|---|---|
| `providers` / `credentials` | **缺** | 在 |
| `session_turns_hot` | **缺** | 在（108 列） |
| `schema_migrations` | **缺** | 在 |
| `request_logs_with_current_month` | **缺** | 在（118 列） |
| `_request_logs`（v1 族） | **在** | 不在（890 已退役） |

★ **`providers` / `credentials` 是 2026-07-05（`c6cce3f43`）就进基线的**
⇒ 一个「跑过基线」的库不可能没有它们
⇒ ⇒ **结论：`itgate_r69_final` 是 R69 轮二分实验时的手工子集库，不是脚本产物。**

★ 而 `_request_logs` **在**、其余新装对象**不在**
⇒ 它对应的是**一棵更早的树**（890 退役 v1 族之前的链）。

### 2.2 真正的 probe 脚本产物是完整的

最新树 + 同脚本 + 同容器（`DB_NAME=itgate_r74_hot KEEP_DB=1`）：

```
== 建一次性库 itgate_r74_hot（容器=llm-gateway-pg）==
  ✓ prereqs
  ✓ baseline
  startup: applied=296 failed=0 missing=0
```

⇒ **链 296 条全过、0 失败、0 缺文件** ⇒
`session_turns_hot` **在**（108 列，与父表 `session_turns` 108 列**四元组差集为空**）
⇒ ⇒ ★★ **「probe 脚本建的库不是产品形态」这个结论不成立。**

⇒ ⇒ ★★ **方法学第三条（本轮新增，与前两条并列）**：
**「量到的形态不对」有两种可能：① 对象真缺；② 我量的那个库根本不是被测流程的产物。**
⇒ ⇒ **判定顺序：先证明「我量的库出自哪条流程」，再谈对象缺不缺。**
★ 本轮的教训是**先判 ②、再判 ①** ⇒ 省掉了整轮基于错对象的推论。

---

## 3. ★★★★ F1：安装链的形态守卫**今天真跑必然红**（3 处过期断言）

### 3.1 触发

R73 轮已证：**官方 runner 真跑 ⇒ 在 startup 重跑阶段就 `exit=2` 中止**
⇒ 所有 `-tags=integration` 的腿**从未被执行** ⇒ ⇒
**集成门里的断言腐烂了没人知道。**

### 3.2 实测（在门自己建的库上逐条评估）

先建空库、`TEST_INSTALLER_FRESH_DB_URL` 指过去、**真跑**：

```
=== RUN   TestFreshInstallerSessionTurnsHotBootstrap
    fresh_installer_integration_test.go:107: fresh installer hot table column count check failed: got "f"
    fresh_installer_integration_test.go:263: query installer integration database: exit status 1
        ERROR:  relation "public._request_logs" does not exist
        LINE 2:    SELECT count(*) >= 0 FROM public._request_logs
--- FAIL: TestFreshInstallerSessionTurnsHotBootstrap (26.37s)
```

★ 再把门里**全部**断言对着「门自己建的库」逐条评估 ⇒ **恰好 3 条过期，其余全过**：

| # | 门里的断言 | 门钉的值 | 实测 | 判定 |
|---|---|---|---|---|
| ① | hot 表列数 | `= 106` | **108** | ★ 过期（绝对数 pin） |
| ② | current-month 视图列数 | `= 55` | 55 | ✓ |
| ③ | details 分区数 | `= 2` | **3**（当月 + 次月 + **default**） | ★ 过期（绝对数 pin） |
| ④ | 734 视图链可 plan | `SELECT … FROM public._request_logs` | **该对象已不存在** | ★ 引用了**已退役对象** |
| ⑤ | hot 表 RLS / 策略数 3 / digest 可空 jsonb 2 / 802 各条 / sessions.title+user_tags / seed 类 | — | 全部符合 | ✓ |

★ ④ 的**双重过期**值得单独记：
- 2026-10-20 那次订正把它从 `request_logs` 改成 `_request_logs`（理由：860 把族改名了）；
- ★★ **而 890（`890_drop_request_logs_v1_family.sql`，链内 index 282）把整个 v1 族真删了**
  （DROP 名单含 `request_logs` **与** `_request_logs`，连同 4 个 v1 视图）
  ⇒ ⇒ **那次订正的前提本身已被推翻** ⇒ 它写下的名字今天也不存在。

### 3.3 改法（**改瞄**，不是「把断言改绿」）

| # | 原断言 | 改成 | 为什么这么改 |
|---|---|---|---|
| ① | `count(*) = 106`（hot 表列数） | `hot 表列数 == 父表列数` | ★ 绝对列数是**必然腐烂的 pin**（104→106 已订正过一次，每加一列就红一次）；它要守的性质（hot 面是父表的完整投影）已被同门那条**四元组逐列 parity** 严格更强地覆盖 ⇒ 保留形态断言、去掉会腐烂的绝对数 |
| ③ | `count(*) = 2`（details 分区数） | **当月 + 次月分区按名字存在** | 本意是「当月与次月已预建」（否则写路径 23514）；「一共几个分区」会随 default 分区加入而变 ⇒ 按名字断言既守本意又不腐烂 |
| ④ | `FROM public._request_logs` | `FROM public.request_logs_with_current_month` | v1 族已退役 ⇒ 改读**退役后的 canonical 视图**（680 建、884 改读会话族）⇒ 口径不变，只是「读哪张表」跟到今天真正在的那张 |

★ 改后真跑：**PASS（25.46s）**。

---

## 4. ★★★★ F2：`TestFreshInstallRuntimeShape_EnsureChain` 是**漏检门**

### 4.1 现象

R69 轮拿 `itgate_r69_final` 跑这道门 ⇒ **PASS**
⇒ 而那个库缺 `credentials` / `session_turns_hot` / `schema_migrations` /
`request_logs_with_current_month` ⇒ ⇒
**门在自己缺关键对象时照样绿** ⇒ 当时据此得出的「库形态不可信」结论，**门本身一声不吭**。

### 4.2 改法：先自检量具有效性，再断言形态

```
⓪ 形态锚点自检（**必须在 db.Open 之前**）
   锚点 = ensure 链自己要改的那几张表：models_canonical / credentials /
          provider_catalog / session_turns
   缺任一 ⇒ **显式 SKIP 并说清缺哪一个**（不假装通过，也不把量具失效报成产品缺陷）

① 「装完」（还没启动）时的安装链形态
   (a) public.session_turns_hot 必须存在
   (b) hot 面与父表 session_turns 必须**逐列同形**（四元组）

② db.Open = 产品启动一次（ensure 链 + 迁移守卫）
③ ensure 自愈清单（原有 12 条列断言）
④ canonical 视图必须投影 request_class + due_at
```

★ **① 为什么必须排在 ② 之前**（这是变异跑出来的，不是想出来的）：
hot 表一旦缺失，**`db.Open` 自己就会死** ——
`rebuild request_logs_with_current_month as session-only two-arm body:
ERROR: relation "public.session_turns_hot" does not exist (SQLSTATE 42P01)`
⇒ ⇒ 若把断言排在 db.Open 之后，它在「hot 表缺失」这个场景里**根本走不到**
⇒ 只能报一句看不懂的启动失败 ⇒ ⇒ **排前面 = 量的是「装完那一刻」，且报的是人话。**

★ **④ 为什么排在 ② 之后**（与 ① 相反）：db.Open 会把 canonical 视图按
「会话族两面」形态**重建** ⇒ 视图层的形态是**启动时**决定的 ⇒ 必须在启动之后量。

★ **为什么 (b) 用四元组而不是「列名集合」**：`column_name / data_type /
is_nullable / ordinal_position` 四元组逐列相同 ⇒ 比集合相等**更强**
（列序、类型、可空性都算上）。★ 口径实测（新装库）：
`session_turns` 108 列、`session_turns_hot` 108 列，差集**双向为空**。

★ **为什么 ensure 链建不了 hot 表、所以要专门断言它**：
全仓 Go 代码里**没有任何 `CREATE session_turns_hot`**（只有测试夹具里的），
`db/db.go` 只有读它的语句 ⇒ 它是**安装链**的 bootstrap + 706b + 707 建的
⇒ ⇒ **没有任何「ensure 之后」的检查覆盖它** ⇒ 这里补上。

---

## 5. 变异矩阵（证明门有牙）

### 5.1 `db::TestFreshInstallRuntimeShape_EnsureChain`（5/5 成立）

| # | 变异 | 落地自证 | 读数 | 证明了什么 |
|---|---|---|---|---|
| **M0** | 对照：不变异 | — | **PASS** | 基线绿 |
| **M1** | DB：删掉 `session_turns_hot`（CASCADE） | 指纹 `hot=false` | **RED**「装完时就不存在」 | 存在性断言有牙 |
| **M2** | DB：父表 `ADD COLUMN` ⇒ hot≠parent | 指纹 `父列=109 / hot列=108` | **RED**「列形状不一致」 | 同形断言有牙 |
| **M3** | DB：视图把 `request_class` 改名 | 指纹 `视图request_class=0` | **RED**「只投影 1/2」 | 投影断言有牙 |
| **M4** | 源码：摘掉形态锚点自检 | 源码标记命中 | 坏库上 SKIP → **RED** | ★ 锚点自检**正是**把假红变成诚实跳过的那一步 |
| **M5** | 源码：把「同形」断言改成恒真 | 替换 1 处 | M2 库上 **PASS** | ★ M2 的红**正是**同形断言给的，不是别的东西给的 |

★ 还原：按**原路径** `md5` **逐字一致**（`4d539f665bf60681cda14341fc295cf8`），
残留变异标记 **0**。

### 5.2 `installer::TestFreshInstallerSessionTurnsHotBootstrap`

| # | 变异 | 读数 | 说明 |
|---|---|---|---|
| **N0** | 对照 | **PASS**（18.46s） | 基线绿 |
| **修复前** | 无变异 | **FAIL ×2** | ★ **「它红过」本身就是它有牙的直接证据**（106≠108 与 42P01 两条都是它自己报出来的） |
| **N1'** | 产品侧：885 不再给 hot 面加 `host`/`tags` ⇒ hot≠parent | **FAIL**（红在链应用失败） | ⚠ **读数作废** ⇒ 详见 §7.1（885 的 `COMMENT ON COLUMN` 与 hot 侧 ALTER 耦合 ⇒ 整个文件回滚） |
| **N2'** | 产品侧：733 + 893 都不建 details 次月分区 | **FAIL**：报的正是改后那条断言 | ✅ **有牙**（详见 §7） |

★ ★ **v1 版变异的两处作废（记下来，别重试）**：
- **N1（bootstrap 多加一列）**：红在 **`apply session_turns_hot_bootstrap.sql:
  exit status 3`**（链自己就应用失败了）⇒ 证明的是「链坏了会被抓到」，
  **不是**「列数断言会红」⇒ 读数**作废**，重做成 N1'。
- **N2（只改 893）**：门**照样 PASS** ⇒ 因为 **733 也建次月分区**
  （`SELECT public.ensure_session_turn_details_partition((CURRENT_DATE + INTERVAL
  '1 month')::date)`）⇒ ⇒ **变异没有移除被测性质** ⇒ 读数**作废**（阴性结论也要复核），
  重做成 N2'（733 + 893 两处都去掉）。

---

## 6. 产物

| 文件 | 改动 |
|---|---|
| `db/db_fresh_install_runtime_shape_test.go` | 形态锚点自检 + 装完形态断言（hot 存在 / 同形）+ canonical 投影断言 |
| `installer/cmd/llm-gw-installer/fresh_installer_integration_test.go` | 3 处过期断言改成形态断言（列数 vs 父表、当月+次月按名字、canonical 视图替代已退役的 `_request_logs`） |

★ **零生产写入**：全部读数在一次性库上做；产品代码**一行未改**。

---

## 7. 变异结果（N1' / N2'）

| # | 变异 | 落地自证 | 读数 | 判定 |
|---|---|---|---|---|
| **N0** | 对照：不变异 | — | **PASS**（18.46s） | 基线绿 |
| **N1'** | 产品侧：885 不再给 hot 面加 `host`/`tags` ⇒ 制造 hot≠parent | 指纹 `hot列=106 父列=106` | **FAIL**（`apply 885_…: exit status 3`） | ⚠ **读数作废**（见下） |
| **N2'** | 产品侧：733 + 893 都不建 details 次月分区 | 指纹 `details分区=2` | **FAIL**：`fresh installer 733+802 chain details partitions pre-created (current + next month) check failed: got "f"` | ✅ **有牙**（报的正是改后那条断言） |
| — | 还原后复跑 | — | **PASS**（57.91s） | 还原干净 |
| — | 6 个被改文件（885/733/893 × 两处） | — | 还原 md5 **6/6 逐字一致** | ✅ |

### 7.1 ★ N1' 为什么作废（顺带挖出一条结构性事实）

★ N1' 把 885 的 **hot 侧** ALTER 去掉 ⇒ 期望「hot 少 2 列 ⇒ 列数断言红」。
⇒ 实测**红在链应用阶段**：`apply 885_session_turns_host_tags.sql: exit status 3`
⇒ 形态指纹显示 **`hot列=106 父列=106`** ⇒ ⇒ **父表也没加上**
⇒ ⇒ 因为 885 里 `COMMENT ON COLUMN public.session_turns_hot.host`（line 88）
**与 hot 侧 ALTER 耦合** ⇒ 去掉 hot 侧 ⇒ COMMENT 报错 ⇒ **整个文件回滚**。

⇒ ⇒ ★★★★ **由此得到一条结构性事实**：
**「hot 面与父表同形」在链层面是**被三重强制**的** ——
① **707 的后置校验**（`RAISE EXCEPTION '707 post-condition failed: session_turns
hot/parent column sets diverge'`）；
② **885 的 hot 侧 ALTER 与 `COMMENT ON COLUMN` 耦合**（去掉一侧 ⇒ 整个文件回滚）；
③ **889 对 hot 侧存在性的守卫**（不存在就跳过）。
⇒ ⇒ ★★ **⇒ 因此「hot≠parent」这个状态在链层面结构性不可达**
⇒ ⇒ ★★ **⇒ 那两条同形断言是「防漂移」（防有人手动改库 / 防未来的迁移破约），
不是「防链回归」** ⇒ ⇒ **要证明它们有牙，只能用 DB 级变异** ——
★ 那正是 `db` 门那一侧的 **M2**（父表 `ADD COLUMN` ⇒ 指纹 `父列=109 / hot列=108`
⇒ 门红「列形状不一致」⇒ 已在 §5.1 记录）✓。

★ ⇒ ★★ **而 integration 门那条「列数 == 父表列数」断言的有牙证据是**它**修复前
真的报过红**：原文 `hot table column count check failed: got "f"`（106≠108）
⇒ ⇒ **它在真实偏差上会红** ✓（不需要变异也成立）。

### 7.2 ★★ 「钉形态不钉绝对数」的必要性：合并后再跑一次

★ 本轮推送时 origin/main 又进了 5 个提交（他人会话的 877/887/888 链改动）
⇒ ⇒ **链变了** ⇒ ⇒ **必须在合并后的树上重跑这道门**（否则我的新断言可能当场失效）：

| 读数 | 值 |
|---|---|
| 合并后 integration 门 | **PASS（124.03s）** |
| 合并后链的形态 | `hot列=108 / 父列=108 / 视图列=55 / details分区=3` |

⇒ ⇒ ★★ **结论**：形态断言在链演进后**仍然成立**
⇒ ⇒ ★★ **而这恰恰是「不钉绝对数」的价值** ——
★ 本次合并进来的迁移**没有**改列数 ⇒ ⇒ **若保留旧的绝对 pin `= 106`，它今天仍然是红的**
⇒ ⇒ **改瞄不是「为了今天绿」，是「为了明天不再红」**。

---

## 8. 边界与未闭合

- ⚠ **诚实边界 ①**：本轮的「全新安装形态」= **本机容器 + 本机 PG 镜像 + 最新树**。
  生产 252 的形态**未复核** ⇒ 「新装 = 生产」这句话在本轮**只对新装库成立**。
- ⚠ **诚实边界 ②**：`installer` 是**独立 Go module**
  ⇒ 那道门**不在主仓的 `go test ./...` 里** ⇒ 必须进 `installer/` 目录
  且带 `-tags=integration` 才跑得到 ⇒ ⇒ **它腐烂了没人知道**的根因就在这里。
- ⚠ **未闭合**：官方 runner 仍在 startup 重跑阶段 `exit=2`（R73 的结论，本轮未改）
  ⇒ ⇒ **即便我把门修绿了，官方门禁今天仍跑不到测试阶段** ⇒
  ⇒ **「修门」≠「门会被执行」** ⇒ **真正的闭环还需要修 runner 的 startup 重跑**
  （那是 D2 的属主决策项，本轮不动）。
- ⚠ **未闭合**：R69 轮那批「canonical 视图 0 列 / 会话族两腿不同形 / `_request_logs`
  父表在 hot 表不在」等结论，**都是在 `itgate_r69_final` 这个手工子集库上量的**
  ⇒ ⇒ **它们应当按「量具用错对象」重新定性**（本轮只订正了最核心的那条）。
- ★ **属主决策项不变**：D2（启动链不可重跑）｜A/B/C（两道 view 门）｜
  `usage_ledger` 的 request_id 级幂等｜v1 族半退役是否查生产。
