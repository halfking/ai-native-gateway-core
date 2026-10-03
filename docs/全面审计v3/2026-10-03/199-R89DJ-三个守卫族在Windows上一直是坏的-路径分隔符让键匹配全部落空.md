# 199 号 · R89-DJ —— 三个守卫族在 Windows 上一直是坏的：**路径分隔符让键匹配全部落空**，而唯一自动化执行路径只跑 Linux

> 结论先行：一条根因，三个族，三种症状，全部**假**。已根修（4 处 `filepath.ToSlash` + 1 处测试前缀），Linux 行为逐位不变。
> 本轮最贵的教训不在代码里：**我差点报出一个不存在的 P1 执行缺口**，靠去找「第三份清单」才撤回（§六·3）。

---

## 〇、起手：198 号结尾自己挖的坑

198 号 §7.4 记了一条：并发提交 `976b871ce` 自述「最小修复仅为解 gate 阻塞」，挡住它的是 `TestNoBareRequestLogsMotherReads`（`sqlreadguard` 族）—— **本会话此前完全不知道这道门存在**。

⇒ 199 号第一顺位不是找新缺陷，是**补上这个知识缺口**：盘点本仓到底有哪些守卫、各自管什么、有没有被执行。

**判据的选择很关键**：函数名里带 `Guard/Invariant/Contract` 会捞出几百个普通单测（实测第一次尝试就是这样）。真正的特征是**谁在 walk 仓库根**：

```
grep -l 'filepath\.WalkDir\(|filepath\.Walk\(|repoRootFromCaller|repoRoot\(' --glob '*_test.go'
⇒ 38 个文件
```

### 0.1 普查结果：7 个系统性守卫包

| 守卫包 | 禁止什么 | 提出轮次 | 判据形态（值得学的部分） |
|---|---|---|---|
| `sqlreadguard` | 生产读面对 `request_logs` **母表**的裸引用（8h 盲区） | R46 §11 → R47 | hot∪母表双腿规则 + **白名单分类**（`LEGIT:` / `DEBT(R47):`）+ **自清洁棘轮**（文件已无命中 ⇒ 残留条目报错，防债务隐身） |
| `rowsguard` | 每个 `for X.Next()` 循环缺 `X.Err()` 终检 | R66 | 判据落在**产物特征**（循环旁是否有 `Err()`），**不是抓 helper 名字**；并明确「取『终检』而非『残留』，因为两者不是同一件事」 |
| `partguard` | 对分区**父表**的写操作 | R78 | 用 `go/ast` 取字面量而非全文正则，**因为注释里提到表名会造成假警报**；另有**死规则棘轮**（`sqliteBacked` 排除项失效 ⇒ 报错，不许留不承重的规则） |
| `routeguard` | 路由资格判定依赖内存熔断器**单向观测镜像** | R87-f | 覆盖面含 `sql/objects/views/` **+ 所有重定义该视图的迁移**（只扫 objects 等于留后门），迁移**动态发现**无硬编码清单 |
| `metricguard` | 指标已声明却从无记录调用 | R77 | 判据是**记录调用**（`Inc/.Add/.Observe/…`）而非标识符引用；并有 `knownUnwired` 登记 + 失效棘轮 |
| `sqlguard` | raw-string SQL 里写 Go 风格 `//` 注释 | 2026-08-08 生产事故 | 由来是一次**真实故障**（注释写进反引号 SQL ⇒ PG 语法错 ⇒ failover 整机 500），`go build`/`go vet` 全程通过 |
| `paramguard` | 参数/方言契约（`reasoning_effort`、`glm_effort`、budget…） | — | ⚠️ **不属同一类**：它是逐函数参数契约测试，不扫仓库。**我自己分类错了**，见 §六·5 |

外加 `internal/dbx/jsonb_param_static_test.go`、`sql/schema/integration_gate_test.go` 等若干仓库级门。

**全部零生产导入** —— 仅有两处生产文件以**注释**形式提及（`security/ipblocklist/store_pgx.go:202`、`domains/session/dual_writer.go:152`）⇒ 改动它们不影响任何客户端观感。

