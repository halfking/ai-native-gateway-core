# R77 轮（db 包形态线）：两条 v1 时代夹具的定性 ＋ canonical 结构覆盖缺口的补门

> 日期：2026-10-21
> 树：`50b7d2738`（本轮起点，已并入 origin/main 全部在途提交）
> 库：`itgate_r74_hot`（probe 脚本产物，全新安装形态）｜DSN 集：见 §5
> 容器：`llm-gateway-pg`（本机 127.0.0.1:5432）
> 触发：并行会话跨会话通报「db 包里还有两条 v1 时代的夹具，不在你的 13 个文件清单里」

---

## 0. 一句话结论

★ 通报的**两条红属实**（逐字复现），但**对 (B) 的定性不准** ⇒ 并且**顺着这条线挖出一个今天
真实存在的覆盖缺口**（canonical 视图的 **FROM/JOIN 结构无人守**）⇒ 本轮补了那道门
（**零语义风险**：只加覆盖、不改任何行为），并把 (A)/(B) 的处置做成**有实测代价表的属主决策**。

---

## 1. 复现（最新树 + 逐条隔离）

```
=== (A) db/request_logs_view_schema_test.go:252 TestRequestLogsCurrentMonthViewEnsureRoundTrip
    request_logs_view_schema_test.go:385: canonical view column count = 0, want 10 (base 7 + customer_id + request_class + due_at)
--- FAIL: TestRequestLogsCurrentMonthViewEnsureRoundTrip (2.29s)

=== (B) db/view_schema_v2_contract_test.go:308 TestRequestLogsViewV2EnsureMatchesMigration
    view_schema_v2_contract_test.go:366: derive clone DDL from live catalogs: table public._request_logs not found in live catalogs
--- FAIL: TestRequestLogsViewV2EnsureMatchesMigration (2.52s)
```

⇒ ★ 与通报一致（连报错文本都逐字一致）⇒ **通报的读数可信**。

---

## 2. ★★★★ 订正通报对 (B) 的定性：「断言全部是会话族形态」**不准确**

★ 通报说 (B)「**断言全部是会话族形态** ⇒ 断言仍有效，死的是夹具」⇒ ⇒ **实测不成立**：

| (B) 的断言 | v4 期是否成立 | 实测依据 |
|---|---|---|
| viewdef 含 `session_turns` | ✅ | v4 视图含 |
| viewdef 含 `session_turn_details` | ✅ | v4 视图含 |
| viewdef 含 `t.client_ip::inet`（816） | ✅ | 实测 true |
| viewdef 含 `pg_input_is_valid`（817） | ✅ | 实测 true |
| viewdef **不含** `client_ip ~ `（弱守卫必须消失） | ✅ | 实测 false |
| viewdef 含 `credits_rate_multiplier`（738） | ✅ | 实测 true |
| viewdef 含 `t.origin_stage` / `t.token_band` / `t.client_forwarded_for`（815） | ✅ | 实测 true |
| **viewdef 含 `h.origin_stage` / `h.token_band` / `h.client_forwarded_for`** | ❌ **必红** | ★★ **现行 ensure 体两臂都用别名 `t`**：`FROM public.session_turns_hot t … UNION ALL FROM public.session_turns t` ⇒ **根本没有 `h.`** |
| **「ensure == 迁移重放链」**（710→734→738→740→815→816→817→824） | ❌ **v1 时代** | ★ 710 是**三臂含 v1 体**的装配（`UNION ALL v1 体（hot∪parent + 577/610/696/700 lateral，冻结）× 反连接`）⇒ 它的重放链要 `_request_logs` 才有意义 |
| **数据语义半边**（往 `_request_logs_hot`/`_request_logs` 插数、断言 v1 腿的 dedup 与 D4 NULL 透传） | ❌ **v1 时代** | v1 族已被 890 真删 ⇒ 那条腿**不存在** ⇒ 这些断言**不可证伪** |
| **夹具预建两个 wrapper 视图**（`…_without_customer_id` / `…_without_request_class_due_at`） | ❌ **已退役** | 890 的 DROP 名单里就有这两个视图 |

⇒ ⇒ ★★⇒ **(B) 是「混合门」**：**会话族一半（v4 有效）+ v1 一半（h. 腿 / v1 数据语义 /
710 重放 / wrapper 链）** ⇒ ⇒ ⇒ **修夹具救不回来**（`h.` 断言与 710 重放即使夹具修好也必红）
⇒ ⇒ ⇒ ★★ **⇒ 通报给的三个选项（A 退役 / B 两条都改瞄会话族 / C 都退役换 v4 专门新门）漏掉了
「混合门」这一类** ⇒ 处置要按**半门**来分，不能按整门来分。

