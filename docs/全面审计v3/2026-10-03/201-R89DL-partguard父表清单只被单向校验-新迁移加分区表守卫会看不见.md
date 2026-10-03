# 201 号 · R89-DL —— `partguard` 的父表清单只被**单向**校验：新迁移加一张分区表，守卫会**完全看不见**（已补反向判据 + 抓到 1 张真实漏网表）

> 结论先行：新增 `TestPartitionParentsAreExhaustive` 补上反向判据，**第一次真跑就抓到一张真实漏网的分区父表** `platform.platform_outbox`；它今天**零 Go 写点**（故定 P3 而非缺陷），已补进清单。
> 本轮最值得记的不是那道门，是**我为自己的判据造了两次假阳性**（`public`、`platform`），两次都是同一个错：把正则抓到的第一个标识符当成表名。

---

## 〇、起手

200 号之后还欠两件事：一件是它自己登记的 P3（stale 告警非致命），另一件是它 §八 的后续。本轮实际做的是**盘点后新发现的缺口** —— 因为 200 号建立的守卫族索引（README 附录 A）第一次被真正用上：我按 objective 的「**所有**的大数据表是以 hot+分区（columnar）表来完成，更新、删除只能在 hot 表中进行」去核对 `partguard`，发现它守的正是这条红线，**但只单向校验清单**。

---

## 一、🔴 F1：`partguard` 的父表清单只做正向校验

### 1.1 既有门做了什么、没做什么

`internal/partguard/guard_test.go:154 TestParentsAreDeclaredInDDL`：

```
扫 .sql → 去注释 → 按 ';' 切句 → 收集含 PARTITION BY 的语句
  ↓
stmts < 50 ⇒ Fatalf              ← 有覆盖下限，good
  ↓
遍历 partitionParents：每个名字必须能在某条 PARTITION BY 语句里找到
```

`parents.go:8-10` 的注释把边界说得很清楚：

> 代价是本列表可能随新迁移漂移——由 `TestParentsAreDeclaredInDDL` 兜住：
> 列表里任何名字必须在仓内 DDL 中确实以 PARTITION BY 声明，否则门报红
> （既防拼错，也防"把不存在的表写进白名单"）

⇒ **它只保证「清单里的都在 DDL 里」，不保证「DDL 里的都在清单里」。**

### 1.2 这为什么是 objective 级的缺口

objective 的红线是「**所有**的大数据表是 hot+分区表，更新、删除只能在 hot 表中进行」。一张**新加的分区父表**如果没进清单，那么：

- `partguard` 对它**零覆盖**；
- Go 侧任何 `UPDATE`/`DELETE` 打到它**都不会被报出来**；
- 而这类写今天之所以能跑，只因为父表**恰好还是 heap**（`parents.go:29-31` 已记这条：citus-columnar 引擎会直接拒绝 UPDATE/DELETE）。

⇒ 也就是说：**守这条红线的门，会在新表上线那天静默失效**，而且失效形态是「本该被挡的写操作畅通无阻」。

### 1.3 交付：补反向判据

```go
func TestPartitionParentsAreExhaustive(t *testing.T)
```

从 DDL 抽出**全部**分区父表，断言每个都在 `partitionParents` 里。三个设计点：

| 设计 | 理由 |
|---|---|
| **自带覆盖下限**（实测 33 ⇒ 阈值 25） | 本门判据是「集合相等」，而**空集合同样相等**。抽取逻辑某天坏掉（DDL 形态变、路径过滤写错）就会安静通过 —— 199 号 §96 的原话：**下限只能证明「我数到了」，不能证明「我数对了」** |
| 失败时**打印抽到的全部名字** | 让人一眼看出是「抽取坏了」还是「清单漏了」，而不是去数 33 个名字 |
| 取点分路径的**最后一段**作为表名 | 见 §二 |

---

## 二、⚠️ 我给自己的判据造了**两次**假阳性

**这两次是本轮最贵的部分，且形态完全一样。**

### 2.1 第一次：`public`

我先写了个离线扫描程序，第一版给三条正则：`PARTITION BY`、**`PARTITION OF`（抓子表名）**、`ALTER … ATTACH PARTITION`。

结果：`DDL 里有 45 个「缺失父表」`，而其中绝大多数是 `*_2026_08` / `*_default` 这类**叶子分区** —— 我把 `PARTITION OF` 后面的**子表**当成了父表。

还有一条 `foo`（来自 `sql/scripts/phase-23-columnar-invariant/02-event-trigger.sql`）和一条 `public`。

**两条都不能直接当发现写进文档。** 查证：

- `foo` / `public` 都来自同一个机制的误抓。基线 `01-schema.sql:164` 是一行 **PL/pgSQL 格式串**：
  `'ALTER TABLE public.%I ATTACH PARTITION public.%I %s'` —— `%I` 不匹配标识符字符类，正则**回溯**把 schema 名 `public` 认成了表名。

⇒ 修正判据（`PARTITION OF` 抓 **parent** 侧、去掉 `alterAttach`）后重跑：**45 → 0**。

