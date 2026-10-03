# 230 号｜R89-EU：`internal/fsstore` vs 现役降级路径 —— **不等价，且能力集是交叉的**；现役 DB 降级**无指标无告警**

- 日期：2026-10-03
- 轮次：R89-EU
- 起点：执行 115 号（台账 §4.28 L1459）**明文列出的下一轮必做项 ④**（本批最后一个）
  「`internal/fsstore` vs 现役 degraded 路径是否等价」
- **新增待裁决 1 条（97）+ 订正 229 号 §188 一处过度概括 + 守卫新增 2 条测试**
- **零生产代码改动**（两个负控探针已回收）

---

## 一、答案：不等价，而且不是「谁包含谁」，是**两者的能力集交叉**

| 决策矩阵三态 | `internal/fsstore` | 现役（`domains/dbdegradation`） |
|---|---|---|
| **① PG 正常 → 用 PG** | ✅ `Probe()` `SELECT 1`（`fallback.go:73-90`）→ `ModePG`（`:48`） | ✅ 默认路径（`telemetry/client.go:1153-1161`） |
| **② 运行期 PG 错误 → 某状态** | ❌ **只有注释，没有实现**：`Mode` 只有 `pg`/`fs` 两个枚举值（`:45-52`），全包 `degraded` **唯一命中是注释 `:8`** | ✅ **真实现**：`monitor.go:150` 连续 3 次 Ping 失败 → `DBStatusDegraded`（阈值 `main.go:5730-5732`） |
| **③ PG 完全不可用 → 改用文件** | ✅ **独有**：`NewFallback` 探测失败 → `ModeFS` + `slog.Warn`（`:115-131`） | ❌ **不存在**：`main.go:5721` 门控 `dbConn.Enabled() && …` ⇒ **启动时 PG 就不可用 ⇒ Monitor 根本不构造** ⇒ 无降级态、无文件兜底 |

⇒ **fsstore 独有的第 ③ 态，恰恰是现役最缺的那一态**。
⇒ 而 fsstore 承诺的第 ② 态是**纯注释**。
⇒ **两者互有对方缺的**，所以「等价」这个问法本身不成立。

---

## 二、⚠️ fsstore 的决策矩阵第三态是「只有注释的设计意图」

`fallback.go:3-11` 的矩阵原文（115 号转述**核实为真**，但它说「包注释自带」，实际在 `fallback.go` 而非 `fsstore.go` 包头）：

```
3:  // Decision matrix (2026-08-26):
5:  //	startup ─► probe PG ── ok ────► primary = PG
8:  //	                              └─ runtime PG error → Mode = "degraded"
9:  //	                                                  (NOT auto-switched:
10: //	                                                   to avoid split-brain)
11: //	startup ─► PG down ──► primary = FS, log loud warning.
```

**而第 8 行那个状态在代码里不存在**：

- `Mode` 枚举只有 `ModePG = "pg"`（`:48`）与 `ModeFS = "fs"`（`:51`）；
- `Mode` 仅在 `NewFallback` 构造期赋值（`:108/:111/:121/:131`），**全包无运行期 switch-Mode 路径**；
- `Fallback` 是**裸字段结构体**（`:59-63`），**无 setter、无 mutex/once、无后台 goroutine**；
- ⇒ 子代理独立复核确认：`degraded|Degraded` 在 `internal/fsstore/**` 全量（7 文件、不截断）
  **唯一命中就是 `fallback.go:8` 那行注释**。

⇒ `:54-58` 反而说得更准：「choice is made **once at startup and frozen for the lifetime**
（no silent toggle to avoid split-brain）」——**这与它自己矩阵的第 8 行矛盾**。
⇒ 实际语义 = **二态、构造期决定、永不切换**（连从 FS 回 PG 的路径都没有）。
⇒ **这正好也意味着它与现役「自动切回」的行为相反**（见下）。

⚠️ 附带：`Close()` 的 DB 分支（`:145-150`）是**空实现**（只有注释）⇒ 若真接线，DB pool 不会被它关闭。
（该包零引用 ⇒ 今日无实际影响，登记 P3。）

---

## 三、现役的降级体系是**五套同名不同物的机制**，别混为一谈

⚠️ 本轮最容易犯的错就是按名字把它们并成一件事。按**触发源 + 数据流向**分组：

