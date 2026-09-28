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

### R78 续 · 守卫自身经历 4 轮自伤，以及它靠什么暴露

上一条加的守卫第一次跑就是绿的，**但它当时什么都没在测**。四个缺陷全部由变异检验
暴露，没有一个是我读代码看出来的：

1. **正则退化**：`\\./[A-Za-z0-9._/-]*?/{0,3}\\.{0,3}\\b` 的非贪婪量词只匹配到 `./`
   前缀，`packagePatterns` 返回垃圾 → `len(patterns)==0` → 守卫**静默跳过每一个域**并
   退出 0。暴露方式：把 D03 门改成指向不存在的包，守卫没红。
2. **路径基准少算一层**：测试 CWD 是 `tests/48h-audit/gates`，仓库根在**三层**之上，
   我用了两层 → `../../tests/...` 解析到 `<repo>/tests/tests/...` → 好域被判成空门。
   修法：引入 `repoRoot` 常量并加 `TestRepoRootIsCorrect` 用 go.mod 存在性自校验——
   **路径常量必须自验证**，否则错了也是静默的。
3. **扫描全文而非门小节**：plan 的子代理派发提示词里有 `./cmd/gateway` 这类路径，
   全文扫描时它能喂饱「门能解析」检查 → 门本身写成什么都不跑也算通过。
   修法：只解析「验收门」小节。
4. **小节定位被自己劫持**：我在 D03 状态行写的「R78 验收门已改指真实证据所在包」里
   含「验收门」三字且**早于**真正的 `## 6. 验收门` → 子串搜索从状态行起步，下一行
   就撞上 `## 1. 审计要点`，截出空小节，D03 被当成没门而跳过。
   修法：要求该行**本身就是** markdown 标题（`#` 开头）且含「验收门」。
   附带：小节终止条件一度写成「任何以 `#` 开头的行」，而门里的 bash 注释正以 `#` 开头，
   于是小节在第一条注释处被截断——**`#` 在 bash 里是注释，在 markdown 里是标题**，
   两种语法共用一个前缀。

**最终变异检验 5/5 全部被抓住**：D03 门改指不存在包 / D03 退回域内空目录（即 R78 原始
缺陷形态）/ 已修域滞留白名单 / 伪造新空门域 / D05 门改指不存在包。

**教训 H**：**「守卫全绿」不等于「守卫在工作」。** 写完守卫必须做变异检验——故意制造它
该抓的缺陷，确认它真的红。本轮 4 个缺陷里，正则退化与路径错位都是「永远绿」型，
在真实使用中不会暴露任何症状，只会让人误以为审计基础设施是健康的。**对「检测缺失的
检测器」尤其如此：它失败时是绿的。**

### R78 续二 · 13 个域全部收口，真空门 14 → 0；以及「散文路径」这个第五个缺陷

**收口结果**：13 个域的门全部改指到真实证据所在包，12 条新门**逐条实跑 exit 0**
（只验证「能解析」是不够的——门指向一个存在但测试失败的包时守卫同样会放行）。
`knownVacuousGates` 白名单已清空。证据分布本身就说明了问题：这些域的勾选项引用的测试
**几乎全部真实存在于仓内别处**，旧门只是没接到它们：

| 域 | 证据包 |
|---|---|
| D03 / D06 / D10 | `./cmd/gateway` lite telemetry sink + `./domains/hooks/compression` 9 条 alignment |
| D04 / D09 / D13 | `./bg` selfcheck 限流分类与中止 + `./ratelimit` race |
| D07 / D17 | `./bg` 归档 cadence 五条（`TestShouldRunRequestLogsArchive_*` / `TestArchiveOldRequestLogs_*`）——即 migration 754 的落地路径 |
| D08 | `./domains/streaming/executors` Ollama 4xx body-aware 与 fail-closed |
| D12 | `./proxy` 健康探测与负载均衡（域知识 §2 代码入口就是 proxy/） |
| D15 | `./telemetry` + `./metrics` |
| D16 | `./internal/outbox` + `./internal/orchestration` + `./domains/hostedtask`（域知识 §2 代码入口） |

