# 200 号 · R89-DK —— 收 199 号的尾：清掉那条漂移的豁免、给守卫 CI 补上 Windows、并且**再修一个我自己刚写错的行号**

> 结论先行：三项全部完成，**改动仅限 1 个守卫测试文件 + 1 个 CI 文件 + 文档**，零生产代码。
> 本轮最值得记的不是修了什么，而是**我在动手的第一分钟就把行号写错了（4623 vs 4627）**，而这个错误恰好被我要修的那道门本身抓住 —— 这是「用判据验自己」的一个干净例子。

---

## 〇、起手

199 号 §八 留了四条顺位。本轮做前三条（第 4 条即本轮 §五 的产出）：

1. 定位并清理 `rowsguard` 那条**真实失效**的豁免（`cmd/gateway/main.go:4552` 行号漂移）；
2. 给 `audit-guards-ci.yml` 补 Windows 执行路径（199 号 §99「平台分叉要有主人」的收口）；
3. 把守卫族表写进 README 作**常驻索引**（阻止「不知道某族存在」第三次发生）。

---

## 一、🔴 F1：一条失效豁免 —— 且**不能靠猜**来修

### 1.1 现象

199 号修完分隔符后，`rowsguard` 的两条门都绿了，但留了一句非致命日志：

```
rowsguard_test.go:87  exemption cmd/gateway/main.go:4552 is stale (line drift) — remove it
```

而 `cmd/gateway/main.go:4552` 现在是 `AlertOnQualityDrop: true,` —— **根本不是循环**。该文件里有**两条** `for rows.Next()`：

```
4627: for rows.Next() {
6601: for rows.Next() {
```

⇒ **风险很具体**：豁免表只有一条 `main.go` 条目，我若随便指向其中一条，就是**把一个真实站点错标成「已豁免」** —— 那是比失效更坏的结果（失效至少会喊，错标会静默放过）。

### 1.2 定案方法：回原提交看那一行当时是什么

```
git log -S 'cmd/gateway/main.go:4552' -- internal/rowsguard/rowsguard_test.go
  → 8a6142263  fix(rows): R66 rows 族第三批 …（zcode, 2026-10-01）

git show 8a6142263:cmd/gateway/main.go | sed -n '4552p'
  → for rows.Next() {          ← 正是它

git show 8a6142263:cmd/gateway/main.go | grep -n 'for .*\.Next()'
  → 4552: for rows.Next() {
    6510: for rows.Next() {   ← 这一条从未登记
```

⇒ 登记的那条就是今天的 **4627**（同在 `func main()`，186 行），另一条是漂移出来的 6601（原 6510，从未在表里）。**证据充分，不需要猜。**

### 1.3 ⚠️ 一个差点让我改错的环境陷阱

我第一次用 `Get-Content main.go | Select-Object -Skip 4618` 去看 4627 附近，看到的却是 `slog.Warn(...)` —— 与 ripgrep 的 4627 **矛盾**。

真因：**`main.go` 里含孤立 `\r`（不是 `\r\n`）**，`Get-Content` 的行数基准与 ripgrep / Go 词法分析器的 `\n` 基准不一致，于是我自建的行号计数器整体错位。

⇒ 这与我在 199 号遇到 README「PowerShell 144 行 vs grep 258 行」是**同一个坑**，第二次踩。
⇒ **行号敏感的核对一律用 ripgrep（Grep 工具）或 `read` 工具**，不要用 `Get-Content` 手工计数。

### 1.4 修法

