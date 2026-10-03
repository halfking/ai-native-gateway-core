# 202 号 · R89-DM —— 两处「非致命即静默」：`rowsguard` 的失效豁免、**`sqlreadguard` 的债务清单可以静默变长**

> 结论先行：两项都改成**致命**，负控逐条验过。**改动 = 2 个守卫测试文件，零生产代码。**
> 本轮的核心是本仓自己写下却只落实了一半的原则 ——「一个已知缺口清单只有在**新增缺口是致命的**时才算棘轮」。

---

## 〇、起手

- 200 号 §六 登记的 P3：`rowsguard` 的 stale 豁免告警是 `t.Logf` **非致命**。
- 201 号 §七 顺位第 3 项：`sqlreadguard` 的 `DEBT(R47)` 是本会话在 199 号盘点时看到、但一直没碰的真实债务。

两件事的共同点不是「都是测试」，而是：**它们失效时都不出声**。

---

## 一、🔴 F1：`rowsguard` 的失效豁免 —— 门在说谎，但只记日志

### 1.1 两条分支，只有一条致命

`rowsguard_test.go:79-92`（`TestExemptionsStillResolve`）里，登记项对不上时分两支：

| 条件 | 现状 | 含义 |
|---|---|---|
| `:88` 该行**还有** `Next()` 循环、只是不在门的 key 集里 | `t.Errorf` **致命** | **门看不见一个真实站点** ⇒ 这是键构造出错的信号（199 号那三个族就死在这里） |
| `:91` 该行**已经没有** `Next()` 循环 | `t.Logf` **非致命** | 登记项指向一个**不存在的位置** |

⇒ 201 号修掉 `4552` 之后第二条分支从未触发，所以它在 CI 里是**沉默**的。

### 1.2 为什么不致命是错的

失效豁免的代价不是「多一条无用记录」，而是**门对这一处不再有话可说**：清单还写着「此处已豁免/已复核」，而下一个人读到的是一个早已不存在的行号。**非致命时它会静静躺在表里，直到某次误改或键构造再次出错才暴露** —— 而那时根因已经不可考了。

### 1.3 改法

`t.Logf` → `t.Errorf`，并把**处置指引写进错误信息**（因为报错的人通常不知道下一步该干什么）：

```
exemption %s is stale (line drift) —— 该行已没有 for X.Next() 循环，这条豁免正在假装有效。
应删除该条目；若循环仍在、只是被改过，先用 git log -S '%s' 回原提交确认是
「同一处漂移」还是「另找一处顶上」再改键
```

升级后仍绿（当前无 stale），且**负控精确命中**（见 §三）。

---

## 二、🔴 F2：`sqlreadguard` 的 `DEBT(R47)` 可以**静默变长** —— 棘轮只做了一半

### 2.1 债务现状（202 号实测登记）

`DEBT(R47)` = 「盲区债：真实单腿读，待逐文件双腿化」。**22 条**（Go 15 + SQL 7）：

| 域 | 条目 |
|---|---|
| admin 读面 | `memora_handlers` / `quality_correlations` / `provider_models` / `probe_history` / `session_sanitize_matches` |
| cmd/gateway | `output_compliance_control` / `main_v3_wiring` |
| domains | `analysis/optimizer` / `analysis/request_summary` / `analysis/projectattr/store` / `sessionforensics/export` / `hooks/goal/history_store` / `hooks/observability/telemetry/client` |
| 其它 | `autoroute/recommend_v2` / `discovery/discovery` |
| SQL 读面 | `v_node_switch_analysis` / `customer_cost_view` / `model_cost_per_task_view` / `v_timeout_effectiveness` / `v_continuation_effectiveness` / `credential_most_used_model_integer_integer` / `get_last_successful_request_character_varying_integer` |

**为什么它是债**：hot 表默认只保留 8h，而这些读面打的是**裸母表** ⇒ **最近 8h 的行根本不在母表里** ⇒ 面板对最近 8h 全盲。并发会话 `976b871ce` 刚为 `usage_enhanced.go` 做过一次转换（裸 `request_logs` → `request_logs_with_current_month`），并自述那是「最小修复仅为解 gate 阻塞」⇒ **修法已验证可行，剩下 21 条只是没人排队**。

### 2.2 既有棘轮只做了一半

`TestSQLReadGuardWhitelistCurrent`（`:197`）管住的是「**条目不得比需要活得更久**」：文件已双腿化 ⇒ 报红要求移除条目。**这半边做得很好**（R72 还专门修过它的失配判定）。

**缺的是另一半**：给一个新文件加一条 `DEBT(R47):` 白名单，**门会一直绿**。

### 2.3 这正是本仓自己写下的失效形态

`sql/schema/integration_gate_test.go:429-431` 原文：

