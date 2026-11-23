# 2026-11-23 `deploy-test-gate` 选包口径——L9 机制缺陷修复（闸门漏选 `./admin`）

> **范围**：`scripts/deploy-test-gate.sh`（部署前测试闸门）的**选包判据**。
> **改动面**：部署脚本 1 文件改（`scripts/deploy-test-gate.sh`）＋ 部署门 1 文件新
> （`scripts/deploygate/deploy_gate_base_ref_selection_gate_test.go`）＋ 归档档 1 ＋ AGENTS.md 1。
> **零迁移 / 零 schema 变更 / 零后端逻辑改动 / 零前端改动**。
> **上轮登记**：用量趋势深链轮 §七 **L9**（机制缺陷，本档兑现）。

---

## 一、报障与缺陷

owner 指令：「请修复 deploy-test-gate 选包逻辑：以 BASE_REF 与 HEAD 的 merge-base 纳入 ./admin 变更，并补变异自证。」

### 1.1 缺陷事实（上一轮实测取证，本轮复现）

上一轮（用量趋势深链轮）的实测记录：

> 本轮部署的 test-gate 输出「`[test-gate] 要跑的包：./cmd/gateway ./ratelimit`」⇒ **闸门通过**；
> ★ **但 `./admin` 根本没进闸门**——而本轮的生产改动**全在 `admin/`**。

取证（`scripts/deploy-test-gate.sh`，旧版）：

- **选包判据**＝三行，**不含 `BASE_REF`**：

  ```bash
  changed_files="$({
    git diff --name-only -z HEAD 2>/dev/null || true
    git diff --name-only -z --cached 2>/dev/null || true
    git diff --name-only -z HEAD~1 HEAD 2>/dev/null || true
  } | tr '\0' '\n' | sort -u)"
  ```

- **`BASE_REF` 在旧脚本里只出现三处**：第 92 行赋值（`BASE_REF="${BASE_REF:-origin/main}"`）、
  第 534 行日志、第 537 行基线对照（`git merge-base HEAD "$BASE_REF"`）⇒
  ★ **它完全不参与选包**。

⇒ ★★ **根因不是「漏写了一路」，而是「两套口径漂移」**：
　选包＝「最后一笔提交」；基线对照＝「基线树」⇒ ⇒ 选出来的包，★ **基线树可能根本没跑过**
　⇒ ⇒ 「这盏灯是新的红还是旧的红」**判不出来** ⇒ ★ **新红会被当成既有红放行**。

---

## 二、事故复现（真实提交树上的逐字对照）

★ 本轮用**真实历史提交树**复刻事故现场（★ 不用 mock）：

- 事故树 HEAD ＝ `ed3e69c89`（并行会话的 ratelimit 改动，★ **不碰 `admin/`**）；
- 其父 HEAD~1 ＝ `044349a2c`（★ **本轮的 `admin/` 生产改动在这一笔**）；
- BASE_REF ＝ `eeaed141`（＝**当时真正的现役树**）。

**同一棵树上、同一套「文件清单 → 包清单」规则，只换 changed_files 的来源**：

| 判据 | 选出的包 |
|---|---|
| ★ **旧**（三行、无 `BASE_REF`） | `./cmd/gateway` `./ratelimit` ← ★ **与事故当时闸门输出逐字相同** |
| ★ **新**（+ 基线 diff 一路） | `./admin` `./cmd/gateway` `./ratelimit` `./security/armor` ← ★★ **`./admin` 回来了** |

⇒ ★★ 这不是推演，是**事故的精确复现 + 修复的精确证明**：
　旧判据的输出**逐字等于**事故日志里那句「要跑的包」；新判据把本该进闸门的 `./admin` 选了出来。

★ **`./security/armor`** 也一并回来（★ 并行会话另有的 armor 改动同样在基线之后）⇒
　★ 这正说明**旧口径不只漏了本轮，还漏了所有「不是最后一笔」的改动**。

---

## 三、修法

### 3.1 选包口径与基线对照口径**统一到同一个 merge-base**

★ 抽三个**可测**函数（★ 沿用上一轮 N10「判据抽成函数是为了让判据可测」的做法）：

| 函数 | 职责 |
|---|---|
| `dd_gate_base_commit()` | 算基线 commit＝`git merge-base HEAD "$BASE_REF"`（★ **唯一入口**） |
| `dd_gate_changed_files [base_commit]` | 改动清单＝**四路并集**（见下） |
| `dd_gate_packages_from_files [files]` | 文件清单 ⇒ 包清单（★ `.go` 目录规则 ＋ 守卫域映射） |

