# 项目经验队列（LESSONS）

> 这里只记录可复用的返工、审查、验证或生产问题，并附可核验的证据。开工时先读相关条目；收尾时只新增可行动的发现，常规成功不必记一条。证据和晋级标准见 `.agents/rules/66-skill-evolution.md`。

## 待观察经验

## 2026-10-15 — 本地化验收要覆盖底层解析异常（Seen: 1/3）
- 任务 / 缺陷：Magpie第二轮browser-use验收；非法URL透出浏览器原生英文，语义拒绝又透出工具函数硬编码中文。
- 基线 / 修复 SHA：部署 `369817bd2` / 2658 实测发现；修复 `98eeb903cdb13b1db05f05ee79cc1c5fdf53a8f0`。
- 根因：组件翻译了自身错误，但共享校验器与原生URL异常仍用自由文本；仅toThrow断言证明拒绝，不能证明错误语义/语言。
- 防复发检查：共享校验用稳定错误键；畸形URL与安全语义拒绝分别断言精确键；切语言重绘既有错误且保留输入；同时确认未revealKey/生成产物。
- 证据：URL6例/provider5例修前精确键断言红；修后39项绿；去解析映射及恢复provider明文变异分别红；独立浏览器实际交互发现遗漏。
- 状态：已修复待观察

## 2026-10-15 — 安全工具链升级需同步 vendor 与实际构建入口（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；Go1.27.1与x/net可达漏洞门。
- 基线 / 修复 SHA：集成基线 `d63d9dcf959aa06143923e33cf637c4da047df1a`，修复 `76579bef134c762c88dd577baf2c16a5d072bf12`。
- 场景与影响：Go测试/构建通过后，govulncheck仍检出13项可达标准库漏洞；只改mod会被旧tracked vendor拒绝，旧CI补丁版与GOTOOLCHAIN=local也不会自动切新SDK。
- 根因：源码依赖、vendor、编译器与CI/镜像入口是独立约束，任一旧层都能阻断或偏离安全升级。
- 防复发检查：同步MVS最小依赖与vendor，生成两遍比哈希；更新活动CI并显式选择修复SDK；扫描默认vendor模式及独立模块，部署后读实际产物go version -m。
- 证据：两模块Go1.27.2扫描均0可达；vendor两次5853文件哈希一致；x/net0.60与配套版本由官方模块依赖边确认，未运行tidy。
- 状态：已修复待观察

## 2026-10-15 — 验证矩阵继承父环境，主门每次都在错误分支早退（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；verify cgo契约。
- 基线 / 修复 SHA：基线 `c708260d0514befacacfdd18f83429f80bfc98f6`，修复 `9257aefa5f165d78e9786456f7d0454858a09b78`。
- 场景与影响：verify先export CGO_ENABLED再调用契约测试，默认入口在Linux模拟分支红，强制cgo入口在Darwin默认分支红，后续全量验证从未开始。
- 根因：单例测试未先清父环境；另用nocgo依赖图查cgo专属边界，前置错误修掉后才暴露第二层假红。
- 防复发检查：每个矩阵子shell先清目标变量再显式预设；动态依赖查询显式指定被登记形态；父环境0/1污染与自然完成对照都要在场。
- 证据：`tests/verify_cgo_convention_test.sh` 五种环境完整门RC0；去unset与去go-list形态钉扎变异分别在污染/空依赖断言红。SDK26.5+cgo1真实SQLite测试RC0，不能把旧SDK不可用当永久前提。
- 状态：已修复待观察

