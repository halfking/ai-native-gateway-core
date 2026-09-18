# D17 代码卫生与冗余治理 子代理报告（窗口：48h = 643735a28^..HEAD；重点 b75c91900..HEAD）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | `.db-audit/` 三个审计脚本与 `docs/database/2026-09-17-db-audit/scripts/` 同名文件**字节级完全相同**（diff 为空），仓库内两份拷贝；根目录 `.db-audit/` 是隐藏目录、无 README；且互指混乱——README 产物索引称可复跑脚本在 `scripts/`，而生成物头注和复跑指引却指向根目录 `.db-audit/`（`cd .db-audit`）。单一 SSOT 缺失，下次重跑或修改会改错份 | `.db-audit/{extract_sql,gen_docs,parse_schema}.py`（3a9dca46d 新增）vs `docs/database/2026-09-17-db-audit/scripts/` 同名；`docs/database/2026-09-17-db-audit/README.md:15` 与 `README.md:304` vs `schema-inventory.md:3`、`schema-diagram.md:3` | 择一归位（建议保留 docs 下带 README 的副本并迁移到 `scripts/` 或 `tools/`），删除另一份；统一 README 复跑指引与生成物头注里的路径。生成物本体（tables.json/sql_catalog.jsonl/schema-*.md）在 docs 下有 README 背书，属有意归档，不算发现 |
| 2 | P3 | 活域文档回注指向已删除的符号名（D08 已报，本代理复核**成立**并给出准确替换名）：R42 双胞胎提交 6dbf55973（`balance_manual_protection.go`）与 9c99f3c40（`balance_manual_guard.go`+`balance_manual_guard_test.go`）经 merge 59f770cf1 去重后，HEAD 只剩前者——**无死文件残留**，但 D08 域文档仍写 `bg/balance_manual_guard.go manualBalanceGuardSQL` 和 `TestManualBalanceGuardPredicateLockstep`。实际符号：常量 `ManualBalanceProtectionPredicate`，测试 `TestBalanceManualProtectionContent/IsWired` + `TestManualBalancePredicateWriteTimeCoverage`；消费点数 5（floor guard 3 + probe_v2 2）文档数字仍对 | docs/audit/playbook/domains/D08-provider-errors.md:54；对照 bg/balance_manual_protection.go:31、bg/balance_manual_protection_test.go:14/39/71、bg/balance_floor_guard.go:863/1013/1027、bg/credential_probe_v2.go:588/609 | 更正 D08 域文档三处名字；轮文档 `docs/audit/2026-09-18-r42-24h-audit-round.md:18` 属历史记录可保留原文 |
| 3 | P3 | 注释漂移（R42"当场对照实现"教训再犯）：`translateThinking` 覆盖范围注宣称 Anthropic 入向"serialize_openai 对其只上报 loss 不输出……属 P5 统一处理范畴，**不在本修复内**"；同日 45 分钟后 7bb1708d5 即落地 `applyThinkingToOpenAIChat`，该路径已从 loss 上报变为按 TargetProvider 方言出向——注释描述的"未来工作"已成过去时 | internal/paramreg/translate.go:39-43；对照 internal/ir/serialize_openai.go:210-247（applyThinkingToOpenAIChat）及 05:38/07:15 两 commit 时序 | 把覆盖范围句改写为"跨协议入向已由 P5 applyThinkingToOpenAIChat 处理"；事故根因叙述（parse_openai→Extensions→restore）仍准确可保留 |
| 4 | P3 | 注释漂移：resolve.go 的 2026-07-14 注释称"所有列均为小写持久化，直接比较，**不要**在每列包 lower(...)"；本窗口 H1 修复恰好在两条 alias SQL 改为 `lower(ma.raw_name) = lower($1)` 且 `ma.status = 'active'`（去掉 COALESCE），与注释两条主张都相悖 | resolve/resolve.go:141-144 vs :199、:240 | 注释诚实化：canonical_name 路径仍小写等值；raw_name 按 2026-09-18 修复改为 lower() 等值匹配（配合函数部分索引），并补 status 不再 COALESCE 的说明 |
| 5 | P3 | 注释漂移（D04 已报，复核成立）：毒丸分支注释称 "markFailed will move it straight to DLQ **after max_attempts**"，实际代码一步直呼 `markDLQ`（validation 失败无 max_attempts 累积，且函数名是 markDLQ 非 markFailed） | internal/outbox/dispatcher.go:257-259 vs :262-263 | 注释改为"corrupt payload 直接 markDLQ 入死信，跳过重试" |
| 6 | P3 | 零调用接缝（D09 已报，D17 视角补充）：`CachedPlatformBool/String/Float` 三读取器 + `cachedEffectiveRaw` + `platformValueCache` 全仓零生产调用方，但 `helpers.go:19` 注释在主动"广告"这个家族、且 `InvalidatePlatformValue` 已在 store_db.go 4 个写点接线——**为无人消费的读路径维护失效机制**；符号头也无 R39 惯例的 `RESERVED(...)` 标注 | settings/ttl_cache.go:20/41-79/91-140；settings/helpers.go:19；settings/store_db.go:82/88/218/234（全仓 grep 无外部读者） | 二选一：函数头补 `RESERVED(待首批消费方)` 标注防下轮重复怀疑；或删除整族含 4 处失效接线（删除性动作须单独立项） |
| 7 | P3 | 测试专用导出：`FeatureStatsWorker.GetLatestStats` 生产零调用（仅 `bg/feature_stats_worker_test.go:159` 消费），注释"用于测试和监控"的"监控"半句无任何接缝；同文件 `FeatureDistribution`/`DedupStats` 注释自认"用于测试"；其读取的 `feature_quality_metrics` 是视图（662），生产读者仅此零调用函数（D09 已报无写入方，D17 确认链路） | bg/feature_stats_worker.go:321-322、:367、:375 | 注释如实化为 test-only，或降 unexport；若保留须注明监控接缝计划 |
| 8 | P3 | 注释漂移：`taskprofile/handler.go` 文件头 endpoint 清单只有最初 4 条（GET /、POST /corrections、GET /corrections/stats、POST /reload），a8d3a5bbe 新增的 export/import/apply-tier-config 三条路由未回填头注 | taskprofile/handler.go:16-29 vs :53-55 | 头注释补齐三条 |
| 9 | P3 | 注释漂移：`taskprofile/registry.go:32` 称 "registry_defaults_test.go pins the tier tiers and thresholds"，该文件不存在；实际钉桩测试是 `registry_test.go` 的 `TestDefaults_MirrorV3TierMapping` | taskprofile/registry.go:32；对照 taskprofile/registry_test.go:15-55 | 改注释里的文件/测试名 |
| 10 | P3 | 本窗口**新增**文件带 gofmt 债：两份新测试 EOF 缺尾随换行（gofmt -d 实证）；与 R39"gofmt 债随修复顺带清"惯例相悖（bg 包其余 20+ 文件为 R37 已登记存量，非本轮新增） | bg/auto_route_affinity_worker_integration_test.go:149、internal/ir/serialize_openai_thinking_dialect_test.go:93 | 机械 `gofmt -w` 两文件，单独 commit |
| 11 | P3（可观察） | `deploy-local-lib.sh` 的 docker info 超时防护依赖 GNU `timeout`：macOS 裸机无 coreutils 时 `timeout 5 docker info` 恒为假 → Docker 探测**永远静默跳过**（非超时才跳过）。本机有 `/opt/homebrew/bin/timeout`，且 `scripts/deploy-local-preflight_test.sh:17` 已有同款先例，风险低，仅登记 | scripts/deploy-local-lib.sh:126、:128 | 可选：加 `_dl_have timeout` 降级分支或注释声明 coreutils 前置；不强制 |
| 12 | P3（登记） | sync-admin 重试逻辑魔法数三写：重试预算 8 出现在 `range(1, 9)`、`attempt < 8`、日志文案 `'/8'` 三处 Python 字面量，调参需三点同步 | scripts/ops/sync-admin-password-from-env.sh:104-115 | 登记轮文档遗留：抽 `RETRY_BUDGET` 常量（标注级即可） |

