==============================================================================
L10 复核轮证据——README 路由口径清单 ↔ 当前树 对拍
生成时间：2026-11-23 10:11:46 CST
树：/tmp/lgw-l9l10（隔离 worktree）HEAD=9caace070 + 本轮未提交改动
==============================================================================

── ① 两点基准自证（工具口径 ≡ README 口径）────────────────────────────────

★ 基准 A：bd8d39757（L10 首轮之前 §1.1 的更新提交）
  path=125 external=5 runtime=130 pureRedirect=19 dynamic=19 glob=2 public=17 auth=113 | tiers 53/47/11/2
  README 当时公布值：125/5/130/19/19/2/17/113 | 53/47/11/2  ⇒ 逐项相等

★ 基准 B：8e0baf950（§1.1 上一次更新所在提交）
  path=129 external=5 runtime=134 pureRedirect=20 dynamic=22 glob=2 public=17 auth=117 | tiers 54/50/11/2
  README 上一版公布值：129/5/134/20/22/2/17/117 | 54/50/11/2  ⇒ 逐项相等

★ 当前树
  path=131 external=5 runtime=136 pureRedirect=20 dynamic=23 glob=2 public=17 auth=119 | tiers 56/50/11/2
  未覆盖=20 已覆盖=116 README§3登记=115

── ② §1.2 行号取证（手抄错误，非树漂移）─────────────────────────────────────
★ 逐提交扫描 router.ts 历史：/catalog 行号 与 catch-all 行号
  d82a9fb06  catalog=220  catchall=374
  16a75be1f  catalog=258  catchall=435
  822c9cc78  catalog=262  catchall=442
  41fb572f3  catalog=270  catchall=467
  ★ README 旧值：catalog=220 catchall=435 ⇒ 两者【不在同一提交】⇒ 当初就抄错

── ③ 三道对拍（全部绿）────────────────────────────────────────────────────

★ AST 口径：node maestro/tools/audit-route-coverage.mjs --check
# README 路由口径 ↔ 当前树 对拍（--check）

| 判据 | README 声明 | 树实测 | |
|---|---|---|---|
| §1.1 path 条目 | 131 | 131 | ✔ |
| §1.1 externalMaintainRedirect 调用 | 5 | 5 | ✔ |
| §1.1 运行时路由总数 | 136 | 136 | ✔ |
| §1.1 纯 redirect | 20 | 20 | ✔ |
| §1.1 带命名动态段 | 23 | 23 | ✔ |
| §1.1 glob 兜底 | 2 | 2 | ✔ |
| §1.1 不需要登录 | 17 | 17 | ✔ |
| §1.1 需要登录 | 119 | 119 | ✔ |
| §1.1 权限档 anyUser | 56 | 56 | ✔ |
| §1.1 权限档 super | 50 | 50 | ✔ |
| §1.1 权限档 platformOps | 11 | 11 | ✔ |
| §1.1 权限档 providerConsole | 2 | 2 | ✔ |
| §1.2 标题条数 = 纯 redirect 实测 | 20 | 20 | ✔ |
| §1.2 表内行数 = 标题条数 | 20 | 20 | ✔ |
| §1.3 标题条数 = 动态段实测 | 23 | 23 | ✔ |
| §1.3 表内行数 = 标题条数 | 23 | 23 | ✔ |
| §3.13 标题条数 = 未覆盖实测 | 20 | 20 | ✔ |
| §3.13 表内路径数 = 标题条数 | 20 | 20 | ✔ |

RESULT: PASS（18 / 18 条一致）
  EXIT=0

★ 零依赖口径：python3 maestro/tools/audit-readme-route-stats.py
# maestro/README 路由口径 ↔ 当前树 对拍（L10 复核轮）
README：maestro/README.md；router.ts：web/src/router.ts

✅ D1 §1.1 权限档求和 == 需要登录  — 权限档求和 119 vs 需要登录 119
✅ D2 §1.1 运行时总数 == 需要登录 + 不需要登录  — 136 vs 119 + 17
✅ D3 §1.2 标题条数 == 表内行数  — 标题 20 vs 表内 20 行
✅ D4 §1.3 标题条数 == 表内行数  — 标题 23 vs 表内 23 行
✅ D5 §3.13 标题条数 == 表内路径数  — 标题 20 vs 表内 20 条路径
✅ D6 README 登记的路由都存在于 router.ts（无幽灵路由）  — 135 条全部存在
✅ D7 router.ts 的路由都被 README 提及（无未登记路由）  — 136 条全部登记

RESULT: PASS（7 / 7 条一致）
  EXIT=0

★ CI 门：bash scripts/checks/maestro-readme-route-stats-gate.sh
[maestro-readme-stats] ok: 对拍工具判定 README 路由口径清单与当前树一致
[maestro-readme-stats] ok: 对照臂 A：幽灵路由存在时工具判红（退出码 1）
[maestro-readme-stats] ok: 对照臂 A：红在幽灵路由上（判据 D6 精确命中，不是随便红）
[maestro-readme-stats] ok: 对照臂 B：树里有未登记路由时工具判红（退出码 1）
[maestro-readme-stats] ok: 对照臂 B：红在未登记路由上（判据 D7 精确命中，不是随便红）
[maestro-readme-stats] ok: 对照臂 C：半回填时工具判红（退出码 1）
[maestro-readme-stats] ok: 对照臂 C：红在 D3（标题条数 vs 表内行数）上（判据精确命中）

