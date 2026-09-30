# 统一优化提示词方案（新旧并行实现收敛 · 可执行提示词包）

> **版本**: v1.0 · **生成**: 2026-10-01 · 基线 `3efae99`（main）
> **用途**: 针对本轮审计确认的 10 组新旧并行实现（证据见 [`parallel-implementations-comparison.md`](./parallel-implementations-comparison.md)），提供**逐项、自包含、可直接投喂给编码代理**的收敛提示词。每条提示词独立成轮，按优先级排序执行；执行前后都以 grep/测试证据为准，不以"看起来能跑"为准。
> **使用方法**:
> 1. 每轮只取一条提示词（连同"通用纪律前置块"一起投喂）；
> 2. 先跑该条的"入场核对"，与对比文档状态不符（例如已被其他轮收敛）则停手登记；
> 3. 完成后按"验收门禁"逐项验证并在轮文档登记证据；
> 4. 涉及删码的条目，删除前必须留存"零生产调用点"grep 计数输出。

---

## 通用纪律前置块（每轮都必须附带）

```text
【通用纪律】(llm-gateway-go 收敛轮，必须遵守)
1. 证据优先：一切"谁在用"的判断用 grep -rl "llm-gateway-go/<pkg>" --include=*.go . | grep -v vendor 复核，不凭记忆与文档。
2. 只做本轮范围内的事；发现范围外问题只登记(文件:行号)不修。
3. 删除任何代码前先输出零生产调用点证据（外部 import 计数=0，或生产装配点已全部摘除）。
4. 每步先补回归测试再动实现；行为开关变化必须保留回滚方式（env/flag/迁移 down）。
5. 提交信息用 conventional commits，正文写清"删了什么/凭什么证据/如何回滚"。
6. 不以编译通过、HTTP 200、页面可打开作为验收通过证据；验收以测试+装配点 wiring 为准。
7. installer/cmd/llm-gw-installer/embeddata/ 下有 startup SQL 镜像，涉及迁移时两处同步。
```

---

## U-01（优先级 0，零风险热身）：删除 7 处零引用死代码

```text
【任务】删除以下 7 处全仓零外部引用的死代码（2026-10-01 审计 P-10，主代理已复核外部 import=0）：
internal/probe、internal/orchestration、internal/capabilityscore、internal/ctxpool、
internal/agent/wsclient、adapter/unified、examples/

【入场核对】
- 对每个包重跑：grep -rl "llm-gateway-go/<pkg>\"" --include=*.go . | grep -v vendor | grep -v "^./<pkg>/" —— 必须为 0（_test.go 引用也计为非零，需先处理：测试一并删除或随包删除）。
- 特别核对 adapter/unified：其 interface.go 头部 2026-08-30 已声明废弃；若有非测试引用出现（说明期间被复活），停手登记。

【步骤】
1. 逐包输出零引用证据到轮文档。
2. git rm 整包（含测试）；docs 中引用这些包的地方改为标注"已于 2026-10 删除"（搜索 docs/ 内包名）。
3. go build ./... && go vet ./... 全绿；运行触及包名引用的守卫测试（grep 测试名含 legacy_selfcheck_gate / auto_route_wiring_guard 等）。

【验收门禁】
- 全仓 build/vet/test 通过；grep 死包名在 *.go 中 0 命中（注释提及除外）。
- MODULES_GUIDE / FEATURES_CATALOG / parallel-implementations-comparison 的 ⚪ 标注更新为"已删除"。

【回滚】git revert 单提交即可（纯删除，无 schema/行为影响）。
```

## U-02（优先级 1）：autoroute 三代收敛（P-03）

