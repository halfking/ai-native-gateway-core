# 231 号｜R89-EV：免费 token 池的「聚合使用」侧 —— **扫描链与注册链是两条不相通的链**，且阻塞它的产品裁决**从未进入待裁决台账**

- 日期：2026-10-04
- 轮次：R89-EV
- 起点：objective 第 32 项「检查**免费的 token 资源自动扫描、注册、和聚合使用，做为一个标准的供应商池来处理**」
  （台账覆盖表 `:55` 状态为「部分」）
- **新增待裁决 1 条（98）+ 守卫新增 2 条测试 + 复核 59 号 P1 仍成立**
- **零生产代码改动**（负控探针已回收，`git diff` 复核为空）

---

## 一、结论：objective 的三个环节，在「自动」这一侧是断的

| 环节 | 实现 | 落点 |
|---|---|---|
| **自动扫描** | `domains/freediscovery`（10 个生产文件 + 9 个测试） | `INSERT INTO free_resource_catalog`（`import_service.go:261-272`） |
| **注册** | **另一条链**：`admin/free_pool_extra.go` | `INSERT INTO credentials`（`:1628`）+ `model_offers`（`:1690-1695`） |
| **聚合使用** | 路由候选 `provider/client.go:1565 candidateQuerySQL` | **只有 (b) 注册链的凭据能进；(a) 扫描链的产物进不了** |

⇒ **两条链不共用任何代码。** 扫描结果能被人看见、能作为参考，
  但**没有任何自动路径把扫描到的资源变成一个可路由的凭据**。

### 决定性证据：`free_resource_catalog` **根本不是凭据表**

完整列清单（28 列，`db/db_omnifree.go:47-82` + `sql/migrations/084-freediscovery-schema.sql:148-152`）：
`id, provider_code, model_id, display_name, display_name_en, free_type, monthly_tokens,
daily_tokens, credit_tokens, pool_key, tos_verdict, tos_notes, tos_reviewed_at,
tos_reviewed_by, constraints_json, discovery_method, verified_at, last_probe_status,
last_probe_error, enabled, disabled_at, disabled_reason, created_at, updated_at,
tenant_id, trains_on_prompts, source_type, discovery_task_id, last_synced_at, upstream_metadata`

⇒ **无 `api_key` / `secret` / `credential_id` 任何一列。**
⇒ 它在架构上**不可能**创建凭据 ⇒ 扫描链的产物**永远无法成为路由候选**。

### 而且它在现役里的角色是「过滤器」，不是「凭据来源」