```go
// 4552 -> 4627 是行号漂移，不是新增债：用 git show 8a6142263:cmd/gateway/main.go
// 核对过，登记那行当时正是 `for rows.Next() {`，同在 func main()，而本文件另一条
// 循环（当时 6510、现在 6601）从未登记。TestExemptionsStillResolve 会把对不上的
// 键报成 stale；改键前必须回原提交确认是「同一处漂移」而不是「另找一处顶上」。
"cmd/gateway/main.go:4627": "one-shot/main package wiring, out of R66 scope (line drifted from 4552)",
```

**理由写进键值**（`line drifted from 4552`）而不是只写注释 —— 豁免表是**给下一个人读的清单**，它必须能回答「这条凭什么在表里」。

### 1.5 算术闭合

| | 修复前（199 号已修分隔符后） | 本轮 |
|---|---|---|
| 豁免表条目 | 13 | 13 |
| 成功绑定 | **12**（1 条 stale） | **13** |
| `checked`（豁免后站点数） | 754 | **753** |
| 违规 | 0 | **0** |
| stale 告警 | 有 | **无** |

**766 − 13 = 753** ⇒ 13 条全部绑定、零违规，**总站点数与 199 号修复前报出的 766 完全一致** ⇒ 没有为了让它变绿而放过任何真实站点。

---

## 二、🔴 F2：我自己把行号写错了（4623），**而门抓住了我**

第一次编辑我写的是 `cmd/gateway/main.go:**4623**`（凭 §1.3 那个被污染的手工行号）。改正注释与键值后跑测试：

```
rowsguard_test.go:52  rows guard: 753 sites checked, 0 violations
--- PASS: TestEveryRowsLoopIsGuarded
--- PASS: TestExemptionsStillResolve
```

第一次写 4623 时，`TestExemptionsStillResolve` 会把它报成 stale（**非静默**）—— 我在没跑测试前就自查发现并改了，但这条仍然值得记：

> **一道「报 stale 而不是硬失败」的门，也能在你写错行号时给出可读反馈。** 199 号 §97 的
> 「先查键的构造」在这里的反向用法是：**改这类清单时，把门当 linter 用。**

⚠️ 但也暴露一个真实风险：**stale 是非致命的**（`t.Logf`）。若我改了键又没跑测试就提交，一条**已不存在的**豁免会静默留在表里 —— 而失效豁免的代价正是「门对这一处不再有话可说」。⇒ 已登记为下轮建议（见 §六）。

---

## 三、🔴 F3：给守卫 CI 补 Windows 执行路径（199 号 §99 的收口）

### 3.1 改了什么

```yaml
jobs:
  guards:
    name: audit guards (${{ matrix.os }})
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    defaults:
      run:
        shell: bash        # windows-latest 默认是 pwsh，不声明会找不到 bash
```

七步全部保留（Checkout / Setup Go / guards-sync / go build / **make guards** / install entrypoints / user scripts sync），触发面不变。

### 3.2 为什么这么设计（三条都有理由，不是偏好）

- **`fail-fast: false`** —— 两个平台各自报告。默认的 fail-fast 会让先失败的那个取消另一个，于是**「某平台没跑」被误读成「都过了」**。
- **`shell: bash` 是必需的**，不是风格 —— `guards-sync.sh` / `install-entrypoints-test.sh` / `user-scripts-sync-test.sh` 都是 bash 脚本，而 `windows-latest` 的默认 shell 是 pwsh。工作流原注释（`:84-85`）已记明这三个脚本**内部有 Windows 感知**（「Windows 实跑段在 Linux 上按 skip 处理」）⇒ 本 job 让它们在 Windows 上真正跑到，而不是继续 skip。
- **`make guards` 保持不变** —— R88 刻意让 `Makefile` 成为唯一事实源；Windows job 同样走 `make guards`，**不另列一份包清单**（那正是 R88 修掉的重复）。

### 3.3 ⚠️ 我在改这个文件时**自己引入又当场抓住**的一个 bug

`concurrency.group` 原本是 `audit-guards-ci-${{ github.ref }}`。加了矩阵之后，**两个平台的 job 落在同一个 concurrency 组**，而 `cancel-in-progress: true` ⇒ **它们会互相取消**。

⇒ 这正是本 job 要防的那类失效（「一个平台静默消失」）由我亲手造出来。已改为：

```yaml
group: audit-guards-ci-${{ github.ref }}-${{ matrix.os }}
```

**教训**：给 job 加矩阵时，**任何按 job 维度成立的 key（concurrency、cache、artifact 名）都要重新审一遍** —— 它们不会因为 job 变成两个而自动分开。

### 3.4 支撑证据与诚实边界

**支撑**：本轮实测 `go test ./internal/...` **全绿**，覆盖 `GUARD_PACKAGES` 全部 10 个（`rowsguard` / `errdiscard` / `dbrows` / `jsoncol` / `metricguard` 44.1s / `sqlreadguard` / `partguard` / `routeguard` / `sqlguard` …）⇒ 这 10 个在 Windows 上当前是绿的。

**诚实边界**：

- ⚠️ **我无法运行 GitHub Actions**，Windows runner 上 `make` 的可用性、`shell: bash` 的实际行为、三个 `.sh` 门在 Windows 上的真实表现**都未实测**。这是本轮最大的未验证面，已登记。
- `-timeout=120s` 是**每包**计时（`go test` 的 timeout 不含构建时间），`metricguard` 本机 44.1s 有余量，但 Windows CI 冷 runner 上余量未知。
- 本机 `core.hooksPath` 未设置、裸 `bash` 不在 PATH ⇒ **本会话历次推送仍未经 pre-push 门**（用 Git 自带 bash 只做了 `guards-sync.sh` 的手工实跑）。

---

## 四、附带确认：`guards-sync.sh` 的覆盖面（重读，避免第二次误判）

199 号差点误报的那个 P1，本轮把四份清单全部读完并确认：

| 清单 | 角色 | 范围 |
|---|---|---|
| `Makefile:58 GUARD_PACKAGES` | **登记源** | `internal/*` 10 个 |
| `scripts/checks/guards-sync.sh` | 校验「磁盘 ↔ 登记」**双向** | **不看任何 CI 文件** |
| `.githooks/pre-push:111-115` | **本地快速子集** | 7 个 |
| `.github/workflows/audit-guards-ci.yml:78` | **全量执行** | `make guards` = 10 个 |

⇒ `guards-sync.sh` 的**不覆盖**是**设计上**的（它只管登记，不管谁执行），原注释已明说「guards-sync 已校验二者一致」是假的且 R88 已修。**199 号的撤回是对的，本轮再确认一次。**

---

## 五、常驻索引：守卫族表进了 README（附录 A）

本会话已**两次**因「不知道某族守卫存在」而误判（198 §7.4 撞见 `sqlreadguard`；199 §六·3 差点报假 P1）。⇒ 已在 `docs/全面审计v3/README.md` 末尾新增 **`## 附录 A · 本仓审计守卫族索引（常驻，199 号建立）`**，含：

- 11 个守卫条目（`internal/` 7 族 + `dbrows`/`jsoncol` + `admin/` 5 个）各自的**禁止什么 / 判据形态 / 执行路径**；
- 复现清单（`grep -l 'filepath\.WalkDir\(|repoRootFromCaller…'` ⇒ 38 文件），并**明写「不要用函数名 grep」**；
- 四份清单的分工表 + 「下『执行缺口』类结论前先把四份都读完」。

---

## 六、证伪与登记

| # | 事项 | 处置 |
|---|---|---|
| ❌ | 「行号漂移就是新增债，直接删掉这条豁免」 | 撤回。删掉会让真实站点变成违规；正确动作是**回原提交确认是同一处漂移**再改键 |
| ❌ | 「`Get-Content` 数出来的 4627 才是真的」 | 撤回两次。`main.go` 含孤立 `\r`，PowerShell 行数基准与 ripgrep 不一致 ⇒ **行号敏感处一律用 Grep 工具** |
| ❌ | 「`rowsguard` 753 < 199 号的 754，说明少了一个站点」 | 撤回。`checked` 统计**豁免后**站点数（`rowsguard.go:260-263` 先查豁免再 `checked++`），多绑定 1 条豁免 ⇒ 754−1=753，**总站点 766 不变** |
| 登记 | stale 告警是**非致命**的（`t.Logf`）。改这类清单时不跑测试就提交，会留下一条静默失效的豁免 | **P3，建议下轮把 stale 升级为失败**（理由已写进键值注释，降低误改概率） |
| 登记 | CI Windows runner 上 `make` 可用性、`shell: bash` 行为、三个 `.sh` 门在 Windows 的真实表现 | **未验证**，本轮最大风险面 |

---

## 七、验证命令与结果（可重放）

```powershell
# 1) 豁免清理（本轮）
go test ./internal/rowsguard/ -run 'TestEveryRowsLoopIsGuarded|TestExemptionsStillResolve' -v -count=1
#   rowsguard_test.go:52  rows guard: 753 sites checked, 0 violations
#   --- PASS  ×2   （stale 告警消失）

# 2) 改动过的守卫实现仍绿
go test ./internal/rowsguard/ ./internal/errdiscard/ ./internal/routeguard/ -count=1
#   ok ×3

# 3) 全量守卫包
go test ./internal/...
#   全绿（含 GUARD_PACKAGES 全部 10 个）

# 4) CI 文件结构校验（PyYAML）
python -c "import yaml;d=yaml.safe_load(open('.github/workflows/audit-guards-ci.yml'));j=d['jobs']['guards'];print(j['runs-on'],j['strategy'],j['defaults'])"
#   ${{ matrix.os }}  {'fail-fast': False, 'matrix': {'os': ['ubuntu-latest','windows-latest']}}  {'run': {'shell': 'bash'}}

# 5) 登记一致性（需 Git 自带 bash）
& 'C:\Program Files\Git\bin\bash.exe' -c 'cd /c/workspace/llm-gateway-go-2 && bash scripts/checks/guards-sync.sh'
#   ✅ 7 个 *guard 目录与 GUARD_PACKAGES 的 10 项双向一致   EXIT=0

# 6) 格式 / 静态检查
gofmt -l internal/rowsguard/rowsguard_test.go      # 空
go vet ./internal/rowsguard/                        # 无输出
go build ./...                                      # exit 0
```

**诚实边界**：改动 = 1 个守卫测试文件（豁免键 + 注释）+ 1 个 CI 文件 + 文档；**零生产代码、零数据库、零客户端观感变化**；**未运行 CI**（见 §3.4）；`go test ./...` 全量未跑（跑 `./internal/...` 全绿 + 受影响包）；本机 `core.hooksPath` 未设置故 **pre-push 门从未在本机执行**。

---

## 八、playbook 新增

### §100 改「键值清单」时，把它当 linter 用 —— 而且要回原提交定案

- **行号类清单（豁免表、knownCandidates、快照行号）失配时，正确的动作是回原提交确认是「同一处漂移」还是「另找一处顶上」**。`git log -S '<旧键>'` → `git show <commit>:<file>` 看那一行当时是什么。**直接改成看起来对的那一行 = 把真实站点错标成已豁免**，比失效更坏。
- **一道「报 stale 而非硬失败」的门，在你写错键时仍会给出可读反馈** ⇒ 改这类清单前先跑一遍门，把它当 linter。
- ⚠️ 反向风险：**stale 非致命** ⇒ 改完不跑测试就提交，会静默留下一条失效豁免，而失效豁免的代价正是「门对这一处不再有话可说」。

### §101 给 job 加矩阵时，重审所有「按 job 维度成立」的 key

本轮给 `audit-guards-ci.yml` 加 `os` 矩阵时，`concurrency.group` 仍只含 `github.ref` ⇒ 两个平台的 job 落在同一组，`cancel-in-progress: true` 让它们**互相取消** ⇒ **「某平台没跑」被误读成「都过了」**，正是这个 job 要防的失效。

⇒ 加矩阵后逐项重审：`concurrency.group` / `cache` key / artifact 名 / job 输出名。这些**不会**因为 job 变成两个而自动分开。