```text
【任务】收敛 autoroute 包内三代并存的决策/分类/评分实现（2026-10-01 审计 P-03，状态 🔴 A 双活，v1 默认）：
- decision.go(v1) vs decision_v2.go（v2 内部 :38-43 遇非 *Index 回退 d.Decide）
- classifier.go(8类) vs classifier_v3.go(10类, flag auto_v3_enhanced_classification)；task_types_ext.go vs task_types_v3.go
- scoring.go vs scoring_v2.go vs scoring_simplified.go（scoring.go:161 自述"三代退役条件"）

【入场核对】
- 读 autoroute/V2_IMPLEMENTATION_STATUS.md 与 scoring.go:161 退役条件，确认本轮执行哪一代退役（默认建议：保留 v2+v3 路径，退役 v1 死臂；若线上 AUTO_ENABLE_V2 仍 false，则先做"默认翻转评估"再删）。
- grep 生产调用点：domains/streaming/auto_route.go:593、auto_route_nonchat.go:160,208、feature_flags.go:341。

【步骤】
1. 先盘点三代各自的测试覆盖与 cmd 工具依赖（traffic-replay / tuning-backtest / auto-testbench 各盯哪代）。
2. 制定并登记退役矩阵（哪代保留为唯一路径、哪些 flag 废弃、测试如何迁移）。
3. 小步执行：先合并 scoring（三选二/一），再 decision（消除 v2→v1 回退臂），最后 classifier（v3 转 flag-删除）。
4. 每小步跑 autoroute 全量测试 + domains/streaming 相关测试 + 三个 cmd 工具的构建。

【验收门禁】
- decision_v2 不再存在回退 v1 的臂（或 v1 被整体删除）；scoring 文件数减少且 flag 矩阵登记；
- 分层抽样回归：用 cmd/auto-testbench 现有套件跑一轮基线对比，macro 指标不劣化（阈值在轮文档定）；
- ADR 登记（docs/adr/）记录代际选择理由。

【禁止】不得在未跑测试台对比的情况下直接翻转 AUTO_ENABLE_V2 生产默认。
【回滚】每小步独立提交；flag 废弃保留一个版本周期的 env 兼容读取+告警日志。
```

## U-03（优先级 1）：legacy 探测 worker 退役（P-02）

```text
【任务】为 legacy 探测 5 worker 设定退役并执行删除（2026-10-01 审计 P-02，状态 🟠 B 新版默认接管）：
bg/self_check_worker.go、bg/credential_probe_v2.go、bg/model_probe.go、bg/passive_probe_listener.go、bg/active_probe_worker.go

【入场核对】
- internal/probemode 为唯一开关权威、默认 true；legacy 需双 env（LLM_GATEWAY_USE_NEW_PROBE_MODE=false 且 LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true）才启动（legacy_selfcheck_gate_test.go:8-14）。
- 确认 rollback 保留期：建议 2026-10-31；未到期则只做本提示词的 1-2 步（对象装配摘除可延后）。

【步骤】
1. 盘点 legacy 对象仍在 main.go 的构造/注入点（:4032 NewCredentialProbeV2、:4134 NewModelProbeRunner、:5489 SetModelProbeRunner 等），列出 admin 面对 legacy 对象的 API 面。
2. admin 仍读 legacy 表的端点改读新表或下线（逐端点登记）；确认 R36 修过的 reviver/guard 不再读 legacy 表。
3. 删除 5 个 legacy worker 文件与双 env 门；更新 main_helpers.go:288-306 注释与 legacy_selfcheck_gate_test.go（测试改为断言"legacy 不存在"）。
4. 探测域回归：/api/admin/probe/* 看板 + /api/self-check/* + node_probe 集成测试全绿。

【验收门禁】grep 5 个 worker 类型名在非测试 go 中 0 命中；探测看板数据源全部指向新表；部署文档/QUICK_REFERENCE 中 env 说明更新。
【回滚】git revert；若需紧急回滚行为，恢复双 env 门（保留一个版本的 git 可恢复性声明）。
```

## U-04（优先级 1）：Session V2 切换治理（P-04）

