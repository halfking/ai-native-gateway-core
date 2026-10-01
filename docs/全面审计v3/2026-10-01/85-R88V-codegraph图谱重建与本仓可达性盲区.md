# 85-R88-v：codegraph 图谱重建 + 其在本仓的可达性盲区（objective 步骤 2）

- 轮次：R88-v
- HEAD 基线：`ae5ef9634`
- 触发：objective「步骤」第 2 条显式要求——「**使用 codegraph 先生成代码图谱，加速代码的关联与索引效率**」。本会话此前一直用 grep/rg 做检索，**从未真正执行这一步**（`tests/48h-audit/00-PLAN.md:31` 也写着「若 codegraph 工具可用：先全量重建图谱」）。
- 结论：**图谱已重建；但 codegraph 的调用级可达性在本仓系统性失真，不可用于判断「死代码 / 可达性」**——且有三条可复现的硬证据
- 改动：`.codegraph/`（gitignored 本地生成物，损坏产物已移入回收站）；**零生产代码、零门**

---

## 1. 重建过程（含一处必须记录的故障）

| 步骤 | 结果 |
|---|---|
| 工具位置 | `__DEV_HOME__/.npm-global/bin/codegraph`，native engine v3.7.0 |
| 旧图谱 | `.codegraph/graph.db`，**238MB**，构建于 2026-09-30 23:23（相对今日 20+ 提交已过期） |
| 首次增量构建 | ❌ **`fatal error — database disk image is malformed`**（日志同时提示 `Engine changed (wasm → native), promoting to full rebuild`） |
| 处置 | `.codegraph/` 是 **gitignored（`.gitignore:63`）、未跟踪、可再生的本地生成物**，且 DB 已损坏不可用；codegraph **无 `clean` 子命令** ⇒ 用可恢复删除移入回收站后全量重建 |
| 重建结果 | **153,104 节点 / 273,708 边 / 42,240 函数 / 39,963 CFG**，6332 文件（go 4631 / ts 951 / bash 601 / py 80 / js 50 / lua 19） |

**登记一条**：`roles` 输出里 `[dead-ffi]` 计数恰好是 **30000** 这个整数——像是分类阶段的截断上限，不是真实分布。做统计时不能把它当精确值。

---

## 2. 硬证据一：`Manager.Allow` 被判死，实际是活的

我在 84 号报告里写了「熔断 5 个方法共 14 个生产调用点」，用的是 grep。本轮用 codegraph 独立复核：

```
$ codegraph where "Manager.Allow"
o Manager.Allow [dead-ffi]  domains/credential/breaker.go:853
  No uses found
```

而 grep 的独立证据：

```
domains/streaming/executors/context_summarize.go:463:  e.Circuit.Allow(...)
domains/streaming/executors/executor_dispatch.go:810:  e.Circuit.Allow(...)
```

**我先假设是「接口调用不可见」**——因为这是调用图工具的经典盲区。**查 `Executor` 的字段类型后发现假设错了**：

```go
// domains/streaming/executors/executor.go:537
Circuit *credential.Manager      // 具体类型，不是接口
```

⇒ **具体类型的跨包字段接收者调用，图谱照样看不见。**

---

## 3. 反向对照：规律是「只解析同文件内的调用边」

不是抽样运气问题，是可复现的规律：

| 符号 | codegraph 判定 | 实际 | 是否同文件内 |
|---|---|---|---|
| `Manager.GetOrCreate` | `[utility]`，Used in ×4 | 正确 | ✅ 4 处全在 `breaker.go`（`:822/:838/:847/:853`） |
| `Breaker.Allow` | `[utility]`，Used in `breaker.go:853` | 正确 | ✅ 同文件 |
| **`Manager.Allow`** | **`[dead-ffi]` No uses found** | ❌ **2 处跨包调用** | ✗ `domains/streaming/executors/*` |
| **`Manager.RecordSuccess`** | **`[dead-ffi]` No uses found** | ❌ 有跨包调用 | ✗ |
| **`Manager.ReleaseProbe`** | **`[dead-ffi]` No uses found** | ❌ **7 处跨包调用** | ✗ |

⇒ **规律精确**：codegraph 解析出了 `GetOrCreate` 的 4 个「使用」，**它们全部是 breaker.go 内部 4 个兄弟方法互相调用**；而**经结构体字段跨包发起的调用，一条都没进图**。

---

## 4. 硬证据二 + 三：入口数与调用覆盖

### 4.1 `entry` 角色只有 4 个，且**没有一个是 Go handler**

`codegraph stats`：

