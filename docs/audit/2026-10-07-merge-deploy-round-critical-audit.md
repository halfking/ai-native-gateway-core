# 2026-10-07 合并部署轮批判式审计（两项根修 + 一处竞品修消重）

审计对象：本会话当日两轮「feat→main 合并+双端部署」（双点 73ef0bed0 → 35cd7bc99 → FF 同步 4933733cb；本地 2487/2489、245 2488/2490）+ 批次间隙同步轮。
审计方法：对每项「已声称完成」逐条复核证据，不采信自述；发现问题的修复一律先做可控实证再入码。

## 结论一览

| # | 发现 | 定级 | 处置 |
|---|------|------|------|
| F1 | 合并轮门禁是手选子集，统一门 verify.sh 一项没跑 | 高（流程） | 最终树全量补跑 `./verify.sh --web`（§3） |
| F2 | deploy-245/154 在临时 worktree 必断链，两轮各手工修一次 | 中（重复仪式/易漏） | **根修**：deploy-lib-resolve.sh 三级解析 + 4 场景自测门（cc3cba679） |
| F3 | 迁移通道存量红：837-840 未登记升级通道 | 高（main 存量，非合并引入） | 本轮补登（bfc22da00）；**main 侧并发会话同日以更强证据独立修复（0b53b7a0f，覆盖到 841），合并时取对方版本消重** |
| F4 | web 桌面端门缩水：只跑裸 vite build，漏 vue-tsc/ui-audit/vitest | 中 | 并入 F1 补跑（§3） |
| F5 | 审计过程自身两坑：管道吃退出码、git cherry 对 merge-only 分支恒空 | 低（方法论） | 如实记录（§5），前者已改文件重定向+显式退出码 |

## F1 合并轮门禁缩水

**声称**：门禁全绿。**实际**：go build + 迁移撞号单测 + web-mobile build 链 + vitest + gate:selftest + web 裸 vite build——本仓统一门 `./verify.sh`（go test ./... 全量、go vet、build-tag 编译矩阵、迁移 checksum、web 双端完整链、冲突标记扫描）一项未跑。

**风险敞口量化**：本轮部署树相对上一已部署双点（73ef0bed0）的 Go 侧增量 = 21 文件 1271 行（admin 租户越权 P1 修复、bg/partition_manager、db/db.go、middleware/auth_mw.go、迁移 840/841 线），编译级验证 ≠ 行为级验证。

**减轻因素（如实记录）**：feat 90-94 批纯 web-mobile TS（零 Go 改动），Go 树与 origin/main 原树逐字节一致；245 部署后 healthz/readyz/background-tasks/凭据解密冒烟全绿；ssh 实证 build_seq/sha/active-port/资产指纹（245 与本地 dist 的 `/m/` 主资源同为 `index-rctxPJAB.js`，移动批 90-94 确已上线）。

**整改**：§3 在最终合并树全量补跑。**流程建议**：合并轮标准门从「手选清单」改为 `./verify.sh --web`，本文件即为依据。

## F2 deploy-245/154 临时 worktree 软链断链（根修 cc3cba679）

- **现象**：两轮合并部署都需先手工把 `scripts/deploy-lib` 软链改成绝对路径才能跑通 deploy-245.sh；适配不可提交、每轮重做。
- **根因**：`deploy-245.sh:32` / `deploy-154.sh:37` 绕过 SSOT 解析直 `source "$SCRIPT_DIR/deploy-lib/parse-wrapper-flags.sh"`；软链是相对路径（`../../../../ai-native-tools/deploy-lib`），仅在 canonical checkout 深度可解析。`_shared-lib.sh:8` 早有 `AIAN_DEPLOY_LIB` env→`$HOME/workspace/...` 默认解析（deploy-local / deploy-seamless 因此幸存），两个旧入口是仅剩的漏网点。
- **修**：新增 `scripts/deploy-lib-resolve.sh` 三级解析（env 预置 → 软链可解析 → `$HOME` 默认，与 _shared-lib 同序，坏 SSOT exit 64）；两入口改经解析路径 source；新增 `scripts/.verify-deploy-lib-resolution.sh` 行为门。
- **实证**：自测 4/4（env 优先/软链命中/断链回落/坏 env 64）；`--dry-run` 在健康态与人为断链态下 245/154 双入口 4/4 通过；软链保持提交态相对路径，零提交内绝对路径。
- **竞品核查**：origin/main 至审计时点（482f797d9）无人修此问题——本修为独有价值。

## F3 迁移通道存量红（main 侧引入；本轮补登后与竞品修消重）

