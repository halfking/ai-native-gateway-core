# 204 号 · R89-DO —— 还 `DEBT(R47)` 的**第一笔债**：`admin/probe_history.go` 的真实流量失败腿对最近 8h **恒为 0 腿**

> 变更面：`admin/probe_history.go`（1 行 SQL + 1 段说明注释）、`internal/sqlreadguard/guard_test.go`（摘掉 2 条登记）。
> **零新增测试、零新增门** —— 这一轮的全部重量在**还债**，不在**建债**。
> 债务存量：22 → **21**（`TestDebtRatchetDoesNotGrow` 实测输出 `DEBT(R47) 现状：21 条（基线 21），新增 0 条`）。

---

## 〇、起手：202 号那句「剩 21 条只是没人排队」，本轮排第一条

202 号钉下 `debtBaseline` 时写了一句判断：

> 并发会话 `976b871ce` 刚为 `usage_enhanced.go` 做过转换并自述「最小修复仅为解 gate 阻塞」⇒ **修法已验证，剩 21 条只是没人排队**。

203 号解除了它自己登记的假阻塞（「需真库验证成本口径」）并给出**五步安全判据**：

1. SQL 须**字面量**（不是 `fmt.Sprintf` 拼装、不是字符串 `+` 拼接）；
2. 须带**可下推的 `ts` 谓词**（否则等于对两腿全表扫，成本比裸读更差）；
3. `ORDER BY … LIMIT n` 跨 `UNION ALL` 需合并排序（`memora_handlers.go:615` 正是此形态，**不满足**）；
4. 改完跑三道门；
5. 每还一条 `debtBaseline` **不动**（移除自动接受，见 §二）。

本轮挑中 `admin/probe_history.go`，因为它是 21 条里**唯一同时满足 1 与 2 的**：字面量反引号 SQL，`ts > NOW() - INTERVAL '6 hours'` 可见，同文件只有 1 处裸读（ripgrep 实测 6 处命中里 5 处是 `request_logs` 这个**词**本身——JSON 字段名 `'request_logs' AS source`、列别名、`RequestLogs int`——**只有 1 处是 `FROM|JOIN`**）。

---

## 一、🔴 F1：窗口完全落在 hot 保留期内 ⇒ 这条腿最近 6h **结构性地恒空**

`request_log_failures` CTE 的窗口是 `ts > NOW() - INTERVAL '6 hours'`（`admin/probe_history.go:486`）。
hot 的保留期是 **8 小时**：

- `sql/objects/functions/promote_request_logs_hot_to_partition_interval_integer.sql:5`
  `CREATE OR REPLACE FUNCTION public.promote_request_logs_hot_to_partition(p_retention interval DEFAULT '8 hours'::interval, …)`
- 且它**确实在后台被调度**（不是一条没人调的函数）：`bg/partition_manager.go:1343`
  `{fnName: "promote_request_logs_hot_to_partition", label: "request_logs_hot"}` —— `promoteSpecs()` 的**第一项**。

⇒ **最近 8h 的 `request_logs` 行只存在于 `request_logs_hot`**，母表里一条都没有。
⇒ 裸母表读的这个 CTE，**最近 8h 内结构性地数不到任何真实流量失败**。

这不是「精度下降」，是**恒为 0 腿**：`0 < 6h < 8h`，窗口 100% 落在盲区里。

### 受害面

`handleRoutingRecentModelFailures`（`admin/probe_history.go:425`）是**模型发现列右端的「近期失败」徽标**的数据源。三条腿 `UNION ALL`：

| 腿 | 表 | 盲区 |
|---|---|---|
| `active_failures` | `node_probe_runs` | 无（probe 表走纯 hot 策略，`:1351-1354` 注明已**取消** promote） |
| `passive_failures` | `passive_probe_state` | 无（无 `request_logs` 血缘） |
| `request_log_failures` | ~~`request_logs`~~ | **最近 8h 全盲** ← 本轮 |

⇒ 徽标的 `request_logs` 分项与 `total_failures` 在**流量故障最需要报警的那 8h**里系统性漏计；而这恰恰是它唯一能提供「真实流量」证据、另两条腿给不出的那一项。

---

## 二、还债动作，以及**为什么 `debtBaseline` 一个字都没动**

```diff
 		request_log_failures AS (
 			SELECT outbound_model AS raw_model_name,
 			       COUNT(DISTINCT credential_id) AS creds_affected,
 			       COUNT(*) AS total_failures,
 			       MAX(ts) AS last_failed_at,
 			       MIN(failure_detail_code) AS sample_error_code
-			FROM request_logs
+			FROM request_logs_with_current_month
 			WHERE lower(COALESCE(request_status, '')) = 'failure'
```

`internal/sqlreadguard/guard_test.go` 同时摘掉两条登记：
`sqlReadGuardAllowFiles` 的 `"admin/probe_history.go": "DEBT(R47): 裸母表"` 与 `debtBaseline` 的同名键。

**`debtBaseline` 里那 22 条基线条目本身不动** —— 这正是 202 号选定的语义：