⚠️ **一条被本轮证伪的既有文档说法**：`partguard/scan.go` 的包注释写着「是 `go/ast` 取字面量而非全文正则，**因为注释里提到表名会造成假警报**」——**本会话在 198 号白走了一遍这条弯路**（先写文件级扫描、在 `admin/routing.go` 上踩到 `AS v(id, priority)` 别名混淆，才改成字面量级）。**这个坑仓库里早有答案，只是没人给它建了索引。**

---

## 一、🔴 F1：一条根因，三个族，三种**假**症状

### 1.1 触发普查的方式

跑全部守卫族，**三个族是红的**：

```
--- FAIL: rowsguard.TestEveryRowsLoopIsGuarded
--- FAIL: rowsguard.TestExemptionsStillResolve
--- FAIL: routeguard.TestMigrationCoverageIsNotZero
      只发现 0 个会 redefine 路由视图的迁移（期望 ≥2，即 326/460）
      实际发现：[… 19 个文件，含 sql\migrations\startup\326_… …]
--- FAIL: errdiscard.TestErrDiscard_NoNewDiscardingCallSites
      scanned 2251 Go files, 3 findings
```

### 1.2 根因（源码级三处同形）

三处都用 **`filepath.Rel` 产出「操作系统原生分隔符」**去构造键，而**所有人工维护的键表都用正斜杠**：

| 位置 | 构造侧（OS 原生） | 键表侧（正斜杠） |
|---|---|---|
| `rowsguard/rowsguard.go:108,221` → `:259 key := fmt.Sprintf("%s:%d", rel, pos)` | `filepath.Rel` ⇒ `cmd\gateway\main.go` | `exemptions` 13 条全写 `cmd/gateway/main.go:4552` |
| `errdiscard/errdiscard.go:102` | `filepath.Rel` ⇒ `admin\work_types.go` | `knownCandidates` 写 `admin/work_types.go` |
| `routeguard/routeguard.go:99` + `guard_test.go:78` | `filepath.Rel` ⇒ `sql\migrations\startup\…` | `MigrationsDir = "sql/migrations"` **正斜杠**常量，再拼 `filepath.Separator` ⇒ `"sql/migrations\"` —— **这个字符串永远匹配不到** |

⇒ **Linux 上两者恰好相同（都是 `/`），Windows 上永不匹配。**

### 1.3 三种症状对照

| 族 | 症状 | 为什么长得完全不一样 |
|---|---|---|
| `routeguard` | 覆盖下限数到 **0 个**迁移（实际扫到 **19** 个） | 比较串 `sql/migrations\` 自身不可匹配 ⇒ **大声的假警报** |
| `rowsguard` | **11 处**违规；13 条豁免**一条都没绑定** | 违规键 `\` vs 豁免表 `/` ⇒ **虚增违规** |
| `errdiscard` | 报「**3 处新增**」**同时**报「**2 处已登记消失**」 | findings `\` vs knownCandidates `/` |

**`errdiscard` 那条是本轮最有诊断价值的一行**：自洽世界里「新增」与「消失」**互斥** —— 同一批已登记债不可能既新又消失。**同时出现这两个方向，就是键匹配失败的签名，不是代码漂移。** 我没有去逐个审那 3 个 `discard` 调用点，而是直接去查键是怎么构造的。

### 1.4 为什么能存活至今

唯一的自动化执行路径是 CI：

```
.github/workflows/audit-guards-ci.yml:50   runs-on: ubuntu-latest
.github/workflows/audit-guards-ci.yml:78   run: make guards
Makefile:58  GUARD_PACKAGES := … 10 项
Makefile:    guards: $(GO) test $(GUARD_PACKAGES) -count=1 -timeout=120s
```

⇒ **CI 跑全部 10 个守卫，但只在 Linux 上。** Windows 上这 3 个族等于不存在，而这也解释了**为什么三个族同时中招却一直没人发现** —— 它们共用同一个错误习惯。

---

## 二、根修

**判据：改「构造键的那一行」，不改「比较键的那一行」。** 在 4 个构造点套 `filepath.ToSlash`（非 Windows 上是恒等函数 ⇒ **Linux 行为逐位不变**），另修 1 处测试里的矛盾前缀：

```go
// rowsguard.go / errdiscard.go：4 处
rel := filepath.ToSlash(mustRel(root, path))