- **现象**：verify.sh 第二步 pre-commit 契约即红：837/838/839/840「有 installer 腿但无升级通道」+ 840 最高编号守卫（本树读数 5 条；main 侧 18:37 读数 6 条，含 841）。
- **归属核查**：`scripts/apply-db-revision-sequence.sh` 与 origin/main 逐字节一致（diff 为空）——**不是合并丢失，是 main 侧 4df816006（837）/68d10ffaa（840）等提交落库时未登记**。整树门（verify.sh）才暴露，staged 域裁剪的提交钩子未拦住（无法回溯当时是否 --no-verify）。
- **本轮裁决（bfc22da00）**：四条按正常升级通道登记 `files=()`；补登揭出第二层——838/839 同体重定义 `analyze_llm_gateway_table_stats` 触发 clobber guard（deploy exit 5），按 572→563→661 先例登记 `intentional_function_chains`（838→839，839 为终态体，runbook §10.106.12.1）。
- **关键机理澄清**（审计中实证）：deploy 前扫描腿按**目录+schema_migrations 台账**独立投递（deploy-lib/db-changelog.sh:241），通道登记只是元数据、不改投递行为；245 共享 PG 上 837-839 早已 applied（竞品修的生产侧证据：838 库函数体 2142B、839-B 含 opts_sql_base 与 0.005），故 2490 部署只实投了缺台账行的 840。本地 dev 库台账另有历史（830-842 区间仅 831），与共享 PG 不可混读。
- **消重**：main 侧并发会话 18:46 以同方案+更强证据独立修复（0b53b7a0f，837-**841** 五迁移六红）。合并 56b900272 时该文件冲突**取对方版本**（超集），bfc22da00 保留在历史中、内容被取代。
- **结果**：契约门 standalone exit 0；`go test ./sql/migrations/startup/` 全过；Go 侧无镜像钉桩需同步（grep 证据，830 式双侧钉仅适用于 channel_gap 豁免类）。

## F4 web 桌面端门缩水

web 完整 build = export-menu-config + element-import-audit + ui-audit --strict + vue-tsc + vite build，且有独立 vitest（window-class / mobileListUx 等规格测试）；合并轮只跑裸 `vite build`。并入 F1 在最终树补跑（§3）。

## §3 补跑结果

### §3.1 首跑（树 2ad7b6282，日志 /tmp/verify-web-final-1007.log，VERIFY_EXIT=1）

前置各步全绿：pre-commit 契约 PASS=5 FAIL=0（F3 修复生效）、迁移通道契约 passed、checksums OK（180 registered / 678 unregistered warn-only）。`go test ./...` 抓到 **4 个失败包**，逐一定性：

| 失败 | 定性 | 证据 | 处置 |
|------|------|------|------|
| admin `TestV1BodiesReadersAreAssessed` | **设计性棘轮红**（测试自述「【预期红】本门故意红：bodies 腿尚未逐点评估」） | request_logs_bodies_retirement_gate_test.go:237；与 memory bodies 退役规划档案互证 | **不修**（修=拆拦截止损，属 owner 决策）；登记为 verify.sh 的已知常红项 |
| cmd/tools/sql_source_indirection_audit `TestDocumentedSnapshotMatchesMeasurement` | 快照漂移：拼接点 72→73、v1 臂视图 26→27（方向=变大=「把看不见变成看得见」，门规允许直接改数字） | manifest_test.go:998 | **已修**：头部快照 + documentedSnapshot 两处同步（67141ca69），单测复跑 ok |
| sql/schema `TestIntegrationTaggedTreeCompiles` | **integration 标签树真编译断裂**：`fallbackBoardSummary` / `queryBoardCreditsExcludingProbes` 双返回值签名漂移，integration 测试两处调用点未跟 | vet: admin/dashboard_board_probe_sql_integration_test.go:168/:197 | **已修**：两处调用点适配（67141ca69）；`go vet -tags=integration ./admin/` exit 0；全树编译门复跑 ok（6.1s） |
| tests/48h-audit/D01-ir-lifecycle/stress `TestStress_IRSerialize_Concurrency50_P99` | **满载毛刺**：全量并行跑时 p99=24.8ms 超阈，p50=5.1µs/p95=14.7µs 正常；单跑复测 ok（0.55s） | serialize_p99_test.go:70-74 | 定性为负载敏感 flake，不改阈值（是否加余量属 owner 决策），登记遗留 |