★ **改动清单的四路**（★ 缺任何一路都会漏选包 ⇒ 漏掉的包**不会被闸门看见**）：

1. 工作区未提交改动 `git diff HEAD`
2. 暂存区未提交改动 `git diff --cached`
3. 最后一笔提交 `git diff HEAD~1 HEAD`（★ **兜底**：BASE_REF 退化成 HEAD 自身时仍能选出一笔）
4. ★★ **相对基线树的全部改动** `git diff <base_commit> HEAD`
   （★ `base_commit` ＝ merge-base(HEAD, $BASE_REF) ＝ ★ **基线对照跑的那棵树**）

★ **基线对照那一段改成走同一个 `dd_gate_base_commit`**（★ 而不是自己再算一次 merge-base）
⇒ ⇒ 选包与基线对照**永不漂移** ⇒ ★★ 「基线树 vs 当前树」的差异 ＝ 选包的差异
⇒ ⇒ **新红/旧红可判定**（★ 这才是闸门本来的职责）。

### 3.2 退化安全

- `BASE_REF` 指向**不存在的 ref** ⇒ `git merge-base` 失败 ⇒ ★ 第④路**安静跳过**（`|| true`）
  ⇒ 不得 unbound、不得让整个闸门崩；兜底第③路仍可用。
- `BASE_REF` 缺省（`origin/main`）⇒ 当部署树 ＝ origin/main 尖端时 merge-base 退化成 HEAD 自身
  ⇒ 第④路为空 ⇒ ★ 仍由第③路兜底 ⇒ **不会退化成「选不出包」**。

### 3.3 影响面（★ 如实登记）

- ★ **闸门会多跑一些包**：第④路带出「基线以来所有改动触及的包」⇒ 比原来多。
  ⇒ ★ 但这些包**本来就该跑**（本次上线的改动就在它们身上）⇒ ⇒ 代价是**时间**、收益是**不再漏检**。
- ★ **既有红仍照旧处理**：多出来的包若本来就红 ⇒ ★ **基线树也跑过它们** ⇒ 基线同红 ⇒
  判为既有红 ⇒ 放行 ⇒ ★ 这正是「口径统一」换来的**可判定性**（★ 旧口径下这些包压根没被选中
  ⇒ ⇒ 它们的红**永远不会被发现**）。
- ★ **零后端/前端逻辑改动** ⇒ 不涉及迁移、schema、运行时行为。

---

## 四、门禁与变异自证

### 4.1 新门（`scripts/deploygate/deploy_gate_base_ref_selection_gate_test.go`，★ 轻包、★ 不进重包）

| 判据 | 内容 |
|---|---|
| ① 【行为】 | ★ **真 git 仓库**（三笔提交：commit1 当基线、commit2 改 `admin/x.go`、commit3 改 `ratelimit/y.go`）⇒ ★ 改动在**倒数第二笔** ⇒ 旧判据**必然**选不出 `./admin` ⇒ 新判据**必须**选出 |
| ② 【变异自证】 | ★ 把第④路替换成 `true` ⇒ 同一场景**必须选不出** `./admin` ⇒ ⇒ 证明①的 PASS 不是恒绿 |
| ③ 【退化安全】 | `BASE_REF` 指向不存在的 ref ⇒ 退出码 0、无 unbound、无崩 |
| ④ 【口径统一】 | ★ **唯一入口约束**：可执行代码里 `git merge-base HEAD "$BASE_REF"` **恰好一次** ＋ `$(dd_gate_base_commit)` **≥2 次** ＋ 那唯一一次**落在函数体内** |

★ ④ 为什么不用「某处调用了 `dd_gate_base_commit`」这种弱判据：★ **只存在一处就放行** ⇒
　基线对照那一处单独被改回旧形态仍会绿 ⇒ ★ **门没牙齿** ⇒ ⇒ 本轮**实际踩到过**
　（首版判据就是这样写的，被自身的变异臂当场判红后改成了唯一入口约束）。

★ ★ 计数一律在**去注释**后的正文上做（★ `gateCodeOnly`）：★ 脚本的说明注释里**引用过**
　`git merge-base HEAD "$BASE_REF"` 这个形态（★ 用来讲历史缺陷）⇒ ★ 按全文计数会把
　**注释**当成「第二个旁路」⇒ ⇒ 门会误报 ⇒ ⇒ ★ **门自己的误报比没门更坏**。

### 4.2 变异自证（★ 真实文件上做的，★ 不是纸面推理）

在**真实脚本副本**上把第④路替换成 `true`：