**第五个缺陷：散文路径**。前四个是解析层的问题，这个是语义层的——门小节里我写的说明
文字「证据在 ./proxy 的健康探测回归」含一个真实可解析路径，`packagePatterns` 把它当成
门的证据，于是**把 D12 的门改指到不存在的包，守卫依然报绿**。D15、D16 同理（都因为
一行说明里列了多个真实路径）。修法：只认以 `go ` 开头的命令行。

这个缺陷的暴露方式同样值得记：我用 python 做替换时**没加断言**，脚本打印了「已收紧」
但替换其实没匹配上，于是又跑了两轮变异才发现。**静默 no-op 的批量编辑比失败的批量
编辑更危险**——失败的会报错，静默的会让你以为改好了。

**最终变异检验 12/12 全部被抓住**（把 12 个域门里的包路径整体改指到不存在的目录），
外加伪造新空门域、已修域滞留白名单两项。

### R78 续三 · 死契约列扫描扩到 credentials：10 个列零消费方，按「是否主动误导」定级

`tests/48h-audit/scripts/dead-column-scan.sh` 扩到 `credentials`（23 列），并用 ripgrep
对前 15 列做独立复核（脚本单次全表扫较慢，ripgrep 更快且结论一致）。

**必须区分两类死契约，否则会把噪声和缺陷混为一谈**：

**A 类 · 主动误导（有非默认值 + 无消费方 = 行为与标记不符）**

| 列 | 表 | 非默认值 | 后果 |
|---|---|---|---|
| `egress_profile` | providers | **14 行 = `'proxy'`** | 标记走代理、实际直连（D12 已登记） |
| `probe_failure_threshold` | credentials | 83 行 = 3、3 行 = 2 | 逐凭据的探针失败阈值**不被尊重**——自检熔断按全局固定阈值跑，运维调这个值没有任何效果 |
| `network_quality_score` | providers | 59 行 = 1.0、1 行 = 0.0 | 每供应商的网络质量分无人参与选型 |

**B 类 · 契约死但当前惰性（值全是默认/空 = 今天没有行为差异）**

| 列 | 表 | 现值 | 备注 |
|---|---|---|---|
| `discount_rate` | providers | 60 行全 1.0 | **成本路径**：一设折扣账单就不变 |
| `pricing_distrust` | credentials | 86 行全 `false` | schema 基线里的 `NOT NULL DEFAULT false` 列（`deploy/sql/schemas/baseline/01-schema.sql:6834`），全仓零引用 |
| `user_overrides_json` | providers | 60 行全 `[]` | 逐供应商覆盖配置不生效 |
| `plan_consumed_json` | credentials | 86 行全 `{}` | — |
| `free_quota_limit` / `free_quota_window_type` | credentials | 0 行 | 仅 `db/db_omnifree.go` 建列，从未读 |
| `relay_overhead_ms` | credentials | 0 行 | 连 admin 侧都无引用 |
| `catalog_version_at_create` / `proxy_subscription_id` | providers | 0 行 | 未启用的占位列 |

**B 类不是「无害」**——它们是**等着被踩的坑**：默认值恰好让后果不可见，一旦运维在管理台
改了值就会静默失效。`pricing_distrust` 尤其值得记：它是 schema 基线里的 `NOT NULL`
列、按设计就该影响定价，却**全仓零引用**，且没有任何换名实现（`grep -i distrust` 只
命中 `router.go` 一句无关注释）。

**误判排除**（与 providers 那轮同一套）：

- **CamelCase 复验**：snake 与 Camel 两种形式都查，`PricingDistrust` / `RelayOverheadMs` /
  `ProbeFailureThreshold` / `PlanConsumedJSON` 均 0 命中。
- **换名实现排查**：`pricing_distrust` 特别查了 `distrust|untrusted.?price|price.?adjust|
  effective.?price` 等同义命名，无等价实现。
- **schema 层确认**：`pricing_distrust` 不在任何 `db/*.go` DDL 里，只在
  `sql/schema/01-schema.sql`、`sql/objects/tables/credentials.sql`、
  `deploy/sql/schemas/baseline/01-schema.sql` 三处 schema 基线中——说明它由 SQL 迁移
  引入并进入了**对照基线**，属于「按设计就该有」而非遗留垃圾。

