# Magpie 第三轮：出站字节黄金门与会话一致性实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. 按任务纵向执行，保存红绿与变异证据，先规格审查再质量审查。

**Goal:** 建立真实三协议出站黄金门，修复本地会话数据落后及跨实例旧读/丢更新，合并推送后本地部署并完整验收。

**Architecture:** 黄金门从真实handler进入、在HTTP发送边界捕获原始body，以独立语义断言保护数字/未知字段/工具轮。会话镜像从真实持久化hook追到canonical PG schema；缓存以共享原子提交维护完整state/body配对、跨实例新鲜度和callback至多一次，Redis故障不得伪装成功。

**Tech Stack:** Go1.27.2、PostgreSQL、Redis、现有dispatch/IR/sessionv2mirror，Go test/真实Docker依赖及browser-use。

## 基线与约束

- 初始源码 `493d24ebcdaf45d792130f32b42e6724cb667905`；Magpie `591ed58b536b17162a0715aa7210d9b7d0db4c09`。
- 本地初始运行2659/fb0b481e，root `~/kaixuan/llm-gateway-go`，active8782；端口和版本在部署时重新读取。
- 工作树 `codex/magpie-round3-20261015`，不改并发checkout，不部署154/245。
- 用户已授权并行、提交合并push和本地部署；无需重复申请同主题确认。
- 每片通过rule17与审查后checkpoint；Task Stop Summary校验必须成功才执行commit/merge/push。
- 生产行为修复必须红→绿并有去保护变异；普通golden运行不能生成文件。
- Redis/PG集成用一次性Docker实例；真实本地验收只使用唯一测试ID和可恢复fixture，不输出凭据。

## 验收契约

| ID | 必须证明的最终状态 | 证据边界 |
|---|---|---|
| G1 | Chat/Responses/Messages全部9方向×stream/nonstream=18，另3种严格Responses中转×2=6，真实handler→Executor→HTTP成功 | 真实IR和stream桥接线；每例命中预期path、次数及有效客户端终态 |
| G2 | 最终出站原字节有可审查golden；缺失/不匹配普通模式失败；仅UPDATE_UPSTREAM_BODIES=1可更新 | 不先Unmarshal/Marshal、TrimSpace或重新缩进；所有重试attempt均记录 |
| G3 | 未知字段、大整数9007199254740993、转义、缓存前缀及完整工具轮未被错误损坏 | 独立字面语义断言先于录制；数字转float/去wire guard/去恢复等变异准确变红 |
| S1 | 请求完成后镜像成功持久化会话turn/body，data-horizon反映真实canonical数据新鲜度 | 完整真实hook/事务与本地实际HTTP请求，用唯一session/request ID查落库；不改阈值掩盖故障 |
| S2 | 有Redis时成功Set/Update已共享确认，另一实例下一读获得同一或更新的完整state/body | warm/cold双实例、同计数不同body、旧L3不能冒充新pair；hash/版本同时校验 |
| S3 | 并发Update不丢更新，每次callback至多调用一次 | 真实Redis两实例有界channel时序；超时/取消/租约过期不能提交或删除后继锁 |
| S4 | Redis失败、callback错误/空state、旧回填竞争、可选字段清零都有明确语义 | 不发布未确认L1、不吞共享写失败；nil Redis仅对象内一致性 |
| R1 | 完整发布门通过，精确最终SHA审查，main合并push，本地新版健康且原症状消失 | 全量Go/installer/前端+strict coherence+真实HTTP+browser；记录限制和回滚命令 |

## Task 1：统一出站黄金门（优先完成）

**Files:** Create `domains/streaming/upstream_bytes_golden_test.go`、同目录专用fixture/fake测试及`testdata/upstream_bodies/`；必要修正限定在真实IR/协议转换的有损点。

- [x] 复用chat_output_commit_test.go的真实executor/dispatch骨架，接真实transformation.NewTransportIRConverter与生产stream桥。
- [x] 编写可编程HTTP fake，io.ReadAll捕获全部attempt，按顶层RawMessage点名拒绝，回复真实JSON/SSE；自测“前提不在场”会失败。
- [x] 普通模式缺golden应红；显式更新仅在交换成功和独立语义通过后写文件；另测普通模式绝不创建。
- [x] 每协议复合输入包含未知嵌套对象/数组/null、精确大整数、转义、工具调用+结果、缓存字段；跨协议结构允许声明的转换，值不得丢失/舍入。
- [x] 先完成同协议6例，随后补全跨协议12例和严格中转6例；retry按顺序保存全部attempt。
- [x] 若暴露有损生产行为，先独立红测再最小修复，不能把损坏输出当golden。每次变异在隔离overlay/副本执行，禁止并发污染源码。
- [x] 命令：`GOTOOLCHAIN=go1.27.2 SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk go test ./domains/streaming -run 'TestUpstreamBodies|TestUpstreamGolden' -count=1`；显式更新后清开关再跑普通模式。
- [x] 规格/代码审查精确SHA与rule17通过，checkpoint合并push；此门完成前后两任务仅做诊断与红测设计。

## Task 2：会话数据写入修复