> An allowlist of known gaps is a ratchet **ONLY if adding a new gap is fatal**. If the
> harness merely prints failures, then every future fresh-install gap is absorbed
> into the same green run, and the list becomes a blanket exemption that nobody
> re-reads.

同文件 `:439 TestGateRatchetsUnlistedStartupGaps` 已经在启动迁移那一侧落实了它。
⇒ **同一个原则，同一个仓库，另一处没落实。** 这正是 198 号 §7.4 那条教训的又一次现身。

### 2.4 改法：钉基线集合，只对新增报错

```go
var debtBaseline = map[string]bool{ /* 22 条 */ }

func TestDebtRatchetDoesNotGrow(t *testing.T) { … }
```

**语义选择很关键**：

| 选择 | 后果 |
|---|---|
| 「集合必须逐字相等」 | 每还一笔债都要来改基线 ⇒ 基线本身变成噪音 ⇒ **大家开始不看它，棘轮退化成清单** |
| **「只对新增报错，移除自动接受」**（本轮） | 移除 = 债务被偿还，不需任何人改本文件；**「删一条加一条」也堵得住**（那是新增，会红） |

配套两点（沿用 201 号 §102）：

- **覆盖下限** `len(debt) < 20` ⇒ 致命。**空集合同样满足「没有新增」**，DEBT 识别坏掉时这道门会安静通过。
- 失败信息**给出处置三步**（更新基线 / 写清为何不能双腿化 / 确认只影响 <8h 窗口），并指出一条常被忽略的出路：**若本条其实不构成债，先修代码再登记，或把理由改成 `LEGIT` 并给出依据。**

---

## 三、负控（两条，都在「未修复」状态下跑过）

| 对照 | 注入 | 结果 |
|---|---|---|
| **失效豁免会红** | 加一条 `"cmd/gateway/main.go:3": "NEGCTRL: this line is not a Next() loop"` | ✅ `FAIL`：`exemption cmd/gateway/main.go:3 is stale (line drift) —— 该行已没有 for X.Next() 循环，这条豁免正在假装有效。应删除该条目…` |
| **债务变长会红** | 加一条 `"admin/zz_negctrl_202.go": "DEBT(R47): NEGCTRL…"` | ✅ `FAIL`：`DEBT(R47) 盲区债新增了 1 条（当前 23 条，基线 22 条）：admin/zz_negctrl_202.go` + 三步处置指引 |

两次变异均已还原（`gofmt` 干净、`vet` 干净、工作树只剩 2 个预期文件）。

⚠️ 附带一个**必须自己查、不能想当然**的点：把 `:91` 升为致命前，我先查了「判据本身会不会误伤」。`rowsguard_test.go:87` 用 `nextLoopRe.MatchString(lines[idx-1])`，而 `nextLoopRe` 是**无锚点**搜索（`for\s+(\w+)\.Next\(`，无 `^`/`$`）⇒ 行尾有**孤立 `\r`** 也不影响匹配。而本仓**确实有**含孤立 `\r` 的文件（`cmd/gateway/main.go` 就在其中，200 号为此踩过一次坑）⇒ 若该正则是行锚定的，这次升级会把 13 条有效豁免里的若干条误判成 stale 而**打红 CI**。**先证判据不误伤，再改判据的严厉程度。**

---

## 四、验证命令与结果（可重放）

```powershell
# 1) 升级后仍绿
go test ./internal/rowsguard/ -v -count=1
#   rows guard: 753 sites checked, 0 violations
#   PASS ×6（含 TestExemptionsStillResolve）

go test ./internal/sqlreadguard/ -v -count=1
#   guard_test.go:287  DEBT(R47) 现状：22 条（基线 22），新增 0 条
#   PASS ×5（含新增的 TestDebtRatchetDoesNotGrow）

# 2) 负控（见 §三）
#   2a 加一条指向非循环行的豁免      → FAIL「stale (line drift) … 正在假装有效」
#   2b 加一条 DEBT 白名单            → FAIL「新增了 1 条（当前 23 条，基线 22 条）」

# 3) 全部 10 个已登记守卫（= make guards 的内容）
go test ./internal/rowsguard/ ./internal/errdiscard/ ./internal/dbrows/ ./internal/jsoncol/ \
        ./internal/paramguard/ ./internal/sqlguard/ ./internal/sqlreadguard/ \
        ./internal/metricguard/ ./internal/partguard/ ./internal/routeguard/ -count=1 -timeout=120s
#   ok ×10，0 FAIL

# 4) 静态检查
gofmt -l internal/rowsguard/rowsguard_test.go internal/sqlreadguard/guard_test.go   # 空
go vet ./internal/rowsguard/ ./internal/sqlreadguard/                              # 无输出
go build ./...                                                                      # exit 0
```

**诚实边界（逐条）**

