# 133 号｜R89-AU：API key **预算闸门有四条独立的 fail-open**，其中「最近 8 小时完全失明」已实证 —— 且一词可修

- 日期：2026-10-01
- 轮次：R89-AU
- 起因：objective 明写「检查内部费用计帐…**可以细化到一个 apikey 或凭据或一个模型**」。
  本轮查「租户 → apikey」这一维度的实现，撞上一个 20 行函数里的四重 fail-open。
- 结论先行：
  1. **统计源不含最近 8 小时**——`usage_ledger_hot` **不是** `usage_ledger` 的分区，
     而 `budgetCheck` 读的正是裸父表。**实证：最近 8h 父表 0 行，hot 2,162 行。**
  2. **查询失败被静默吞掉并当作「没超预算」**（`//nolint:errcheck` 明写 best-effort）。
  3. **`COALESCE(SUM(cost_usd),0)`** 与待裁决 47 同型：缺数据 = 花费 0。
  4. **硬编码 `tenant_id = 'default'`** ⇒ 非 default 租户的 key 一律 404（功能缺失，**不是**越权）。
  5. **净效果：四种独立情况下都返回「没超预算」，且每次都返回 200 与一组自洽的数字。**

---

## 一、缺陷 A（本轮最实锤）：**预算对最近 8 小时完全失明**

### 1.1 拓扑：`usage_ledger_hot` 不是 `usage_ledger` 的分区

```sql
SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid
 WHERE c.relname='usage_ledger_hot';          -- → 0     ★ 不是分区

SELECT string_agg(c.relname, ',')
  FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid
  JOIN pg_class p ON p.oid=i.inhparent
 WHERE p.relname='usage_ledger';
-- → usage_ledger_2026_07, 2026_08, 2026_09, 2026_10, usage_ledger_default
--    （★ 没有 _hot）
```

即：`usage_ledger` = 5 张月/默认分区；`usage_ledger_hot` = **独立表**（8 小时保留）。

### 1.2 实证（真库，最近 8 小时）

| 数据源 | 最近 8h 行数 |
|---|---|
| `usage_ledger`（裸父表） | **0** |
| `usage_ledger_hot` | **2,162** |
| `usage_ledger_with_current_month` | **2,162** |

⇒ `admin/keys.go:973` 的

```sql
SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE api_key_id = $1
```

**看不到最近 8 小时的任何花费。** 且由于 promote 是按批调度，**任何时刻都存在一个滚动的 8 小时盲区**。

⇒ **盲区恰好覆盖「最可能超支」的时段**——这正是预算闸门最该起作用的窗口。

### 1.3 一词可修（**已实证，不是推测**）

`usage_ledger_with_current_month` 的定义里**含 `FROM usage_ledger_hot`**：

```sql
SELECT definition FROM pg_views WHERE viewname='usage_ledger_with_current_month';
-- 从中 grep 到： FROM usage_ledger   与   FROM usage_ledger_hot
```

⇒ **把 `usage_ledger` 换成 `usage_ledger_with_current_month`，
最近 8 小时从 0 行变 2,162 行**，缺陷 A 精确消失，无需迁移、无需新表。

## 二、缺陷 B：**查询失败 = 「没超预算」**

```go
var spent float64
//nolint:errcheck // best-effort exec, non-critical
h.db.QueryRow(ctx, `SELECT COALESCE(SUM(cost_usd),0) FROM usage_ledger WHERE api_key_id=$1`).Scan(&spent)

exceeded := budgetUSD != nil && spent >= *budgetUSD
```

`Scan` 的错误被显式忽略（注释写明 `non-critical`）
⇒ **DB 超时 / 连接断开 / SQL 错误时 `spent` 保持零值 0**，
然后照常 `writeJSON(..., 200)` 返回 `spent_usd: 0`、`exceeded: false`。

⇒ **这是一条 fail-open 的闸门：查不到数据 ⇒ 放行。**
⚠️ 与 130 号同族：**「表里没有」与「表里有且值为 0」在输出上完全一样。**

## 三、缺陷 C：`COALESCE(...,0)` —— 与待裁决 47 **完全同型**

`COALESCE(SUM(cost_usd), 0)` 把「无行」「全 NULL」「全 NULL 且无定价」统统编码成 **0**。
本会话已在 4 处发现同一形态（UI、对账 `nullDiffRate`、`AggregateMonth`、对账网关侧 `COALESCE`），
**这是第 5 处**。⇒ 建议与待裁决 47 **合并成一条统一修法**：引入「数据不完整」哨兵值。

## 四、缺陷 D：硬编码 `tenant_id = 'default'`

```go
SELECT budget_usd FROM api_keys
WHERE id = $1 AND tenant_id = 'default' AND COALESCE(status,'active') <> 'revoked'
```

真库已知 `users` 表有 7 个 API key 绑在非 default 租户
⇒ **这些 key 的预算检查一律返回 404 "api_key not found"。**

⚠️ **诚实定性：这是功能缺失，不是跨租户越权。**
按「不可达的缺陷不是缺陷」，**不能报成隔离漏洞**——
硬编码 `'default'` 反而**收紧了**可见性。
但它意味着**「按 apikey 控预算」这项 objective 要求，对非 default 租户不成立**。

## 五、可达性与消费面（按 §30 逐腿核，每条独立取证）

| 断言 | 取证 | 结果 |
|---|---|---|
| 端点存在 | `admin/keys.go:950` `func (h *Handler) budgetCheck` | ✅ |
| 路由已挂载 | `admin/handler.go:1249` `mux.HandleFunc("/api/keys/", admin(h.handleKeys))` | ✅ **可达** |
| 分发到 budgetCheck | `admin/keys.go:210` `case "budget-check": h.budgetCheck(w,r)` | ✅ |
| 前端有消费面 | 形态 2：全 `web/`（ts/tsx/vue/js/html）→ 0 命中 | ❌ |
| 同上 | 形态 3：全仓库非 Go 文件 → 0 命中 | ❌ |

⚠️ **注意第一腿**：`grep -rn "\.handleKeys("` 是 **0 命中**——
因为它是**函数值注册**（`HandleFunc("/api/keys/", admin(h.handleKeys))`），**不产生调用点**。
**若停在计数就会误判成「死代码」**（§17 同族，本会话已栽过 3 次）。

⇒ **诚实边界：端点是活的，但当前无 UI 消费面。**
⇒ **因此本条按「代码无条件成立」定 P1，而非「当前正在造成用户损失」。**
一旦被接上 UI 或被外部调用，四条 fail-open 立即生效。

## 六、修法方向（**待裁决，不擅自动手**）

1. **缺陷 A**：`usage_ledger` → `usage_ledger_with_current_month`（**一词，实证 0 → 2,162 行**）。
2. **缺陷 B**：`Scan` 错误必须**返回 5xx 或独立错误态**；**绝不能**让 `spent` 保持 0 当作合规。
3. **缺陷 C**：去掉 0 兜底，引入「数据不完整」哨兵（与待裁决 47 统一修法）。
4. **缺陷 D**：去掉硬编码 `'default'`，改用既有租户闸门（`assertKeyTenantScope` 同族）。
5. **补 129/130 号**：预算统计应能识别「该窗口台账数据不完整」，
   否则 09-03~09-12 那 7 天的花费**永久不计入任何 key**。

**优先级判断**：A 与 B 是一行/一个错误处理的成本，**收益极高**；
C/D 涉及语义变更（错误态、租户闸门），需产品确认。