**工具本身的可用性核验**：本机 `/usr/bin/env bash` 为 5.3.9，脚本用的 `${seg^}` 驼峰
转换正常。（中途我有一条临时 `bash -c` 命令走了 `/bin/bash` 3.2 而报 `bad substitution`
——那是临时命令的问题，**已提交的脚本本身可用**。若需在 bash 3.2 环境运行，应把
`to_camel` 换成 sed 实现。）

**教训 J**：**「死契约」必须按「是否已有非默认值」分诊**。全量报「10 个列无消费方」
会淹没真正要修的那 3 个；但只报 A 类又会让人以为 B 类无需处理——B 类是定时炸弹。
报告必须同时给出**非空行数**和**取值分布**，让读者能自己分诊。

**本轮工具的教训**：单次全表 grep 在本仓（vendor + 大测试树）要 7 分钟以上，两表
合计超过 15 分钟，不可用于交互式排查。后续应给脚本加 `--exclude-dir` 与列清单缓存，
或直接用 ripgrep（本次复核即如此，快一个数量级）。

### R78 续四 · 配置字段死管道扫描：15 个零读取字段，其中 5 个是「双路径分裂」陷阱

把「grep 调用方才证明被使用」用到 `config/config.go`：82 个带 `env`/`yaml` tag 的字段中
**15 个在 `config/` 之外零引用**。

**这一步的关键是不要停在第一层结论。** 初扫给出「WeChat 集成未实现」「RequestSurvival
特性未实现」，两个都**判错了**——特性都在，只是没走 config。逐条追到 env 名才看清真实
形态：

**形态 1 · 双路径分裂：config 声明的前缀与实现读取的前缀不一致（最危险）**

| config 声明（`config.go:262-267, 633-636, 711`） | 实现实际读取 | 结果 |
|---|---|---|
| `LLM_GATEWAY_WECHAT_CORP_ID` | **`WECHAT_CORP_ID`**（`main_notification.go:85`） | 设了带前缀的 → 集成静默不启动 |
| `LLM_GATEWAY_WECHAT_CORP_SECRET` | **`WECHAT_CORP_SECRET`**（`:86`） | 同上 |
| `LLM_GATEWAY_WECHAT_AGENT_ID` / `_AES_KEY` / `_BASE_URL` | 无任何读取 | 纯死字段 |

Config 结构体的 5 个 `WeChat*` 字段**填了值、进了 struct、在 config 外零读取**；真正
构建通知配置的是 `main_notification.go` 里那条独立的 `os.Getenv` 路径，读的是**无前缀**
的 `WECHAT_*`。

**后果**：运维照 `config.go`（或任何由它生成的文档）配 `LLM_GATEWAY_WECHAT_CORP_ID`，
企业微信通知**静默不启动**——没有任何报错，因为代码只是读不到。反过来
`WECHAT_CORP_SECRET` 生效却从未在 config 里声明。这是本轮唯一一个「文档与环境事实
双向不一致」的项。

**形态 2 · 字段从未被读 ⇒ env 与 yaml 两个绑定同时失效**

`DefaultCred` / `DefaultProvider` / `PoolGracePeriod` / `PythonEndpoint` /
`RequestSurvival{MaxActiveTasksPerTenant,StatusIntervalSeconds,TenantAllowlist}`
——对应 env 在 `config/` 外**零引用**。字段本身没人读，所以它的 `env:` 与 `yaml:`
两个绑定**都**不产生任何效果。

`RequestSurvival` 特别说明：特性本身**是活的**（`main.go:290-293` 读
`cfg.RequestSurvivalEnabled` 并与 `StreamRetryEnabled` 互斥），但上面这 3 个**限流/白名单
开关是死的**——运维想给某租户开白名单或调探测间隔，设了没有任何反应。

**形态 3 · env 生效、config 字段是死管道（不构成缺陷，但会误导读代码的人）**

`CursorHMACSecret`（`CURSOR_HMAC_SECRET`，13 处直读）、`DeployEnv`（`LLM_GATEWAY_ENV`，
7 处）、`IdentitySalt`（`domains/identity/identity.go:19` 直读）。