- 改动 = **2 个守卫测试文件**，**零生产代码、零数据库、零客户端观感变化**。
- **本轮没有偿还任何 DEBT**：22 条一条未动。`debtBaseline` 只是**把现状钉住**，让「变多」从此会响；真正缩小它需要改生产 SQL（裸母表读 → `request_logs_with_current_month`），而**那条路需要真库验证成本口径**（先例 `976b871ce` 用 30 天窗口核对了 150.15 美元两表一致）⇒ 本机无 PG，**不做**。
- **未跑 `go test ./...` 全量**（跑全部 10 个守卫包）；`go build ./...` exit 0。
- **未运行 CI**（沿用 200 号登记）；本机 `core.hooksPath` 未设置 ⇒ 本会话历次推送都未经 pre-push 门。

---

## 五、证伪与登记

| # | 事项 | 处置 |
|---|---|---|
| ❌ | 撤回「`sqlreadguard` 白名单没有 stale 检查」 | 撤回。`TestSQLReadGuardWhitelistCurrent` **有**且做得很好（R72 专门修过失配判定）⇒ 本轮补的是**另一个方向**，不是补一个不存在的东西 |
| ❌ | 撤回「把 `:91` 直接升为致命是安全的」 | 我一开始就是这么假设的；查 `nextLoopRe` 后发现必须先确认它对孤立 `\r` 不敏感（本仓确有这种文件），才敢改 |
| 登记 | 22 条 `DEBT(R47)` 债务本身 | **P2，登记不修**。理由：本轮无 PG，无法验证双腿化的成本口径；先例 `976b871ce` 是拿真库 30 天窗口核对的。⇒ 下轮若能起真库，从 `admin/probe_history.go` 这类读面最窄的开始逐条还 |
| 登记 | `debtBaseline` 需随还款更新 | 设计上**移除会自动接受**，所以只在新增时改；基线只增不减的成本为零 |

---

## 六、下一轮顺位

1. **还 DEBT 债**（需真库）：`976b871ce` 已给出转换配方（裸 `request_logs` → `request_logs_with_current_month`，两腿同吃 `ts` 谓词下推），剩 21 条排队。
2. 待裁决 **82**（已扩大一条）→ **81** → **79** → **80**。
3. `partguard` 清单里 `platform_outbox` 属 `sql/migrations/local/`，**未验证**它是否在任何生产部署路径执行 —— 若不执行，可考虑把 local 迁移目录从反向判据的扫描范围里排除并在门里注明（**但要先确认那不会形成新的盲区**）。

---

## 七、playbook 新增

### §104 「非致命」是守卫设计里最容易被默认掉的一个决定

- 本轮两处修复同源：**门在说谎，但只 `t.Logf`**。
  - `rowsguard` 的失效豁免：登记项指向一个不存在的行号，门却绿；
  - `sqlreadguard` 的 `DEBT(R47)`：加一条白名单，门却绿。
- ⇒ **凡是「门对某一处不再有话可说」的情形，都不该只记日志。** 判断标准不是「这条信息重不重要」，而是「**它为假时，有没有任何别的机制会喊**」。
- ⇒ **错误信息要带处置指引**。报错的人通常不知道下一步做什么；`用 git log -S 回原提交确认是「同一处漂移」还是「另找一处顶上」` 这种话写进错误信息，比写在文档里有用得多。

### §105 债务清单的棘轮：**只对新增报错，对移除自动接受**

- 原则在本仓已写下（`sql/schema/integration_gate_test.go:429-431`）：「一个已知缺口清单只有在**新增缺口是致命的**时才算棘轮」。⇒ 审计任何 allowlist 时，**先问「新增一条会不会红」**，而不只是「失效条目会不会红」。
- **语义选择**：
  - 「集合必须逐字相等」⇒ 每还一笔债都要改基线 ⇒ 基线变噪音 ⇒ **没人再看它，棘轮退化成清单**；
  - **「只对新增报错、移除自动接受」** ⇒ 还债零成本，而「删一条加一条」仍会被抓到。
- ⇒ 配**覆盖下限**：空集合同样满足「没有新增」，识别逻辑坏掉时这道门会安静通过（201 号 §102 的同一条教训）。
- ⇒ 失败信息里**指一条常被忽略的出路**：「若本条其实不构成债，先修代码再登记，或把理由改成 `LEGIT` 并给出依据」—— 否则人会去改基线而不是改代码。

### §106 把判据改严之前，先证它**不会误伤**

- 本轮把 `:91` 从 `t.Logf` 升为 `t.Errorf` 前，必须确认 `nextLoopRe` 对**孤立 `\r`** 不敏感 —— 因为本仓**确实有**含孤立 `\r` 的文件（`cmd/gateway/main.go`），而行锚定的正则会把有效豁免误判成 stale，**直接打红 CI**。
- ⇒ 与 199 号 §93「阈值要贴着真实值」、§96「下限的被测量必须平台无关」同源：**判据的严厉程度与判据的准确度是两个独立决定**，先证后者再动前者。
