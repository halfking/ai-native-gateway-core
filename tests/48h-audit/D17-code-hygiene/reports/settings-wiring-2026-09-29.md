# D17 · 平台设置「有声明必有消费方」普查（2026-09-29）

## 结论

297 个平台设置 Key = **266 有消费方 + 31 无消费方（10.4%）**。

**死配置比死代码危险**：死代码不承诺任何东西，运维不会指望它。
死配置会承诺——有 Key、有类型、有默认值、有界面名称与说明、`HotReload: true`、
还有 `DangerLevel: Warning`。运维改它、看到保存成功、在界面上看到新值，
然后什么也不会发生，且**没有任何报错**。

## 最整齐的一组：lifecycle.*_ttl_days

这 5 个设置被迁移 391 播种进设置表：

```sql
-- sql/migrations/startup/391_state_table_storage_hardening.sql:185-191
('lifecycle.credential_model_call_history_ttl_days', '30'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
('lifecycle.usage_ledger_ttl_days',                   '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
('lifecycle.request_wal_ttl_days',                    '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
('lifecycle.credit_ledger_ttl_days',                  '1'::jsonb, 'int', 'platform', 'lifecycle', NOW()),
('lifecycle.tool_usage_stats_ttl_days',               '1'::jsonb, 'int', 'platform', 'lifecycle', NOW())
```

它们与 5 张表**严格一一对应**，而 `drop_old_state_partitions()` 覆盖的恰好是另外 5 张表：

| TTL 设置（已播种，无人读） | 表 | 现状 |
|---|---|---|
| `lifecycle.usage_ledger_ttl_days` | `usage_ledger` | **1001 MB / 2,038,536 行** |
| `lifecycle.request_wal_ttl_days` | `request_wal` | **375 MB / 975,153 行** |
| `lifecycle.credential_model_call_history_ttl_days` | `credential_model_call_history` | **102 MB / 284,877 行** |
| `lifecycle.credit_ledger_ttl_days` | `credit_ledger` | 160 kB / 0 行（尚无代价） |
| `lifecycle.tool_usage_stats_ttl_days` | `tool_usage_stats` | 224 kB / 0 行（尚无代价） |

`drop_old_state_partitions()` 覆盖的 5 张：`candidate_failure_logs`、
`credential_model_index`、`handoff_logs`、`model_probe_runs`、`routing_decision_log`
——**一张都不在上表里**。

**即：TTL 声明恰好是为「最终没有实现清理机制的那批表」而写的，然后从未被读取。**
三张已膨胀的表合计约 1.48 GB / 330 万行；另两张还空着，代价尚未发生。

## 另两组成簇的未接线

- **`self_check.*` 5 个键全部无消费方** —— D09 子系统的 spec 先落地，接线后延。
- **`sessions_v2.*` 5 个「点号」形式的键全部无消费方** —— 而**活**的键是
  `sessions_v2_compression_read`（**下划线**形式，P1-1 的可达性分析用的就是它）。
  **声明时用了与活键不同的命名形状**，于是那 5 个从未被读到。

## 检测器自己错了三次（三次都是门的错，不是代码的错）

| # | 门的缺陷 | 错误答案 |
|---|---|---|
| 1 | 把 `.md` 文档算作消费方 | 死设置数从 31 缩到 **4**（文档提到 Key ≠ 代码读它） |
| 2 | 用 `grep <key>`（未转义的 `.`）当判据 | `handoff.threshold` 匹配上 `handoff_threshold`，把真死设置判成活的。**Key 里的 `.` 是正则元字符** |
| 3 | 把迁移里的**种子 INSERT** 当消费方 | 5 个 `lifecycle.*_ttl_days` 全部「存活」。`INSERT … VALUES (key, …)` 是**写入默认值**，不是读取 |

第 2 条尤其值得记：我用逐文件 grep 去「纠正」精确子串扫描的结果，
而**被纠正的那个才是对的**。**普查结果先拿已知案例校准，再信它。**

## 门

`TestData_SettingsSpec_EveryKeyHasAConsumer`
（`tests/48h-audit/D17-code-hygiene/hygiene/settings_wiring_test.go`）

- 判定：精确子串（**不用正则**）、只认 `.go/.sql/.vue/.ts/.tsx/.js`、排除
  `settings/spec_*` 自身与 `_test.go`、排除 `docs/ tests/ scripts/`
- **种子行不算消费方**（`seedValueLine` 识别 `('key', '1'::jsonb, 'int', …)` 形状）
- 三条自收缩断言：未登记的死设置 → 红；已登记但出现消费方 → 红；
  登记了 spec 里不存在的键 → 红
- 账目不变式：四个桶（wired / known-debt / unwired / shrunk）**加总必须等于 Key 总数**

**变异检验 3 处红转绿**：撤登记 → 红并指名该键 / 给有消费方的键加登记 → 红「已失效」/
登记幽灵键 → 红「已不存在」。

**门自己暴露的一个 bug**：账目不变式最初放在逐键报错之前、且漏了 `shrunk` 一栏，
于是「一条过期登记」只报一句 `accounting does not add up`、**不指名是哪个键**——
**门在失败的同时把自己的诊断藏起来了**。已修：加总补上 shrunk，校验挪到所有报错之后。