`IdentitySalt` 一条要特别澄清：我一度把它列为「安全相关的零读取字段」，追到
`domains/identity/identity.go:19` 才发现它**确实生效**——`var identitySalt =
os.Getenv("LLM_GATEWAY_IDENTITY_SALT")`。**config 字段是死的，env 变量是活的。** 这与
`egress_profile`（列本身没人读）是完全不同的性质。

**方法论教训 K**：**扫「配置字段」不能只扫字段名，必须追到 env 名的读取点**，因为
消费方有三条完全不同的路径——① 走 `cfg.X`（字段有引用）；② 绕开 config 直接
`os.Getenv("NAME")`（**字段零引用但 env 生效**）；③ yaml 配置文件（仅结构体路径）。
只做第 ① 层会同时产生**假阴性**（把形态 2/3 的 env 误报为失效）与**假阳性**（把形态 3
误报为特性未实现）。本轮 15 个初筛结果里，有 2 个在第二层就被推翻了。

**与列扫描的对照**：列没有这种「绕开」路径——DB 列只能经 SQL 读取，`SELECT *` 已核为
不存在，所以列扫描的结论可以直接落地。**配置扫描必须多一层验证。**

**未修**：形态 1 需确定 WeChat 配置的单一真源（改 config 声明对齐实现，或改实现读
config）；形态 2 需确定 7 个开关是补实现还是删声明；形态 3 建议把直读 env 回填进 config
以消除双源。三者都属配置契约裁决，审计轮只登记。

### R79 回注 · 动态 SQL 里的列名：「形状核对」这一类门禁在结构上抓不到 42703

**发现**（D07 S-01，2026-09-29，本机 PostgreSQL 17.10 真跑）：迁移 754 的
`archive_request_logs_default()` **自落地起从未成功执行过一次**。首跑即
SQLSTATE 42703：

```
ERROR:  column "session_id" does not exist
HINT:  Perhaps you meant to reference the column "request_logs_2026_08.gw_session_id".
```

candidates CTE 投影的 11 个摘要列里有 **2 个在 request_logs 上不存在**——真名是
`gw_session_id` 与 `upstream_status_code`。仓库基线 `01-schema.sql`（137 列）与线上
4 个真实分区（含 2,125,857 行的那张）逐列核对一致，两处都没有 `session_id` /
`status_code`。**照 session_* 族表的列清单抄的**——`session_turns` 确实有 `session_id`，
迁移 754 的头注也正引自 session-storage 那轮审计。

**为什么六轮审计、十几道门全部放行**。三条独立原因叠加，缺一不可：

1. **函数体是 `format('%I')` + `EXECUTE` 的动态 SQL**，列名要到运行时才解析。
   `CREATE FUNCTION` 阶段不做任何列存在性校验，所以安装成功、台账自登记成功、
   升级通道也全绿。
2. **D-01「migration 754 function/installer/embed/caller 形状核对」是对文本断言**——
   查文件在不在、函数名对不对、调用点接没接。形状全对，内容全错。
3. **整条归档链路从未被任何测试驱动过一次真执行**，线上也没有——所以
   「每日 03:00 扫一次」「statement_timeout 已钉在事务内」这些 R73/R75 的接线结论
   全部属实，但**接线的那根线从第一天起就插在空插座上**。

**方法论教训 L**（这是 R53「钉桩自身的 fallback 分支也是被钉语义」的姊妹条）：

> **静态形状门禁对动态 SQL 是结构性失明的。** 凡是以 `format()`/`EXECUTE`/字符串拼接
> 构造的 SQL，其「引用了什么」只有真跑一次才知道；而真跑一次的门槛通常比写一道静态
> 门更高（要库、要数据、要正确的时点）。于是这类代码天然落在「谁都不跑」的黑洞里。
>
> 可推广的判定：**看到迁移/函数体里有 `format(` + `EXECUTE`，就默认它未被执行验证过**，
> 并要求该文件至少有一条「真库真跑」的门——哪怕数据量小到几千行。门禁分层应当是
> 「静态形状」+「跨源列名交叉校验」+「真库首跑」三道，缺中间那道是本案的直接成因。

