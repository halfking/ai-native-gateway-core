# WP-A：R37 修复独立复审

- 复审对象：`cc499a382`（fix(r37-audit)，2026-10-03 10:39），审计文档 `docs/12小时内修订审计-20261003-1045.md`。
- 复审方法：全只读。git show / 源码逐行核对 + 四组独立实证（入口门新旧两版实跑、pwsh 修前/修后对照实跑、maintain 转发管道假 curl 模拟、Go/TS 钉测变异重放×2）。两处变异均已还原，`git status` 中本任务触达文件零残留（bg/、web/src/composables/ 当前脏文件属并行 WP 在制，非本轮所为；已核对时间线无互踩）。
- 前置事实：`cc499a382..HEAD` 区间无任何提交再触达本轮被审文件，HEAD 状态 == 提交状态。

## 逐项判定表

| # | R37 声称 | 判定 | 证据（文件:行号）+ 理由 |
|---|---|---|---|
| 1 | P1: install.ps1 `$Action` 去 ValidateSet 改运行时校验 + Position=0 具名化 | **成立** | `install.ps1:40-47`（`[Parameter(Position=0)]` + `$Action` 无 ValidateSet，其余参数全部 `[Parameter()]` 具名化）、`:300-313`（运行时对照 `$builtinActions`/`$subcommands`，未知点名 Die）。**修前对照实证**（`cc499a382^` 版 + 参数绑定探针）：`upgrade` 单独形态 = ValidateSet 绑定报错（逐字复现）；`install upgrade` = 探针打印 `BIND: Action=[install] Channel=[upgrade]`——确实绑进 `$Channel`。**修后四形态实证**（pwsh 7.6.6）：`help`→exit0；`upgrade`→透传分支（无二进制按预期 Die「找不到」）；`frobnicate`→exit1 点名；`install upgrade`→`$Rest` 透传。加假二进制后 `upgrade --target 1.2.3`→`FAKE_ARGS: [upgrade] [--target] [1.2.3]` exit0、`install upgrade`→`FAKE_ARGS: [upgrade]` exit0。P1 症状与修复双向坐实 |
| 2 | P2: 入口门假二进制按 uname -s 拼名（unix 必红→71/71） | **成立** | `scripts/checks/install-entrypoints-test.sh:246-257`（Darwin/Linux→无后缀名）。**双向实证**：新版门在本机（darwin/arm64）实跑 pass=71 fail=0；旧版门（`cc499a382^`，经 /tmp 符号链接根跑当前 install.sh）pass=59 **fail=6**（PT 透传段全红）——「unix 必红」属实，非仅推断 |
| 3 | P2: install.ps1 `-Yes` 活化（Test-Interactive 首行） | **成立** | `install.ps1:160-164`：`if ($Yes) { return $false }` 确为函数首行（在 NO_INTERACTIVE/DryRun 之前）。门断言 `install.ps1 Test-Interactive 读取 $Yes` 绿。非交互全链未在无 tty 环境逐帧复跑，但源形状与门双重钉住 |
| 4 | P2: maintain 通道转发 --mode/--yes（bash -c 管道转发） | **成立** | `install.sh:334-343`。**管道模拟实证**（原行仅 curl 换假桩，转发机制逐字原样）：MODE=full+ASSUME_YES=1 → 官方脚本收到 `[--mode] [full] [--yes]`（与 R37 文档 §三 PIPELINE-GOT 输出一致）；MODE 空 + ASSUME_YES=1 → 仅 `[--yes]`（`${fwd[@]+"${fwd[@]}"}` 在 `set -u` 下空数组安全）。回归面：shebang `#!/usr/bin/env bash`（install.sh:1），脚本恒在 bash 下执行，zsh 差异不适用；该空数组卫语是 bash 3.2 兼容惯用形（install.sh:65 注释自证同族用法） |
| 5 | P2: 预算台账 cap 按 2×max(env,默认) 定尺寸且触顶改截断 | **成立** | `bg/capability_backfill.go:189-195`（定尺寸）、`:508-527`（截断）。三个重点子项：①**真"清最旧"非整体重置**——`b.probes` 为 append 序（chargeProbe:500 只 append `now`；窗口压实 `keep := b.probes[:0]` 保序），`copy(b.probes, b.probes[drop:]); b.probes = b.probes[:cap]` 丢的是最旧端；截断后 len=cap ≥ budget，闸门保持关闭（fail-closed，与注释「剩余额度只会被低估，方向保守」一致）。②**env=0 回落**——构造时 `budget > 0` 才放大，0/负→默认 4800；且 dailyBudget<=0 时 chargeProbe:488-490 根本不记账，cap 永不触顶，回落自洽；pruneProbesLocked:512-514 对结构体字面量构造的 0 值再回落包级底数。③**并发安全**——pruneProbesLocked 全仓仅两个调用点 budgetRemaining:481 / chargeProbe:495，均先 `budgetMu.Lock()`（:478/:492），覆盖完整。遗留两条 P3 见下 |
| 6 | P2: live-stream 负控制 openConnection 计数观测 | **成立** | `web/src/composables/liveStreamStore.ts:292`（计数）、`:1294-1295`（函数首行自增，含惰性早退——注释明说这正是观测点）、`:1509`（`__testing.openConnectionTotal`）；测试 `liveStreamStore.test.ts:769,777-780,786,795-800`。**变异 B 重放实证**：`if (true \|\| missedWhileHidden)` → 31 例中恰 1 红，红点即新断言 `:800 expect(openConnectionTotal()).toBe(opensBefore)`；还原后 31/31 绿。归因订正（「判据不在被测性质上」而非 listener 卸载）与实现/测试对读成立 |
| 7 | P2: 两道门接进 audit-guards-ci | **部分成立** | `.github/workflows/audit-guards-ci.yml:86-90` 两 step 确在、门无网络依赖、npm 探针 node 守卫属实。但触发面有洞：workflow paths 过滤（:23-27/:30-34）只含 `**/*.go`/`Makefile`/`scripts/checks/**`/自身，**不含 install.sh / install.ps1 / install.bat / npm\*\***——即两道门的主要被测输入。verify-ci（无 paths 过滤）的 verify.sh 不跑这两门，Makefile 也不含。只改 install.sh 的提交（恰是 45fbb0c75 回归的同类改动面）不会唤醒任何 CI 门。注释「触发面已含 scripts/checks/**」字面为真但覆盖不足 → 新发现 P2-1 |
| 8 | 钉测 TestBudget_LedgerCapScalesWithConfiguredBudget 变异承重 | **成立（独立重放）** | `bg/capability_backfill_budget_test.go:357-386`。重放：删 `if budget > 0 && budget*2 > ledgerCap` 放大段（等效钉死 4800）→ 钉测红，报错 `台账上限 4800 必须 ≥ 预算×2=10000`；`git checkout` 还原 → 绿；`git status` 该文件零残留 |
| 9 | 注释批抽查×2（node_state 时钟域 / client.go 谓词门） | **成立** | ①`credentialfpslot/node_state.go:519-527` 注释称年龄用本地单调时长——实现 `:605-608` 确为 `time.Since(prefetched.SnapshotReadAt)`，SnapshotReadAt 全部写点（:309 及批读 :340-380）均为本地 `time.Now()`/批次 readAt，`json:"-"`（:150）；期限判定另采 Redis TIME，两时钟域分立与注释一致。②`domains/hooks/observability/telemetry/client.go:2087-2097` 注释撤回「normalize 保证四值」——实现 `normalizeRequestStatus:3319-3328` 对非 nil 非空 status 早退，确只回填 nil/空、不清洗非空脏值，订正后归因（靠调用方常量纪律）与代码一致 |

## 变异重放结果

| 变异 | 对象 | 结果 | 还原 |
|---|---|---|---|
| cap 定尺寸回退为钉死默认（4800） | `bg/capability_backfill.go:193-195` | `TestBudget_LedgerCapScalesWithConfiguredBudget` **红**（4800 < 10000，第一条断言即拦） | git checkout 还原，重跑绿，零残留 |
| 变异 B（`if (true \|\| missedWhileHidden)` 无条件重连） | `web/src/composables/liveStreamStore.ts:319` | 31 例中**恰 1 红**，红点=负控制新断言 `openConnectionTotal` 不变（test.ts:800）；恢复用例的 opens+1 断言不受影响 | git checkout 还原，31/31 绿，零残留 |

## 新发现问题

- **P2-1（新）**：audit-guards-ci 触发面不覆盖两道门的被测输入。`.github/workflows/audit-guards-ci.yml:23-27` paths 无 `install.sh`/`install.ps1`/`install.bat`/`npm/**`；全仓无其他 workflow 跑这两门（verify.sh、Makefile 均不含）。R37 接线本体真，但「门会在回归时醒来」只对 `*.go`/`scripts/checks/**` 改动成立——45fbb0c75 型（只改 install.sh）回归仍无人拦。建议 paths 补 install.* / npm/**（或把两门挂进无过滤的 verify-ci）。
- **P3-1**：`bg/capability_backfill.go:193` `budget*2` 理论整型溢出——env 预算 > MaxInt/2（≈4.6e18）时乘积回绕为负、条件不成立、cap 静默回落 4800，重新引入「cap<budget」形态。现实命中率近零，登记即可（与 env 值域无上限校验同源）。
- **P3-2**：截断态下 `budgetRemaining`（:473-481）可返回负数（budget−cap），而唯一消费点 `:426` 只在 `rem == 0` 时打「预算耗尽」日志——被旁路刷爆台账的病态场景会记成 cycle done + 负数 daily_remaining，观测口径小失真。仅日志面，闸门本体（chargeProbe）不受影响。
- **P3-3（文档口径）**：R37 审计文档内部计数不一致——§二⑧「入口门源形状断言 ×6」与 §三「新增断言 ×7 全绿」矛盾（实测 65→71，新增 ok 断言为 6 条）。
- **范围外观测（不计入 R37）**：并行 WP 在制未提交改动给 `bg/capability_backfill.go` chargeProbe 加 metrics 时存在 `_ = metrics.RecordCapabilityBackfillProbeBudgetBlocked // M7`（取函数值未调用，blocked 计数器疑似不会自增）——属该 WP 未完成工作，提请协调者转告，非本轮被审内容。

## 结论

R37 交付 9 项中 8 项完全成立、1 项部分成立（CI 接线本体真实但触发面未覆盖被测输入），无虚报；两处核心承重声明（Go 钉测、TS 负控制）经独立变异重放双向坐实，P1 修复经修前/修后 pwsh 实证，质量高于本轮复审的准入线。新发现 1×P2（触发面缺口）+ 3×P3（溢出理论面/负余额日志口径/文档计数）+ 1 条并行 WP 在制观测。