## 2026-10-15 — 脱敏工具脚本与人工凭据声明必须区分作用域（Seen: 1/3）
- 任务 / 缺陷：r1034，MiniMax 合法 sed 脱敏命令被 output_policy_blocked；原始人工账号/密码组合漏脱敏。
- 基线 / 溯源：`14d9fed9c0b801a5ef2d71feae6417835fa77903`；`docs/superpowers/specs/2026-10-15-minimax-policy-credentials-design.md`。
- 根因：凭据赋值正则把 sed 匹配表达式当真值；前置守卫单测未覆盖后置检查器对 JSON 转义和半截参数的处理；去引号后问句/变量豁免又可能误放真实字面口令。
- 防复发检查：匹配段与替换段均证明无秘密才屏蔽；人工自然语言规则只进输入；断言映射含完整真值；保留原始引号语义并匹配完整分隔词，避免“改为”被后缀“为”当问句。工具JSON解码不得静默丢弃重复键。允许侧同时跑真实合规全链及秘密/数字/键的反向样本，校验同属主策略。
- 证据：`TestR1034Production1971ByteArguments`、`TestR1034ThreeProtocolStreamsRestoreHumanInput`、`TestR1034HumanCredentialDeclarationsRoundTrip`、`TestR1034ToolShapeRedactionHonorsOwnerPolicy`；撤除关键修复会在对应断言变红。
- 状态：观察中

## 2026-10-15 — probeOne 进程级 MemoryFlight + holdTimeout TTL → 包测串味（Seen: 1/3）
- 任务 / 缺陷：`TestExecutor_NoCandidateProbe_HonorsClientCancel` 单独绿、连跑红
- 基线 / 修复 SHA：合入 main 的 24h 审计提交（基线 magpie `9ea45f7f8`）
- 场景与影响：同包先跑 NoCandidateProbe 后，Cancel 测报 ProbeSync never called
- 根因：`defaultMemoryProbeOneFlight` 进程单例；`TryAcquire(model, holdTimeout)` 无 Release
- 防复发检查：单飞默认 per-Executor 惰性 memory；钉子 `TestDefaultProbeOneFlightIsPerExecutor`
- 证据：连跑组修前红 / 修后 `go test ./domains/streaming/executors/ -count=1` RC=0
- 状态：观察中

## 2026-10-15 — session_online ⋈ 停写后视图 → EXPLAIN 52s/0 行（Seen: 1/3）
- 任务 / 缺陷：dashboard `/api/admin/sessions/online` 5s→500
- 基线 / 修复 SHA：本轮 24h 审计（生产 252 EXPLAIN）
- 场景与影响：视图臂 `id` 对 session 行为 NULL；`session_last_requests` 0 行仍付规划代价
- 根因：S4 后仍 JOIN `request_logs_with_current_month` 取 tenant
- 防复发检查：改读 `session_summaries`；钉子禁 `request_logs_with_current_month` / `session_last_requests`
- 证据：旧 JOIN 52.8s/0 行；summaries 近窗 0.76ms/180 行
- 状态：观察中

## 2026-10-14 — miniredis「跨实例」测用了不同 Redis key 前缀 → 假红（Seen: 1/3）
- 任务 / 缺陷：`p0b_rawlogs_miniredis_convergence_test` 首跑 LearnedParams=[]
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）
- 场景与影响：Diagnose/RecordTerminal 正确，实例 B 永远读空；单独 debug 子测却绿
- 根因：`newTestRedisStore` 前缀 `llmgw:reqanom:test`，实例 B 误用 `…:rawlogs`——同 miniredis 不同 key 空间
- 防复发检查：跨实例夹具断言两侧 Store 前缀字符串相等；先 `storeA.List` 再读 B
- 证据：改同前缀后 rawlogs 两子测 + Diagnose error.loc 绿；M6 去 error.loc 臂双红
- 状态：观察中

## 2026-10-14 — macOS `strings|grep -q` 在 pipefail 下把命中判成指纹假 MISS（Seen: 1/3）
- 任务 / 缺陷：magpie P0 部署观测脚本 `scripts/observe-magpie-p0-postdeploy.sh`
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）；工作区已改脚本为 python 字节搜索
- 场景与影响：Go 二进制里确实有 `probeOne single-flight` / `llmgw:probe_one:`，但脚本全报 MISS；裸 `strings|grep -F` 却 OK
- 根因：`set -o pipefail` + `grep -q` 提前关管 → `strings` 收 SIGPIPE 非 0 → `if` 整段失败；另：const 标识符名不进 rodata，须钉字面量并经 `init` 保活防 DCE
- 防复发检查：指纹扫描用 `python3` 字节 `in`；契约类指纹用 `init` 赋值到 package var；审计文档写明禁止 `strings|grep -q`
- 证据：`TARGET=local BIN=/tmp/magpie-p0-bin/gateway bash scripts/observe-magpie-p0-postdeploy.sh` → 三指纹 OK（OBS=0）；修前 OBS=1 全 MISS
- 状态：观察中