```
--- FAIL: TestDeployGateSelectsPackagesChangedSinceBaseRef
    选包判据没有选出 ./admin（★ 改动在基线之后、但不在最后一笔 ⇒ 必须由基线 diff 那一路选出来）
--- FAIL: TestDeployGateBaseRefSelectionHasTeeth
    原判据都没选出 ./admin ⇒ 判据①的 PASS 是假的 ⇒ 本门自证不成立
```

⇒ ★ **2 条精确报红** ⇒ ⇒ 恢复后 **`ok github.com/kaixuan/llm-gateway-go/scripts/deploygate 1.445s`** ⇒ ★ 12/12 全绿。

★ 另：判据④的变异臂（把基线对照那处换回旧形态）实测 ⇒ 直接调用次数 1→2、调用形态次数 2→1
⇒ ⇒ 两条唯一入口约束**都真的咬住了**。

### 4.3 门禁结果

| 门 | 结果 |
|---|---|
| `bash -n scripts/deploy-test-gate.sh` | ✅ |
| `go test ./scripts/deploygate/` | ✅ **12/12**（既有 8 ＋ 新增 4） |
| `scripts/ursmcheck` 定向（部署/AGENTS/审计档/章节编号/CI 载体） | ✅（见 §七） |

---

## 五、通用式

1. ★★ **「闸门通过」≠「你的改动过了闸门」**——闸门**选包**与**基线对照**是两件事：
   `BASE_REF` 只参与基线对照、**不参与选包** ⇒ ⇒ **每次上线前都要自己确认
   「我这次改的包到底进没进闸门」**。
2. ★★ **两套口径漂移 ⇒ 新红会被当成既有红放行**——选包与基线对照**必须共用同一个 merge-base**，
   否则「基线树 vs 当前树」的差异 ＝ 选包的差异这条等式不成立 ⇒ ⇒ 闸门失去判定能力。
3. ★★ **「只看最后一笔提交」在并行开发下必然漏检**——本仓多人并发推 `origin/main`，
   一次部署的树往往叠着**别人的**提交 ⇒ ⇒ **闸门要按「相对现役的全部改动」选包**，
   而不是「最后一笔」。
4. ★ **验收事故用真实提交树复现，比写 mock 更有说服力**——本轮直接拿 `ed3e69c89` 那棵树
   跑旧/新两套判据 ⇒ ★ 旧判据输出**逐字等于**事故日志 ⇒ ⇒ 这就是铁证。
5. ★ **门自己的误报比没门更坏**——本轮两度自伤：① 弱判据（存在即放行）被自身变异臂判红；
   ② 唯一性计数把**注释里引用的形态**算成旁路 ⇒ ⇒ 门的判据必须先被自己的变异臂咬过。
6. ★ **变异点定位必须确定性**——★ 「锚点 + 第一个匹配」会命中**同名局部变量**
   （本轮：`local base_commit="${1:-}"`）⇒ ⇒ 变异改了个不相干的地方 ⇒ ⇒ 断言数对不上
   ⇒ ⇒ **门自己先红** ⇒ ⇒ 定位要按**出现次数**（第几处）而不是「第一个」。

---

## 六、干净检出演练（★ 真实脚本、★ 不是抽函数）

★ 上一节的门测验证的是**抽取出来的判据函数**；★ 本节是**端到端演练**：
★ 在**隔离 worktree 做干净检出**、用**旧现役 BASE_REF**、跑**真实脚本**
（`scripts/deploy-test-gate.sh`），确认 ★★ **`./admin` 被实际选包、实际跑完、且绿**。

### 6.1 演练设置

| 项 | 值 |
|---|---|
| 检出方式 | `git worktree add --detach` ＋ `git reset --hard origin/main` ＋ `git clean -fd` ⇒ ★ **树干净**（`git status --short` 为空） |
| 演练 HEAD | `10f24d33e`（origin/main 尖端） |
| **BASE_REF** | ★ **`eeaed141`**（＝**旧现役**，build 3236 那一版） |
| 相对基线的改动 | ★ **14 个 `admin/` 文件**（★ 这正是「旧现役之后 admin 改过」的形态） |

### 6.2 ★ 选包对照（同一棵干净检出、同一 BASE_REF、只换脚本版本）

| 脚本 | 实际输出 |
|---|---|
| ★ **旧**（修复前，`7aa5ed803~1`） | `./admin/ ./domains/sanitizerules/ ./domains/hooks/outputcompliance/ ./security/sanitize/` ← ★★ **这就是 FALLBACK 回落清单** |
| ★ **新**（修复后） | `./admin` `./cmd/gateway` `./domains/hooks/security` `./domains/provider` `./domains/security` `./domains/streaming` `./installer/cmd/llm-gw-installer` `./installer/internal/dbinit` `./internal/sessioncontext` `./internal/sqlguard` `./internal/verdictkv` `./metrics` `./plugin-runtime` `./ratelimit` `./scripts/deploygate` `./scripts/ursmcheck` `./security/armor` `./security/sanitize` `./sql/migrations/startup` `./sql/schema`（★ **21 个包，`./admin` 在第一位**） |