## 二、核实为健康的面

- **R42 双胞胎 merge 残留无死文件**：`bg/balance_manual_guard.go`（9c99f3c40 创建，29 行）与 `_test.go`（57 行）已被 merge 59f770cf1 去重，HEAD 无残留；`ManualBalanceProtectionPredicate` 单一定义、5 个 SQL 消费点齐、3 个守卫测试在位——R42 报告担心的"死文件"不存在，仅剩发现 #2 的文档名残留。
- **taskprofile 无死导出、无未接线接缝**：全部导出符号有生产消费——admin 路由注册 `admin/handler.go:1359`，CorrectionSource 适配 `cmd/gateway/routing_optimizer_init.go:73/78/131`，overlay 加载 `main.go` initTaskProfile；与 autoroute 的任务类型词表/tier 常量/MinConfidence 数据重复是**文档化的有意自包含**（types.go 明确边界"不 import autoroute"，registry.go 标注 MUST stay in sync + test 钉住字面量），符合 D17"重复实现→两处标注指向"处置基线，不算违例（仅发现 #9 的测试文件名漂移）。
- **`dbx.VacuumFullMutex` 未因 vacuum_worker 重写变死代码**：`admin/data_lifecycle_storage.go:787` 仍消费；vacuum_worker.go 新注释（分区父表无存储可重写、不再占集群锁窗口）与实现一致，VACUUM (ANALYZE) hot 表语义正确。
- **`admin/data_lifecycle_storage.go` lock_timeout 修复注释准确**：SET（非 SET LOCAL）+ defer RESET；defer LIFO 保证 RESET 先于 Release 注释成立。
- **config 注释诚实化到位**：`config/storage.go ApplyDefaults` 新注释（生产不消费、factory full 为桩、以 "postgres connected max_conns=N" 日志为准）与 `cmd/gateway/main.go:440-453` 的 STORAGE_MAX 启动护栏 Warn、`storage/factory/stubs.go` 桩现状三方一致。
- **deploy 脚本修复本体质量良好**：deploy-154/245 空参数修复按 `((${#ARGS[@]} > 0))` 守卫正确覆盖 bash3.2+set -u；deploy-seamless 锁路径钉死 `/tmp` 与 AC-L15 测试（machine-level default path）、AC-L16（kill -0 EPERM 不误杀活锁持有者，PID 1 探针）互为锁步；`deploy_local_contract_test.sh` fixture 补 `sql/migrations` 有出处注释（dadf1e66f 的 exit 64）；`local-host-deploy.sh` `$ATTACH_DIR`→`$attach_dir` 修正与 layout-helper 输出键名（:87-89）对齐，CGO_ENABLED 注释与 routingopt cgo 约束一致。
- **outbox gauge 节流新注释准确**：`gaugeRefreshInterval` 30s、两个 COUNT 探测、注释与实现一致（`internal/outbox/dispatcher.go:40-46、:395-402`）。
- **serialize_openai P5 新分支注释与实现一致**：dialect 取 `req.TargetProvider`（非模型名）、不复用 `reasoncap.Resolve` 的理由、已有键不覆盖、kimi 等未验证方言留 loss 路径、`GenericThinkingObject` 存在（`internal/reasonnorm/norm.go:229`）——未发现失实。
- **R42 已清项验证保持**：errorsx `KindClientBug` 已并入 case 列表带合并注释（`errorsx/failover_policy.go:112-113`）；taskprofile 前端 API 路径与后端路由一一对应（`web/src/api/taskProfile.ts` vs `taskprofile/handler.go:50-56`），AnnotationView 闭环调用真实接线（best-effort catch 注释诚实）。
- **窗口内新导出符号均有接缝**：routingopt/confidence.go（CorrectionSource 被 init 消费）、`migration_726_test.go`、部署测试 AC-L15/16 均已接线或属测试本体。

