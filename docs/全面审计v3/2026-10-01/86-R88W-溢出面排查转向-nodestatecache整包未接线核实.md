# 86-R88-w：溢出面排查转向 —— `domains/nodestatecache` 整包未接线核实

- 轮次：R88-w
- HEAD 基线：`5aada5ca0`
- 触发：objective 显式点名的安全类要求——「检查各类场景安全：…**各数据结构与参数有无溢出可能性**」
- 结论：**溢出面本身无缺陷**（位打包做了饱和与环绕防护）；排查过程中撞出一个更大的发现——`domains/nodestatecache` **整包在生产完全未接线**，但**核实后确认这是设计文档规定的门禁结果，不是缺陷**
- 改动：**零生产代码、零配置、零门**。仅本报告 + README 索引

---

## 1. 排查路径：溢出线索指向了一个更大的问题

起点是 objective 的「有无溢出可能性」。窄化整数转换的分布（生产代码，排除 vendor/test/web）：

```
int32( → 40    uint32( → 30    uint16( → 21    int16( → 1    uint8( → 6
```

绝大多数是无害的（状态枚举 `int32(State)`、哈希 `uint32(offset32)`、位运算 `uint32(state)|uint32(errKind)<<8`）。**唯一值得深挖的是 `domains/nodestatecache/resources.go` 的位打包**：

```go
type NodeResourceSlot struct {
    ConcUsed, ConcLimit uint16 // 在途并发（Limit=0 不限）
    FPUsed, FPLimit     uint16
    RPMUsed, RPMLimit   uint16
    WindowID, Flags     uint16
}
// word0 = ConcUsed(16) | ConcLimit(16) | FPUsed(16) | FPLimit(16)
```

三个 Limit 都是 **uint16 ⇒ 上限 65535**，而注释说「Limit 由集成者/对账注入」。若注入值超过 65535 会**静默截断**，而 `computeFull` 用 `Limit>0 && Used>=Limit` 判满载 ⇒ 截断成小值会导致**提前判满载**（错误限流）或 `Limit` 变 0（**变成不限**，绕过准入）。这是真正值得查的面。

### 1.1 但这个溢出面**不成立**——因为喂它的路径从未执行

顺着 Limit 的来源往上追：

```
NodeResourceSlot.Limit  ←  reconcile.go:74-76  ←  AuthorityRecord（reconcile.go:12-14，全 uint16）
                       ←  c.opts.Authority  （nodestatecache.go:37，导出字段）
```

查 `Authority` 的设置方：

| 检查 | 结果 |
|---|---|
| 全仓构造 `AuthorityRecord{` | **0 命中**（生产代码） |
| 全仓设置 `Authority:` | **0 命中** |
| `SetAuthority` / `WithAuthority` setter | **0 命中**（仅 vendor grpc 里的同名词，与本仓无关） |
| 其余 "authority" 命中 | 全是 `cmd/license-authority`（**完全无关的 license 服务**） |

而消费侧 `reconcileTick`（`reconcile.go:88-94`）第一行就是：

```go
if c.opts.Authority == nil { return ReconcileReport{} }
```

⇒ **对账路径在生产从未执行**，`Reconcile` 的位图漂移校正、状态漂移校正、资源上限注入**全部从未发生** ⇒ **uint16 截断的触发条件不可达**。

**登记一条**：`0 命中`在这里差点变成误判（结论会是「没人设 Authority，可能是我搜错了字段名」）。我按 §10.2 复核了 setter、字段名、导出符号后才敢定论。

### 1.2 位打包本身的防护是到位的

追查过程中确认三处**做对了**的地方（不是缺陷，是应该被记录的正确设计）：

- `satInc16r`（`resources.go:80-84`）在 `math.MaxUint16` **饱和**而非回绕 ⇒ `Used` 不会溢出翻转成小值；
- `minuteWindow`（`:92`）16-bit 每 65536 分钟（~45 天）环绕，**已在注释里写明**且环绕后果良性（窗口视为翻转、RPM 复位）；
- `load`（`:107-112`）有 `base < 0 || base+1 >= len(t.words)` 双重边界检查，负 id 与越界都安全。