```text
【任务】不删除代码，为 Session V1/V2 双写建立切换治理闭环（2026-10-01 审计 P-04，🔴 A 迁移中）：
V1 PRIMARY（domains/session, 122 文件）/ V2 SHADOW（domains/session/v2, 44 importer）；门禁 dual_read_validator.go 7 天零漂移。

【步骤】
1. 把 dual-read 对账指标（漂移行数/延迟/body 完整率）接入 /metrics 与告警（阈值：连续 7 天漂移=0 才具备翻主读资格）。
2. 在 settings_kv 或文档中登记 cut-over 决策点：最迟翻主读日期或回退日期，二选一并报批。
3. 冻结 V1 侧 schema 变更（登记约束：迁移不再加 V1-only 列；新列必须 V2 同步）。
4. internal/sessionv2mirror 回填与实时影子写的竞争审计：确认 outbox replay 不会覆盖更新的行（版本号/时间戳比较）。

【验收门禁】对账指标可在 Prometheus 查到并有告警规则；cut-over 决策登记入 ADR；mirror 竞争场景有回归测试。
【禁止】本轮不翻主读、不动 dual_writer.go 的 PRIMARY/SHADOW 语义。
```

## U-05（优先级 2）：outbox 构造器死臂删除 + 内核抽取设计（P-05 + P-08 第一步）

```text
【任务】第一步删除 outbox 死构造器；第二步产出 4 套 outbox 的公共内核设计（2026-10-01 审计 P-05/P-08）。

【第一步（可直接执行）】
- internal/outbox/event_builder.go 的 BuildRequestCompletedEvent(:24) 与 BuildRequestCompletedEventV2(:100) 现仅测试引用（生产走 event_builder_v1.go:75 的 V3，调用点 telemetry/client.go:1957）。
- 入场核对 grep 两符号的生产引用=0 后，删除两个函数并迁移/删除其测试；wire_schema_test 只保留 V3 断言。

【第二步（设计轮，不改行为）】
- 对 durable/pending_outbox.go、durable/settlement_outbox.go、internal/outbox/、internal/sessionv2mirror/outbox.go 四套做能力矩阵：幂等键、重放策略、退避、签名、死信、可观测。
- 产出 docs/adr/ 草案：统一内核接口（Enqueue/Claim/Ack/Dead + 幂等键规范），各调用方迁移波次。
- 不在本轮实施迁移。

【验收门禁】第一步：grep 两符号 0 命中、outbox 测试全绿；第二步：ADR 草案含四套现状矩阵与迁移波次表。
```

## U-06（优先级 2）：admin vs control CQRS 迁移收尾或回退（P-07）

```text
【任务】裁决 routing overrides 写路径的 CQRS 半迁移（2026-10-01 审计 P-07：create 已迁 control/routing/create.go，delete/extend 留 admin/routing_overrides.go:207/:260，两包互指）。

【步骤】
1. 评估两个选项：A=继续迁移（delete/extend/audit 全部迁入 control/routing，admin 只留 handler 壳）；B=回退（create 迁回 admin，control/routing 降级为实验）。按 ADR-0002 依赖方向与现有测试覆盖给出建议并登记 ADR。
2. 执行选定项；若选 A，保证路由前缀与语义零变化（/api/admin/routing/overrides 不变）。
3. 契约测试：list/create/delete/extend/audit 五操作 e2e 断言迁移前后一致。

【验收门禁】同一资源的写命令单一归属包；五操作契约测试全绿；ADR 登记。
```

## U-07（优先级 2）：gateway-v2 三重暴露面裁决（P-01）

```text
【任务】收敛 v2 Pipeline 的三重暴露面（2026-10-01 审计 P-01：①cmd/gateway-v2 独立演示二进制 ②进程内 /v2/* 路由组（LLM_GATEWAY_V2_ENABLED）③v1 端点包装桥（LLM_GATEWAY_USE_V2_PIPELINE），均默认 OFF；生产镜像不构建 v2）。

【步骤】
1. 裁决三选一：A=保留 ③（包装桥）作为唯一灰度路径，冻结并归档 ①②；B=推进 ② 为目标形态；C=全部冻结等 session v2 切换后再议。
2. 按裁决执行：若 A——cmd/gateway-v2 移至 archive 或加显式 BUILD_TAGS 守卫、docker-compose.dev-research.yml 的 v2 profile 注明冻结原因；/v2/* 路由组保持 OFF 并登记。
3. main.go:6144-6160 与 main_pipeline.go:161 的 OR 语义 flag 文档化（哪个 flag 控制哪个暴露面，写入 QUICK_REFERENCE env 表）。

【验收门禁】v2 暴露面数量减少或全部显式登记冻结状态；e2e_test.go / mock_probe_e2e_test.go 若保留 ① 则继续可跑；env 文档更新。
【禁止】不得在裁决前删除任何 v2 路径（切流参考价值待 session v2 门禁结论）。
```