**Files:** `domains/session/v2/`的BodiesWriter/迁移兼容，`internal/sessionv2mirror/`，必要的启动迁移/installer/embed/通道，以及本地部署有效写门；实际路径由诊断证据定位后记录于审计。

- [x] 用只读本地PG与日志证明失败事务、outbox状态、有效开关及canonical schema的实际形状；对比启动迁移和独立installer登记。
- [x] 将缺kind列/写门实际缺陷复现为真实PG回归或完整hook测试；修复后同事务turn/body均存在，迁移双路径幂等。
- [x] 修复必须兼容既有数据和回滚；如需DDL，使用新编号幂等迁移，不改已执行迁移内容；同步installer嵌入及台账/通道。
- [x] 对过渡期失败outbox先预览/备份再只重试本轮测试记录，不无界重放全部历史任务。
- [x] 以本地真实Chat/Responses/Messages请求、唯一session ID验证写入与告示，包含无显式session ID的正常请求语义；不能靠放宽告示窗口通过。
- [x] 去掉schema/接线修复的变异使对应断言红；完成后审查、rule17及checkpoint。

## Task 3：完整配对及跨实例缓存一致性

**Files:** `domains/hooks/compression/session_cache.go`、独立共享Redis原子适配文件、`session_compressor.go`、`recovery_coordinator.go`、`approval_resume.go`；相关测试；`scripts/audit/run-session-storage-stream.sh`与`tests/session_storage_audit_gate.sh`。

- [x] 先运行旧strict门保留真实红；把callback内双到场barrier改成首callback挂起、第二实例等待锁、释放后观察顺序，避免正确串行化导致测试死锁。
- [x] 使用带随机token的Redis租约，获取等待响应ctx；提交Lua同时校验token、写完整state/body+revision/TTL；release compare-token，独立短cleanup ctx。
- [x] callback每调用至多一次；租约丢失返回显式错误，不重放callback；无Redis保持对象内stripe契约。
- [x] 原子保存确切body与state，保留hash和完整字段清零语义；检查现有隐私/大小约束，不用相同MsgCount替代配对证明。
- [x] 有Redis时共享状态权威；L1只复用相同revision对应body；DB backfill持同锁重读避免覆盖新写；错误不得返回旧L1成功。
- [x] 把压缩updateCache/CommitFinal、recovery、approval的RMW收敛到Update；callback外副作用保持一次，审批更新保留body。
- [x] 覆盖warm/cold、并发计数、同计数异body、租约过期A/B、取消三时点、错误、零值清理、回填竞态与业务RMW。
- [x] 强门默认要求coherence；显式require-coherence缺依赖必须非零，不能SKIP RC0；验证脚本失败传播的正反控。
- [x] 命令：`GOTOOLCHAIN=go1.27.2 SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk bash tests/session_storage_audit_gate.sh --require-coherence --race`，及完整compression/sessionaudit/streaming包测试。
- [x] 撤共享刷新/锁/token校验、吞错误/先L1、旧body替换等隔离变异必须对应变红；严格门与review通过后checkpoint。

## Task 4：最终发布验收与研究更新

- [x] 更新`docs/research/2026-10-14-magpie对比与优化方案.md`及本轮审计，记录已落地/未覆盖/后续项，不把历史证据当新结果。
- [x] Go1.27.2+SDK26.5+cgo1运行`verify.sh --web`；installer单独测试；真实严格存储门与新黄金门均通过。
- [x] 各slice由spec与code/Go reviewer审查精确SHA；涉及UI时由TS reviewer及browser-use实际操作验证。
- [x] 保存全量变更Task Stop Summary并校验，commit→merge --no-ff main→push，冲突后重验。
- [x] `AIAN_DEPLOY_LIB=$HOME/workspace/ai-native-tools/deploy-lib GOTOOLCHAIN=go1.27.2 /opt/homebrew/bin/bash scripts/deploy-local.sh deploy --root $HOME/kaixuan/llm-gateway-go`，完整重建两前端。
- [x] 核active-port/current/version/compiler/readyz/healthz；实际协议请求、唯一会话落库与浏览器告示；清理临时资源。
- [x] 回滚：同环境运行`deploy-local.sh rollback --root $HOME/kaixuan/llm-gateway-go`；代码revert本轮merge，不改生产。
- [x] 保存精确源码、回滚及验收证据，按finish流程核对main/remote和全量变更，推送后清理专用工作树/分支；最终状态由Task Stop Summary记录。

## 执行结果

冻结发布源`76e185e71cebe87611746d03cea56bbc352a4d7b`；源码合并`2a6bb99bf`已推送。
本地2661/8782、Go1.27.2，完整发布门RC0；HTTP18/18、canonical17/17、旧12补偿和真实浏览器均通过。
具体审查SHA、异步验收边界、回滚、已清理资源及未覆盖范围见[审计记录](../../audit/2026-10-15-magpie第三轮字节与会话一致性.md)。

## 会话约束

本机`~/.acc/state/token.json`仍缺失，ACC session.append/pin/archive不能鉴权，仓内审计保留实际证据；不得声称已外部归档。本checkout无`.acc-session-policy`，rule50 opt-in不适用。