★★ ★★ **旧口径的真相比「漏选 admin」更严重**：★ 演练 HEAD 的最后一笔是**纯 docs 提交**
⇒ `HEAD~1..HEAD` 里**没有任何 .go 改动** ⇒ ★ **旧口径在这棵干净检出上「一个包都选不出」**
⇒ ⇒ 只能**回落 FALLBACK**（4 个核心包）⇒ ★★ **除 FALLBACK 之外、本次上线真正改过的包全部漏检**
⇒ ★ 而 `./admin` 只是**碰巧**在 FALLBACK 里（★ 换句话说：**它的「被覆盖」是回落清单的运气，不是选包判据的功劳**）。

### 6.3 ★ `./admin` 实际跑完且绿

真实闸门输出（★ 节选，★ 21 个包里的 17 个已出结果）：

```
ok   github.com/kaixuan/llm-gateway-go/admin            204.421s   ← ★★ 本轮改动所在的包
ok   github.com/kaixuan/llm-gateway-go/cmd/gateway        28.679s
ok   github.com/kaixuan/llm-gateway-go/domains/hooks/security  0.062s
ok   github.com/kaixuan/llm-gateway-go/domains/provider      0.053s
ok   github.com/kaixuan/llm-gateway-go/domains/security       0.037s
ok   github.com/kaixuan/llm-gateway-go/domains/streaming    235.564s
ok   github.com/kaixuan/llm-gateway-go/internal/sessioncontext 18.571s
ok   github.com/kaixuan/llm-gateway-go/internal/sqlguard      18.519s
ok   github.com/kaixuan/llm-gateway-go/internal/verdictkv     1.837s
ok   github.com/kaixuan/llm-gateway-go/metrics                0.100s
ok   github.com/kaixuan/llm-gateway-go/plugin-runtime          5.932s
ok   github.com/kaixuan/llm-gateway-go/ratelimit               0.086s
ok   github.com/kaixuan/llm-gateway-go/scripts/deploygate     2.824s
ok   github.com/kaixuan/llm-gateway-go/security/armor          0.455s
ok   github.com/kaixuan/llm-gateway-go/security/sanitize     115.803s
ok   github.com/kaixuan/llm-gateway-go/sql/migrations/startup 23.198s
ok   github.com/kaixuan/llm-gateway-go/sql/schema            114.359s
```

⇒ ★★ **`./admin` 被实际选包、实际跑完（204.4s）、且绿**。

### 6.4 ★ 唯一的红：3 条既有红（★ 基线逐字同红 ⇒ 闸门放行）

★ 唯一有红的是 `scripts/ursmcheck` 的 **3 条台账门**：

```
--- FAIL: TestDbObjectGapLedgerIsFullyRecomputable
    db_object_census_gate_test.go:910: ledger has 941 rows, expected 959
--- FAIL: TestInstallerReachabilityLedgerIsRecomputable
    installer_reachability_gate_test.go:144: reachability ledger has 941 rows, expected set has 959
--- FAIL: TestInstallerReachabilityLedgerPinsItsInputs
    ★ reachability ledger was generated against a manifest of 387 files, the installer manifest now has 391
    ★ reachability ledger was generated against a distribution package of 387 migrations, the package now has 395
```

⇒ ★★ 在**基线树**（`eeaed141`）上跑**同样这 3 条** ⇒ ★ **逐字相同的红** ⇒ ⇒ **既有红**。

★ 闸门判定逻辑（`scripts/deploy-test-gate.sh`）：

```bash
new_reds="$(comm -23 <(printf '%s\n' "$current_reds") <(printf '%s\n' "$base_reds") | grep -v '^$' || true)"
...
if [[ -z "$new_reds" ]]; then   # ★ 集合差为空 ⇒ 没有新红 ⇒ 放行
```

⇒ ★★ 这 3 条**不在 `new_reds` 里**（基线同红）⇒ ⇒ **闸门判「既有红」⇒ 放行** ✔
⇒ ⇒ ★★ **口径统一换来的可判定性，在这里第一次被真正兑现**（★ 旧口径下这 3 条压根不会被跑，也就无从判定）。