## U-08（优先级 3）：URSM k2 迁移推进（P-06）

```text
【任务】推进 URSM key schema 三代收敛（legacy node namespace → domains/ursm/v2 → k2）。当前 cmd/k2-migrate-ursm 为 dry-run only（M5-0/T0 BLOCKED/NO-GO，文件头自述）；URSM_V2_KEY_SCHEMA_MODE=dual|canonical|k2。

【步骤】
1. 读 NO-GO 门禁条件（k2-migrate-ursm/main.go:1-6 与 ursm-k2-preflight 输出），列出解锁清单（checksum 通过率/回滚演练/观测覆盖）。
2. 先把 legacy 读路径清单化（admin/handler.go、credentialfpslot/node_state.go 等 grep node namespace key），逐条迁移到 v2 读。
3. 达成解锁清单后再评估 k2 实迁；本轮只做 1-2，不做 k2 实迁。

【验收门禁】legacy 读路径清单归零或登记剩余；dual 模式下对账无 WRONGTYPE 告警；门禁文档更新。
```

## U-09（优先级 3）：centeragent 直连 DB 路径退役（P-09）

```text
【任务】退役 centeragent 旧直连 DB 上报路径（OPS_COLLECT_DIRECT_DB=1 → agent_direct.go），默认 HTTP（OPS_COLLECT_URL）已接管（agent.go:13-16）。

【步骤】确认生产无 DIRECT_DB 部署（查部署配置/环境清单）→ 删除 agent_direct.go 与 env 分支 → 文档更新。
【验收门禁】grep OPS_COLLECT_DIRECT_DB 0 命中；centeragent 测试全绿。
```

## U-10（持续项）：文档-代码同步守卫

```text
【任务】把本轮重生的三份权威文档纳入防漂移：
docs/01-requirements/SYSTEM_REQUIREMENTS.md、docs/01-requirements/functional/FEATURES_CATALOG.md、
docs/03-design/01-architecture/architecture/ARCHITECTURE.md（快照 2026-10-01）。

【步骤】
1. 每轮收敛完成后，同步更新：FEATURES_CATALOG 的 🔴/⚪ 标注、parallel-implementations-comparison 的状态列、ARCHITECTURE §8 表。
2. 大改动（新增功能域/新并行对）时先改 SYSTEM_REQUIREMENTS 再改码。
3. 每季度或每 30 个迁移号重刷一次"事实快照"日期与统计数字（迁移号、包数、worker 数）。
```

---

## 执行顺序建议与依赖

```
U-01 (死代码热身, 零风险)
  └→ U-05 第一步 (outbox 死构造器) —— 同为纯删除，可同轮
U-02 (autoroute) 与 U-03 (legacy 探测) —— 并行开跑，互不依赖，优先级最高
U-04 (session 治理) —— 持续项，与 U-02/U-03 并行
U-05 第二步 (outbox 内核 ADR) —— 依赖 U-05 第一步
U-06 (CQRS) / U-07 (v2 暴露面) / U-09 (centeragent) —— 择机
U-08 (URSM k2) —— 依赖其自身 NO-GO 门禁解锁
U-10 —— 每轮收尾必做
```

**每轮通用验收产物**：轮文档（证据 + 变更清单 + 回滚方式）、conventional commit、FEATURES_CATALOG/对比文档状态同步、（涉码时）测试绿。

---

**最后更新**: 2026-10-01 · 生成于全码审计轮