| # | 机制 | 触发源 | 降级后动作 | 会自动恢复吗 |
|---|---|---|---|---|
| 1 | **DB 降级** `domains/dbdegradation` | `db.Ping` 连续 3 次失败（`monitor.go:150`） | 请求日志早退到 `c.fallback.WriteRequestLog`（`telemetry/client.go:1143-1148`）；会话快照/轮换写文件（`session_state.go:320/462`）；Redis TTL 7d→30d（`main.go:5766-5767`） | ✅ **会**（连续 3 次成功 → `Available`，`monitor.go:162`） |
| 2 | 路由降级 `streaming/executors/router.go:530,1837` | 候选凭据被 transient 原因过滤 | 换候选，**仍发 LLM 请求** | — |
| 3 | 凭据降级 `credentialhealth/checker.go:410` | 失败率超阈值 | 标记 `credential_model_bindings` 冷却 | — |
| 4 | 存储模式 `cmd/gateway/storage_mode_init.go:182` | 启动期 env/YAML 静态 | lite=SQLite / full=PG | ❌ 运行期不变 |
| 5 | 仪表盘占位 `admin/dashboardapi/performance.go:96` | 缺 relation 错误 | 只读返回占位 | — |

⚠️ 告警目录里的 `degrad` 字样**全部属于别的东西**：
`deploy/prometheus/alerts/auto-summary-failures.yaml`（LLM map-reduce 降级）、
`fp-slot-saturation.yaml:2,6`（`routing_degradation`，是上面第 2 套），
**没有一条针对第 1 套 DB 降级**（本人对 `deploy/prometheus/alerts/` + `rules/` 逐文件扫过）。

---

## 四、【P2 · 待裁决 97】DB 降级在监控栈里**完全不可见**

进入 DB 降级时（`main.go:5797`）只有一行 `slog.Warn("⚠️ ENTERED DEGRADED MODE …")`。
**没有 Prometheus 指标、没有告警规则**。

⇒ 可观测的**只有**：
- **5 个已注册的管理端读端**（本人逐条核实注册）：
  `handler.go:1140` `/api/admin/db-status`；
  `handler.go:1153-1156` `data-lifecycle/degradation/{control,status,recover,tasks/{id}}`；
- 日志。

⚠️ **危害的准确表述**（不要夸大）：降级期间**请求日志停止入库、改写文件**
（`client.go:1143-1148`）。运维**能**通过管理端查到，但
**① Prometheus 抓不到 ⇒ 做不了趋势与阈值告警；
② 告警规则不存在 ⇒ 不会主动通知 ⇒ 必须有人主动访问那个管理页。**

⇒ 定 **P2**（不是 P1：读端齐全，状态可查）。
⇒ 与 objective「状态更新需要在一个模块中进行，由它来响应多个地方的反馈」**方向一致**：
  `dbdegradation` 正是那个统一模块，**但它自己不把自己上报出去**。
⇒ 修法方向登记为「进/出降级各加一个 gauge（如 `llmgw_db_degraded`）并配一条告警」，
  **不擅自动手**（改指标面属对外契约）。

---

## 五、⚠️ 订正 229 号的 §188：它把话说得太绝对了

229 号 playbook §188 写：「**B/C 的分界不是「有没有调用方」，而是「设计是否仍然成立」**」，
并据此把 `adapter/unified` 改判为 C 类。

**本轮 `fsstore` 证明该规则需要补一个条件**：

- `fsstore` 的设计**没有被否决**——`fsstore.go:42-45` 写「调用方（cmd/gateway/main.go）
  决定启动时谁是权威」、`fallback.go:13` 写「callers should pass an explicit pool」、
  `fallback.go:26-28` 写「Phase B keeps the boundary visible so the next operator audit
  can grep for …」⇒ **它是在等调用方，不是被否决**。
- 而它**有三样无现役等价物的能力**：
  ① **启动期「PG 不可用 → 文件兜底」**（现役门控导致不存在）；
  ② **实体表**（provider/tenant/user/api_key 的 JSON 落盘 + 检索）；
  ③ **Bleve 全文检索**（`entities.bleve/` + `requests.bleve/`）。
- ⇒ **115 号把它判为 B 类是保守正确的**，本轮**不改**。

⇒ **§188 修正为**：判 C 类（可删）需要**两个条件同时成立**：
  **① 设计已被否决**（有废弃声明/ADR 裁定）**且 ② 其独有能力在别处有等价物**。
  只满足 ① 而独有能力无等价物 ⇒ 仍是 B 类，**该问的是产品需求而不是接线**。

⚠️ 顺带一处**同名不同源**（承 220 号已记的 `apihub.HealthState` vs `credentials.health_status`）：
本仓至少有 **5 处**叫 degraded 的语义，**跨包 grep 这个词会全部撞上**。