```
Nodes: 153104 total      Edges: 273708 total
  calls 57618            imports 942
Roles: 273680 classified symbols
  dead 127474   dead-leaf 89252   dead-ffi 30000
  dead-unresolved 8222   core 7810   utility 5529
  test-only 5389   entry 4
Graph Quality: 71/100
  Caller coverage: 37.2% (18448/49534 functions have >=1 caller)
```

那 4 个「入口」实际是：

| 符号 | 位置 | 真身 |
|---|---|---|
| `event:console` | `deploy/llmgo-245-ui-test.mjs:62` | **JS 脚本里的一行 console 输出** |
| `event:mocktheme` | `docs/proposals/2026-09-30-stats-ui/assets/mockup.js:217` | **docs 里的 mockup 脚本** |
| `event:data` | `web/scripts/i18n-fix.mjs:257` | **i18n 修复脚本** |

**反向对照（真实入口数）**：

```
grep -rE '\.(HandleFunc|Handle|GET|POST|PUT|DELETE|PATCH)\(' admin/ cmd/  （排除测试）
  → 598 处路由注册（其中 admin 包内 434 处）
```

⇒ **598 个 Go 路由注册，codegraph 一个都没认成 entry；它认出来的 3 个「入口」是 JS 里的 console。** 这不是定义差异，是**识别失败**。

### 4.2 调用覆盖只有 37.2%

49,534 个函数里只有 18,448 个有已知调用方；57,618 条 `calls` 边摊到 49,534 个函数上 ≈ **1.16 条/函数**，对这种体量的仓库明显偏少。

---

## 5. 结论：codegraph 在本仓的**可用边界**

| 能力 | 本仓是否可信 | 依据 |
|---|---|---|
| 依赖/耦合热点（`stats` 的 fan-in/fan-out、imports 边、communities 0.8091） | ✅ **可用** | 数值与代码结构吻合（如 `web/src/api/tuning.ts` fan-out 659） |
| 复杂度（cognitive/cyclomatic/MI）、`cycles` | ✅ 大体可用 | 与 `go vet`/gocyclo 类工具同源 |
| **调用级可达性（`where` 的 uses、`roles` 的 dead-*）** | ❌ **不可用** | §2/§3/§4 三条硬证据 |

**因此**：

- **不要**用 `codegraph roles --role dead*` 作为**删除死代码**的依据——`Manager.Allow`/`RecordSuccess`/`ReleaseProbe` 会被标成死代码，而它们在主 dispatch 热路径上被调用。照做会**直接删掉熔断器的对外 API**。
- **不要**用 `where ... No uses found` 判定「该函数无人调用」。**低命中/零命中在这里是工具失真，不是事实**（与 conventions §10.2「低命中数是危险信号」同族，但方向相反：那次是搜错名字，这次是**工具没看见**）。
- 可以放心用的：模块地图、耦合热点、复杂度排序、社区划分（`communities` / `structure`）——这些是**文件/导入级**，不受调用边缺失影响。

**与本会话既有教训的关系**：这与「恒真的门」「门全绿≠门覆盖我」「成熟工具负收益」**同族但方向相反**——那三类是**门看起来在守、实际不守**；这一类是**工具报告「这里是死的」、实际是活的**。**共同点：失败时输出都很干净、很有说服力，而唯一能识破的手段是「换一个触达面不同的方法去独立复核 + 配反向对照」。**

---

## 6. 明确未做

- **未**用 codegraph 重做 84 号报告里的 14 个调用点计数（已用 grep 定稿，且本轮证明 codegraph 在该问题上不可信）；
- **未**排查 graph.db 损坏的根因（SQLite 文件损坏；未查是否与引擎切换 wasm→native 或磁盘有关）；
- **未**评估 `codegraph check`（manifesto 规则门）在本仓的可靠性——按本报告结论，它的判定同样依赖调用图，**预期不可信，未实测**；
- **未**修 `roles` 的 `[dead-ffi]=30000` 是否为分类截断（工具内部行为，未读其源码）。

---

## 7. 变更清单

| 文件 | 改动 |
|---|---|
| `.codegraph/` | 损坏产物移入回收站后**全量重建**（gitignored 本地生成物，不入库） |
| `docs/全面审计v3/2026-10-01/85-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |
| `docs/audit/playbook/conventions.md` | 新增 **§12**：调用图工具的可达性判据边界 |

**零生产代码、零配置、零门、零 CI 行为变化。**

---

## 8. 交叉引用

- conventions **§11.2**（选型量触达面/误报面）——本报告是它的**加强版**：本例中 codegraph 在「覆盖类别」上远不输 bodyclose，但**触达面在本仓的关键问题上为 0**
- conventions **§10.2**（低命中数是危险信号）——方向相反的同族
- 82 号 §5：同一次审计里对 `bodyclose` 的负收益判定；两者合起来构成「工具选型」的一对正反例