## 2026-10-14 — `/usr/bin/env bash` 在 macOS 落 bash 3.2，`declare -A` 直接炸（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 基线对照段首加 `declare -A COUNTS`
- 基线 / 修复 SHA：未提交；已改为普通 `for` + python 打基线 diff（无 assoc array）
- 场景与影响：本机 `bash scripts/observe-…` 在指纹 OK 后 `declare: -A: invalid option`，基线 diff 整段跳过
- 根因：shebang/`env bash` ≠ `~/sdk/bash5`；deploy-lib 族已要求 bash5，观测脚本却假定 4+
- 防复发检查：观测/部署辅助脚本禁止 `declare -A` / `${var^^}`，或显式用 `$HOME/sdk/bash5/bin/bash`；改完用裸 `/usr/bin/env bash` 复跑
- 证据：修前 OBS 中段炸；修后对照 `magpie-p0-154-baseline-20261014.json` 三指纹 `CHANGED`/`OK expected`
- 状态：观察中

## 2026-10-14 — 154 观测脚本默认 `gateway` + 裸 journal unit + grep 零命中 → 假 FAIL/中途死（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 对 154 现网只读烟测
- 基线 / 修复 SHA：未提交；已改双候选 `llm-gateway-go|gateway`、canary@port 日志/unit、`grep|wc || echo 0`
- 场景与影响：BIN 默认 `/opt/.../gateway` → FileNotFound；`journalctl -u llm-gateway-go` 几乎空；`set -o pipefail` 下首个 0 计数 `grep` exit 1 打死脚本（指纹 MISS 后看不到 count/baseline）
- 根因：多 checkout 文档混用二进制名；蓝绿 canary unit 未进脚本；零命中被当成管道失败
- 防复发检查：观测脚本必须解析两名；优先 `gateway-canary-$port.log`；凡 `grep|wc` 计数加 `|| echo 0`；改后对 154 现网（故意 MISS 指纹）跑通到 baseline compare
- 证据：修后 `OBS_154` BIN 解析成功、readyz ok、count[*]=0、baseline same/MISS 打印完整
- 状态：观察中

## 2026-10-16 — 已登记迁移不等于物理契约成立（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；708有marker/hash，但body parent/hot/unified均缺kind，12条补偿报42703。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；修复 `8b04755c13e67546670b9dae27db64d067591e63`。
- 根因：只检查ledger无法证明真实列、默认值、索引、视图和权限；历史成因不能从单一marker倒推。
- 防复发检查：新编号增量修复，保留旧迁移原字节；升级通道即使marker齐全也跑可复用postcondition；真实PG保留原行、NULL拒绝整笔回滚、writer完整hook均需在场。
- 证据：独立PG17 ingest四族与settings三组通过；删除物理断言的隔离变异准确红；独立DB review通过017653d81。实际业务部署另见审计，不把夹具结果冒称现网结果。
- 状态：已修复待观察

## 2026-10-16 — 成功黄金矩阵必须配失败终态和思考语义（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；24成功交换全绿仍漏response.failed被当stop及thinking经Chat中转丢失。
- 基线 / 修复 SHA：发现于 `3643d9db9ac876c7d8d15365e4b1da0b6d024fc0`；修复终点 `7c7102af0378fe246c967bf62f684ecc8c4d2360`。
- 根因：只约束最终请求体和成功回复，无法覆盖错误外显；中间协议的序列化不是无损容器。
- 防复发检查：真实handler→Executor桥加入失败、unknown incomplete、EOF/read error、取消及thinking enabled/adaptive；完整独立语义断言先于黄金录制。
- 证据：真实failed流原黄金用例PASS、新判据红；修后14负向/截断路径和8思考黄金通过，7对应变异准确红；原26黄金字节保持。
- 状态：已修复待观察