**归属核查**：`git diff origin/main HEAD -- '*.go'` 为空——四项全部是 **origin/main 存量**，本会话两轮合并零 Go 改动、如实继承；最小合并门（go build 级）对②③结构性不可见，这正是 F1 的实证。

### §3.2 复跑（树 67141ca69，日志 /tmp/verify-web-final2-1007.log，VERIFY_EXIT=1）

- §3.1 的②③修复生效（两包转绿）；④本次通过（非确定性再证实）。
- **新增一例同类满载 P99**：domains/nodestatecache `TestSelectP99Under1ms` 满载红、单跑 ok（0.84s）——首跑它是过的，与 D01-stress 同类（§6.6 升级为两例）。verify.sh 因 `go test ./...` 未全绿在 set -e 下中止，**web 各节与 govulncheck 再次未跑到**。
- **web 各节手工等价执行（最终树，全部 exit 0）**：web lockfile / vue-tsc / vitest **2395/2395** / responsive:check / element:check（273 文件）/ color:check（40 违规均在 40 条基线内，无新增）/ 完整 build；web-mobile vitest **6548/6548**（含 95-98 批）/ gate:selftest；`./scripts/govulncheck.sh` 绿；冲突标记扫描零命中。
- **go test ./... 终态**：全绿除两项已登记——admin bodies 棘轮（设计性常红，§6.5）+ nodestatecache P99（满载 flake，§6.6）。

**结论**：verify.sh --web 在最终树上「除已登记常红外全绿」（§6.5 判据）；合并轮门禁缺口（F1/F4）就此收口，后续合并轮建议直接以 `./verify.sh --web` + §6.5 判据为标准门。

## §5 审计过程自身的坑（如实记录）

1. 第一次后台门禁用 `| tail -80` 收尾 → 管道退出码被 tail 吃掉，「completed exit 0」通知不可信，实际 verify 在第二步契约就中止了。复审发现后改为 `> file 2>&1; rc=$?; echo VERIFY_EXIT=$rc >> file`。
2. `git cherry` 对纯 merge 分支恒输出空（cherry 跳过 merge 提交），不能当「内容已吸收」的证据。tmp/merge-main-1013 删除前的实际核验=非 merge 提交可达性 + merge 双亲祖继检查。
3. 批判式审计本身也会踩并发：C2 与 main 侧 0b53b7a0f 竞争同一修复面。发现即消重（取对方超集），不因「我已修过」而护短。

## §6 遗留债登记（不在本轮扩修）

1. `deploy.sh` / `deploy-252-gateway.sh` / `local-host-blue-green.sh` 及 tests/ 5 文件同款直连软链（`grep -rF 'source "$SCRIPT_DIR/deploy-lib/' scripts/`）——只在 canonical checkout 运行、未翻车；修法照 F2（换 deploy-lib-resolve.sh），下一轮顺手清。
2. 契约测试 required-sequence 清单止于 800（801-836 未入清单）——与 F3 同族的滞后债。
3. LEGACY 13 个未定义 token（theme.spec.ts）——既定令牌化批次清偿。
4. 245 handoff 14.6s/18.6s>2s 警告——非阻塞设计行为（旧实例保留回滚），实测两次部署后新实例 200/零 error 日志。
5. **verify.sh 的「全绿」定义问题**：`go test ./...` 含设计性棘轮红（§3.1 第一行），verify.sh 在 bodies 退役评估完成前结构性无法 exit 0。若采纳 verify.sh 为合并轮标准门，判据须为「除已登记常红外全绿」，常红清单见 §3.1。
6. P99 应力测试满载敏感性（§3.1 第四行）——单跑稳定、满载偶发；阈值余量或资源隔离属 owner 决策。
7. 移动端 95-98 批与 main 侧基准价判据（482f797d9）已随本轮推送上 main，**尚未部署**——留下一轮常规双端部署。

## §7 本轮提交

| commit | 内容 |
|--------|------|
| cc3cba679 | F2 根修：deploy-lib-resolve.sh + 两入口改造 + 4 场景自测门 |
| bfc22da00 | F3 补登（837-840 + 函数链；后被 0b53b7a0f 超集取代，合并取对方） |
| 56b900272 | merge origin/main（通道竞品修 0b53b7a0f + 基准价判据 + runbook） |
| 2ad7b6282 | merge origin/feat（移动端 95-98 批，纯新增 API 层） |
| 67141ca69 | §3.1 首跑两红收口：快照同步 + integration 标签树编译断裂根修 |
| 本文档 | docs/audit/2026-10-07-merge-deploy-round-critical-audit.md |