// routeguard/guard_test.go：前缀也用同一套归一化
migPrefix := filepath.ToSlash(MigrationsDir) + "/"
```

新增 `mustRel` 兜底（`filepath.Rel` 出错时退回原路径，保证键仍是稳定键）。

**为什么归一化放在构造侧而不是比较侧**：人工键表（豁免 13 条、`knownCandidates`、其他包的常量）全是正斜杠，跨平台可读、跨平台一致；归一化一次就够，且以后新写键表的人照着正斜杠写就是对的。

---

## 三、🔴 F2：验证「修好了」不等于「门还活着」

**从红变绿本身不能证明修复正确 —— 也可能是我把门弄瞎了。** 按 §95 三条逐一核对：

| 门 | 关键量 | 修复前 | 修复后 | 判读 |
|---|---|---|---|---|
| `errdiscard` | **扫描文件数** | 2251 | **2251** | 覆盖**未变**；3 个 findings 仍被报出，只是正确匹配到 2 个已登记文件 |
| `rowsguard` | `checked`（**豁免后**站点数，`rowsguard.go:260-263` 先查豁免再 `checked++`） | 766，11 违规 | **754，0 违规** | 豁免表 **13** 条 − **1** 条真实失效 = **12** 条绑定；**766 − 12 = 754，算术闭合** ⇒ 总站点数未变，11 处**全是假阳性** |
| `routeguard` | 迁移计数 | 0（假） | ≥2 | 与它自己打印的 19 个文件一致 |

`internal/...` 全包绿（含 `errdiscard 12.0s`、`metricguard 44.1s`、`dbrows`、`jsoncol`）。

⚠️ **诚实边界**：我只能论证「Linux 行为不变」（`ToSlash` 在非 Windows 上是恒等），**无法在本机实跑 Linux 复核**。`guards-sync.sh` 与 pre-push 钩子需要 bash —— 裸 `bash` 不在 PATH，但 Git 自带的 `C:\Program Files\Git\bin\bash.exe` 可用，本轮已用它实跑过 `guards-sync.sh`（**绿**：「7 个 *guard 目录与 GUARD_PACKAGES 的 10 项双向一致」）。

---

## 四、登记不修

| 事项 | 定级 | 不修的理由 |
|---|---|---|
| `rowsguard` 豁免表 1 条**真实失效**：`cmd/gateway/main.go:4552 is stale (line drift)` | P3 | 门已**报出**（非致命，日志可见）。要修需定位该循环迁移到哪一行，属独立小工单；本轮不猜行号 |
| 本机 `core.hooksPath` **未安装** + 裸 `bash` 不在 PATH ⇒ pre-push 门在本机**从未执行** | 登记 | 这是**本机环境**事实，不是仓库缺陷；但它意味着本会话历次推送都未经该门（诚实登记） |
| CI 只跑 `ubuntu-latest` | **登记（需产品/运维裁决）** | 要么加一个 `windows-latest` 矩阵，要么在守卫里统一 `ToSlash`（**本轮已做后者**，等于把平台分叉从「3 个族」压到「0 个已知」） |

---

## 五、附带确认：我自己 197/198 的门**没有**漏登记

`admin/` 下的门（197 `billing_wide_table_columns_test.go`、198 `view_column_contract_test.go` 等）不在 `GUARD_PACKAGES` 里，但这是**设计如此**而非缺口：

- `guards-sync.sh` 的职责边界是「`internal/` 下每个 `*guard` 包都要登记」；
- `admin/` 的门由 `make test` = `go test ./... -count=1 -timeout=300s`（Makefile 标注「**CI 默认入口**」）覆盖，**含 `./admin/...`**。

⇒ 不制造新登记义务。

---

## 六、证伪清单（5 条，含一次高价的自我推翻）

1. ❌ 撤回「`routeguard` 覆盖塌陷为 0 / 这道门已经瞎了」 —— 它**扫到 19 个**迁移（含预期的 326/417/460/672），只是前缀比较数错了。**主门一直覆盖着。**
2. ❌ 撤回「`rowsguard` 报出 11 处真的缺 `rows.Err()` 终检」 —— 13 条豁免里本该有 12 条绑定，**11 处全是分隔符假阳性**。
3. ❌ **撤回「3 个已登记守卫（`metricguard` / `partguard` / `routeguard`）从不执行，是 P1 执行缺口」—— 本轮最贵的一次。** 我看到 `.githooks/pre-push` 的执行表只有 7 个、`Makefile` 登记表有 10 个，判定为「执行缺口」。**去查第三份清单时发现 `.github/workflows/audit-guards-ci.yml:78` 跑的是 `make guards`＝全部 10 个**，且 `:71` 注释明写「**R88 改：直接调 `make guards`，让 Makefile 的 GUARD_PACKAGES 保持唯一**」。⇒ pre-push 那 7 个是**本地快速子集**，CI 才是全覆盖。**如果我不去问「第三份清单在哪」，就会写进一份不存在的缺陷。**
4. ❌ 撤回「这三族红是本会话引入的」 —— `git show --name-only ed9207f06 24b7918cc` 证明本会话两个提交只碰了 `admin/view_column_contract_test.go` 与 3 个文档，**零 `internal/` 改动**。
5. ❌ 撤回「`paramguard` 属于扫仓库守卫族」 —— 它是逐函数参数契约测试（`budget=4095` / `reasoning_effort` / `tool_choice`），**不扫仓库**，不属同一类。**我自己分类错了**，普查表里已标注。

---

## 七、验证命令与结果（可重放）

```powershell
# 1) 修复前：三族红（HEAD 既有，非本会话引入）
go test ./internal/routeguard/ ./internal/rowsguard/ ./internal/errdiscard/
#   FAIL ×4，其中 routeguard 打印「只发现 0 个…期望 ≥2」并列出实际 19 个文件

