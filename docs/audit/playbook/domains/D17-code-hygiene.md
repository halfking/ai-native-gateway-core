# D17 — 代码卫生与冗余治理

> 领域编号: D17 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1 ｜ 恒查域（每轮必审，轻量）

## 1. 领域边界

**管**：冗余流程与代码的标注/隔离/迁移准备——死代码、零调用接缝、重复实现、误导性注释、遗留 TODO 的诚实化；为后续清理建立"待清清单"。
**不管**：任何功能正确性（各域）；清理动作本身在本域只做"标注或移文件"，删除性重构须单独立项。

## 2. 参考基线

- 历史模式：R30 P3 批（restorePulledCurrency 死代码移除、误导注释更正）、R31（CloseProbe/ProbeCheck 死接缝删除）
- `docs/03-design/01-architecture/domains/DOMAIN_REGISTRY.md` — 领域注册表（归属判断）

## 3. 检查清单

1. **零调用接缝**：窗口内改动暴露出的导出函数/端点/表列若无生产调用方——标注 `// DEAD-CODE(<轮次>): <理由>` 或直接删除（低风险时）。
2. **重复实现**：窗口内新代码与既有工具函数重复（复制粘贴型）——改为复用或在两处标注指向。
3. **注释诚实**：窗口内 diff 中被行为变更波及的旧注释/日志文案同步更正（"exponential 不实"教训）；文档与代码矛盾以代码为准并回改文档。
4. **待清清单**：不适合本轮清理的，登记到轮文档 §遗留（带 file:line），格式与 R30 一致；下轮从这里捞。
5. **文件归位**：明显放错位置的代码（如领域逻辑滞留在 transport 层）移到正确文件并在 CHANGELOG 提及，不做大开大合重构。

## 4. 历史回归点（轮末回注区）

- [R30] restorePulledCurrency 死代码移除 + 注释如实化；709 down 注释错字；freediscovery README 与代码矛盾回改 —— 模式基准
- [R31] Manager.ProbeCheck/CloseProbe 零调用死接缝删除（防 billing-blind 复活）—— 模式基准
- [R37] 测试自身可成为跨测试毒源：TestOutcomeBackfill_RespectsWriteBound 占坑 goroutine 不释放槽位→32 槽永久泄漏→全包后续反馈写静默 shed（dropped 恒定+4、新测试超时假失败）——占资源类测试必须对称释放；恒真测试（TestDefaultDispatchFollowUpHitsLiveServer 手工造请求自证）与命名误导（stub 冒充 production dispatcher）已改写；gofmt 存量 272 文件待按包机械批单独 commit

## 5. 子代理派发提示词

```text
你是 D17（代码卫生与冗余治理）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D17-code-hygiene.md 全文。
第二步：以窗口改动面为主扫描死代码/重复实现/误导注释/零调用接缝，按域文档 §3 清单核对。审计窗口：<窗口>。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line；建议只到"标注/迁移"粒度。
```

### R39 回注（2026-09-17）
- **P0 抓获：main 分支网关不可编译**（cd76a7f0e 漏 import "net"，merge 入主后无人跑 build 门）。教训：直推 main 的提交，push 前至少 `go build ./cmd/gateway/`（仓库最大包，import 面最易漂移）。
- gofmt 债随修复顺带清（R39 一并清 5 文件）；新导出符号若属分阶段特性，函数头补 `RESERVED(<特性>): ... 未接线` 标注（DiscoverGateways/IsRunning/Status 已标），防后续轮重复怀疑死代码。
- go.mod 直接 import 的依赖勿留 // indirect（go mod tidy 归位 + go mod vendor 零 churn 验证）。

### R42 回注（2026-09-18，注释诚实化批量清账 + 双胞胎教训）
- 六处注释漂移一次清（node_probe 头注 6h 封顶/无 pause、SetModelQualityTrigger attempt>=2、deescalate 过时括注、rls.go policy 形状、720 "8 tables"、shadow_actors 第二写入口）——R40 一轮之内产生四处新漂移，**注释描述"另一分支/另一机制"时必须当场对照实现**。
- failover_policy KindClientBug 重复 case 已合并；counter Help 补合成轮 abandon 口径。
- **双胞胎提交教训**：并行会话同题同补丁双落（e9d46b37e/6276a3ff9）靠 merge 去重，掩盖"第二个提交没人真正看"——push 前 `git log --since='5 minutes'` 自查（conventions §7 候选）。