---

## 3. ★★★ (A) 的定性：**整条都是 v1 时代**（比通报说的更彻底）

★ 通报说 (A)「断言的是 v1 动态重建形态 ⇒ 改瞄夹具救不了，断言本身要改」⇒ ✔ 正确，且更彻底：

| (A) 的段落 | v1 专属度 |
|---|---|
| 夹具：手工造 `_request_logs` + `_request_logs_hot`（含 HOT_ONLY `caller_id`） | v1 |
| 断言：canonical 列数 == （两表列交集）**+ 3** | v1（v1 期 canonical 只有 10 列） |
| 断言：HOT_ONLY 列 `caller_id` 不得漏进视图 | v1 专属（v4 无 HOT_ONLY 概念） |
| 断言：经视图读 hot 行 / parent 行的数据往返 | v1 专属（v4 的两臂是会话族） |
| 段落：frozen-chain replay（重建两个已退役 wrapper 视图） | v1 专属 |

⇒ ⇒ ★★ **现行 canonical 视图 = 118 列**（实测），而 (A) 期望 **10 列**
⇒ ⇒ ★★ **⇒ (A) 的「10」是一个 v1 期的绝对数 pin** ⇒ ⇒ ★★
**与 R74 轮同款：绝对数 pin 必然腐烂 ⇒ 应当钉形态**。

★ ★★ **但 (A) 的「本意」在 v4 载体里还活着**（实测）：

| (A) 想守的性质 | v4 载体里的对应物 | 实测 |
|---|---|---|
| canonical 视图必须投影 `customer_id` / `request_class` / `due_at` | **三列都在** | **3/3** ✅ |
| canonical 视图必须存在 | 在 | ✅ |
| （v1 专属）HOT_ONLY 不漏 / v1 列交集 / v1 数据往返 / frozen wrapper 重放 | **无对应物** | ⇒ v1 专属，**不需要补** |

⇒ ⇒ ★★★ **⇒ (A) 的处置有一个「零语义风险」的形态**：
**把「列数 == 10」换成「canonical 视图存在且必须投影 `customer_id`/`request_class`/`due_at`」**
⇒ 同一意图、v4 载体、**不丢任何仍有意义的覆盖**。
★ ★★ **注**：`request_class` + `due_at` 两列**R74 轮已补进** `db::TestFreshInstallRuntimeShape_EnsureChain`
⇒ ⇒ **只差 `customer_id` 一列**。

---

## 4. ★★★★ 覆盖缺口清单（退役会丢什么 / 已被谁覆盖）

| 覆盖项 | 现在由谁守 | 退役 (A)+(B) 之后 |
|---|---|---|
| canonical 投影 `request_class` / `due_at` | ★ **R74 轮新增**（`db::TestFreshInstallRuntimeShape_EnsureChain`） | **保住** |
| canonical 投影 `customer_id` | (A) | ★ **丢** ⇒ 需补一列 |
| canonical 视图存在 | (A)+(B) | 需补 |
| ensure 幂等（第二遍不改 viewdef） | (B) | ★ **丢** ⇒ 需补 |
| **details LEFT JOIN 在位**（733 形态） | (B) | ★★ **丢** ⇒ **这是真缺口** |
| client_ip 有源投影 + 语义守卫（816/817）+ 弱守卫必须消失 | (B) | 需搬 |
| `credits_rate_multiplier`（738） / `t.*` 列（815） | (B) | 需搬 |
| **Go 自愈体 == 迁移体的 projection** | ★ **890 静态门已有**（`TestMigration890ProjectionMatchesComposer`） | **保住** |
| ★★ **Go 自愈体 == 迁移体的 FROM/JOIN 结构** | ★★ **无人守** | ⇒ **本轮补了（§5）** |
| HOT_ONLY 不漏 / v1 列交集 / v1 数据往返 | (A) | **无需补**（v1 专属） |

---

## 5. ★★★★ 本轮补的缺口：**canonical 视图的 FROM/JOIN 结构无人守**

### 5.1 证据（三条，都不是推测）

1. `sql/migrations/startup/migration_890_v1_family_drop_test.go` 的
   `TestMigration890ProjectionMatchesComposer` **只比 projection**（`$proj$` 块 ⇔ Go 投影常量）。
2. ★ `db/retirement_contract_exports.go` 里 **`CanonicalSessionOnlyDDLForTest()`（整段 DDL 的导出）
   没有任何门在用** —— 它的文档注释写着「契约门比 $proj$」，可导出摆在那里没人调
   （`grep` 全仓：唯一调用方是 `CanonicalSessionOnlyProjectionForTest`）。