**跨源交叉校验是唯一不需要数据库的兜底**：从**基线 DDL** 解析 request_logs 的真实列
集合（`01-schema.sql` 是 SSOT），与迁移体里显式列出的投影列逐一比对。基线一改，守卫
跟着改；迁移一错列名，守卫立刻红。见
`tests/48h-audit/D07-hot-columnar/data/archive_source_columns_test.go`。

**真库首跑门必须自建 schema，不能依赖被测环境现成的表**。本轮
`tests/48h-audit/D07-hot-columnar/stress/archive_request_logs_exec_test.go` 每次
`CREATE DATABASE` 一个一次性 scratch 库，用**仓库自己的基线 DDL** 建表、用
**仓库自己的迁移文件**建函数、再调用——零手写列名，所以它不会随真实 schema 漂移
成假绿。变异检验：把 754 改回坏版本，data 门与 stress 门**同时红**（stress 门拿到
`42703`），还原后同时绿。

**顺带订正一条自己写过的注释**：754 头注原写「本 SQL 从未在真实 PostgreSQL 上执行过，
不宜在此盲改」——这句在 S-01 实测后成了自欺（不是「不宜盲改」，是**它根本没跑过**，
而「盲改」的判断本身就是没跑过的人做的）。凡以「没跑过所以先不改」为理由保留的缺陷，
都在欠一次实跑。

### R79 续 · 另外三条同源教训（小样本压测 / 双副本迁移 / 「没跑过所以先不改」）

R79 主段记的是「动态 SQL 的列名静态门抓不到」。同一次实测还带出三条独立的、
跨项目可迁移的：

**L2｜小样本压测证明不了 N² 游标。** 本轮自建的 5000 行压测门在缺陷存在时**全绿**
（冷跑 34.5ms）——因为对 5000 行做全表扫描很便宜。而真实代价是 O(rows²/1000)：
2.1M 行时每批读 4.2 GB、整轮 30 分钟超时回滚。**这正是「写完压测门就以为压过了」**
的失效形态，且它与本会话另一条既有教训同构（守卫全绿 ≠ 守卫在工作）——区别在于
这次绿的是**守门人自己写的压测门**，而它压根没测该测的性质。
可推广判定：**凡「按某列排序 + LIMIT 分页」的批处理，压测必须断言 EXPLAIN 计划形状
（走不走索引），不能断言小样本耗时。** 耗时的量级会随 n 平方变化，样本量不会告诉我们
这件事。另注：计划形状门要**自带对照**（先断言未加索引时必为 Seq Scan，再断言加索引后
必为 Index Scan），否则「断言计划里有 Index」可能在解析器换版本后恒真或恒假。

**L3｜一份 SQL 修复落在双副本迁移的其中一份，等于没落。** 本仓库每个 startup 迁移
同时存在于 `sql/migrations/startup/`（canonical，契约测试读它）与
`installer/cmd/llm-gw-installer/embeddata/startup/`（delivery，installer 实际执行它）。
R79 的 42703 修复**先只改了 delivery 那份**，canonical 仍是坏的——是本轮新增的字节
一致门在下一步抓出来的。安装通道与契约通道从此看到的是两份不同的 SQL。
可推广：**凡要改一个在多处留有副本的文件，先确认哪份是 canonical、哪些是交付副本，
一次改全，并在提交前跑一遍全量逐字节比对**（本轮的全量比对脚本形态是一条
`cmp -s` 循环，代价 <1s，应成为改迁移文件的固定动作）。

**L4｜以「没跑过所以先不改」为理由保留的缺陷，都在欠一次实跑。** 754 头注原写
「本 SQL 从未在真实 PostgreSQL 上执行过，不宜在此盲改，留作后续」。S-01 实测后证明：
不是「不宜盲改」，是**它根本没跑过**；而「不宜盲改」这个判断本身就出自没跑过的人。
这类句子有一个隐蔽的自指——它把「我没验证」包装成「我判断不该动」，读起来像是审慎，
实际是把无知伪装成结论。**凡在代码/迁移里看到「没跑过/无法验证，先不改」，要么本轮
跑一次，要么把它改写成一条带责任人的待办，而不是留在正文里当理由。**