[maestro-readme-stats] pass=7 fail=0
  EXIT=0

── ④ 变异自证（6 臂，逐一精确报红）────────────────────────────────────────
── M1 §1.1 path 条目 131→130 ⇒ EXIT=1
     | §1.1 path 条目 | 130 | 131 | ❌ |
     RESULT: FAIL（17 / 18 条一致）
── M2 §3.13 标题 20→18 ⇒ EXIT=1
     | §3.13 标题条数 = 未覆盖实测 | 18 | 20 | ❌ |
     | §3.13 表内路径数 = 标题条数 | 20 | 18 | ❌ |
     RESULT: FAIL（16 / 18 条一致）
── M3 §1.2 表删 1 行 ⇒ EXIT=1
     | §1.2 表内行数 = 标题条数 | 19 | 20 | ❌ |
     RESULT: FAIL（17 / 18 条一致）
── M4 §1.1 需要登录 119→117 ⇒ EXIT=1
     | §1.1 需要登录 | 117 | 119 | ❌ |
     RESULT: FAIL（17 / 18 条一致）
── M5 §1.3 标题 23→22 ⇒ EXIT=1
     | §1.3 标题条数 = 动态段实测 | 22 | 23 | ❌ |
     | §1.3 表内行数 = 标题条数 | 23 | 22 | ❌ |
     RESULT: FAIL（16 / 18 条一致）
── M6 树退回（--router 指向 bd8d39757 的 router.ts）、README 不退 ⇒ EXIT=1
     | §1.1 path 条目 | 131 | 125 | ❌ |
     | §1.1 运行时路由总数 | 136 | 130 | ❌ |
     | §1.1 纯 redirect | 20 | 19 | ❌ |
     | §1.1 带命名动态段 | 23 | 19 | ❌ |
     | §1.1 需要登录 | 119 | 113 | ❌ |
     | §1.1 权限档 anyUser | 56 | 53 | ❌ |
     | §1.1 权限档 super | 50 | 47 | ❌ |
     | §1.2 标题条数 = 纯 redirect 实测 | 20 | 19 | ❌ |
     | §1.3 标题条数 = 动态段实测 | 23 | 19 | ❌ |
     | §3.13 标题条数 = 未覆盖实测 | 20 | 15 | ❌ |
     RESULT: FAIL（8 / 18 条一致）

★ 恢复后复跑：
     RESULT: PASS（18 / 18 条一致）
     RESULT: PASS（7 / 7 条一致）
     

── ⑤ 本轮引入的门 ⇒ 自动进了 pipefail 竞态台账（已逐条裁决）────────────────
★ 新门脚本 scripts/checks/maestro-readme-route-stats-gate.sh 的 3 处 printf|grep -q
  ⇒ 由 scripts/audit/measure-pipefail-race-surface.py 静态判据自动纳入候选集
  ⇒ 已逐条人工裁决（verdict=benign-if-bounded；写端  实测 1,536 B，5/5 稳定）
148:scripts/checks/maestro-readme-route-stats-gate.sh	93	1	<top>	grep-q	judgment	unknown	if printf '%s' "$arm_out"	benig
149:scripts/checks/maestro-readme-route-stats-gate.sh	124	1	<top>	grep-q	judgment	unknown	if printf '%s' "$arm_out"	beni
150:scripts/checks/maestro-readme-route-stats-gate.sh	151	1	<top>	grep-q	judgment	unknown	if printf '%s' "$arm_out"	beni
  ⇒ 重跑量具：沿用人工裁决 470 / 新增 0 / 待裁决 0 ⇒ 裁决可保持

── ⑥ 既有红归因（基线树实测，非本轮引入）────────────────────────────────
★ ursmcheck 整包在【origin/main 基线树】（worktree /tmp/lgw-om，detached 9caace070）上同样红：
    --- FAIL: TestInstallerReachabilityLedgerIsRecomputable (0.33s)
        installer_reachability_gate_test.go:144: reachability ledger has 941 rows, expected set has 959
    --- FAIL: TestInstallerReachabilityLedgerPinsItsInputs (0.00s)
        installer_reachability_gate_test.go:331: ★ reachability ledger was generated against a manifest of 387 files, the installer manifest now has 391
        installer_reachability_gate_test.go:336: ★ reachability ledger was generated against a distribution package of 387 migrations, the package now has 395
  ⇒ 与本轮读数【逐字相同】⇒ 既有红（上游 installer/migrations 漂移）⇒ 本轮只登记、不代改
  ★ 本轮改动面：maestro/ 4 文件 + scripts/checks/ 1 新 + .workflow/main-verify.yml 1 改 + 文档 2 ⇒ 零 installer 文件