> **只对新增报错，对移除自动接受。** 移除 = 债务被偿还，不需要任何人改本文件。

本轮实测输出 `DEBT(R47) 现状：21 条（基线 21），新增 0 条` ⇒ 门**接受**了这次移除（绿）。若语义是「集合必须逐字相等」，还债就得先来改基线，基线每次都变成噪音 ⇒ **棘轮退化成清单**（202 号原文）。

⚠️ **方向性不可反**：同一份实测也证明**新增仍然致命**（202 号已验，本轮未重跑负控，因为本轮**没有**新增登记）。

### 列集逐个核对（不是「视图肯定有」）

视图 `sql/objects/views/request_logs_with_current_month.sql` 两腿各 114 列，位置对应。本 CTE 用到的 6 列全部在册：

| 列 | hot 腿 | 母表腿 |
|---|---|---|
| `ts` | `:8` | `:124` |
| `outbound_model` | `:14` | `:133` |
| `credential_id` | `:15` | `:134` |
| `error_kind` | `:26` | `:142` |
| `failure_detail_code` | `:44` | `:197` |
| `request_status` | `:56` | `:191` |

⇒ 换视图**不丢列**，`MIN(failure_detail_code)` 等聚合语义不变（⚠️ 类型未变：两腿同源同类型，`MIN` 不产生新类型提升；`SampleErrCode` 仍是 `string`）。

---

## 三、⚠️ 一处**有意的**客户端观感变化，必须点名登记

objective 要求「原则上不改变客户端观感」。本轮**确实改变**了一个可见数字，必须说清方向：

- 视图是母表的**超集**（hot ∪ mother）；
- 203 号已**四步静态证明**两腿在任一提交快照上**互斥**（promote 是单条数据修改 CTE，删与插原子；`INSERT` 无 `ON CONFLICT` ⇒ 冲突时整体回滚、行留在 hot 下轮重试 ⇒ **fail-safe**）；
- ⇒ 计数**只可能持平或上升**，不会下降。

所以模型发现页的「近期失败」徽标在部署后会**变高**，且会**持续**变高（在最近 6h 窗口内）——这是**修正**，不是回归：改之前那 8h 的真实流量失败根本没被统计。

⚠️ **但幅度未实测**：无 PG，本轮**未执行任何 SQL、未取任何真实计数**。「变高」是机制推论，不是量级结论。

---

## 四、验证：负控**先在未修复代码上跑**（这是本轮唯一的鉴别力证明）

### 4.1 负控（未修复代码）

只摘两条登记，**不动生产代码**：

```
$ go test ./internal/sqlreadguard/ -run 'TestNoBareRequestLogsMotherReads|TestDebtRatchetDoesNotGrow|TestSQLReadGuardWhitelistCurrent' -v
=== RUN   TestNoBareRequestLogsMotherReads
    guard_test.go:189: 生产读面出现 1 处裸 request_logs 母表读（8h 盲区）：
        admin/probe_history.go:472 裸 request_logs 读（双腿化、内联 sqlreadguard:allow 或登记白名单）
--- FAIL: TestNoBareRequestLogsMotherReads (1.24s)
=== RUN   TestDebtRatchetDoesNotGrow
    guard_test.go:285: DEBT(R47) 现状：21 条（基线 21），新增 0 条
--- PASS: TestDebtRatchetDoesNotGrow (0.00s)
=== RUN   TestSQLReadGuardWhitelistCurrent
--- PASS: TestSQLReadGuardWhitelistCurrent (0.05s)
FAIL
```

⇒ 门**在盲区代码上**精确点名 `:472`，不是恒绿。**「绿」是改完之后才拿到的。**

### 4.2 修复后

```
$ go test ./internal/sqlreadguard/ -v -run 'Bare|Debt|Whitelist'
--- PASS: TestNoBareRequestLogsMotherReads (1.16s)
--- PASS: TestDebtRatchetDoesNotGrow (0.00s)
--- PASS: TestSQLReadGuardWhitelistCurrent (0.04s)
--- PASS: TestNoBareRequestLogsMotherWrites (1.02s)
ok  github.com/kaixuan/llm-gateway-go/internal/sqlreadguard  2.511s
```

### 4.3 全部 10 个已登记守卫（= `make guards`）

```
ok  …/internal/rowsguard      7.947s
ok  …/internal/errdiscard     9.112s
ok  …/internal/dbrows         (cached)
ok  …/internal/jsoncol        (cached)
ok  …/internal/paramguard     (cached)
ok  …/internal/sqlguard       5.362s
ok  …/internal/sqlreadguard   3.363s
ok  …/internal/metricguard    8.089s
ok  …/internal/partguard     14.647s
ok  …/internal/routeguard     (cached)
```

`guards-sync` 双向门：`✅ 7 个 *guard 目录与 GUARD_PACKAGES 的 10 项双向一致` / `✅ CI 未硬编码守卫包清单`。

### 4.4 受影响包 + 静态检查

