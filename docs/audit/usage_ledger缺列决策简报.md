# usage_ledger 缺两列：证据与决策简报

> 状态：**待决策**。页面已不报错（走降级返回零值），但相关指标是空的。
> 本文给出实测证据与两条可选路径，供拍板。

## 现象

`/admin/usage` 页面的两个接口在任何环境都会 500：

| 接口 | 报错 |
|---|---|
| `GET /api/admin/usage/period-compare` | `column ul.gw_session_id does not exist (SQLSTATE 42703)` |
| `GET /api/admin/usage/cache-economics` | `column ul.compression_strategy does not exist (SQLSTATE 42703)` |

引入 commit：`b9a8baba4`（feat(session-analytics): implement Phase 1）。

## 关键事实一：不是缺迁移，是数据从来没写过

`usage_ledger_hot` 的写入点是固定列清单的 INSERT
（`domains/hooks/observability/telemetry/client.go:1302`）：

```
request_id, ts, tenant_id, application_id, api_key_id,
end_user_id, credential_id, provider_id, canonical_id,
raw_model_name, prompt_tokens, completion_tokens,
cache_read_tokens, cache_write_tokens,
total_tokens, cost_usd, latency_ms, success, error_kind,
rate_multiplier
```

**清单里没有 `gw_session_id`，也没有 `compression_strategy`。** 这两列属于
`request_logs`（见 `deploy/sql/schemas/baseline/01-schema.sql:3717-3743`）。

所以「给 usage_ledger 加两列的迁移」这条路是错的：加出来的列会永远是 NULL，
查询不会报错，但结果恒为空——比现在更难发现。

## 关键事实二：唯一的桥是 request_id，而那条路慢到不能用

`usage_ledger` 与 session 之间唯一的关联键是 `request_id`。

| 写法 | 近 30 天耗时 |
|---|---|
| `usage_ledger` ⋈ `request_logs` ON `request_id`，取 `COUNT(DISTINCT gw_session_id)` | **54.3 秒** |
| 直接扫 `request_logs` 取同口径 | **0.23 秒** |
| 压缩请求数：直接扫 `request_logs` | **1.8 秒** |
| 半连接 EXISTS 写法（30 天） | 2.6 秒 |

join 那条路在任何交互式接口上都不可接受。

## 关键事实三：两张表口径不一致，混用会让页面自相矛盾

近 30 天同一时间窗：

| 表 | 行数 |
|---|---|
| `usage_ledger_with_current_month` | 2,082,837 |
| `request_logs` | 2,169,970 |

差 **87,133 行 / 4.0%**。

所以「把这两个指标改成从 `request_logs` 取」虽然快，但会让同一个卡片里
**分母来自 usage_ledger、分子来自 request_logs**，两个数对不上
（例如「压缩 48,711 / 总计 2,082,837」与另一处的 2,169,970 并列）。
这比指标为空更难解释。

## 两条可选路径

### 路径 A：给 usage_ledger 写入管道补两列（+ 迁移 + 回填）

- 改 `client.go` 的 INSERT 列清单与 `usage_ledgers` 结构体
- 加迁移给 `usage_ledger` / `usage_ledger_hot` 及其分区加列
- 历史数据回填（`request_logs` 有 `request_id → gw_session_id` 可用）
- 之后两条查询原样保留，不用改

代价：动**热写入路径**，风险最高；收益是口径统一、查询不变。
回填要注意：54 秒那次 join 说明全量回填必须分批、离线跑。

### 路径 B：这两个指标改从 request_logs 取

- 只改两处查询，不动写入路径、不加迁移
- 需要接受口径变化，并在 UI 上明确标注这两个指标来自 request_logs
- 若要避免自相矛盾，需同时把同卡片里其它指标也切到 request_logs，或
  在页面上分开呈现两组口径

代价：低风险、快；但引入「同一页两套口径」的认知负担。

## 我没有动手的原因

两条路径都会改变**对外可见的指标口径**，这属于业务决策而非实现细节：
哪个表是这些指标的准绳、4% 的差异要不要向用户披露，得先有定论。
硬选一条很可能得到「看起来能跑、但数字对不上」的结果——那比现在的
明确降级更难排查。

在此之前，两处 handler 已加降级（`IsSchemaBehindError`，见
`admin/dashboard_degrade.go`）：schema 落后时返回 200 + 空指标并记服务端日志，
页面不报错、也不显示假数据。

## 相关门禁

- `admin/json_contract_test.go`：Go nil 切片序列化契约门（同类白屏问题）
- `cmd/gateway/wiring_gate_test.go`：handler 接线门
- `web/scripts/ui-sweep.mjs --selftest`：运行时判据自检