⇒ 顺带得出一个可复用的算术结论：`alterAttach` 这个模式**贡献 0 个独有父表、1 个假阳性** ⇒ 净有害，**拿掉而不是修补**（`33 − 1(public) = 32`，正好等于清单长度）。

### 2.2 第二次：`platform`（这次是在新门里）

新门第一次真跑就报了一条缺失：

```
platform <- sql/migrations/local/641_local_shared_platform_schema_fixup.sql
```

**第一反应不该是「把它加进清单」** —— 那是把判据错误固化成事实。查 `641` 的原文：

```sql
41: CREATE TABLE IF NOT EXISTS platform.platform_outbox (
56: ) PARTITION BY RANGE (created_at);
```

⇒ `platform` 是 **schema 名**，真正的表是 `platform_outbox`。我的正则写成
`CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z_]…)` ——
**只认 `public.` 前缀**，于是遇到 `platform.` 就把 schema 名当成了表名。

⇒ 修法：抓整条点分路径，取**最后一段**（这才是 SQL 语义上的表名）：

```go
nameRe := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*(?:\s*\.\s*[a-z_][a-z0-9_]*)*)`)
…
if i := strings.LastIndex(path, "."); i >= 0 { path = path[i+1:] }
```

### 2.3 两次的共同教训

> **顺序反了。** 两次都是「先把正则抓到的第一个标识符当成表名，再怀疑清单」。
> 正确顺序是：抓到名字后，**先问它是不是 schema**（点分路径的第一段通常就是），
> 再问「清单里有没有它」——而后者的缺失**不应该**成为改清单的理由。

---

## 三、🔴 F2：真实发现 —— `platform.platform_outbox` 确实漏网

判据修对之后，门稳定报出**一条**：

```
platform_outbox <- sql/migrations/local/641_local_shared_platform_schema_fixup.sql
从 3389 个 .sql 抽出 33 个分区父表，清单 32 条，缺 1 条
```

**核实它是真表**：`641:41-56` 确实是 `CREATE TABLE IF NOT EXISTS platform.platform_outbox ( … ) PARTITION BY RANGE (created_at)`，且 `:57` 有 `platform.platform_outbox_default PARTITION OF platform.platform_outbox DEFAULT` ⇒ 是货真价实的分区父表 + 默认分区。

**核实有没有实际危害**：

```
grep -i 'platform_outbox|platform\.\w+' --include=*.go
  → 只有本门测试的注释 + tests/48h-audit/D07-hot-columnar/data/partitioned_parent_ctid_form_test.go:67
    （该测试已把它列为分区父表）
```

**全仓 Go 代码零处写它** ⇒ **定 P3，不是缺陷**：

| 为什么仍是 P3 而非 P3-关闭 | 理由 |
|---|---|
| 不是「已合规」而是「没人碰」 | 今天零写点；但清单外的分区父表，Go 侧**任何一处**新增写都不会被 `partguard` 拦下 |
| 补进清单的成本 | **零** —— 它零违规，因此不需要新增 `allowedParentDML` 登记项（那张表要求「集合恰好相等 + 条数相等 + 理由必填」，零站点不产生条目） |
| 补充说明已写进 `parents.go` | 它属 `platform` schema（与 `workflow`/`integration` 同属 local 迁移那套本地 schema），清单按**不带 schema** 的表名登记 |

补进后：`抽出 33 个，清单 33 条，缺 0 条`。

---

## 四、负控（两条，都先在「未修复」状态下跑过）

| 对照 | 注入 | 结果 |
|---|---|---|
| **反向判据有牙齿** | 把清单里的 `platform_outbox` 改名成 `platform_outbox_198_NEGCTRL`（等价于「新迁移加了父表、没人登记」） | ✅ 红：`platform_outbox <- …/641_….sql`，`缺 1 条` |
| **覆盖下限不是装饰** | 把 `minParents` 从 25 临时抬到 40（实测 33 之上） | ✅ 红：`只从 3389 个 .sql 里抽出 33 个分区父表（下限 40）：抽取逻辑很可能已失效…` 并打印全部 33 个名字 |

两次变异均已还原（`gofmt` 干净、`go test ./internal/partguard/` 全绿、工作树只剩 2 个预期文件）。

**为什么这两条缺一不可**：第一条只证明「门能抓到漏网表」；第二条才证明**「门在抽取坏掉时会喊」** —— 否则一个恒真的集合比较会把两种情况一起吞掉（199 号 §96）。

---

## 五、证伪清单（4 条）

1. ❌ 撤回「`partguard` 漏了 45 个分区父表」—— 那是我的 `PARTITION OF` 判据把**子分区**当成了父表；判据修正后为 **0**。
2. ❌ 撤回「`foo` 是一张漏网的分区表」—— 来自 `sql/scripts/phase-23-…/02-event-trigger.sql`，是脚本演示内容，且经复核与 `public` 同为格式串回溯产物。**未单列**（它既不是 `PARTITION BY` 声明，也不在任何父表位置）。
3. ❌ 撤回「`platform` 是一张漏网的分区表」—— 它是 **schema 名**；真表是 `platform_outbox`。
4. ❌ 撤回「这是 P1/P2 缺陷」—— 实测**零 Go 写点**，定 **P3**（清单与 DDL 不一致的防御性补全）。**不把「今天恰好没人碰」说成「已合规」**。

---

## 六、验证命令与结果（可重放）

```powershell
# 1) 新门
go test ./internal/partguard/ -run TestPartitionParentsAreExhaustive -v -count=1
#   从 3389 个 .sql 抽出 33 个分区父表，清单 33 条，缺 0 条
#   --- PASS