★ ★ 这 3 条红**与本次改动无关**：★ 本次只碰了 `scripts/deploy-test-gate.sh` ＋ 新增
`scripts/deploygate/deploy_gate_base_ref_selection_gate_test.go` ⇒ ★ 台账行数（941 vs 959）
取决于仓内 db 对象 census ⇒ ★ 与选包判据无关。

### 6.5 ★ 演练被**主动中止**的那一段（如实登记）

★ 演练在 installer 两个包处被**我主动停掉**，原因：★ `installer/cmd/llm-gw-installer` 包的测试
会触发 ★ **设备在线激活**（★ 实测输出：`▶ 在线激活（注册即激活）...` ＋ 实例 ID ＋ 硬件哈希 ＋ 凭据有效期）
⇒ ★★ 这是**外部副作用**、★ 且闸门的基线对照会**再跑一遍同样的包** ⇒ ⇒ **会二次激活**。

⇒ ★ 所以「闸门全程跑完」这一条**本轮未完成**（★ 卡在 installer，★ 不是卡在红）⇒ ★ 如实登记，不粉饰。
⇒ ★★ 但**owner 要的三件事已全部达成**：★ ① `./admin` **被实际选包**（选包行第 1 个）；
★ ② **实际跑完且绿**（204.4s）；★ ③ **闸门的红全是既有红**（基线树逐字同红 ⇒ `new_reds` 为空 ⇒ 放行）。
⇒ ★★ 且**基线对照这一步**用**等价的小实验**完成（★ 只跑那 3 条，不跑整包 ⇒ 不触发激活）。

★ 附带发现（★ 与本次改动无关、属既有行为）：★ `scripts/ursmcheck` 重包在本机**能跑完**（454.0s），
★ 与上一轮记录的「跑不完」不同（★ 上一轮那次挂在 `os/exec` 的其它用例上）⇒ ★ 该包耗时需按 ~7.5 分钟计。

### 6.6 ★ 选包规模的现实口径

★ 本次演练选了 **21 个包**，★ 因为 BASE_REF 用的是**很旧的现役**（`eeaed141`）⇒ 累积了大量改动。
★ ★ **真实部署里 BASE_REF ＝ 当时的现役**（很近）⇒ 选包 ＝ 本次上线**真正改动**触及的包。
★ 实测：★ 以**当前现役**（`42ae8b70`）为 BASE_REF 时，演练 HEAD 相对它的差异 **40 个文件**、
★ 且**不含 `admin/`**（★ 因为 admin 改动早已上线）⇒ ⇒ **`./admin` 不会被选** ⇒ ★★ 这正是**正确行为**
（没改就不该跑）⇒ ★ 顺带证明：**第④路是按「相对现役的真实改动」选包，不是无脑全选**。

## 七、登记

| # | 条目 |
|---|---|
| **L1** | ★★ **闸门选包口径修复**：`changed_files` 增第四路（相对基线树的全部改动），基线对照改走同一个 `dd_gate_base_commit` ⇒ ★ 选包与基线对照**口径统一** ⇒ 新红/旧红**可判定**。改动面＝部署脚本 1 改 ＋ 部署门 1 新 ＋ 归档档 1 ＋ AGENTS.md 1。**零迁移 / 零 schema 变更 / 零后端逻辑改动 / 零前端改动**。 |
| **L2** | ★ 闸门会**多跑**一些包（基线以来所有改动触及的包）⇒ 代价是时间、收益是不再漏检；既有红的处理不变（基线树也跑过它们 ⇒ 判为既有红 ⇒ 放行）。 |
| **L3** | ★ `BASE_REF` 指向不存在的 ref ⇒ 第④路**安静跳过**、兜底第③路仍可用 ⇒ 闸门不崩。 |
| **L4** | ★ 新门放在 `./scripts/deploygate`（★ 轻包、★ 约 1.4s）⇒ ★ **不要**把它挪回 `scripts/ursmcheck` 重包（★ 上一轮已因重包拖慢部署闸门而拆出）。 |
| **L5** | ★ 用量趋势深链轮的 **L9** 条目已改写为「✅ 已修复」并指向本档（★ 原档保留事故现场与前后对照）。 |
| **L6** | ★★ **干净检出演练（§六）**：★ 旧口径在干净检出上**一个包都选不出**（最后一笔是纯 docs 提交 ⇒ 回落 FALLBACK）⇒ ★ `./admin` 当初被覆盖是**回落清单的运气**、不是判据的功劳。 |
| **L7** | ★ 演练在 installer 包处**主动中止**（★ 该包会触发**设备在线激活**、且基线对照会二次激活）⇒ ★ 「闸门全程跑完」本轮未完成 ⇒ ★ **闸门判定改用等价小实验取证**（★ 只跑 3 条既有红，不跑整包）。 |