# 2) 修复后：三族绿
go test ./internal/routeguard/ ./internal/rowsguard/ ./internal/errdiscard/
#   ok  routeguard 0.715s / rowsguard 7.518s / errdiscard 8.677s

# 3) 关键：覆盖未丢
go test ./internal/errdiscard/ ./internal/rowsguard/ -v -count=1
#   errdiscard_test.go:86  scanned 2251 Go files, 3 findings   ← 与修复前逐字相同
#   rowsguard_test.go:48   rows guard: 754 sites checked, 0 violations
#   rowsguard_test.go:87   exemption cmd/gateway/main.go:4552 is stale (line drift) ← 真实债，已登记

# 4) 更大范围回归
go build ./...                                  # exit 0
go test ./internal/...                          # 全绿

# 5) 登记一致性（需 Git 自带 bash；裸 bash 不在 PATH）
& 'C:\Program Files\Git\bin\bash.exe' -c 'cd /c/workspace/llm-gateway-go-2 && bash scripts/checks/guards-sync.sh'
#   ✅ 7 个 *guard 目录与 GUARD_PACKAGES 的 10 项双向一致    EXIT=0

# 6) 格式与静态检查
gofmt -l internal/rowsguard/rowsguard.go internal/errdiscard/errdiscard.go internal/routeguard/*.go
#   （空）
go vet ./internal/rowsguard/ ./internal/errdiscard/ ./internal/routeguard/   # 无输出
```

**诚实边界（逐条）**

- **改动仅限 4 个测试基础设施文件**（3 个守卫实现 + 1 个守卫测试），**零生产代码**、零数据库、零客户端观感变化。
- **无法在本机实跑 Linux 复核**；「Linux 行为不变」是**论证**（`ToSlash` 在非 Windows 上是恒等），不是实测。
- **未跑 `go test ./...` 全量**（跑了 `./internal/...` 与受影响的 3 个包）。`admin/` 全包在 198 号已跑过（ok 84.540s），本轮未改 `admin/`。
- **pre-push 钩子整体未执行**：本机 `core.hooksPath` 未设置，且裸 `bash` 不在 PATH。钩子的 guard 段我**只验证了它列出的包集合与 `make guards` 的关系**，没有执行钩子本身。

---

## 八、下一轮顺位

1. **补 Windows 执行路径**：给 `audit-guards-ci.yml` 加 `windows-latest` 矩阵（当前 `:50` 硬编码 ubuntu）。本轮把 3 个已知族修好了，但**平台分叉本身仍在**，下一个用 `filepath.Join` 构造键的新守卫还会踩。
2. **`rowsguard` 那条失效豁免**（`cmd/gateway/main.go:4552` 行号漂移）定位并清理。
3. 待裁决 **82**（已扩大一条）→ **81** → **79** → **80**（见 196/198 号文档）。
4. 把本轮 §0 那张守卫族表**补进 `docs/全面审计v3/README.md`** 的索引区 —— 本会话两次因「不知道某族守卫存在」而误判（198 号 §7.4、199 号 §六·3）。

---

## 九、playbook 新增

### §96 **下限断言的被测量本身必须平台无关** ——「大声」不等于「正确」

198 号 §94 立的规矩是「覆盖面必须是断言，不是日志」。本轮补上它的下一层，也是它的**反面**：

- `routeguard` 的覆盖下限**响得非常大声**（「只发现 0 个迁移，期望 ≥2」），**但它数错了**：被比较的前缀串 `MigrationsDir + filepath.Separator` = `"sql/migrations\"` 是个**永远匹配不到**的字符串。⇒ **下限只能证明「我数到了 0」，不能证明「我数对了」。**
- ⇒ 写下限前先问：**这个被测量的量，本身是不是平台/表示无关的？** 归一化要在**构造侧**做一次，而不是指望比较侧去适配。

### §97 同一根因会在不同族里长出三种症状；**判据要落在构造侧**

- 三族症状完全不同（假警报 / 虚增违规 / 棘轮双向断裂），但根因是**同一行习惯**。**按症状逐个修会修三次，而且每修一次都会以为自己找到了新问题。**
- ⇒ 定位判据要问「**这个键是在哪一行被造出来的**」，而不是「它在哪里比较失败」。
- ⇒ **「同时报新增与消失」是键匹配失败的签名**：自洽世界里两者互斥。见到这组矛盾输出，不要去逐个审代码，**去查键的构造**。

### §98 登记 / 执行 / 校验是**三份清单**；发现两份不一致时，**先去找第三份**

- 本轮差点报出一个不存在的 P1：`pre-push` 执行表（7）≠ `Makefile` 登记表（10）⇒ 判为「3 个守卫从不执行」。**真相是 CI 跑 `make guards`＝全部 10 个**（且 `audit-guards-ci.yml:71` 注释明写 R88 已把 Makefile 设为唯一源）。
- ⇒ **两份清单不一致 ≠ 缺陷，它可能是「本地快速子集 vs 全量」的刻意设计。** 下结论前必须问：**还有谁在读这份清单？**
- ⇒ 推广版：**清单类配置要有唯一源**（本仓的答案是 `Makefile:GUARD_PACKAGES`），但**唯一源不等于唯一读者** —— 读者可以有多个，**只要有一个读者覆盖全集**。

### §99 平台分叉要有主人

- 本仓三个守卫族同时中招而长期无人发现，直接原因是 `audit-guards-ci.yml:50` 硬编码 `runs-on: ubuntu-latest`。**一条路径坏掉时，另一条路径是静默的。**
- ⇒ 任何「在某平台上从未被执行过」的代码路径，都**默认失效**，直到有证据表明它在另一平台上被验证过。