---

## 2. 更大的发现：整包未接线

因为 `Authority` 无人设置，顺着 `New` 查下去，发现的是**整个包与仓库完全隔离**：

| 检查 | 结果 |
|---|---|
| `nodestatecache.New` / `nodestatecache.Options` 的生产调用点 | **0** |
| 全仓 import `github.com/kaixuan/llm-gateway-go/domains/nodestatecache` | **0**（生产 0、测试 0） |
| `Full` 位图的消费方 | 只有包内 `selector.go:195/241`——而包从未被实例化 |
| 是否在 `Makefile` / `GUARD_PACKAGES` 等专门门里 | **否** |
| 包体量 | **23 个 go 文件 / 3,324 行**（测试 1,629 行 ⇒ **生产约 1,695 行**） |
| `go test ./domains/nodestatecache/` | **ok 0.938s** ⇒ **是可用代码，不是坏代码** |

若只看到「零 import + 测试全绿」，很容易写成「1,695 行废弃死代码，建议清理」——**那是错的**。

---

## 3. 决定性交叉验证：未接线是**设计规定的门禁结果**

`domains/nodestatecache/README.md` 本身是**诚实的**：把五个接入点明确标为「集成接线点（五个注入接口）」+「**建议**实现位置」，并自陈 `domains/ursm/v2/resource/pools.go` 是「**现为死脚手架恒 0**」。所以**不是「注释说谎」型**（区别于 R87-j 的 `DeleteOlderThan`、R88-t 的幽灵 `routing_health_checks.circuit_open`）。

再往上游找依据——`docs/会话优化v4/客户端会话保持.md:731`：

> 任务：实现 `domains/nodestatecache`…**前置门禁：必须先通过 P0 的 URSM Ready/Redis failure contract 测试；在 mirror-first 矛盾修复前不得接入生产路由。**

`:887` 复述：`domains/nodestatecache`（**仅在 P0/P1 权威与资源门禁后接入**）。

⇒ **未接线是方案文档明文规定的门禁结果。** 这条交叉验证是本报告的关键——**若止步于「零 import」，就会把一个按设计搁置的组件误报成废弃代码。**

---

## 4. 门禁现状：T0 看起来**已满足**

文档为 T0 列了 3 项剩余工作。逐项核实：

| T0 剩余项 | 核实结果 |
|---|---|
| ① 按 §14.3 冻结档位命名 | ✅ 已做——`manager.go:457-470` 命名了 `target gear`（默认 `MirrorGraceEnabled=false`）与 `optional grace gear`（settings_kv `llmgw_ursm_mirror_grace_enabled`） |
| ② 清理 `manager.go:376-384` 的过时 fail-open 注释 | ✅ 已做，且**行号已漂移**（内容在 `:457` 起） |
| ③ 补 Redis 故障 / ready gate / 代际过期 / **跨租户 key** 单测 | ✅ 大体已有（详见下） |
| 验收产物 `CONTRACT_FREEZE_<日期>.md` | ✅ **存在**：`docs/会话优化v4/CONTRACT_FREEZE_2026-08-22.md` |

`go test ./domains/ursm/v2/...` → **15 个包全 ok，exit 0**。

### 4.1 两次自我修正（比结论本身更值得记）

**修正一：「跨租户 key 测试 0 命中」是假 0。** 我用 `CrossTenant|crossTenant` 搜，`domains/ursm/v2/` 下 **0 命中**，差一步就写成「T0 第 ③ 项未完成」。换命名再搜才发现测试都在，只是名字不含这两个词：