---

## 六、`fsstore` 零引用 ≠ 无人需要文件存储

⚠️ 这是 115 号那句话（「零引用 ⇒ 真孤儿」）在本轮暴露出的隐患，实证如下：

- `storage/file` 的 **`RequestMirror`（`storage/file/request_mirror.go:63`）已在做
  「PG 故障时请求落文件」且现役已接线**：该文件 `:13-32` 列了 5 个接线点，
  `cmd/gateway/storage_mode_init.go:45` import 它。
- `storage/file` 的 `FileBodiesStore`（`bodies_store.go:41`）承担现役 body 文件层。
- 两包**零互相引用**（fsstore 只 import `internal/atomicrename`；`storage/file` 无 bleve 依赖）。

⇒ 「PG 故障时请求落文件」这个能力**已经有人实现了**（RequestMirror），
  fsstore 的第 ③ 态**与它部分重叠** ⇒ 真正独有的只剩「**启动期**兜底」这一层
  （现役 Monitor 在启动期 PG 不可用时根本不构造）。

⚠️ 另有一点本轮**未核实、必须如实登记**：`request_mirror.go:14-16` 写镜像在
「PG 往返与 degraded 早退**之前**投递」⇒ 降级期镜像**仍然投递**，
而 `client.go:1143-1148` 降级期**另**写一份 fallback
⇒ **两者是否会对同一请求双写、是否需去重，本轮未坐实**（需真库/真进程）⇒ **不报缺陷**。

---

## 七、守卫：`internal/sqlguard` 新增 `degraded_observability_guard_test.go`（集合仍 14 包）

| 测试 | 失败条件 | 设计说明 |
|---|---|---|
| `TestFsstoreModeHasNoDegradedVariant` | `internal/fsstore` 出现 `degraded` 的**代码**用法（当前仅注释命中） | ⚠️ **这是本轮唯一真正有牙齿的门**：若有人把 `fallback.go:8` 那个承诺实现出来（加 `ModeDegraded`），说明设计被复活 ⇒ 门转红并要求「先裁决它与现役 `DBStatusDegraded` 的关系」 |
| `TestDegradedStateObservabilitySurface` | —— | **只打印不自失效**：列出 DB 降级在指标 / 告警规则 / 管理端读端 / 日志 四面上的**机械清单**。将来有人补了指标或告警，门会打印「可观测缺口可能已补，请人工确认并关闭待裁决 97」 |

⇒ 为什么第二条不做成失败条件：待裁决 97 **尚未裁决**，
  按 §157「不让门一直红」，已登记的缺陷只能豁免 + 打印。

---

## 八、诚实边界

- **未起真进程、未连 PG/Redis/Docker、未执行 SQL** ⇒
  **两列/各降级路径的真实运行期行为未观测**；`main.go:5721` 门控导致「启动期 PG 不可用
  ⇒ 无兜底」是**静态结论**（未在真环境复现）。
- ⚠️ **`storage/file` 与 fsstore 的「能力重叠」判断基于包注释与 import 图**，
  **未逐方法 diff** `PutRequest`/`PutBodyPart` 与 `FileBodiesStore.Write` 的字段覆盖
  ⇒ 「部分重叠」是**方向性结论**，不是穷尽证明。
- ⚠️ **fsstore 的三个测试文件本轮未执行**（`go test ./internal/fsstore/`），
  能力清单来自静态阅读；「决策矩阵三态测试覆盖 = 0」是**静态检索结论**。
- **未核实** `request_mirror` 与 telemetry fallback 是否在降级期双写同一请求（见 §六）。
- **未核实** fsstore 的 Bleve 检索需求是否已被 PG 全文检索（tsvector）满足
  ⇒ 「删掉会丢能力」这一判断**只覆盖代码层等价物，不覆盖产品层替代方案**。
- `go test ./...` 全量未跑；**CI 从未运行**；本轮**零生产代码改动**。

---

## 九、编号与去向

- **新增待裁决第 97 条（P2）**：DB 降级**无 Prometheus 指标、无告警规则**，
  只有 `slog.Warn` + 5 个管理端读端（`handler.go:1140,1153-1156`）
  ⇒ 「请求日志已停止入库」监控栈完全看不见。
  修法方向：进/出降级各加 gauge + 一条告警规则。
- **115 号必做项 ④ 关闭**：fsstore 与现役降级**不等价**，
  且能力集**交叉**（fsstore 独有「启动期 PG 不可用兜底 + 实体表 + Bleve 检索」，
  现役独有「运行期降级 + 自动恢复 + 5 个读端」）。