## 2026-10-16 — Redis原子执行不提供错误回滚，清理失败也不撤销已确认提交（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；ACL拒绝PEXPIRE后原pair已改变；release-only失败使strip/tasks从4/7重复累计为5/9。
- 基线 / 修复 SHA：本轮缓存实现，基线 `d357bf985ec58f728632a9fb6a0146c4be8a8543`；最终提交见审计精确SHA。
- 根因：Lua不会回滚先前DEL/HSET；把cleanup错误覆盖primary ack会让调用方保留已生效的待累计增量。
- 防复发检查：同脚本第一次持久修改前按实际参数检查所有写权限；真实Redis逐项拒绝DEL/HSET/PEXPIRE并校验完整旧hash及绝对TTL；独立有界token cleanup只记录故障，primary结果保持。
- 证据：真实ACL负控原实现准确红；修后真实strict race通过；release-only仓内红→绿，updateCache→CommitFinal保持4/7；撤ack保护/吞主错误/去token保护均命中正确断言。
- 状态：已修复待观察

## 2026-10-16 — 跨入口会话验收须分开响应契约与异步落库（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮验收](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；脚本把裸UUID发canonical头、要求Chat回同名头，随后又用5秒同步可见性判异步镜像，连续两轮假红。
- 基线 / 修复 SHA：发布源码`76e185e71cebe87611746d03cea56bbc352a4d7b`未变；修正验收脚本，不据此修改生产。
- 根因：同为会话ID不代表三入口的header值域/响应行为相同；HTTP完成不代表后台镜像/补偿已同步完成。
- 防复发检查：先读真实入口契约；用实际返回request_id且要求互异，按tenant查canonical，分别比较显式ID和存在的响应头；独立核turn/body/date/终态/双delta，有界等待并记录elapsed，不重发请求或放宽告示阈值。
- 证据：最终18HTTP与17唯一pair通过；8类验收负控正确拒绝；首条旧5秒窗口后的可见性不足以证明冷启动或补偿，原RC1保留。
- 状态：已修复待观察

<!-- 只有出现具体问题时才新增条目。 -->

```markdown
## <日期> — <一句话模式>（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：<链接或编号>
- 基线 / 修复 SHA：<精确 SHA；未提交时注明“未提交”并记录基线 SHA>
- 场景与影响：<可观察事实>
- 根因：<缺失了哪条约束或验证>
- 防复发检查：<规则 / 回归用例 / 命令>
- 证据：`<命令>` → <实际结果；未运行项和原因>
- 状态：观察中 | 已修复待观察 | 已晋级 → <目标链接>
```

## 2026-10-15 — 版本化配置必须接管全部执行链（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：`codex/sensitive-rule-management`；管理页面停用规则不能仍由旧PII/凭据字段/环境动作执行。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；本任务代码提交前记录。
- 根因：配置CRUD、语义匹配、协议入口与最终写回的所有权不一致；Chat、Messages、Responses并不共享ServeHTTP入口。
- 防复发检查：规则启停、log/mask/block、重叠豁免、可信还原、三协议输入开/关均在真实执行入口断言；write-time阻断须核HTTP和失败审计，不能只测字节。
- 证据：`TestManagedRuleDisablingStopsCredentialFieldChain`、`TestManagedOverlappingRulesKeepStrictBlock`、`TestManagedOutputTrustAndExplicitScopeActions`、`TestAllProtocolRequestsPinBeforeInputAndWithoutInput`通过；撤还原/去重/双协议Pin/Type限制五项overlay变异均在断言红。
- 状态：已修复待观察

