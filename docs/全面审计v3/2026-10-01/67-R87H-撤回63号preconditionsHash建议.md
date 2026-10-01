# 67 号报告：撤回 63 号 §2（preconditionsHash 建议）—— 引用了错误的代码点

- 日期：2026-10-01
- 轮次：R87-h
- 性质：**撤回**。本轮**无代码改动**。
- 关联：63 号 §2、54 号报告 F3

---

## 0. 一句话

63 号 §2 建议「给 `bg/credential_recovery.go:748-756` 的 `health_status_reset` 加 `preconditionsHash`，防运维『看着 A 的状态、清掉 B』」——**该建议不成立，因为那处根本不是运维接口**。**撤回。**

---

## 1. 错在哪：先写结论，后找依据

63 号 §2 的推理链是：「看到一段无条件 UPDATE ⇒ 推断它被运维接口调用 ⇒ 推断存在读-改-写竞态 ⇒ 建议加前置条件哈希」。

**中间两步从未被验证。** 实测：

```bash
# 1. admin/ 与 cmd/ 里没有任何 health_status 复位写点
grep -rn "health_status\s*=\s*'unknown'|health_status = \$" --include=*.go admin/ cmd/
# → 0 命中

# 2. 该代码所在的函数
grep -n "^func " bg/credential_recovery.go | awk -F: '$1<748' | tail -1
# → 343:func (r *CredentialRecovery) run / 383:func (r *CredentialRecovery) recover
```

⇒ `:748` 位于 **`func (r *CredentialRecovery) recover(ctx)`**，是**定时后台批量恢复**，`dispatchRecoveryHooks("health_status_reset", ...)` 消费 `RETURNING id` 的**行数**。

**没有「运维看着 A 的状态」这回事**——没有人在读它，**也就没有读-改-写竞态**。

更关键的是那段 SQL 的 `WHERE` **本身就编码了前置条件**：

```sql
WHERE health_status NOT IN ('healthy', 'unknown')
  AND (health_checked_at IS NULL OR health_checked_at < NOW() - INTERVAL '2 minutes')
  AND COALESCE(availability_state,'ready') NOT IN ('suspended','auth_failed')
```

**「只重置陈旧超过 2 分钟的状态」就是它的设计意图，且由 SQL 原子保证。** 它是幂等的自守卫批量作业，加一个「运维视角的快照哈希」是**给它加一个不存在的需求**。

---

## 2. 真正的并发缺口在别处（且早已登记）

扫描 `admin/` 对 `credentials` 的全部 UPDATE 写点后，**唯一有真实读-改-写竞态形态**的是：

```go
// admin/provider_credential.go:695
query := "UPDATE credentials SET " + strings.Join(sets, ", ") +
         fmt.Sprintf(" WHERE id = $%d AND provider_id = $%d RETURNING revision",
                     len(args)-1, len(args))
```

`revision` 被 `RETURNING` 出去（`:541` 读、`:697` 回显给客户端），**但 WHERE 里从不与客户端期望值比较** ⇒ 两个并发的编辑请求**后写覆盖前写**，客户端却各自拿到自己那次成功的 `revision`。

**这正是 54 号报告 F3【P2】「`credentials.revision` 不是乐观锁」**——**我的建议等于把一个已登记的发现，挂在了错误的代码点上。**

⇒ **正确的修法归属**：`provider_credential.go:695` 的 WHERE 加 `AND revision = $expected`，把 `revision` 真正用作乐观锁。**该项已在 54 号 F3 登记，本报告不重复立项，只更正代码点。**

---

## 3. 本会话的同类失误统计（这是本报告真正的价值）

到本轮为止，我**自己**因「先写结论、后找依据」而出的错已有 **6 次**：

| # | 轮次 | 错在哪 | 当轮是否自查 |
|---|---|---|---|
| 1 | R83b | 用「取值 81/86 不同」推「语义分属不同子系统」 | 是（子代理推翻后撤回） |
| 2 | 61 号 | 未核实就说 `partguard`/`metricguard` 缺白名单保险 | 是（M5/M6 变异后撤回） |
| 3 | 62 号 | 未核实就写「`EmptyState` 组件根本不存在」（实为子代理搜错目录，我未复核） | 是（子代理推翻后更正） |
| 4 | 63 号 §1.3 | 规格写了 `policy.ResetAfter`，**该字段不存在** | 是（实施前读结构体时抓到） |
| 5 | 63 号 §2 | 把后台批量恢复误当运维接口 | **本报告** |
| 6 | 62 号建议 1 | 建议放宽 `is_routable` 合取，但未意识到那会改变对外行为 | 是（自行标注为待裁决） |

**共同形态：把「看到的一段代码」直接外推成「它的用途与调用上下文」，而调用上下文恰恰是代码本身不说的那部分。**

⇒ 已立为硬规则写入 `docs/audit/playbook/conventions.md` §10（见下）。

---

## 4. 新增纪律（已入册）

**§10 定点核实纪律**：对任何「建议在 X 处加/改 Y」的修复意见，落笔前必须完成**三项**核实，缺一不得写入方案文档：

1. **读包围函数**——该行属于哪个函数、这个函数的契约是什么（定时作业 / 请求处理器 / 迁移）；
2. **查调用方**——谁调用它、参数从哪来、是人工触发还是自动触发；
3. **读 SQL 的 WHERE**（若是 SQL）——**它是否已经自带前置条件**。

**为什么第 3 条必须单列**：一段「无条件 UPDATE」看起来缺前置条件，但如果它是带自守卫 WHERE 的幂等批量作业，那么「缺前置条件」本身就是**误读**——把它当缺陷修，等于给正确设计加无用复杂度。

**为什么不能只靠子代理**：第 3、5 两次错误的源头都是**我在整合阶段转述时没有回到原代码**。子代理的结论本身可能是对的（本次就是——「无条件 UPDATE」确实无条件），**错的是我对它的用途推断**。

---

## 5. 本轮未做

- **无代码改动**，未跑测试（纯核实轮）。
- **F3 的实际修复未做**（`provider_credential.go:695` 加 `AND revision = $expected`）——它会**改变 API 契约**（并发编辑从「后写覆盖」变为「409 冲突」），按纪律需产品/owner 认可，**不擅自改**。
- 其余既有守卫（`rowsguard`/`errdiscard` 等 6 个）本轮未审。