```
cache/nodemirror_prefix_test.go:10  TestNodeMirrorConfiguredPrefixAndTenantIsolation
recovery/warmup_test.go:25          TestWarmupNumericTenantUsesTaggedKey
admin_test.go:106                   TestClearStateForTenant_MissingKeyIsSuccess
probe_test.go:128                   TestApplyProbeCanonicalSchemaRejectsEmptyTenant
manager_t0_contract_test.go:14/32   TestFilterAndScoreReadyGatePrecedesEmptyTenant
                                     TestFilterAndScoreRejectsEmptyTenantBeforeMirrorOrRedis
```

⇒ **「假 0 命中」第 13 次**，与 conventions §10.2 同族（搜的名字不对，不是东西不存在）。

**修正二：我差点把已修的注释报成仍在。** 我 grep 到 `manager.go:459` 有 "fail-open" 字样，本欲报告「T0 第 ② 项未完成、过时注释仍在」。读原文后发现那句话是**在描述该行为已被关闭**：

> `会话优化 v4 §14.3 mirror-first gear contract (2026-08-18, closes the P0-1 residual): a full mirror hit is **NO LONGER** an unconditional fail-open path that skips Redis entirely.`

⇒ **恰恰相反**：T0 第 ② 项是**已完成**的，这条注释是它完成后的准确描述。**只 grep 关键词就会得出方向相反的结论。**

---

## 5. 结论与建议

| 判断 | 依据 |
|---|---|
| 溢出面**无缺陷** | `satInc16r` 饱和、`minuteWindow` 环绕已文档化且良性、`load` 双重边界检查；唯一的截断风险路径（`Authority` 注入）**从未执行** |
| `nodestatecache` 未接线**不是缺陷** | `docs/会话优化v4/客户端会话保持.md:731/:887` 明文门禁；README 亦如实标注为「建议接入」 |
| **但门禁现已具备** | `CONTRACT_FREEZE_2026-08-22.md` 存在、T0 契约测试存在且通过、mirror-first gear contract 有测试（`manager_filter_test.go:149`）、URSM v2 全 15 包绿 |

⇒ **这不是一个「要不要清理死代码」的问题，而是一个「前置条件已具备、是否现在接入」的决策项。**

**本轮不擅自接入**，理由：接入生产路由是最高风险级别的行为变更，且该方案要求 5 个注入接口（`Scorer` / `StickyLookup` / `ModelFallback` / `RecentSuccessLookup` / `AuthoritativeSnapshot`）由集成者实现——**这不是一个包能自己完成的工作**，需要产品与架构裁决工作量。**新登记为待裁决第 35 条。**

---

## 6. 明确未做

- **未**核实这 5 个注入接口各自的实现成本（`Scorer` 需包 URSM `FilterAndScoreReadyWithSource` 等）；
- **未**评估接入后的行为风险（`nodestatecache` 是**派生读缓存**，`README` 明写「权威仍在 URSM Redis、禁止双写」，接入面涉及 dispatch 路由）；
- **未**重读 `docs/会话优化v4/客户端会话保持.md` 全文（只查了 T0/T10 相关段落与 grep 命中的行）；
- **未**核实 `domains/ursm/v2/resource/pools.go` 这个「恒 0 死脚手架」是否也被 `nodestatecache` 之外的东西消费（它有生产文件 `domains/ursm/v2/resource/pools.go:71`，与 nodestatecache 无关，是另一处待查项）；
- **未**修改任何代码或文档（含那份已过期的 T0「剩余工作」清单——按「不回改历史」只登记现状）。

---

## 7. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/86-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |

**零生产代码、零配置、零门、零 CI 行为变化。**

---

## 8. 交叉引用

- conventions **§10.2**（先定位函数体再决定搜什么名字）——§4.1 修正一是该家族的第 13 次
- 83 号 §4.1（glob 假干净第 12 次）——本报告 §4.1 是第 13 次，**两次都发生在同一轮内**
- 待裁决 27 的「新增 2：域 A 整域休眠的定性」——本报告是**更大粒度**的同型案例，但**结论相反**（那个待定性，本报告已定性为「按设计搁置」）
- 84 号 §4.1（幽灵消费面）——区别在于：那个是注释说谎，这个是注释诚实 + 上游文档有门禁依据