## 三、未覆盖项与原因

- **e2e_loop_integration_test.go（516 行）逐断言审计**：仅浏览了结构，未按 R37"占坑 goroutine/毒源测试"模式逐测试核对称释放——量大且属 D09/D16 交叉域，建议下轮专查。
- **web 前端 Vue 组件逐行**：只核对了 taskProfile.ts API 路径与 AnnotationView 调用闭环；ProvidersView/CredsTab 改动未逐行（非本轮派发重点）。
- **`docs/database/2026-09-17-db-audit` 生成物内容正确性**（tables.json 4 万行、2510 条 SQL 目录）：只验证了"生成物与脚本、README 的引用关系"，未抽查数据本身正确性（属 D05/DB 审计域）。
- **installer main.go/runner.go 的 516/520 五点同步逐行**：上轮 R42 P0 修复域，本轮只确认注释无漂移面抽查，未重审。
- **deploy-lib/targets.sh 与 deploy-seamless.sh 锁实现本体**：核对了消费端改动与 AC-L15/16 测试，未逐行重读锁库全部函数（73c8a6c51 已有锁测试背书）。
- **R42 双胞胎另一半 6276a3ff9**：窗口外且上轮已定案（D17 域文档 R42 回注已记录教训），未重新核查。
- **`.db-audit` 脚本 Python 逻辑执行验证**：只读静态阅读，未运行（环境无真库且本代理禁止写文件/产生输出）。

**主代理复核结论（R43）**：#2/#3/#4/#5/#8/#9/#10 已修；#1 登记遗留（归位动作涉及删文件，单独轮处理）；#6 已加 RESERVED 注解；#7 已修注释（test-only 如实化）；#11/#12 登记。健康面采信。