```
$ go test ./admin/
ok  github.com/kaixuan/llm-gateway-go/admin  81.696s
$ gofmt -l admin/probe_history.go internal/sqlreadguard/guard_test.go   # 无输出
$ go vet ./admin/ ./internal/sqlreadguard/                              # 无输出
```

---

## 五、证伪与**未 settle** 的边界

**本轮撤回/修正的说法**

1. ~~「`admin/probe_history.go` 是拼接 SQL 不好改」~~ —— 不是，它是纯反引号字面量，是 21 条里**最容易**改的一条之一。
2. ~~「这 6h 窗口可能是刻意只看历史」~~ —— 窗口是 `NOW() - 6 hours`，**恰恰是最新的那一段**；不是历史回看，是实时徽标。
3. ~~「换视图会让数字变大，是回归」~~ —— 是**修正**（见 §三），且 203 号的四步证明保证「变大」=「把漏掉的补回来」，不是「把同一行数两遍」。

**未 settle（如实登记）**

- **幅度未实测**：无 PG，徽标会涨多少**未知**。若部署后发现某模型失败数突然跳变，那是**预期内**的（盲区被补上），但本轮**无法**给出预期区间。
- ** EXPLAIN 未跑**：两腿都吃同一条 `ts` 谓词，下推应有；但 `UNION ALL` + `GROUP BY outbound_model` 的实际计划**未验证**。先例 `976b871ce` 记的是同形态查询按 `ts` 过滤实测 1.7s，本查询多一层 `GROUP BY` 且列更宽，**不能直接套用那个数字**。
- **203 号未 settle 的两项仍然未 settle**（本轮未触碰）：① 语义重复（两条不同 id 的行共享同一 `request_id`/`client_request_id`）静态不可判定 ⇒ **不宣称视图「完全安全」**；② installer 内嵌迁移未逐个打开是否往母表灌行。
- **债务还剩 21 条**，且 203 号已明确：其中**大多数**连「是否有可用 `ts` 下推」都不可见（文件级 grep 看不见动态拼装的谓词）⇒ **不能按「看起来简单」批量还**，必须逐个过 §〇 的五步判据。

---

## 六、下一轮顺位

1. **继续还债，但仍只挑「字面量 + 可见 `ts` 谓词 + 单别名 + 无跨 UNION 排序」的**。候选需先自己过五步判据，**不要**照抄本轮。
2. 优先看 `admin/memora_handlers.go`：`:522` 已是双腿视图、`:615-616` 仍是拼接裸读 ⇒ **同文件形态不一致**是最值得先收的（读者会以为该文件已双腿化）。
3. 每次还债后**立刻**跑 `make guards` + 受影响包，并**重跑一次负控**（摘登记 → 门必须红）——否则「绿」可能来自门坏了。

---

## 七、playbook 新增

### §109 还债的负控是**摘登记**，不是「先跑一遍看看」

还一笔登记在案的债时，「验证」极易滑成**改完跑绿**——而那道门**本来就是为这条债存在的**，它在改之前也绿（因为债在白名单里）。

⇒ **顺序固定为**：先只摘登记（**不动生产代码**）⇒ 跑门 ⇒ **必须红且点名那一行** ⇒ 再改代码 ⇒ 跑门 ⇒ 绿。
⇒ 与「先把判据改严之前先证它不误伤」（§106）同源：**门的绿必须由「它抓到了」挣来，不能由「它没说话」挣来。**

### §110 债务存量数是**要被逐步消耗的**，所以门必须**只对新增报错**

还债型白名单与「能力清单」型白名单语义相反：清单被**消耗**是正常的，棘轮若要求两者逐字相等，就等于**给还债设闸**。

⇒ 审计任何 allowlist 先问一句：**这张清单的方向是「会长大」还是「会缩小」？**
  - 会长 ⇒ 双向棘轮（收窄了也要有人知道）；
  - 会缩小 ⇒ **单向**棘轮（只拦新增），否则还债要改基线 ⇒ 基线变噪音 ⇒ **没人再看它**。
⇒ 判据：**看这份清单被移除条目时，谁会受益。** 受益的是还债的人，就单向。

### §111 「窗口 < 保留期」⇒ **结构恒空**，不是精度问题

判定一个读面是不是真盲区，别只看「hot 8h / 母表历史」这种**定性**描述，要**把两个数字对齐**：

> 读面的时间窗口 `[NOW()-W, NOW()]` 与 hot 保留期 `R`：
> `W ≤ R` ⇒ 该窗口**结构性地 100% 落在盲区**（恒空，零行）；
> `W > R` ⇒ 只盲最近 `R`，越老的窗口越正常。

⇒ 本轮 `W = 6h < R = 8h` ⇒ **恒 0 腿**。若 `W = 30d` 那就是「盲 8h / 30d」，量级差三个数量级，**不该同定级**。
⇒ 配套：`R` 必须是**实测**的（`p_retention interval DEFAULT '8 hours'`）**且确实被调度**（`promoteSpecs()` 里有它）——**只读函数默认值不够**。