- **订正 229 号 §188**：判 C 类需**两个条件同时成立**（设计已被否决 **且** 独有能力有等价物）；
  只满足前者时仍是 B 类。⇒ 本轮**不改** 115 号对 fsstore 的 B 类定性（它是对的）。
- **登记 P3 三条**：① `fallback.go:145-150` `Close()` 的 DB 分支空实现；
  ② fsstore 决策矩阵**三态测试覆盖 = 0**，跨进程 flock 无测试；
  ③ `domains/dbdegradation/fallback.go:12-54` 的 `DegradationController` 零生产调用点。
- **守卫**：`internal/sqlguard` 新增 2 条测试，集合仍 14 包。
- **115 号 4 个必做项全部完成**（① 227 号 ② 228 号 ③ 229 号 ④ 本轮）。
- **playbook**：§190。

---

## 十、playbook §190

**§190 「默认参数」与「我以为的默认」相反时，尺子会静默地报 0，而 0 长得和真相一模一样。**

本轮 `TestFsstoreModeHasNoDegradedVariant` 的核心任务是区分
「`degraded` 只存在于注释里」与「`degraded` 存在代码里」。
第一版报 **注释 0 次、代码 0 次** ⇒ 看上去像「那个承诺从来没被写下来」。

真相是 `fallback.go:8` **明明白白写着** `Mode = "degraded"`（我亲自读过该文件）。

真因：我用 `parser.ParseFile(fset, path, nil, 0)`，**第四个参数 `mode=0`
意味着未启用 `parser.ParseComments`** ⇒ `f.Comments` **恒为空切片**
⇒ 注释统计**永远**是 0，而这段代码在逻辑上「永远正确」。

⚠️ 这比前几轮的尺子错更隐蔽，原因是：
  - 它**不报错**（`f.Comments` 是合法字段，只是空的）；
  - 它报的数字是 **0**，而 **0 是本审计里最容易被采信的数字**（§157/§181）；
  - 它与「真的没有注释」**在输出上完全无法区分**。

⇒ **判别式**：凡是「用某个 API 统计注释 / 文档 / 元数据」的尺子，
  **第一次跑出来的 0 必须用一个已知存在的事实去反证**，
  而不是「跑出来是 0，那就当 0」。
  本轮的已知事实是「我刚刚逐行读过 `fallback.go:1-29`，第 8 行就在那儿」——
  **有 firsthand 观察时，尺子优先被怀疑**。
⇒ 配一条通用自查：**若一条尺子的两个分支（这里是有/无注释）都报 0，
  先怀疑分支本身失效，而不是怀疑世界为空。**
  两个分支同时为 0 的概率，远低于「同一个默认参数让两个分支一起瞎」。
⇒ 修法一行：`parser.ParseFile(fset, path, nil, parser.ParseComments)`。
  ⇒ 已检查本会话其它守卫：228 号 `productionCallSites` 与 229 号 `importersOfPackage`
  **只取 `Ident`/`SelectorExpr`/`CallExpr`/`f.Imports`，不依赖 `f.Comments`** ⇒ 未受影响；
  229 号的废弃声明检查走 `os.ReadFile` 读文本 ⇒ 也未受影响。

**附带一条关于「门自己的输出」**：NC-P 转红时，那行总结仍打印
「在代码中出现 0 次」——因为 `t.Errorf` 不中断执行，计数变量没被更新。
⇒ **门的总结行与它的失败条件必须读同一份计数**，
  否则「门红了」和「日志说有 0 个」会同时出现在一次运行里，
  而**读日志的人会相信后者**。已改为 `codeHits` 计数 + 分支化结论。
⇒ 与 §189（门会误报自己的订正文字）互补：那条讲**判据**误报，
  这条讲**同一份输出里失败信息与总结信息互相矛盾**。

**§190 之外的一条方法论订正（针对 229 号 §188）**

229 号 §188 写「B/C 的分界不是有没有调用方，而是设计是否仍然成立」——
本轮 `fsstore` 证明它**需要补一个条件**（详见 §五）：
判 **C 类（可删）** 需要**两个条件同时成立**：
**① 设计已被否决**（有废弃声明或 ADR 裁定）**且 ② 其独有能力在别处有等价物**。
只满足 ① 而独有能力无等价物 ⇒ **仍是 B 类**，该问的是产品需求而不是接线。
⇒ `fsstore` 三样独有能力（启动期 PG 兜底 / 实体表 / Bleve 检索）都**无现役等价物**
⇒ **115 号的 B 类定性本轮不改**。