# 2) 整个 partguard 包（8 条门全绿）
go test ./internal/partguard/ -v -count=1
#   PASS ×8：NoParentTableDMLBeyondKnown / SQLiteExclusionIsLoadBearing /
#            ParentsAreDeclaredInDDL / PartitionParentsAreExhaustive /
#            ScannerParsesRepoGo / SelfTest×3

# 3) 全部守卫包
go test ./internal/rowsguard/ ./internal/errdiscard/ ./internal/routeguard/ \
        ./internal/partguard/ ./internal/metricguard/ -count=1
#   ok ×5

# 4) 负控（两条，见 §四）
#   4a 把 "platform_outbox" 改名 → FAIL「缺 1 条」
#   4b minParents 25→40        → FAIL「下限 40 … 抽取逻辑很可能已失效」

# 5) 静态检查
gofmt -l internal/partguard/parents.go internal/partguard/guard_test.go   # 空
go vet ./internal/partguard/                                            # 无输出
go build ./...                                                          # exit 0
```

**诚实边界（逐条）**

- 改动 = **1 个守卫实现文件（`parents.go` 加 1 个表名 + 注释）+ 1 个守卫测试文件（新增 1 条门）**，**零生产代码、零数据库、零客户端观感变化**。
- 抽取是**正则**形态的判据。`parents.go:5-7` 已经论证过为什么这个族选择硬编码而非全量 DDL 解析（形态跨行、带 `IF NOT EXISTS` / schema 限定，逐文件正则解析的脆弱度高于它防住的风险）—— 我**没有**推翻那个决定，只在它旁边补了一道**会响的**双向比较，而不是把抽取器做得更聪明。
- **`platform_outbox` 属 `sql/migrations/local/`**：本轮**未验证**它是否在任何生产部署路径上执行（未起进程、未连 PG、未查部署脚本）。定级 P3 时已把这一点记进代码注释。
- **未跑 `go test ./...` 全量**（跑 `./internal/...` 与全部守卫包）。`go build ./...` exit 0。
- 本机 `core.hooksPath` 未设置 ⇒ **本会话历次推送都未经 pre-push 门**（沿用 199/200 号登记）。

---

## 七、下一轮顺位

1. **200 号登记的 P3**：把 `rowsguard` 的 stale 豁免告警从 `t.Logf` 升级为失败（理由：失效豁免的代价是「门对这一处不再有话可说」，而非致命就等于静默）。
2. 待裁决 **82**（已扩大一条）→ **81** → **79** → **80**。
3. `sqlreadguard` 的 `DEBT(R47)` 清单（13 条 `admin/domains/bg/cmd` 读面裸母表读）逐条双腿化 —— 那是 199 号盘点时看到、但本会话没碰的真实债务。

---

## 八、playbook 新增

### §102 补「反向判据」时，先证你的抽取器不产假阳性 —— 而且**顺序不能反**

- 一道只做**单向**校验的门（清单 ⊆ 现实）会**静默腐化**：新迁移加一张表，守卫对它零覆盖，而失效形态是「本该被挡的写操作畅通无阻」。
- ⇒ 清单类守卫应成对：正向查「清单里的都在」，**反向**查「现实里的都在」。本轮实证这条反向当天就抓到 1 张真实漏网表（`platform_outbox`）。
- ⚠️ **反向判据的抽取器本身就是新的假阳性来源**，而它产出的缺失**看起来和真实缺陷一模一样**。本轮两次栽在同一个地方：正则抓到点分路径的**第一段**（schema 名）当表名。
  ⇒ **顺序铁律：先证「抓到的是表名」，再问「清单有没有它」。**
  **绝不能**为了让门变绿而把抓到的名字加进清单 —— 那会把判据错误固化成事实。
- ⇒ 配套两件套（本轮都验过）：
  1. **覆盖下限 + 失败时打印抽到的全部名单**（让「抽取坏了」与「清单漏了」一眼可分）；
  2. **零收益模式要拿掉，不是修补** —— `ALTER … ATTACH PARTITION` 贡献 0 个独有父表、1 个假阳性，净有害。

### §103 正则抓出来的「缺失」，先分类再定级

本轮 `partguard` 的三条候选缺失，最后落到三种不同结论：**45 条是子分区**（判据错）、**1 条是 schema 名**（判据错）、**1 条是真表但零写点**（真发现，P3）。
⇒ 一个「缺失清单」在定级之前必须先过一遍分类：**子表还是父表 / 表名还是 schema 名 / 真有写点还是没人碰**。跳过这一步就会把判据错误当成仓库缺陷写进报告。