## 2026-10-15 — Redis连接成功不等于订阅成功（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；SUBSCRIBE ACK黑洞曾阻断数据库补偿轮询和退出。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：启动同步等待pubsub ACK，Receive默认无真实读取期限；立即报错及正常miniredis测试都看不见该失败。
- 防复发检查：订阅建立与DB轮询解耦；真实socket握手期限；专测接受TCP、吞掉ACK、取消退出。
- 证据：独立代理探针修前DB v2/实例v1且退出阻塞；修后v2收敛并正常退出；`TestRedisWithheldSubscribeACKHasDeadline -race -count=3`通过。
- 状态：已修复待观察

## 2026-10-15 — 翻译文件齐全不证明运行时已注册（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；8语言111键源文件齐全，6个index只import未export。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：测试只检查独立字典文件，没有从真实locale入口装配解析器。
- 防复发检查：加载真实index导出，对每个语言namespace逐键执行i18n解析；同时跑前端全套断点门。
- 证据：真实bundle测试先在6语言undefined断言红；修复后实际locale及i18n逐键解析通过，前端2474测试、类型及构建通过。
- 状态：已修复待观察

## 2026-10-16 — 字符串内容像JSON不代表它是参数容器（Seen: 1/3 个不同工作日）

- 根因：工具根arguments/input和真实嵌套字符串共用JSON解码入口；解码清空字段标签，导致人工配置auth_blob等规则及内置password规则被绕过。
- 防复发：仅协议根容器允许解码；实际字符串叶子先沿配置字段规则处理并保留完整还原边界。覆盖内置与自定义字段、JSON样对象/数组串、普通根容器子键的正负判据。
- 证据：48个真实上游载体+Redis完整映射通过；独立公开API探针在password/auth_blob的普通和JSON样串均仅看到占位符；撤guard变异变红。
- 溯源：本任务基线493d24ebcdaf45d792130f32b42e6724cb667905；docs/audit/2026-10-16-sensitive-rule-management-validation.md。

## 2026-10-16 — 路由插入还会改变移动端的源码引用（Seen: 1/3 个不同工作日）

- 任务 / 基线：敏感规则管理；493d24ebcdaf45d792130f32b42e6724cb667905。
- 根因：新增管理路由后只关注后端行为与桌面UI，移动端的Go行号注释及精确断言同样依赖被插入的源文件。
- 防复发：修改路由注册块后运行移动端全树Go引用扫描及真实源码断言；按实际注册处订正注释和断言，不扩大漂移/误报名单。
- 证据：桌面236文件2476测试绿后，移动端原门仍有6文件10条行号失败；完整修复及验证见本任务交付报告。
- 状态：已修复待观察。

## 2026-10-16 — 熔断观测镜像不能抢占恢复准入（Seen: 1/3 个不同工作日）

- 任务 / 基线：Vapeur grok-4.6 经154失败；会话 `mvs_4bfb2af7bdd34b2487a7d70d8785cbf0`，分支 `fix/vapeur-circuit-mirror-20261016`，基线 `dd0d824866`。
- 根因：SQL视图禁用异步 `circuit_state` 并不足够；Go Candidate.UnavailableReason 又用镜像摘流，重启/丢失closed事件时请求无法抵达内存熔断器的半开恢复门。
- 防复发：镜像保留诊断但不做候选准入；同时验证无pin的stale-open候选能到真实forward、live-open/quarantine零上游调用，权威鉴权/额度/人工停用仍拒绝；新门须先红再绿并反向变异。
- 证据：154 `f9fc8b1b97b1ce39d8a1a717a8867f77` 非流式503；`9eb908939f3f0df745042055222d9a29` 流式244秒空正文后失败（不是HTTP200成功）。provider镜像回归修前红；恢复镜像摘流与跳过真实Allow两种变异均命中相应断言；恢复后provider/executors/credential/routeguard全包绿。端到端部署验收另行记录实际结果，不把本地测试当现网成功。
- 状态：代码已修，现网验收待完成。

## 晋级记录

将已晋级条目移到这里，并链接目标规则、清单或技能。同一模式只按不同工作日计 `Seen`；出现三天后晋级。经证据确认的安全、隐私、数据丢失或生产事故，若已有可执行的防复发检查，可立即晋级。