### R43 回注（2026-09-18，双胞胎 merge 符号存活 + RESERVED 惯例）
- 回注/注释引用符号名前先 `git ls-tree`/grep 验证存活（R42 双胞胎 merge 淘汰了 manualBalanceGuardSQL，D08 域文档指引落空一轮）。
- 零消费方的新 API 家族（CachedPlatformBool/String/Float）用 `RESERVED(待首批消费方):` 头注标注（settings/ttl_cache.go 已标）——写端失效接线先于读者存在时尤其要标，防下轮重复怀疑。
- 测试专用导出的注释如实化为 test-only（feature_stats GetLatestStats 原注释"用于测试和监控"的"监控"半句无接缝）。

### R71 回注（2026-09-27，注释漂移三案）
- 注释漂移三案收口：helpers.go 行为契约要点与钉桩测试矛盾（首元素语义写反）、调用站点 V1/V2 描述与事实相反、probe_necessity 占位符 "~xx:35-39" 未写实——新增注释的"契约描述"必须能与同文件测试互相印证。

### R74 回注（2026-09-28）
- **cmd/gateway plugin 族 500 传播链未甄别**（pluginLifecycleError msg / plugin_installer_init.go:53 / main_v2_pipeline.go:397 是否达 HTTP 响应面）——最后一块 500 回显存量，收口前必须逐链追到 handler 出口，勿按 grep 计数盲改。
- gofmt 存量漂移新增两处：licensing/crypto.go、licensing/activation_codes_test.go（并入 R69 §四 gofmt 全仓债独立批次）。
- message↔diff 门新形态：`5fe87cadc` 式"打包标记提交"——宣称 feat 三层修复实改 1 行，内容散落紧邻合并分支。对账时看内容是否真实存在于窗口内（区别于 R66 凭空虚构），但失配本身必须轮文档点名。

### R78 回注 · 审计工具自身的卫生：14/17 域的验收门是真空门

D17 域知识「代码卫生」含注释/文档漂移。本轮把同一把尺子量到**审计工具自己**上，结果
是 D17 自己的定义被违反：

**事实（HEAD `7174f3ab5` 实测）**。每个域的 `plan.md` §6 验收门都写成
`go test -race -timeout Ns ./tests/48h-audit/D<NN>-<name>/...`。Go 的包模式匹配不到
任何包时**不是错误**——`go test` 打印 `matched no packages` 警告、**退出码 0**。于是：

| 状态 | 域 | 数量 |
|---|---|---|
| 门能实跑（含真实测试包） | D01 / D02 / D14 | 3 |
| 门匹配 0 包，**永不可能失败** | D03–D10 / D12 / D13 / D15–D17 | 14 |

其中 **12 个域的 `plan.md` 里有 `[x]` 勾选项**却域内无任何测试包。

**关键区分：勾选项不一定在撒谎，撒谎的是门。** D03 的 B-01 写
`go test -race ./cmd/gateway -run 'TestLiteRequestLogSink_'`——那个测试**真的存在并通过**，
只是不在域目录里。D05 的 B-01 指向 `./domains/streaming/executors`，同样真实。**证据是真的，
但验收门没有接到证据上**，于是「门绿」不承载任何信息。

**危害高于「没有门」**：不跑的人（包括后续轮次）会把绿色的门读成证据。R43 起的五轮
挂账与 R73 的「无证据勾选」撤销，都是这个模式的下游后果。

**已加守卫**（`tests/48h-audit/gates/vacuous_gate_test.go`，3 条）：
- `TestNoNewVacuousAcceptanceGate`：`plan.md` 自指验收门的域，若 `./tests/48h-audit/<域>/...`
  背后没有 Go 包（**递归含子目录**，`...` 本就含子目录——D01/D02/D14 的测试在
  `business/` `safety/` `stress/` 里，只看顶层会误判）且不在白名单 → 失败。
- `TestVacuousGateAllowlistIsMinimal`：白名单里已有包的域 → 失败，防止过期豁免变成墓碑。
- `TestVacuousGateDebtIsReported`：每次运行打印 13 条未清债务。沉默的白名单正是下一轮
  重复错误的方式。

**变异检验**：伪造空门域 `D99-fake-empty` → 守卫红并点名；把已收口的 D01 塞进白名单 →
`TestVacuousGateAllowlistIsMinimal` 红并点名。两次恢复后全绿。

**白名单为什么存在**：不加白名单仓库立刻红、守卫永远合不进去，而**长期红的门没人看**
（与 R77 选型层阈值棘轮同一推理）。债务显式打印 + 白名单自收缩，比「一次性要求 14 个域
全补齐」更能真正落地。

**仍未完成（真活）**：13 个域补真实测试包，或把门改指到证据真实所在的包。守卫只保证
**不再变多**，不代替逐域审计。