3. ★ 现行 ensure 体与 890 迁移的 FROM 结构**同形**（实测 `itgate_r74_hot`）：
   `FROM public.session_turns_hot t LEFT JOIN public.session_turn_details_hot d … UNION ALL
   FROM public.session_turns t LEFT JOIN public.session_turn_details d …`
   ⇒ ⇒ **结构漂移会静悄悄通过静态层**，而它的后果正是 890 契约门文件头写的那一类。

### 5.2 补法（零语义风险：只加覆盖，不改任何行为）

新门 `TestMigration890ViewBodyMatchesComposer`（加在 890 契约门同一文件里）：
★ **不 grep 关键字**（grep 只能证明「出现过」，证不了「结构相同」，还会被注释里的示例骗过）
⇒ ⇒ **把迁移里的 `$ddl$` 模板 + `$proj$` 投影组装成迁移实际装出的整段语句**，
与 `db.CanonicalSessionOnlyDDLForTest()` **归一化后逐字比** ⇒ ⇒ **projection 与结构一起被守住**。
★ 阳性对照：组装结果必须**非空**且**含两臂 + 两条 details 腿 + UNION ALL**
（否则「抽错了范围」会伪装成「两边一致」）。

### 5.3 变异（2/2 全红 + 还原 md5 逐字一致）

| # | 变异 | 读数 |
|---|---|---|
| M0 | 对照 | **PASS** |
| M1 | **Go 侧**：canonical DDL 的 hot 臂 join 改成 parent 特征层（`session_turn_details_hot` → `session_turn_details`） | **RED**：`890's installed view body and db.canonicalSessionOnlyDDL have drifted.` |
| M2 | **迁移侧**：`$ddl$` 模板两臂顺序颠倒 | **RED**（同上） |
| — | 还原（按原路径 md5） | **逐字一致**；复跑 **PASS** |

### 5.4 ★★ 顺带发现：同一个 JOIN 结构在 Go 文件里**写了 4 处**

`db/request_logs_view_schema.go` 里 `session_turn_details_hot` 出现 **6 次**：
① line 548（**注释**里的形态说明）、② line 572（canonical DDL 的 `hotJoin`）、
③ ④ line 613/628（`SessionFamilyTurnsSourceSQL` / `SessionFamilyTurnsHotSourceSQL` 两个
native reader 源）⇒ ⇒ ★★ **「canonical DDL 那一处」现在被门守住了，但 613/628 两处与注释仍未被守**
⇒ ⇒ ★★ **⇒ 登记为后续项**（要么一并纳入门，要么明确写「只有 canonical DDL 受门约束」）。

---

## 6. 属主决策项（本轮**未替属主拍板**）

★ 通报已提 A/B/C 三案；★ 本轮实测后，**可行的处置是按「半门」拆**：

| 方案 | 内容 | 覆盖得失 | 代价 |
|---|---|---|---|
| **A′（本轮实测支持）** | 退役 (A) 整条；退役 (B) 的 **v1 半**；保留 (B) 的**会话族半**并**换载体**（会话族 scratch + 不重放 710） | 保住会话族半的 5 项；丢 `customer_id` 一列 ⇒ 补 | 中：重写 (B) 的夹具与数据语义段 |
| **B′** | 两条都退役，换**一道 v4 期专门新门**（投影三列 + details join + client_ip 守卫 + credits + `t.*` + 幂等） | 同上，但集中在**一道**门里 | 中高：新建门 + 退役两条 |
| **C′** | 两条都退役，**只靠** 890 静态门 + R74 那道门 | ⚠ **丢 details join 在位、幂等、client_ip 守卫、`credits`/`t.*` 列** ⇒ ⇒ **不可接受**（除非本轮的 §5 门再扩展） | 低，但**覆盖损失明显** |

★ ⇒ ★★ **本轮的建议 = A′ 或 B′（两者都要补 `customer_id` 一列 + details join 在位 + 幂等）**
⇒ ★★ **C′ 在当前形态下不可接受**（覆盖损失实测可列，见 §4）⇒ ⇒ **「都退役」这个方向本身要带条件**。

★ **属主另需拍板的一条**：§5.4 的 613/628 两处 native reader 源**要不要一并纳入门**。

---

## 7. 边界与未闭合

