# 66 号报告：R87-g 路由门控守卫 `internal/routeguard`（新建）

- 日期：2026-10-01
- 轮次：R87-g
- 性质：**补门**。承接 65 号 §4 的建议。
- 变更面：新建 `internal/routeguard/`（3 文件）+ `Makefile` 登记一行

---

## 0. 这道门防什么

R87-f（65 号）核实到：`credentials.circuit_state` / `cooling_until` 是**内存熔断器的单向观测镜像**（`state_sync` 只写不读、全仓无恢复路径、跨进程会陈旧），而熔断本身是**纯内存**的（重启即 `Closed`）。它们**今天不在路由视图里**——这是对的。

**风险在于它们长得太像路由输入**：在 `credentials` 表上、有时间戳、名字里带 `circuit/cooling`。任何人「顺手」把 `circuit_state = 'open'` 写进 `v_routable_credential_models.sql:17` 那个**单一 AND 合取**，就会让**重启后陈旧的 DB 值直接决定摘流**。

**这不是假想**：F1 就是同一个合取里加了一项（`health_status='warning'` 不在 `{healthy,unknown}`）造成的凭据级连坐。**合取项越加越容易全体出事，而「顺手加一项」的阻力极低。**

---

## 1. 覆盖面设计：为什么不能只扫 `sql/objects/views/`

`grep -rl is_routable --include=*.sql` 显示除规范对象文件外，还有两个 `CREATE OR REPLACE VIEW` 同一视图的迁移：

- `sql/migrations/startup/326_fix_routable_view_quota_check.sql`
- `sql/migrations/startup/460_v_routable_credential_models_periodic_exhausted.sql`

**迁移在部署时是真的会执行的**。只扫 `sql/objects/` 等于**给后门留了一道门**——而这恰恰是本项目反复出现的形态（镜像三处写点、告警 24 处假泄漏、门只扫单一 OS…）。

⇒ **迁移目录按「内容含 `is_routable`」动态发现**，未来新增的 redefine 迁移自动纳入，**无需维护清单，也就���有清单腐化**。实测当前发现 **2 个** redefine 迁移（门里 `TestMigrationCoverageIsNotZero` 要求 ≥2，即 326/460）。

---

## 2. 三条断言

| 断言 | 防什么 |
|---|---|
| `TestRoutingViewMustNotDependOnCircuitMirror` | **主门**：任何路由资格视图（规范文件 + 所有 redefine 迁移）出现 `circuit_state` / `cooling_until` 即红 |
| `TestCanonicalViewExistsAndCarriesMarker` | 清单/路径失效：规范文件缺失、被改名，或视图形态变了（不再含 `is_routable`）即红。**否则本包会退化成「什么都没扫」而恒绿** |
| `TestMigrationCoverageIsNotZero` | 只扫规范文件的退化；要求 redefine 迁移覆盖数 ≥2 |

---

## 3. 合成夹具自测（`conventions.md` §9.3 / §9.4 的硬要求）

本门是**磁盘读取型**守卫，**不受 `go test -overlay` 影响**（overlay 只影响编译）⇒ 判别力由 `t.TempDir()` 合成夹具承担：

- `TestScanContentFlagsForbiddenColumns` —— 干净视图 0 违规；注入任一禁用列**恰好报 1 条**、列名与行号都对。**这是「无参数字面量不存在」型判据的自证**（§9.3 明确要求这类判据必须自行证伪）。
- `TestRoutingViewFilesDiscoversRedefiningMigrations` —— 合成仓里造三份迁移：含 `is_routable` 的 redefine（**必须被动态发现**）、不含的（**必须不被误纳入**，证明 marker 过滤不是恒真）、以及注入了 `circuit_state` 的 redefine（**必须被抓且归因到该文件**）。

---

## 4. 变异验证 3/3 全红，全部 `cmp` 逐字节还原

| 变异 | 期望 | 实测 |
|---|---|---|
| **M-e** 往**规范视图**的 `is_routable` 合取里加 `circuit_state` | 红 | ✅ 红 |
| **M-f** 往**迁移**里加 `cooling_until`（**后门路径**） | 红 | ✅ 红 |
| **M-g** 清空 `ForbiddenColumns`（**判据被整体绕过**） | 红 | ✅ 红（由合成夹具 `TestScanContentFlagsForbiddenColumns` 抓住） |

M-g 特别说明：**主门在「禁用列表为空」时天然是绿的**——这正是 §9.3 警告的「判据自证漏洞」。**是合成夹具把它救回来的**，而不是主门。这也验证了「补门时必须同时补合成夹具」不是形式主义。

---

## 5. 过程中自己踩的两个坑

1. **`repoRootFrom(".")` 的 `filepath.Dir(".") == "."`** —— 向上找 `go.mod` 的循环第一步就判定「到顶了」，返回包目录，三条用例全部报 `lstat sql/migrations: no such file or directory`。**已改为先取绝对路径**，并把坑写进函数注释。
2. **合成夹具文件里写出一行无效 Go**（`"...".ReplaceAll := ...`）—— 属于纯粹的语法失误，已修正。**登记在此是因为它说明：新建守卫包时，「先跑一次编译」比「先写完三个文件」更省。**

---

## 6. 验证

```
go test ./internal/routeguard/ -count=1   → 5/5 通过
make guards-sync                          → ✅ 全部 7 个守卫包已登记（GUARD_PACKAGES 内 10 项）
make guards（10 包）                       → 全部 ok
```

已按 `Makefile:54` 的维护纪律登记（新增 `*guard` 包必须同时进 `GUARD_PACKAGES`）。

---

## 7. 本轮未做

- **未修 F1**：`warning` 臂仍等 F1 修法定案（63 号 §3）。**本门不解决 F1，只防止再往那个合取里加新连坐点。**
- **未处理 65 号 §3 的 P3**（`circuit_state` 重启后陈旧却被当权威展示）——两种修法已登记未实施。
- **未覆盖 `is_routable` 之外的路由判据**：本门只看这两个列。若将来有别的观测镜像列（`node_probe_state` 之类）被加进合取，本门**不报**——**这是已知的覆盖边界，如实登记**。
- `internal/rowsguard` 等 6 个既有守卫**本轮未审**（不在本轮范围）。