`free_resource_catalog` 全仓唯一消费路径是 auto/* 虚路由：
`handler_autocombo.go:183` 取目录条目 → `:208-224` 对每条 `model_id`
调**同一个** `provider.Client.GetCandidates` → `virtual_factory.go:300 filterCandidates` 做 allowlist 过滤。

⇒ 即：**它是既有 credential 上的过滤器**；没有对应 credential 时贡献 0 候选，
  且 `handler_autocombo.go:188-195` 直接返回 `ErrOmniFreeNoCandidates`（**不降级**）。

⇒ 59 号登记的「标准供应商池零成员：86 条凭据中 0 条 free」
（`free_pool_extra.go:1628`；`routing.go:4120`）**至今仍成立**，
但 59 号没有说清**为什么**会零 —— 现在的答案是：**能进池的那条链是手工/环境变量驱动的，
自动扫描链与它不连通**。

---

## 二、复核 59 号的 P1：仍成立，而且**阻塞点是代码自己写下的产品裁决**

59 号 P1「扫描入口结构性锁死：模板全 disabled + `Run()` 在 `createTask` 前返回 + 无自动复活
⇒ 整域休眠且不可自愈」。

**本轮逐行复核（HEAD `b4daddfcd`）**：

- `discovery_engine.go:110-116` 取模板；`:117-120`
  `if !tpl.Enabled { metrics…("template_disabled"); return nil, ErrTemplateDisabled }`
  ⇒ 确实**在 `:123` `createTask` 之前返回**。
- `template_manager.go:312-319`：`enabled` 只能在管理员显式传 `*req.Enabled` 时改；
  且**只有设为 enabled 时**才清 `consecutive_scan_failures` / `last_scan_failure_at` / `auto_disabled_at`
  ⇒ 存在「自动禁用 + 手工复活」，**无自动复活**。

⚠️ **但真正的关键在注释里**（`discovery_engine.go:107-109`，原文）：

> `//    R30 域A P1-②（round 31 标注）：disabled 是单向门——本拒绝 + 调度器`
> `//    只取 enabled=TRUE，且无自动复活路径；免费 token 池「暂不投入」的产品裁决未拍板前，`
> `//    此入口对 disabled 模板结构性锁死（复活只能手工）。`

⇒ **这条 P1 比 59 号知道的更早被登记（R30 / round 31）**，
  且**阻塞点被明写为「产品裁决未拍板」**，不是技术缺陷。

### 🔴 而台账里没有这个裁决项

在 `00-审计覆盖台账.md` 全文检索「暂不投入 / 免费 token 池 / freediscovery / pool_group / 标准供应商池」，
**没有任何一条【待裁决】条目与它对应**（台账里与免费池相关的唯一待裁决是 225 号的
**待裁决 91**，那是个不同的问题：UI 文案教运维往一个没人加载的文件里写 Key）。

⇒ **一条 P1，若没有对应的裁决项，就永远不会被解决** ——
  它既不会被修，也不会被判为「已接受的设计」。
  这与 228 号发现的「115 号把有答案的项重新排队」是同一族**队列机制**缺陷，
  但方向相反：**那一类是队列里多了不该有的，这一类是队列里缺了必需的**。

**⇒ 新增待裁决 98**：把「免费 token 池是否投入生产」正式立为裁决项，
  附本轮已备齐的决策材料：三环节断链的机制说明（§一）+ 两种路线的产品含义 +
  若不投入则应把 `discovery_engine.go:103-120` 的单向门改为**显式关闭态**
  （现在的形态是「看起来在跑、实际永远跑不到」，属误导性状态）。

---

## 三、⚠️ 推翻子代理的一条推断：`pricing_plans` **不会**覆盖免费池的 0 价

子代理报告：「候选取价用 `COALESCE(mo.unit_price_in_per_1m, pp_fb.plan_in)`（`client.go:1643-1644`），
pricing_plans 的非零行会覆盖 `free_pool_extra.go:1691-1695` 写的 0 价」。

**不成立**。逐行核实：

- `free_pool_extra.go:1695` 的 INSERT 显式写入 `billing_mode='free', currency='CNY', 0, 0`
  ⇒ `unit_price_in_per_1m = 0`、**不是 NULL**；
- `client.go:1643` 是 `COALESCE(mo.unit_price_in_per_1m, pp_fb.plan_in)`
  ⇒ **`COALESCE` 只在 NULL 时回退**，而 `0` 不是 NULL
  ⇒ 取到的就是 `0` ⇒ **免费价保持 0，不会被价表覆盖**。

⇒ 子代理的错法很典型：**看到「LEFT JOIN 到价表」就推断「价表会生效」**，
  而**取数表达式的语义**（`COALESCE` 的两个分支哪个先命中）才是决定性的。
  与 §183（判据要对着权威/实际表达式写）、§177（宽严与权威一致）同族。

⚠️ **诚实边界**：以上只证明**注册链写出的 offer** 是 0 价。
  真库里是否存在**其它路径**写出的 `billing_mode='free'` 且 `unit_price_*` 为 **NULL** 的
  `model_offers` 行（那样才会回退到价表），**本轮未查**（需真库）⇒ **不报缺陷**。

---

## 四、两条**正向**事实（不要只报坏的一面）

1. **免费池在候选排序里是第一档，不是被排除。**
   `client.go:1793-1802` 的 `ORDER BY`：
   `CASE COALESCE(mo.billing_mode,'per_token') WHEN 'free' THEN 1 … ELSE 2 END`
   ⇒ 一旦注册进去，**优先级最高**。
2. **候选查询没有任何按「免费/来源」的排除条件。**
   `client.go:1773-1790` 的全部过滤项（租户 / `status` / `v.is_routable` / broken pair）
   与视图 `460_v_routable_credential_models_periodic_exhausted.sql:33-53` 的内部门禁
   **都不含 free/source 维度**。
   ⇒ 也就是说：**机制是齐备的（会优先选免费），缺的是成员（没人注册进去）。**

---

## 五、⚠️ 端到端测试覆盖缺口：只测了「注册成功」，没测「被选中」

- `admin/routing_free_pool_registration_test.go:44,72,90,113,132`
  五个用例**全是 sqlmock**，只验 INSERT/UPDATE 与事务回滚；
- `domains/freediscovery/import_service_test.go:106-330` 只验 `free_resource_catalog` 的 SELECT/INSERT/UPDATE；
- `domains/autocombo/virtual_factory_test.go`、`domains/freeresource/pool_dedup_test.go`
  全文 **0 命中** `credentials` / `model_offers` / `GetCandidates`；
- 候选侧测试一律用 fake resolver（`domains/streaming/candidate_modality_test.go:18`、
  `audio_endpoints_test.go:31`、`survival_no_nodes_e2e_test.go:47`），
  **不触真实 `candidateQuerySQL()`**。

⇒ **(a)(b) 之间的断链在测试层面完全不可见。**

---

## 六、守卫：`internal/sqlguard` 新增 `omnifree_chain_guard_test.go`（集合仍 14 包，零接线成本）

| 测试 | 失败条件 | 为什么这样设计 |
|---|---|---|
| `TestOmniFreeCatalogHasNoCredentialColumns` | `free_resource_catalog` 出现任一凭据列（`api_key`/`secret`/`credential_id`/`*_ciphertext`/`access_token` 等 10 个词形） | 报告 231 的**全部结论都建立在这个结构前提上**。加了凭据列 ⇒ 架构变了 ⇒ 门转红并要求「重新推导 231 的每一条结论」——**这是结构变化，不是缺陷修复** |
| `TestOmniFreeGapIsRecorded` | —— | **只打印不自失效**：打印「free 档是第 1 优先档」与代码自陈的阻塞点原文 |

⚠️ 为什么第一条要**解析 DDL 而不是读 Go 结构体**：
列集是**数据库实际强制**的东西；一个从不写入的 struct 字段会让表**看起来**像凭据源。
⇒ 另加一条防空跑断言：**解析不到任何 DDL 块 ⇒ `Fatal`**
（否则「判据扫错地方」会表现为「通过」，§181）。

**负控 NC-Q**：在 `sql/migrations/075-omnifree-schema.sql` 的该表加一列
`api_key_encrypted text` ⇒ ✅ 转红、精确点名列名、并把列数从 23 报到 24；探针已回收、`git diff` 复核为空。

---

## 七、诚实边界

- **未起真进程、未连 PG/Redis/Docker、未执行 SQL** ⇒
  - 59 号「86 条凭据中 0 条 free」的**真库结论本轮无法复验**（沿用其结论，并已独立复核其代码侧机制）；
  - 真库里是否存在 `billing_mode='free'` 且 `unit_price_*` 为 **NULL** 的 offer 行**未查**（见 §三）；
  - `free_resource_catalog` **真库行数**未查。
- `domains/freediscovery` 的 9 个测试文件**未执行**（`go test ./domains/freediscovery/`），机制清单来自静态阅读。
- **未核**「若产品决定投入」时的改造成本（注册链的 `collectEnvProviderConfigs`
  是否需要新增一条 catalog 驱动的来源，225 号已指出该函数只读 `os.Getenv`）。
- `go test ./...` 全量未跑；**CI 从未运行**；本轮**零生产代码改动**。

---

## 八、编号与去向

- **新增待裁决第 98 条**：「免费 token 池是否投入生产」正式立为裁决项。
  **它当前没有任何对应条目，而阻塞它的 P1 已在代码注释里标注了 N 轮。**
  附本轮备齐的决策材料；并指出若决定不投入，应把单向门改为**显式关闭态**
  （当前形态是「看起来在跑、实际永远跑不到」＝误导性状态）。
- **复核确认**：59 号 P1「扫描入口结构性锁死」在 HEAD `b4daddfcd` **仍成立**。
- **机制澄清**（59 号未给）：免费池是**两条互不相通的链**；
  `free_resource_catalog` **不是凭据表**，在现役里的角色是**过滤器**。
- **推翻一条子代理推断**：`pricing_plans` 不会覆盖免费池的 0 价（`COALESCE` 只在 NULL 回退）。
- **新增覆盖缺口登记**：无端到端断言「免费池凭据能被路由选中」。
- **守卫**：`internal/sqlguard` 新增 2 条测试，集合仍 14 包。
- **playbook**：§191。

---

## 九、playbook §191

**§191-A 「看到 LEFT JOIN 价表」不等于「价表生效」——决定性的是取数表达式的分支语义。**

本轮推翻子代理那条推断时用的就是这个判别式。子代理说：
「候选查询 `LEFT JOIN pricing_plans`（`provider/client.go:1565` `candidateQuerySQL`）
⇒ 免费池的 0 价会被价表覆盖成付费价」。

只读 SQL 的**结构**（有哪些 JOIN）会给出「会覆盖」；但**取价格式**才是决定性的：

    -- provider/client.go:1643-1644 原文
    COALESCE(mo.unit_price_in_per_1m,  pp_fb.plan_in)::float8  AS unit_price_in_per_1m,
    COALESCE(mo.unit_price_out_per_1m, pp_fb.plan_out)::float8 AS unit_price_out_per_1m,

`COALESCE` **只在 NULL 时回退**（第一个参数非 NULL 就直接取它）。免费池注册时写的是**显式 0**
（`admin/free_pool_extra.go:1690-1695` 的 `model_offers` 插入字面量 `0, 0`），
0 不是 NULL ⇒ **永远不会回退到价表**。

⇒ **判别式**：审计「A 会不会被 B 覆盖」时，
  **先问 A 在源头是「缺失(NULL)」还是「显式取值(0/空串/哨兵)」**。
  缺失类才会被回退链吃掉；显式取值类对回退链**免疫**，
  无论那个回退链有多长、看起来多权威。
  与 §175（注释不是契约）同族：**链的存在不改变链上每一跳的语义**。

⚠️ 与前几轮尺子错的区别：这次不是尺子算错，而是**尺子量错了对象**
（量 JOIN 结构，而不是量取值分支）⇒ 数字本身合理，**问题不可见**。

**§191-B 一条 P1 若没有对应的待裁决项，就永远不会解决——缺项是队列的静默失效模式。**

59 号的 P1 结论是准确的（结构性锁死），
但**它被登记成了「缺陷」，而缺陷的修法是「改代码」**。

`domains/freediscovery/discovery_engine.go:107-109` 的注释自己写明了真相：

    // R30 域A P1-②：产品裁决未拍板
    // …（先于 createTask 返回）

⚠️ 于是本轮出现一个此前没见过的**失效形态**：
**这条 P1 的阻塞点不在代码里，而在人的决策里。**
- 若登记为「缺陷」⇒ 每一轮审计都会「复核确认：仍成立」，
  然后**继续开下一条**，队列零减少；
- 它**不能**被登记为「代码缺陷」而用代码去修 ⇒ 正确的修法是**拍板**，
  而台账里**没有任何条目承载「拍板」这个动作**（§153 查重查的是缺陷条目，
  查不到一个「这里缺一个决策」）。

⇒ **判别式（可机械执行）**：对每条已登记的 P 级结论，追加一次检查——
  **「它阻塞我的东西是代码里的什么位置？」**
  - 答案是文件:行号 ⇒ 它是真缺陷，可进代码修复队列；
  - 答案是「没人决定」/「注释自陈待拍板」/「取决于产品形态」⇒
    **它缺的是一条待裁决项**，缺了就把它立为待裁决项，
    并把「阻塞点 = 决策」明写进裁决项正文。
⇒ 这与 §178（检查范围不能由被检查对象当前状态定义）同族：
  **审计的待办集合不能由「缺陷的修法是否属于写代码」来定义。**
⇒ 附带一条处置原则：**单向门 + 注释说明「不投入」是误导性状态**——
  运维看到的是「有一个扫描器在跑」。
  本轮的建议是：裁决为「不投入」时把门改为**显式关闭态**（配置项 + 启动日志），
  而不是留着「看起来在跑、实际永远跑不到」的门。
  这属于改对外行为，**只登记为建议，未改代码**。