- ⚠ **诚实边界 ①（包级数字：诚实数字是「隔离口径 4 条真红」）**：
  ★ 我对 `db` 包连测多遍，**数字各不相同**（每一次都带坐标）：

  | 口径 | 红数 | 说明 |
  |---|---|---|
  | ① 摘要管道（`head -5` + 计数） | 「2」 | ★ **摘要吃了信息** ⇒ 不可引用 |
  | ② 整包跑（库已被前一轮污染） | 10 / 11 | ★ **污染产物** ⇒ 不可引用 |
  | ③ 整包跑（**干净库** + 全量 DSN 集） | **6** | 其中 **2 条是同包内污染造成的假红** |
  | ④ ★ **逐条隔离**（干净库 + 全量 DSN 集） | ★ **4 条真红** | **诚实数字** |

  ★ ⇒ **④ 的 4 条真红**：

  | 红 | 隔离下的性质 | v1 时代？ |
  |---|---|---|
  | `TestRequestLogsCurrentMonthViewEnsureRoundTrip`（(A)） | 夹具 v1 形态 ⇒ ensure 装不上 | ✔ **是** |
  | `TestRequestLogsViewV2EnsureMatchesMigration`（(B)） | 夹具克隆 v1 表 + 已退役 wrapper 链 | ✔ **是** |
  | `TestEnsureAsrCatalogSeed_RealDB` | `provider_models row missing for zhipu/glm-asr` ⇒ **seed 缺** | ✘ 否 |
  | `TestRoutingAnalyticsSourceGates_RealDB` | `routing fixture names already exist; supply an empty scratch database` ⇒ **靶子库要求空库** | ✘ 否 |

  ⇒ ⇒ ★★★ **⇒ 通报「db 包里还有两条 v1 时代的夹具」这个**定性**是准确的** ✓（只有 1、2 两条是 v1 时代）
  ⇒ ⇒ ★★ **⇒ 「db 包整体红只有 2 条」要加口径**：严格说是「**只有 2 条是 v1 时代的**」；
  ★ **包级真红是 4 条**（另 2 条是 **seed 缺** 与 **靶子库要求空库** ⇒ **都不是产品缺陷**）⇒ ⇒ **结论一致**。

  ★ ⇒ ★★★ **⇒ 「同包内污染」是真实存在的**：★ `TestSessionDimCoverageIsAUserSessionMetric`
  在**整包跑**里报 `relation "public.session_turns_hot" does not exist`（42P01），
  ★ **逐条隔离跑则绿** ⇒ ⇒ **它是被同包前面的测试毁掉形态后的假红**。
  ★ ⇒ ★★ **⇒ 决定性证据**：跑完 db 包后，**形态库被掏空** ——
  `schema_migrations` / canonical 视图 / `session_turns_hot` **全都不存在**（关系数 2338 → 2281）；
  ★ 而**逐条隔离跑完，形态库仍完整**（三者都在）⇒ ⇒ ⇒ **⇒ 污染源在包内某条测试**
  （★ `TestDDLSkipGuardsAgainstRealPostgres` 嫌疑最大：它在共享库上建 scratch + 做 DDL，
  且它自己的红是**清理超时**而不是断言红）
  ⇒ ⇒ ⇒ ★★★ **⇒ 「形态测量」与「包级测试」必须分库**：同库时**先跑的测试会毁掉形态**，
  而**后跑的测试会因为形态没了而假红** ⇒ ⇒ **⇒ 形态测量要在跑包级测试之前做，或在另一个库上做**
  ⇒ ⇒ ⇒ ★ **⇒ 归因顺序：先排除「库被污染」与「靶子库要求空库」，再谈产品缺陷**。
- ⚠ **诚实边界 ②**：「新装形态」= **本机容器 + 本机 PG 镜像 + 最新树** ⇒ 生产 252 未复核。
- ⚠ **未闭合**：(A)/(B) 的处置 = 属主决策项（本轮只做证据与代价表，**未动那两道门**）。
- ⚠ **未闭合**：§5.4 的 613/628 两处 native reader 源未纳入门。
- ⚠ **未闭合**：本轮**没有**在「靶子库（空库）」口径下复测 db 包 ⇒ ⇒ **「干净靶子库下 db 包到底几条红」仍未知**
  ⇒ ⇒ 下一轮第一件事：**用空库当靶子重跑 db 包**（⇒ 才能与通报的「2 条」在同一口径上对拍）。
- ★ 属主决策项不变：D2｜A/B/C（两道 view 门）｜`usage_ledger` 幂等｜v1 族半退役是否查生产｜
  ★ **新增：(A)/(B) 两条 db 包 v1 时代门的处置（A′ / B′ / C′）+ 613/628 是否纳入门**。
